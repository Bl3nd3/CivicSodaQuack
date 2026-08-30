// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// External modes and bindings let someone add a city, or a whole analysis
// profile, without a Go toolchain. A file declares either a mode (concepts,
// queries, caveats) or a binding (one portal's datasets mapped onto a mode's
// concepts), and csq loads it at startup.
//
// Both YAML and JSON are accepted, and they are the same document — the fields,
// the validation, and the error messages do not vary by extension. YAML is
// pleasant to hand-write; JSON is what a program emits, which is how the
// personal mode writes a profile an LLM authored. Anything one can express the
// other can too, so a generated file stays editable by hand and a hand-written
// file stays machine-readable.
//
// Validation is deliberately strict and the messages are written for someone
// who is not a Go programmer: unknown keys are rejected rather than silently
// ignored, and every error names the file, the field, and what to do about it.

// fileKind distinguishes the two document types a file may hold.
type fileKind string

const (
	kindMode    fileKind = "mode"
	kindBinding fileKind = "binding"
)

// externalFile is the top level of a modes YAML file.
type externalFile struct {
	Kind fileKind `yaml:"kind" json:"kind"`

	// Mode fields (kind: mode)
	Name     string          `yaml:"name" json:"name,omitempty"`
	Title    string          `yaml:"title" json:"title,omitempty"`
	Summary  string          `yaml:"summary" json:"summary,omitempty"`
	About    string          `yaml:"about" json:"about,omitempty"`
	Concepts []externalConc  `yaml:"concepts" json:"concepts,omitempty"`
	Queries  []externalQuery `yaml:"queries" json:"queries,omitempty"`
	Caveats  []string        `yaml:"caveats" json:"caveats,omitempty"`

	// Binding fields (kind: binding)
	Mode             string `yaml:"mode" json:"mode,omitempty"`
	Portal           string `yaml:"portal" json:"portal,omitempty"`
	City             string `yaml:"city" json:"city,omitempty"`
	Population       int64  `yaml:"population" json:"population,omitempty"`
	PopulationSource string `yaml:"population_source" json:"population_source,omitempty"`

	//nolint:revive // grouped with the binding fields above for readability.
	Datasets map[string]externalBind `yaml:"datasets" json:"datasets,omitempty"`
	Notes    []string                `yaml:"notes" json:"notes,omitempty"`
}

type externalConc struct {
	Name     string   `yaml:"name" json:"name"`
	Purpose  string   `yaml:"purpose" json:"purpose"`
	Required []string `yaml:"required" json:"required,omitempty"`
	Optional []string `yaml:"optional" json:"optional,omitempty"`
}

type externalQuery struct {
	Name string `yaml:"name" json:"name"`
	Desc string `yaml:"desc" json:"desc"`
	SQL  string `yaml:"sql" json:"sql"`

	// Entity and Measure name the result columns a concentration reading is
	// computed over. Both or neither: a share taken over the wrong column is a
	// confidently wrong percentage, so an unset pair simply gets no reading.
	Entity  string `yaml:"entity" json:"entity,omitempty"`
	Measure string `yaml:"measure" json:"measure,omitempty"`
}

type externalBind struct {
	ID    string `yaml:"id" json:"id"`
	Table string `yaml:"table" json:"table"`
	Name  string `yaml:"name" json:"name"`
	Rows  int64  `yaml:"rows" json:"rows,omitempty"`
	Notes string `yaml:"notes" json:"notes,omitempty"`

	// Columns maps a concept column onto this portal's column, or onto any SQL
	// expression over it. When non-empty it is authoritative — see the doc
	// comment on BoundDataset.Columns for why a missing entry must mean
	// "unavailable here" rather than "not mapped yet".
	Columns map[string]string `yaml:"columns" json:"columns,omitempty"`
}

// DefaultModesDir is where csq looks for external modes and bindings.
func DefaultModesDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".csq", "modes")
}

