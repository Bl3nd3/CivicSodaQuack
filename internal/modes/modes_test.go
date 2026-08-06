// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"strings"
	"testing"
)

// TestRegistryIntegrity guards the invariants that make a mode usable: unique
// names, non-empty prose, and — because these modes report on procurement and
// policing — that every one carries interpretation caveats.
func TestRegistryIntegrity(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range All() {
		if m.Name == "" {
			t.Fatalf("mode with empty name")
		}
		if seen[m.Name] {
			t.Errorf("duplicate mode name %q", m.Name)
		}
		seen[m.Name] = true

		if m.Name != strings.ToLower(m.Name) {
			t.Errorf("mode %q: name must be lowercase (Lookup folds case)", m.Name)
		}
		if m.Title == "" || m.Summary == "" || m.About == "" {
			t.Errorf("mode %q: Title, Summary and About are all required", m.Name)
		}
		if len(m.Queries) == 0 {
			t.Errorf("mode %q: has no queries", m.Name)
		}
		if len(m.Caveats) == 0 {
			t.Errorf("mode %q: has no caveats; every mode must state its limits", m.Name)
		}

		qseen := map[string]bool{}
		for _, q := range m.Queries {
			if q.Name == "" || q.Desc == "" || strings.TrimSpace(q.SQL) == "" {
				t.Errorf("mode %q: query %q is incomplete", m.Name, q.Name)
			}
			if qseen[q.Name] {
				t.Errorf("mode %q: duplicate query name %q", m.Name, q.Name)
			}
			qseen[q.Name] = true
		}

		tseen := map[string]bool{}
		for _, d := range m.Datasets {
			if d.ID == "" || d.Table == "" || d.Name == "" || d.Why == "" {
				t.Errorf("mode %q: dataset %q is incomplete", m.Name, d.ID)
			}
			if tseen[d.Table] {
				t.Errorf("mode %q: duplicate table name %q", m.Name, d.Table)
			}
			tseen[d.Table] = true
		}
	}
}

// TestQueryPlaceholdersMatchScope ensures single-portal modes use {{P}} and
// cross-portal modes use the union placeholders — mixing them yields SQL that
// cannot be expanded.
func TestQueryPlaceholdersMatchScope(t *testing.T) {
	for _, m := range All() {
		for _, q := range m.Queries {
			hasSingle := strings.Contains(q.SQL, PlaceholderPortal)
			hasUnion := strings.Contains(q.SQL, PlaceholderCatalog) ||
				strings.Contains(q.SQL, PlaceholderSyncRuns)

			if m.MultiPortal && hasSingle {
				t.Errorf("mode %q query %q: cross-portal mode uses %s",
					m.Name, q.Name, PlaceholderPortal)
			}
			if !m.MultiPortal && hasUnion {
				t.Errorf("mode %q query %q: single-portal mode uses a union placeholder",
					m.Name, q.Name)
			}
			if !hasSingle && !hasUnion {
				t.Errorf("mode %q query %q: no portal placeholder; SQL would not resolve",
					m.Name, q.Name)
			}
		}
	}
}

// TestSinglePortalQueriesReferenceDeclaredTables catches the failure mode where
// a query is written against a table the mode never syncs.
func TestSinglePortalQueriesReferenceDeclaredTables(t *testing.T) {
	for _, m := range All() {
		if m.MultiPortal {
			continue
		}
		declared := map[string]bool{}
		for _, d := range m.Datasets {
			declared[d.Table] = true
		}
		for _, q := range m.Queries {
			for _, ref := range tableRefs(q.SQL) {
				if !declared[ref] {
					t.Errorf("mode %q query %q references undeclared table %q",
						m.Name, q.Name, ref)
				}
			}
		}
	}
}

// tableRefs extracts the table names a query reads via the {{P}}.main.<table>
// convention used by single-portal modes.
func tableRefs(sqlText string) []string {
	const prefix = PlaceholderPortal + ".main."
	var out []string
	rest := sqlText
	for {
		i := strings.Index(rest, prefix)
		if i < 0 {
			return out
		}
		rest = rest[i+len(prefix):]
		end := strings.IndexFunc(rest, func(r rune) bool {
			return !(r == '_' || (r >= 'a' && r <= 'z') ||
				(r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
		})
		if end < 0 {
			end = len(rest)
		}
		if end > 0 {
			out = append(out, rest[:end])
		}
	}
}

func TestExpandSinglePortal(t *testing.T) {
	got, err := Expand("SELECT * FROM {{P}}.main.contracts", []string{"chi"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if want := "SELECT * FROM chi.main.contracts"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestExpandSinglePortalRejectsMultipleDBs(t *testing.T) {
	if _, err := Expand("SELECT * FROM {{P}}.main.x", []string{"a", "b"}); err == nil {
		t.Error("expected an error when a {{P}} query gets two portals")
	}
}

func TestExpandUnionsCatalog(t *testing.T) {
	got, err := Expand("SELECT portal FROM {{CATALOG}}", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	for _, want := range []string{
		"SELECT 'a' AS portal, * FROM a._csq.catalog",
		"UNION ALL",
		"SELECT 'b' AS portal, * FROM b._csq.catalog",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("expansion missing %q\ngot: %s", want, got)
		}
	}
}

func TestAliasFor(t *testing.T) {
	cases := map[string]string{
		"data.cityofchicago.org.duckdb":            "data_cityofchicago_org",
		"/tmp/datacatalog.cookcountyil.gov.duckdb": "datacatalog_cookcountyil_gov",
		"311.duckdb":         "p_311",
		"weird!!name.duckdb": "weird_name",
	}
	for in, want := range cases {
		if got := AliasFor(in); got != want {
			t.Errorf("AliasFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUniqueAliasesDisambiguates(t *testing.T) {
	got := UniqueAliases([]string{"/a/chi.duckdb", "/b/chi.duckdb"})
	if got[0] == got[1] {
		t.Errorf("aliases collided: %v", got)
	}
}

func TestConfigYAMLIncludesEveryDataset(t *testing.T) {
	m, err := Lookup("corruption")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	yaml, err := m.ConfigYAML()
	if err != nil {
		t.Fatalf("ConfigYAML: %v", err)
	}
	for _, d := range m.Datasets {
		if !strings.Contains(yaml, d.ID) {
			t.Errorf("config missing dataset id %q", d.ID)
		}
		if !strings.Contains(yaml, "table: "+d.Table) {
			t.Errorf("config missing table override for %q", d.Table)
		}
	}
	if !strings.Contains(yaml, "portal: "+m.Portal) {
		t.Errorf("config missing portal line")
	}
}

// TestConfigYAMLRejectsDatasetlessModes documents that `modes init ranking` is
// meaningless: the mode reads databases you already built.
func TestConfigYAMLRejectsDatasetlessModes(t *testing.T) {
	m, err := Lookup("ranking")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if _, err := m.ConfigYAML(); err == nil {
		t.Error("expected ConfigYAML to fail for a mode with no datasets")
	}
}

func TestLookupIsCaseInsensitive(t *testing.T) {
	if _, err := Lookup("  Corruption "); err != nil {
		t.Errorf("Lookup should fold case and trim: %v", err)
	}
	if _, err := Lookup("nope"); err == nil {
		t.Error("expected an error for an unknown mode")
	}
}
