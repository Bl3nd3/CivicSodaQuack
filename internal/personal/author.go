// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/cache"
	"github.com/neomantra/CivicSodaQuack/internal/llm"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
	"github.com/neomantra/CivicSodaQuack/internal/version"
)

// Fingerprint gathers every input that determines a draft.
//
// It lives here rather than in the cache package because this is where the
// inputs are: adding a new one to the prompt and forgetting to add it here is
// the way a cache starts serving the wrong draft, and keeping the two in the
// same file is the cheapest defence against that.
func Fingerprint(req Request, cfg llm.Config, system string, schema map[string]any) cache.Fingerprint {
	return cache.Fingerprint{
		Format:          cache.FormatVersion,
		CsqVersion:      version.Version,
		Model:           cfg.Model,
		Effort:          cfg.Effort,
		PromptDigest:    cache.DigestString(system),
		SchemaDigest:    cache.DigestJSON(schema),
		Question:        cache.NormaliseQuestion(req.Question),
		ModeName:        req.ModeName,
		Portal:          req.Portal.Host,
		InventoryDigest: cache.InventoryDigestOf(req.Portal.Shape()),
		Samples:         req.Samples,
		ExistingDigest:  existingDigest(req.Existing),
	}
}

// existingDigest hashes what the model is told already exists. Only the parts
// shown to it matter: the concept and query names it is asked not to repeat.
// Hashing the whole document would invalidate the cache when a user edits a
// caveat, which changes nothing about what is being asked.
func existingDigest(d *Document) string {
	if d == nil {
		return ""
	}
	var b strings.Builder
	for _, c := range d.Concepts {
		fmt.Fprintf(&b, "C %s %s %v %v\n", c.Name, c.Purpose, c.Required, c.Optional)
	}
	for _, q := range d.Queries {
		fmt.Fprintf(&b, "Q %s %s\n", q.Name, q.Desc)
	}
	return cache.DigestString(b.String())
}

// DefaultModeName is the mode a drafted profile lands in unless --as names
// another. "personal" is the user's own mode: whatever they have asked about,
// accumulated as queries they can read and edit.
const DefaultModeName = "personal"

// Request is one authoring run.
type Request struct {
	// Question is what the user asked, in their own words.
	Question string
	// ModeName is the mode to create or extend.
	ModeName string
	// Portal is the inventory the queries must run against.
	Portal *Portal
	// City labels the jurisdiction in the binding.
	City string
	// Existing is the current mode document, when one is already on disk.
	// Queries are appended to it rather than replacing it.
	Existing *Document
	// Samples records whether the inventory carries sample values, so a run
	// that showed them never reuses a draft written without them.
	Samples bool

	// Cache, when set, is consulted before the model is called and written to
	// after. A nil store simply disables caching.
	Cache *cache.Store
	// Refresh calls the model even when a valid entry exists, and replaces it.
	// It is the escape hatch for the one input the fingerprint cannot see: the
	// model itself, which can change behind a stable id.
	Refresh bool
}

// Outcome records how a draft was obtained, so the caller can tell the user
// whether they were billed for it.
type Outcome struct {
	// Verdict is what the cache concluded. Its State is StateMiss when no
	// store was configured.
	Verdict cache.Verdict
	// Cached reports that the draft came from the store rather than the API.
	Cached bool
	// Elapsed is how long obtaining the draft took.
	Elapsed time.Duration
}

// Draft is what the model returned, after parsing and local checks.
type Draft struct {
	Mode    *Document `json:"mode"`
	Binding *Document `json:"binding"`
}

