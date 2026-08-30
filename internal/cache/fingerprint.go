// Copyright (c) 2026 Neomantra Corp

// Package cache stores drafts returned by the model, so asking the same
// question twice does not bill twice.
//
// What is cached, and what deliberately is not, is the whole design. A drafted
// mode is *code*: given the same question, the same schema, the same prompt and
// the same model, the SQL it produces means the same thing tomorrow. Reusing it
// costs nothing in truthfulness. A query *result* is data, and reusing one
// would put a figure on screen that the portal may have revised — precisely the
// failure the confidence scores exist to prevent. So csq caches the draft and
// never the answer; `csq modes run` always re-executes against DuckDB.
//
// Two rules follow, and both are load-bearing:
//
//   - The fingerprint must cover every input that can change a draft. A field
//     left out of it is not a smaller cache key, it is a cache that serves the
//     wrong draft — the schema changed, the prompt was rewritten, the model was
//     swapped, and csq hands back SQL written for the old world.
//   - A cache hit skips the network call and nothing else. The read-only guard,
//     the inventory cross-check, the loader's validation, and EXPLAIN all run
//     on a cached draft exactly as they run on a fresh one. The cache is an
//     optimisation, never a shortcut past a safety check.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// FormatVersion is the on-disk entry format. Bumping it invalidates every
// existing entry, which is the correct response to a change in what an entry
// means — an old file read under new rules is worse than a cache miss.
const FormatVersion = 1

// Fingerprint is every input that determines a draft.
//
// It is stored inside the entry as well as hashed into its key, because a key
// alone can only say "different". Keeping the fields lets csq tell the user
// *which* input changed, which is the difference between "your cache is stale"
// and "your cache is stale because you synced three new columns".
type Fingerprint struct {
	// Format guards against reading an old entry under new rules.
	Format int `json:"format"`
	// CsqVersion changes when the checks around a draft change, and a draft
	// accepted by an older csq is not necessarily one this csq would accept.
	CsqVersion string `json:"csq_version"`

	// Model and Effort change what the model produces from identical input.
	Model  string `json:"model"`
	Effort string `json:"effort"`

	// PromptDigest and SchemaDigest cover the instructions and the grammar the
	// model was held to. Editing the system prompt must invalidate the cache;
	// otherwise a prompt fix appears not to work.
	PromptDigest string `json:"prompt_digest"`
	SchemaDigest string `json:"schema_digest"`

	// Question is the normalised user question.
	Question string `json:"question"`
	ModeName string `json:"mode_name"`
	Portal   string `json:"portal"`

	// InventoryDigest covers the tables, columns, and types the model was
	// shown — and the sample values too, when the user asked for them. A new
	// column, a retyped column, or a resync that changed how a category is
	// spelled all have to invalidate: SQL written against the old shape may no
	// longer plan, and worse, may still plan while meaning something else.
	InventoryDigest string `json:"inventory_digest"`
	// Samples records whether values were shown at all, so a --samples run and
	// a plain one never share an entry even if the samples happened to be empty.
	Samples bool `json:"samples"`

	// ExistingDigest covers the mode already on disk. The model is told what is
	// there and asked not to repeat it, so a file that has grown since is a
	// different question.
	ExistingDigest string `json:"existing_digest"`
}

// Key is the content address of this fingerprint: the identity of one exact
// set of inputs.
func (f Fingerprint) Key() string {
	return digestJSON(f)
}

// Slot identifies the question rather than the inputs around it: same mode,
// same portal, same question.
//
// The store keeps one entry per slot, which is what lets a miss be explained.
// Addressing entries by Key alone would mean a stale entry is simply never
// found, and csq could say only "no cached draft" when the useful answer is
// "there is one, from before you synced two new tables".
func (f Fingerprint) Slot() string {
	return digest(strings.Join([]string{
		"v" + fmt.Sprint(FormatVersion),
		f.ModeName,
		f.Portal,
		f.Question,
	}, "\x00"))
}

// NormaliseQuestion folds the incidental differences between two askings of the
// same question: surrounding space, internal runs of whitespace, capitalisation,
// and trailing punctuation.
//
// It goes no further on purpose. Stripping stopwords or stemming would start
// merging questions that differ in meaning, and a cache that answers a question
// the user did not ask is worse than one that misses.
func NormaliseQuestion(q string) string {
	q = strings.ToLower(strings.Join(strings.Fields(q), " "))
	return strings.TrimRight(q, " ?.!")
}

