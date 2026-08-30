// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// A drafted mode is written to disk only after it has survived three checks,
// applied in this order because each is cheaper than the next:
//
//  1. the read-only guard and the inventory cross-check, in Author;
//  2. the loader's own validation, by loading the files csq is about to keep;
//  3. DuckDB planning every query, which is the only check that can prove the
//     SQL actually resolves against the columns it claims to read.
//
// Step 3 is what separates this from "the model said so". A query that parses,
// validates, and then fails on a missing column would otherwise fail in front
// of the user, halfway through an answer.

// Paths reports where a mode's two documents live.
type Paths struct {
	Mode    string
	Binding string
}

// PathsFor returns the file paths for a mode and its binding in dir.
func PathsFor(dir, modeName, portal string) Paths {
	return Paths{
		Mode:    filepath.Join(dir, modeName+".json"),
		Binding: filepath.Join(dir, modeName+"."+safeFilename(portal)+".binding.json"),
	}
}

// LoadExisting reads a mode document already on disk, if there is one.
//
// A file that does not parse is reported, not ignored: silently starting over
// would discard whatever the user had written there.
func LoadExisting(path string) (*Document, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var d Document
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%s exists but does not parse: %w\n"+
			"  Fix or move it — csq will not overwrite a file it cannot read", path, err)
	}
	return &d, nil
}

// Save writes the draft, then validates it by loading it back through the same
// parser that loads every other external mode.
//
// It writes into dir directly rather than a staging area, because the loader
// resolves a binding against the mode registry and the two must be visible
// together. On a validation failure the files are rolled back to whatever was
// there before, so a rejected draft never leaves a broken mode behind.
func Save(dir string, d *Draft, p Paths) (rollback func(), err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}

	restoreMode, err := writeWithBackup(p.Mode, d.Mode)
	if err != nil {
		return nil, err
	}
	restoreBinding, err := writeWithBackup(p.Binding, d.Binding)
	if err != nil {
		restoreMode()
		return nil, err
	}
	rollback = func() {
		restoreBinding()
		restoreMode()
	}

	// Load the directory, not just these two files: a mode and its binding are
	// validated against each other, and against anything else the user keeps
	// here.
	if _, err := modes.LoadDir(dir); err != nil {
		rollback()
		return nil, fmt.Errorf("the drafted mode did not pass validation, so nothing was "+
			"saved:\n  %w", err)
	}
	return rollback, nil
}

// writeWithBackup writes doc to path, returning a function that restores the
// previous contents — or removes the file, if there were none.
func writeWithBackup(path string, doc *Document) (restore func(), err error) {
	prev, readErr := os.ReadFile(path)
	existed := readErr == nil

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", filepath.Base(path), err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(path, body, 0o644); err != nil {
		return nil, fmt.Errorf("write %s: %w", path, err)
	}

	return func() {
		if existed {
			_ = os.WriteFile(path, prev, 0o644)
			return
		}
		_ = os.Remove(path)
	}, nil
}

// VerifyQueries plans every query in the mode against the attached database.
//
// EXPLAIN binds names and types without producing rows, so this is fast even
// against a very large table and proves what no amount of validation can: that
// the columns the SQL reads are really there, under the names the binding
// claims. Only queries new to this draft are checked — a query the user has
// been running for weeks does not need re-proving, and re-checking it would
// turn an unrelated edit of theirs into a failure of this run.
func VerifyQueries(db *sql.DB, modeName, alias, portal string, only map[string]bool) []QueryProblem {
	m, err := modes.Lookup(modeName)
	if err != nil {
		return []QueryProblem{{Query: modeName, Err: err}}
	}
	b, err := modes.LookupBinding(modeName, portal)
	if err != nil {
		return []QueryProblem{{Query: modeName, Err: err}}
	}

	var problems []QueryProblem
	for _, q := range m.Queries {
		if only != nil && !only[q.Name] {
			continue
		}
		expanded, err := modes.ExpandConceptsFor(m, q.SQL, alias, b)
		if err != nil {
			problems = append(problems, QueryProblem{Query: q.Name, Err: err})
			continue
		}
		if _, err := db.Exec("EXPLAIN " + expanded); err != nil {
			problems = append(problems, QueryProblem{Query: q.Name, Err: err})
		}
	}
	return problems
}

// QueryProblem is one query that would not plan.
type QueryProblem struct {
	Query string
	Err   error
}

// safeFilename reduces a portal host to something usable in a filename.
func safeFilename(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "portal"
	}
	return out
}