// Document is one mode-or-binding file. It mirrors the loader's own shape so a
// drafted file and a hand-written one are the same artefact — see
// modes.DocumentSchema, which is what the model is constrained to.
type Document struct {
	Kind string `json:"kind"`

	Name     string     `json:"name,omitempty"`
	Title    string     `json:"title,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	About    string     `json:"about,omitempty"`
	Concepts []Concept  `json:"concepts,omitempty"`
	Queries  []DocQuery `json:"queries,omitempty"`
	Caveats  []string   `json:"caveats,omitempty"`

	Mode             string                `json:"mode,omitempty"`
	Portal           string                `json:"portal,omitempty"`
	City             string                `json:"city,omitempty"`
	Population       int64                 `json:"population,omitempty"`
	PopulationSource string                `json:"population_source,omitempty"`
	Datasets         map[string]DocDataset `json:"datasets,omitempty"`
	Notes            []string              `json:"notes,omitempty"`
}

// Concept is a logical table the drafted queries read.
type Concept struct {
	Name     string   `json:"name"`
	Purpose  string   `json:"purpose"`
	Required []string `json:"required,omitempty"`
	Optional []string `json:"optional,omitempty"`
}

// DocQuery is one drafted analysis.
type DocQuery struct {
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	SQL     string `json:"sql"`
	Entity  string `json:"entity,omitempty"`
	Measure string `json:"measure,omitempty"`
}

// DocDataset is one concept's binding to a local table.
type DocDataset struct {
	ID      string            `json:"id"`
	Table   string            `json:"table"`
	Name    string            `json:"name"`
	Rows    int64             `json:"rows,omitempty"`
	Notes   string            `json:"notes,omitempty"`
	Columns map[string]string `json:"columns,omitempty"`
}

// GeneratedCaveat is added to every drafted mode and never removed.
//
// A reader looking at a table of numbers cannot tell which SQL a person wrote
// and which a model did, and the difference changes how much checking the
// numbers deserve. Every other caveat in a mode is the author's judgement;
// this one is a fact about the file's provenance, so csq asserts it rather
// than asking the model to remember.
const GeneratedCaveat = "These queries were drafted by a language model from the column " +
	"names of your local tables, then run unchanged by csq. The model did not see the data " +
	"and did not produce any number here — but it did choose which columns to trust, and it " +
	"can misread what a column means. Read the SQL under 'csq modes show' before quoting a " +
	"result, and treat a surprising figure as a reason to check the query first."

// Peek reports what the cache holds for a request without calling anything.
//
// The CLI uses it to decide whether an API call is going to happen at all, so
// it can skip both the credential check and the "about to contact the API"
// confirmation when the answer is already on disk.
func Peek(cfg llm.Config, req Request) cache.Verdict {
	if req.Cache == nil || req.Portal == nil {
		return cache.Verdict{}
	}
	return req.Cache.Lookup(Fingerprint(req, cfg, systemPrompt(), draftSchema()))
}

// Author obtains a draft — from the cache when every input still matches,
// otherwise from the model — and checks what came back.
//
// The returned draft has been parsed, had its identity fields forced to match
// what the caller asked for, and had every query's SQL checked as read-only. It
// has not yet been validated by the loader or executed — see Save and Verify.
//
// c may be nil when the cache is certain to hit; Author returns a clear error
// rather than a nil dereference if it turns out a call was needed after all.
func Author(ctx context.Context, c *llm.Client, cfg llm.Config, req Request) (*Draft, Outcome, error) {
	started := time.Now()
	var out Outcome

	if strings.TrimSpace(req.Question) == "" {
		return nil, out, fmt.Errorf("no question given")
	}
	if req.Portal == nil || len(req.Portal.Tables) == 0 {
		return nil, out, fmt.Errorf(
			"this database holds no synced tables, so there is nothing to write a mode against.\n" +
				"  Sync something first, e.g. 'csq modes init corruption --output c.yaml && csq sync --config c.yaml'")
	}

	system := systemPrompt()
	user := userPrompt(req)
	schema := draftSchema()
	fp := Fingerprint(req, cfg, system, schema)

	// The cache is consulted for the model's raw reply only. Everything below
	// this point — parsing, the identity overrides, the read-only guard, the
	// inventory cross-check — runs identically whether the bytes came from the
	// API or from disk, so a cached draft is never trusted more than a fresh
	// one. Caching the *checked* draft instead would make the cache a way to
	// skip the checks, which is the one thing it must never be.
	var raw []byte
	if req.Cache != nil {
		out.Verdict = req.Cache.Lookup(fp)
	}
	if out.Verdict.Hit() && !req.Refresh {
		raw = []byte(out.Verdict.Entry.Payload)
		out.Cached = true
		_ = req.Cache.Touch(out.Verdict.Entry)
	} else {
		if c == nil {
			return nil, out, fmt.Errorf(
				"a model call is needed but no client was configured (the cached draft "+
					"was %s)", out.Verdict.State)
		}
		var err error
		raw, err = c.JSON(ctx, llm.JSONRequest{System: system, User: user, Schema: schema})
		if err != nil {
			return nil, out, err
		}
		if req.Cache != nil {
			// A cache that cannot be written is a slower csq, not a broken one.
			// The draft is in hand and the user asked for a mode, not for a
			// cache write, so this failure must not lose them the answer.
			if _, err := req.Cache.Put(fp, req.Question, raw); err != nil {
				out.Verdict.Reasons = append(out.Verdict.Reasons,
					fmt.Sprintf("the draft could not be cached: %v", err))
			}
		}
	}
	out.Elapsed = time.Since(started)

	var d Draft
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, out, fmt.Errorf("the model's draft did not parse: %w", err)
	}
	if d.Mode == nil || d.Binding == nil {
		return nil, out, fmt.Errorf("the model's draft was missing its mode or its binding")
	}

	// Identity is csq's to set, not the model's. Forcing these removes a whole
	// class of failure — a draft naming the wrong portal, or a binding pointing
	// at a mode that does not exist — without another round trip.
	d.Mode.Kind = "mode"
	d.Mode.Name = req.ModeName
	d.Binding.Kind = "binding"
	d.Binding.Mode = req.ModeName
	d.Binding.Portal = req.Portal.Host
	if strings.TrimSpace(d.Binding.City) == "" {
		d.Binding.City = req.City
	}

	// A population the model recalled from training is a denominator with no
	// citation, and a per-capita rate computed from it is confidently wrong.
	// The loader would reject it anyway; dropping it here says why.
	d.Binding.Population = 0
	d.Binding.PopulationSource = ""

	if err := d.check(req.Portal); err != nil {
		return nil, out, err
	}
	d.Mode.Caveats = appendUnique(d.Mode.Caveats, GeneratedCaveat)

	if req.Existing != nil {
		d.Mode = MergeMode(req.Existing, d.Mode)
	}
	return &d, out, nil
}

// check applies the constraints the schema cannot express: that SQL is
// read-only, that concepts referenced are declared and bound, and that bound
// tables actually exist locally.
func (d *Draft) check(p *Portal) error {
	if len(d.Mode.Queries) == 0 {
		return fmt.Errorf("the model returned no queries")
	}

	declared := map[string]bool{}
	for _, c := range d.Mode.Concepts {
		declared[c.Name] = true
	}

	for _, q := range d.Mode.Queries {
		if err := CheckReadOnly(q.SQL); err != nil {
			return fmt.Errorf("drafted query %q was rejected: %w", q.Name, err)
		}
		refs := modes.ConceptRefs(q.SQL)
		if len(refs) == 0 {
			return fmt.Errorf("drafted query %q names its tables directly instead of "+
				"using {{c:concept}}, so it would only ever work on this one portal", q.Name)
		}
		for _, r := range refs {
			if !declared[r] {
				return fmt.Errorf("drafted query %q reads {{c:%s}}, which the draft never "+
					"declares as a concept", q.Name, r)
			}
			if _, ok := d.Binding.Datasets[r]; !ok {
				return fmt.Errorf("drafted query %q reads {{c:%s}}, which the draft's "+
					"binding never maps to a table", q.Name, r)
			}
		}
	}

	// Every bound table must be one csq actually holds. A hallucinated table
	// name would otherwise surface much later as a DuckDB binder error in the
	// middle of a result.
	for concept, ds := range d.Binding.Datasets {
		t, ok := p.Table(ds.Table)
		if !ok {
			return fmt.Errorf("the draft binds concept %q to table %q, which this database "+
				"does not have (it holds: %s)",
				concept, ds.Table, strings.Join(p.TableNames(), ", "))
		}
		// Fill in the facts csq knows better than the model does.
		ds.Rows = t.Rows
		if t.DatasetID != "" {
			ds.ID = t.DatasetID
		}
		if t.DatasetName != "" {
			ds.Name = t.DatasetName
		}
		if err := checkColumns(concept, ds, t); err != nil {
			return err
		}
		d.Binding.Datasets[concept] = ds
	}
	return nil
}

// checkColumns verifies that every plain column mapping names a real column.
//
// A mapping may also be an SQL expression, which cannot be checked this way and
// is left for the verification pass to execute. The distinction is drawn by
// looking for anything that is not a bare identifier.
func checkColumns(concept string, ds DocDataset, t Table) error {
	have := map[string]bool{}
	for _, c := range t.Columns {
		have[strings.ToLower(c.Name)] = true
	}
	for canonical, expr := range ds.Columns {
		if !isBareIdentifier(expr) {
			continue // an expression; the EXPLAIN pass will judge it
		}
		if !have[strings.ToLower(expr)] {
			return fmt.Errorf("the draft maps %s.%s to column %q, which table %q does not "+
				"have", concept, canonical, expr, t.Name)
		}
	}
	return nil
}

func isBareIdentifier(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

// MergeMode folds a new draft into the document already on disk.
//
// The existing file wins every conflict. A user who edited a caveat, renamed a
// query, or corrected a column mapping must not have that work reverted by the
// next question they ask — the model is extending their file, not owning it.
func MergeMode(existing, drafted *Document) *Document {
	out := *existing
	out.Kind = "mode"

	// Concepts are merged column-wise, not skipped wholesale. A second query
	// against a table the mode already knows commonly needs one more column
	// from it — a date to group by, a category to split on. Keeping only the
	// existing concept would leave that column undeclared, and the query that
	// reads it would then fail to load. The existing *wording* still wins; it
	// is only the column lists that grow.
	out.Concepts = append([]Concept(nil), out.Concepts...)
	byName := map[string]int{}
	for i, c := range out.Concepts {
		byName[c.Name] = i
	}
	for _, c := range drafted.Concepts {
		i, ok := byName[c.Name]
		if !ok {
			out.Concepts = append(out.Concepts, c)
			byName[c.Name] = len(out.Concepts) - 1
			continue
		}
		existing := out.Concepts[i]
		for _, col := range c.Required {
			existing.Required = appendUnique(existing.Required, col)
		}
		for _, col := range c.Optional {
			// A column already required must not be demoted to optional.
			if !containsString(existing.Required, col) {
				existing.Optional = appendUnique(existing.Optional, col)
			}
		}
		out.Concepts[i] = existing
	}

	taken := map[string]bool{}
	for _, q := range out.Queries {
		taken[q.Name] = true
	}
	for _, q := range drafted.Queries {
		q.Name = uniqueName(q.Name, taken)
		taken[q.Name] = true
		out.Queries = append(out.Queries, q)
	}

	for _, c := range drafted.Caveats {
		out.Caveats = appendUnique(out.Caveats, c)
	}
	return &out
}

// MergeBinding folds a drafted binding into one already on disk, on the same
// terms as MergeMode: what is there already stays.
func MergeBinding(existing, drafted *Document) *Document {
	if existing == nil {
		return drafted
	}
	out := *existing
	out.Kind = "binding"
	if out.Datasets == nil {
		out.Datasets = map[string]DocDataset{}
	}
	for name, ds := range drafted.Datasets {
		prev, ok := out.Datasets[name]
		if !ok {
			out.Datasets[name] = ds
			continue
		}
		// Same reasoning as concepts: a new query on a known table usually
		// needs one more column mapped. Dropping the whole new dataset would
		// leave that mapping missing, and because a non-empty columns map is
		// authoritative, the column would read as unavailable rather than as
		// simply unmapped. The user's existing mappings still win one by one.
		if len(ds.Columns) > 0 {
			if prev.Columns == nil {
				prev.Columns = map[string]string{}
			}
			for canonical, expr := range ds.Columns {
				if _, taken := prev.Columns[canonical]; !taken {
					prev.Columns[canonical] = expr
				}
			}
		}
		out.Datasets[name] = prev
	}
	for _, n := range drafted.Notes {
		out.Notes = appendUnique(out.Notes, n)
	}
	return &out
}

func uniqueName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(s)) {
			return list
		}
	}
	return append(list, s)
}