// LoadDir reads every .yaml/.yml file in dir and registers what it finds.
// A missing directory is not an error — most users have none.
//
// Files are applied in two passes so a binding may reference a mode defined in
// a file loaded later, which matters because directory order is not something a
// user should have to reason about.
func LoadDir(dir string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read modes directory %s: %w", dir, err)
	}

	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if isModeFileExt(e.Name()) {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)

	parsed := make([]*externalFile, 0, len(paths))
	loaded := make([]string, 0, len(paths))
	for _, p := range paths {
		f, err := parseFile(p)
		if err != nil {
			return loaded, err
		}
		parsed = append(parsed, f)
	}

	// Pass 1: modes, so bindings in any file can find them.
	for i, f := range parsed {
		if f.Kind != kindMode {
			continue
		}
		if err := applyMode(f, paths[i]); err != nil {
			return loaded, err
		}
		loaded = append(loaded, paths[i])
	}
	// Pass 2: bindings.
	for i, f := range parsed {
		if f.Kind != kindBinding {
			continue
		}
		if err := applyBinding(f, paths[i]); err != nil {
			return loaded, err
		}
		loaded = append(loaded, paths[i])
	}
	sort.Strings(loaded)
	return loaded, nil
}

// LintFile parses and validates one file without registering it, so an author
// can check their work before shipping it.
func LintFile(path string) (kind string, name string, err error) {
	f, err := parseFile(path)
	if err != nil {
		return "", "", err
	}
	switch f.Kind {
	case kindMode:
		if err := validateMode(f, path); err != nil {
			return "", "", err
		}
		return "mode", f.Name, nil
	case kindBinding:
		if err := validateBinding(f, path, true); err != nil {
			return "", "", err
		}
		return "binding", f.Mode + " → " + f.Portal, nil
	}
	return "", "", fmt.Errorf("%s: unreachable", path)
}

// isModeFileExt reports whether a filename is one csq will try to load.
func isModeFileExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".yaml", ".yml", ".json":
		return true
	}
	return false
}

func parseFile(path string) (*externalFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	f, err := decodeDocument(path, data)
	if err != nil {
		return nil, err
	}
	switch f.Kind {
	case kindMode, kindBinding:
	case "":
		return nil, fmt.Errorf("%s: missing 'kind'; the document needs "+
			"\"kind\": \"mode\" or \"kind\": \"binding\" at its top level",
			filepath.Base(path))
	default:
		return nil, fmt.Errorf("%s: kind %q is not valid; use 'mode' or 'binding'",
			filepath.Base(path), f.Kind)
	}
	return f, nil
}

// decodeDocument parses one file as JSON or YAML according to its extension.
//
// Both decoders are put in strict mode, so an unrecognised key fails the load
// rather than being dropped. That matters more than it looks: the keys here
// carry the caveats and the column mappings, and a silently ignored "caveats"
// or "columns" would produce a mode that runs and answers wrongly, which is
// worse than one that refuses to load.
func decodeDocument(path string, data []byte) (*externalFile, error) {
	var f externalFile
	base := filepath.Base(path)

	if strings.EqualFold(filepath.Ext(path), ".json") {
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&f); err != nil {
			return nil, fmt.Errorf("%s: %w\n  (csq rejects unknown keys, so a mistyped "+
				"field is reported here rather than ignored; run 'csq modes schema' "+
				"for the exact shape)", base, err)
		}
		// A second document in the same file would be silently ignored by
		// Decode, which reads only the first value.
		if err := trailingJSON(dec); err != nil {
			return nil, fmt.Errorf("%s: %w", base, err)
		}
		return &f, nil
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // typos are errors, not silent no-ops
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w\n  (check indentation and spelling; "+
			"csq rejects unknown keys so a typo is caught here rather than ignored)",
			base, err)
	}
	return &f, nil
}

// trailingJSON reports content after the first JSON value. One file holds one
// document; a second would otherwise load as nothing at all.
func trailingJSON(dec *json.Decoder) error {
	if _, err := dec.Token(); err == nil {
		return fmt.Errorf("contains more than one JSON document; " +
			"put each mode or binding in its own file")
	}
	return nil
}