// Diff reports which fingerprint fields differ, in a form written for the user
// rather than for a log: each entry says what changed, not which struct field.
func Diff(old, current Fingerprint) []string {
	var out []string
	add := func(cond bool, msg string) {
		if cond {
			out = append(out, msg)
		}
	}
	add(old.Format != current.Format, "the cache format changed with a csq upgrade")
	add(old.CsqVersion != current.CsqVersion,
		fmt.Sprintf("csq changed version (%s → %s)", old.CsqVersion, current.CsqVersion))
	add(old.Model != current.Model,
		fmt.Sprintf("the model changed (%s → %s)", old.Model, current.Model))
	add(old.Effort != current.Effort,
		fmt.Sprintf("the effort level changed (%s → %s)", old.Effort, current.Effort))
	add(old.PromptDigest != current.PromptDigest, "csq's authoring instructions changed")
	add(old.SchemaDigest != current.SchemaDigest, "the mode file schema changed")
	add(old.InventoryDigest != current.InventoryDigest,
		"the tables you hold changed — a column, a type, or a sampled value differs")
	add(old.Samples != current.Samples,
		fmt.Sprintf("sample values were %s before and %s now",
			wereShown(old.Samples), wereShown(current.Samples)))
	add(old.ExistingDigest != current.ExistingDigest,
		"the mode on disk changed, so the model is being asked something different")

	// Question, ModeName and Portal are part of the slot, so a difference in
	// one of them is a different slot entirely and cannot show up here. Naming
	// them anyway would be a message the user can never see.
	return out
}

func wereShown(b bool) string {
	if b {
		return "shown"
	}
	return "not shown"
}

// InventoryDigestOf hashes a table inventory: names, columns, types, and any
// sample values, all in a stable order.
//
// Sorting is what makes this a fingerprint rather than a coin flip. DuckDB does
// not promise catalogue order, and an unsorted digest would change between two
// identical databases, turning every run into a miss.
func InventoryDigestOf(tables []TableShape) string {
	shapes := append([]TableShape(nil), tables...)
	sort.Slice(shapes, func(i, j int) bool { return shapes[i].Name < shapes[j].Name })

	var b strings.Builder
	for _, t := range shapes {
		fmt.Fprintf(&b, "T %s %d\n", t.Name, t.Rows)
		cols := append([]ColumnShape(nil), t.Columns...)
		sort.Slice(cols, func(i, j int) bool { return cols[i].Name < cols[j].Name })
		for _, c := range cols {
			fmt.Fprintf(&b, "  C %s %s", c.Name, c.Type)
			samples := append([]string(nil), c.Samples...)
			sort.Strings(samples)
			for _, s := range samples {
				fmt.Fprintf(&b, " |%s", s)
			}
			b.WriteString("\n")
		}
	}
	return digest(b.String())
}

// TableShape and ColumnShape are the parts of an inventory that affect a draft.
//
// They exist so this package does not import internal/personal, which imports
// this one. The alternative — hashing the inventory inside personal — would put
// the definition of "what invalidates a draft" in a different package from the
// rules that read it.
type TableShape struct {
	Name    string
	Rows    int64
	Columns []ColumnShape
}

// ColumnShape is one column as the model saw it.
type ColumnShape struct {
	Name    string
	Type    string
	Samples []string
}

// DigestString hashes arbitrary text, for the prompt and any other input whose
// exact content matters but whose bulk should not be stored.
func DigestString(s string) string { return digest(s) }

// DigestJSON hashes any value by its canonical JSON encoding.
func DigestJSON(v any) string { return digestJSON(v) }

func digestJSON(v any) string {
	// encoding/json sorts map keys, and every struct here has a fixed field
	// order, so this encoding is stable across runs and machines.
	b, err := json.Marshal(v)
	if err != nil {
		// A fingerprint that cannot be computed must never collide with a real
		// one, so it degrades to a value that can never match.
		return "unhashable-" + digest(fmt.Sprintf("%#v", v))
	}
	return digest(string(b))
}

func digest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
