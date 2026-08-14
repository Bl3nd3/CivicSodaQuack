// Copyright (c) 2026 Neomantra Corp

// Package modes provides curated, ready-to-run analysis profiles ("modes")
// over Socrata portal data.
//
// A mode bundles three things a newcomer would otherwise have to assemble by
// hand: the set of datasets worth syncing for a given civic question, the SQL
// that turns those tables into an answer, and the interpretation caveats that
// keep the answer honest. Modes are data, not code paths — adding one means
// appending to the registry in this package, not touching the CLI.
//
// Modes deliberately do not draw conclusions. They surface public-record
// patterns and state plainly what each pattern can and cannot support.
package modes

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Placeholders substituted into Query.SQL at run time. Single-portal modes use
// Portal; cross-portal modes use the union placeholders, which expand to a
// UNION ALL over every attached portal with a leading `portal` column.
const (
	// PlaceholderPortal expands to the single attached portal's alias.
	PlaceholderPortal = "{{P}}"
	// PlaceholderCatalog expands to a unioned _csq.catalog across all portals.
	PlaceholderCatalog = "{{CATALOG}}"
	// PlaceholderSyncRuns expands to a unioned _csq.sync_runs across all portals.
	PlaceholderSyncRuns = "{{SYNCRUNS}}"
	// PlaceholderAliases expands to a quoted, comma-separated alias list, for
	// scoping engine metadata functions such as duckdb_columns() — which span
	// every attached database — to just the portals the user asked for.
	PlaceholderAliases = "{{ALIASES}}"
)

// Query is one canned analysis a mode can run.
type Query struct {
	Name string // slug, unique within the mode
	Desc string // what the result shows
	SQL  string // may contain the Placeholder* tokens
}

// Mode is a curated analysis profile.
type Mode struct {
	Name    string // slug used on the command line
	Title   string
	Summary string // one line, shown in listings
	About   string // paragraph, shown by `csq modes show`

	// MultiPortal reports whether the mode compares several portals at once.
	// Single-portal modes require exactly one --db; cross-portal modes accept
	// one or more.
	MultiPortal bool

	// Concepts are the logical tables this mode needs, described by content
	// rather than by dataset id. A Binding maps them onto one portal's
	// datasets, which is what lets a mode serve more than one city. Empty for
	// modes that read only the _csq bookkeeping schema (ranking, research).
	Concepts []Concept

	Queries []Query

	// Caveats are interpretation limits. They are printed by `csq modes show`
	// and again above query output, because a number without its caveat is
	// how civic data gets misread.
	Caveats []string

	// Source is the file an external mode was loaded from; empty for built-ins.
	// Shown by `csq modes show` so a user can tell which file to edit.
	Source string
}

// registry is the ordered set of built-in modes.
var registry = []*Mode{
	corruptionMode,
	rankingMode,
	policeMode,
	researchMode,
}

// All returns every registered mode, ordered for display.
func All() []*Mode {
	out := make([]*Mode, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the mode with the given name, matching case-insensitively.
func Lookup(name string) (*Mode, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, m := range registry {
		if m.Name == want {
			return m, nil
		}
	}
	return nil, fmt.Errorf("unknown mode %q (see 'csq modes' for the list)", name)
}

// Names returns every mode name, for usage strings and error messages.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, m := range registry {
		out = append(out, m.Name)
	}
	return out
}

// Query returns the named query within the mode.
func (m *Mode) Query(name string) (*Query, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for i := range m.Queries {
		if m.Queries[i].Name == want {
			return &m.Queries[i], nil
		}
	}
	names := make([]string, 0, len(m.Queries))
	for _, q := range m.Queries {
		names = append(names, q.Name)
	}
	return nil, fmt.Errorf("mode %q has no query %q (have: %s)",
		m.Name, name, strings.Join(names, ", "))
}

// Expand substitutes the placeholder tokens in SQL for the given portal
// aliases. Aliases must be valid SQL identifiers (see AliasFor).
func Expand(sql string, aliases []string) (string, error) {
	if len(aliases) == 0 {
		return "", fmt.Errorf("no portals attached")
	}
	if strings.Contains(sql, PlaceholderPortal) {
		if len(aliases) != 1 {
			return "", fmt.Errorf(
				"this query targets a single portal but %d were attached", len(aliases))
		}
		sql = strings.ReplaceAll(sql, PlaceholderPortal, aliases[0])
	}
	if strings.Contains(sql, PlaceholderCatalog) {
		sql = strings.ReplaceAll(sql, PlaceholderCatalog, unionAll(aliases, "_csq.catalog"))
	}
	if strings.Contains(sql, PlaceholderSyncRuns) {
		sql = strings.ReplaceAll(sql, PlaceholderSyncRuns, unionAll(aliases, "_csq.sync_runs"))
	}
	if strings.Contains(sql, PlaceholderAliases) {
		quoted := make([]string, 0, len(aliases))
		for _, a := range aliases {
			quoted = append(quoted, "'"+a+"'")
		}
		sql = strings.ReplaceAll(sql, PlaceholderAliases, strings.Join(quoted, ", "))
	}
	return sql, nil
}

// unionAll builds a derived table unioning one _csq relation across portals,
// prefixed with a `portal` column naming the source.
func unionAll(aliases []string, relation string) string {
	parts := make([]string, 0, len(aliases))
	for _, a := range aliases {
		parts = append(parts, fmt.Sprintf("SELECT '%s' AS portal, * FROM %s.%s", a, a, relation))
	}
	return "(" + strings.Join(parts, " UNION ALL ") + ")"
}

var nonIdent = regexp.MustCompile(`[^a-zA-Z0-9_]+`)

// AliasFor derives a SQL identifier from a DuckDB file path, so
// `data.cityofchicago.org.duckdb` becomes `data_cityofchicago_org`. This
// mirrors how the MCP server aliases unnamed --db arguments.
func AliasFor(path string) string {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, ".duckdb")
	base = nonIdent.ReplaceAllString(base, "_")
	base = strings.Trim(base, "_")
	if base == "" {
		return "portal"
	}
	if base[0] >= '0' && base[0] <= '9' {
		base = "p_" + base
	}
	return strings.ToLower(base)
}

// UniqueAliases derives an alias per path, disambiguating collisions with a
// numeric suffix so two files with the same basename can both be attached.
func UniqueAliases(paths []string) []string {
	seen := make(map[string]int, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		a := AliasFor(p)
		if n, ok := seen[a]; ok {
			seen[a] = n + 1
			a = fmt.Sprintf("%s_%d", a, n+1)
		} else {
			seen[a] = 1
		}
		out = append(out, a)
	}
	return out
}

// commas formats n with thousands separators.
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// wrap breaks s into lines of at most width characters, on word boundaries.
func wrap(s string, width int) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return nil
	}
	var lines []string
	cur := fields[0]
	for _, f := range fields[1:] {
		if len(cur)+1+len(f) > width {
			lines = append(lines, cur)
			cur = f
			continue
		}
		cur += " " + f
	}
	return append(lines, cur)
}