func validateMode(f *externalFile, path string) error {
	base := filepath.Base(path)
	if f.Name == "" {
		return fmt.Errorf("%s: a mode needs a 'name' (lowercase, no spaces)", base)
	}
	if f.Name != strings.ToLower(f.Name) || strings.ContainsAny(f.Name, " \t") {
		return fmt.Errorf("%s: mode name %q must be lowercase with no spaces", base, f.Name)
	}
	if f.Title == "" || f.Summary == "" || f.About == "" {
		return fmt.Errorf("%s: mode %q needs 'title', 'summary', and 'about'", base, f.Name)
	}
	if len(f.Queries) == 0 {
		return fmt.Errorf("%s: mode %q has no queries", base, f.Name)
	}
	// Caveats are required for the same reason the built-in modes enforce them:
	// these profiles report on procurement and policing, and a number without
	// its limits is how civic data gets misread.
	if len(f.Caveats) == 0 {
		return fmt.Errorf("%s: mode %q has no 'caveats'. Every mode must state what "+
			"its numbers cannot show — this is required, not advisory", base, f.Name)
	}

	declared := map[string]bool{}
	for _, c := range f.Concepts {
		if c.Name == "" || c.Purpose == "" {
			return fmt.Errorf("%s: every concept needs a 'name' and a 'purpose'", base)
		}
		if declared[c.Name] {
			return fmt.Errorf("%s: concept %q is declared twice", base, c.Name)
		}
		declared[c.Name] = true
	}

	seenQ := map[string]bool{}
	for _, q := range f.Queries {
		if q.Name == "" || q.Desc == "" || strings.TrimSpace(q.SQL) == "" {
			return fmt.Errorf("%s: every query needs 'name', 'desc', and 'sql'", base)
		}
		if seenQ[q.Name] {
			return fmt.Errorf("%s: query %q is defined twice", base, q.Name)
		}
		seenQ[q.Name] = true

		// A concentration reading needs both halves. One without the other is
		// not a partial feature — it is a column name with nothing to compute
		// against, and accepting it silently would hide the author's mistake
		// behind a result that simply never reports concentration.
		if (q.Entity == "") != (q.Measure == "") {
			return fmt.Errorf("%s: query %q sets only one of 'entity' and 'measure'. "+
				"Set both to get a concentration reading, or neither to skip it",
				base, q.Name)
		}

		refs := conceptRefs(q.SQL)
		if len(refs) == 0 {
			return fmt.Errorf("%s: query %q references no concept. Table names must be "+
				"written as {{c:name}} so the query works on any city that binds it",
				base, q.Name)
		}
		for _, r := range refs {
			if !declared[r] {
				return fmt.Errorf("%s: query %q uses {{c:%s}} but the mode declares no "+
					"concept named %q. Add it under 'concepts:' or fix the spelling",
					base, q.Name, r, r)
			}
		}
	}
	return nil
}

