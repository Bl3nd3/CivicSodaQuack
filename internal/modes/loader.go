// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"bytes"
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
	Kind fileKind `yaml:"kind"`

	// Mode fields (kind: mode)
	Name     string          `yaml:"name"`
	Title    string          `yaml:"title"`
	Summary  string          `yaml:"summary"`
	About    string          `yaml:"about"`
	Concepts []externalConc  `yaml:"concepts"`
	Queries  []externalQuery `yaml:"queries"`
	Caveats  []string        `yaml:"caveats"`

	// Binding fields (kind: binding)
	Mode     string                  `yaml:"mode"`
	Portal   string                  `yaml:"portal"`
	City     string                  `yaml:"city"`
	Datasets map[string]externalBind `yaml:"datasets"`
	Notes    []string                `yaml:"notes"`
}

type externalConc struct {
	Name     string   `yaml:"name"`
	Purpose  string   `yaml:"purpose"`
	Required []string `yaml:"required"`
	Optional []string `yaml:"optional"`
}

type externalQuery struct {
	Name string `yaml:"name"`
	Desc string `yaml:"desc"`
	SQL  string `yaml:"sql"`
}

type externalBind struct {
	ID    string `yaml:"id"`
	Table string `yaml:"table"`
	Name  string `yaml:"name"`
	Rows  int64  `yaml:"rows"`
	Notes string `yaml:"notes"`
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
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext == ".yaml" || ext == ".yml" {
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

func parseFile(path string) (*externalFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var f externalFile
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true) // typos are errors, not silent no-ops
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("%s: %w\n  (check indentation and spelling; "+
			"csq rejects unknown keys so a typo is caught here rather than ignored)",
			filepath.Base(path), err)
	}
	switch f.Kind {
	case kindMode, kindBinding:
	case "":
		return nil, fmt.Errorf("%s: missing 'kind'; the first line should be "+
			"'kind: mode' or 'kind: binding'", filepath.Base(path))
	default:
		return nil, fmt.Errorf("%s: kind %q is not valid; use 'mode' or 'binding'",
			filepath.Base(path), f.Kind)
	}
	return &f, nil
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
	for name := range f.Datasets {
		if _, ok := m.Concept(name); !ok {
			known := make([]string, 0, len(m.Concepts))
			for _, c := range m.Concepts {
				known = append(known, c.Name)
			}
			return fmt.Errorf("%s: mode %q has no concept named %q. "+
				"Bindings map concepts, not arbitrary names (mode %q declares: %s)",
				base, f.Mode, name, f.Mode, strings.Join(known, ", "))
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
		m.Queries = append(m.Queries, Query{Name: q.Name, Desc: q.Desc, SQL: q.SQL})
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
		Mode:     f.Mode,
		Portal:   f.Portal,
		City:     f.City,
		Notes:    f.Notes,
		Source:   path,
		Concepts: make(map[string]BoundDataset, len(f.Datasets)),
	}
	for name, d := range f.Datasets {
		b.Concepts[name] = BoundDataset{
			ID: d.ID, Table: d.Table, Name: d.Name, Rows: d.Rows, Notes: d.Notes,
		}
	}
	registerBinding(b)
	return nil
}