func validateBinding(f *externalFile, path string, standalone bool) error {
	base := filepath.Base(path)
	if f.Mode == "" || f.Portal == "" {
		return fmt.Errorf("%s: a binding needs 'mode' and 'portal'", base)
	}
	if f.City == "" {
		return fmt.Errorf("%s: binding for %s needs a 'city' label, e.g. \"Seattle, WA\"",
			base, f.Portal)
	}
	if len(f.Datasets) == 0 {
		return fmt.Errorf("%s: binding for %s lists no datasets", base, f.Portal)
	}
	for name, d := range f.Datasets {
		if d.ID == "" || d.Table == "" || d.Name == "" {
			return fmt.Errorf("%s: dataset %q needs 'id', 'table', and 'name'", base, name)
		}
	}
	// A denominator without a citation cannot be used in a comparison someone
	// might act on, so the source is mandatory whenever a population is given.
	if f.Population > 0 && strings.TrimSpace(f.PopulationSource) == "" {
		return fmt.Errorf("%s: binding for %s sets 'population' but no "+
			"'population_source'. Record where the figure came from, e.g. "+
			"\"2020 Decennial Census, table P1\"", base, f.Portal)
	}
	if f.PopulationSource != "" && f.Population <= 0 {
		return fmt.Errorf("%s: binding for %s has a 'population_source' but no "+
			"'population'", base, f.Portal)
	}

	// Cross-check against the mode when it is known. During a standalone lint
	// the mode may live in a file not yet loaded, so an unknown mode is a
	// warning path rather than a hard failure.
	m, err := Lookup(f.Mode)
	if err != nil {
		if standalone {
			return nil
		}
		return fmt.Errorf("%s: binding refers to mode %q, which does not exist. "+
			"Define it in a 'kind: mode' file or check the spelling", base, f.Mode)
	}
	for name, d := range f.Datasets {
		c, ok := m.Concept(name)
		if !ok {
			known := make([]string, 0, len(m.Concepts))
			for _, k := range m.Concepts {
				known = append(known, k.Name)
			}
			return fmt.Errorf("%s: mode %q has no concept named %q. "+
				"Bindings map concepts, not arbitrary names (mode %q declares: %s)",
				base, f.Mode, name, f.Mode, strings.Join(known, ", "))
		}
		// A non-empty column map is authoritative, so a required column absent
		// from it is not "not mapped yet" — it is a column this portal cannot
		// supply, and every query reading it would project a column that is not
		// there. Catching that here is what keeps it from surfacing as a binder
		// error in the middle of an answer, or worse, as a NULL that reads like
		// a real value.
		if len(d.Columns) == 0 {
			continue
		}
		var missing []string
		for _, col := range c.Required {
			if _, ok := d.Columns[col]; !ok {
				missing = append(missing, col)
			}
		}
		if len(missing) > 0 {
			return fmt.Errorf("%s: dataset %q maps 'columns' but omits %s, which concept "+
				"%q requires. Add %s (the value may be any SQL expression over this "+
				"portal's columns), or remove this concept from the binding so queries "+
				"needing it exclude this portal by name",
				base, name, strings.Join(missing, ", "), c.Name, strings.Join(missing, ", "))
		}
	}
	return nil
}

func applyMode(f *externalFile, path string) error {
	if err := validateMode(f, path); err != nil {
		return err
	}
	m := &Mode{
		Name:    f.Name,
		Title:   f.Title,
		Summary: f.Summary,
		About:   f.About,
		Caveats: f.Caveats,
		Source:  path,
	}
	for _, c := range f.Concepts {
		m.Concepts = append(m.Concepts, Concept{
			Name: c.Name, Purpose: c.Purpose,
			Required: c.Required, Optional: c.Optional,
		})
	}
	for _, q := range f.Queries {
		m.Queries = append(m.Queries, Query{
			Name: q.Name, Desc: q.Desc, SQL: q.SQL,
			Entity: q.Entity, Measure: q.Measure,
		})
	}

	// An external mode replaces a built-in of the same name, so a user can fix
	// or extend a shipped mode without rebuilding csq.
	for i, existing := range registry {
		if existing.Name == m.Name {
			registry[i] = m
			return nil
		}
	}
	registry = append(registry, m)
	return nil
}

func applyBinding(f *externalFile, path string) error {
	if err := validateBinding(f, path, false); err != nil {
		return err
	}
	b := &Binding{
		Mode:             f.Mode,
		Portal:           f.Portal,
		City:             f.City,
		Population:       f.Population,
		PopulationSource: f.PopulationSource,
		Notes:            f.Notes,
		Source:           path,
		Concepts:         make(map[string]BoundDataset, len(f.Datasets)),
	}
	for name, d := range f.Datasets {
		b.Concepts[name] = BoundDataset{
			ID: d.ID, Table: d.Table, Name: d.Name, Rows: d.Rows, Notes: d.Notes,
			Columns: d.Columns,
		}
	}
	registerBinding(b)
	return nil
}
