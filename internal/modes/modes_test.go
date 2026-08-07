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

		cseen := map[string]bool{}
		for _, c := range m.Concepts {
			if c.Name == "" || c.Purpose == "" {
				t.Errorf("mode %q: concept %q is incomplete", m.Name, c.Name)
			}
			if cseen[c.Name] {
				t.Errorf("mode %q: duplicate concept %q", m.Name, c.Name)
			}
			cseen[c.Name] = true
		}
	}
}

// TestQueryPlaceholdersMatchScope ensures each query carries a placeholder its
// mode can actually expand: concept refs for concept-based modes, union
// placeholders for cross-portal ones. Mixing them yields SQL that will not
// resolve at run time.
func TestQueryPlaceholdersMatchScope(t *testing.T) {
	for _, m := range All() {
		for _, q := range m.Queries {
			hasSingle := strings.Contains(q.SQL, PlaceholderPortal)
			hasConcept := len(conceptRefs(q.SQL)) > 0
			hasUnion := strings.Contains(q.SQL, PlaceholderCatalog) ||
				strings.Contains(q.SQL, PlaceholderSyncRuns) ||
				strings.Contains(q.SQL, PlaceholderAliases)

			if m.MultiPortal && (hasSingle || hasConcept) {
				t.Errorf("mode %q query %q: cross-portal mode must not use %s or a concept ref",
					m.Name, q.Name, PlaceholderPortal)
			}
			if !m.MultiPortal && hasUnion {
				t.Errorf("mode %q query %q: concept-based mode uses a union placeholder",
					m.Name, q.Name)
			}
			if !hasSingle && !hasConcept && !hasUnion {
				t.Errorf("mode %q query %q: no placeholder; SQL would not resolve",
					m.Name, q.Name)
			}
		}
	}
}

// TestQueriesReferenceDeclaredConcepts catches a query written against a
// concept the mode never declares — the portable equivalent of referencing a
// table that was never synced.
func TestQueriesReferenceDeclaredConcepts(t *testing.T) {
	for _, m := range All() {
		for _, q := range m.Queries {
			for _, ref := range conceptRefs(q.SQL) {
				if _, ok := m.Concept(ref); !ok {
					t.Errorf("mode %q query %q references undeclared concept %q",
						m.Name, q.Name, ref)
				}
			}
		}
	}
}

// TestBindingsCoverDeclaredConcepts ensures every binding maps only concepts
// the mode declares, and records at least one.
func TestBindingsCoverDeclaredConcepts(t *testing.T) {
	for _, m := range All() {
		for _, b := range BindingsFor(m.Name) {
			if b.Portal == "" || b.City == "" {
				t.Errorf("mode %q: binding missing Portal or City", m.Name)
			}
			if len(b.Concepts) == 0 {
				t.Errorf("mode %q: binding %s has no concepts", m.Name, b.Portal)
			}
			for name, bd := range b.Concepts {
				if _, ok := m.Concept(name); !ok {
					t.Errorf("mode %q binding %s: binds unknown concept %q",
						m.Name, b.Portal, name)
				}
				if bd.ID == "" || bd.Table == "" || bd.Name == "" {
					t.Errorf("mode %q binding %s: concept %q incomplete",
						m.Name, b.Portal, name)
				}
			}
		}
	}
}

// TestConceptModesHaveAtLeastOneBinding keeps a concept-based mode from
// shipping unusable.
func TestConceptModesHaveAtLeastOneBinding(t *testing.T) {
	for _, m := range All() {
		if len(m.Concepts) == 0 {
			continue
		}
		if len(BindingsFor(m.Name)) == 0 {
			t.Errorf("mode %q declares concepts but has no bindings", m.Name)
		}
	}
}

func TestExpandConcepts(t *testing.T) {
	m, _ := Lookup("corruption")
	b, err := LookupBinding("corruption", "data.cityofchicago.org")
	if err != nil {
		t.Fatalf("LookupBinding: %v", err)
	}
	got, err := ExpandConcepts("SELECT * FROM {{c:contracts}}", "chi", b)
	if err != nil {
		t.Fatalf("ExpandConcepts: %v", err)
	}
	if want := "SELECT * FROM chi.main.contracts"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if _, ok := m.Concept("contracts"); !ok {
		t.Error("corruption should declare a contracts concept")
	}
}

func TestRunnableReportsMissingConcepts(t *testing.T) {
	m, _ := Lookup("police")
	partial := &Binding{Mode: "police", Portal: "x", City: "X",
		Concepts: map[string]BoundDataset{
			"complaints": {ID: "a-1", Table: "c", Name: "C"},
		}}
	for _, q := range m.Queries {
		ok, missing := m.Runnable(q, partial)
		if !ok && len(missing) == 0 {
			t.Errorf("query %q not runnable but reported no missing concepts", q.Name)
		}
	}
	if un := m.Unbound(partial); len(un) == 0 {
		t.Error("Unbound should report the concepts this partial binding lacks")
	}
}

func TestPortalFromDBPath(t *testing.T) {
	if got := PortalFromDBPath("/x/data.cityofchicago.org.duckdb"); got != "data.cityofchicago.org" {
		t.Errorf("got %q", got)
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

func TestExpandAliasList(t *testing.T) {
	got, err := Expand("WHERE database_name IN ({{ALIASES}})", []string{"a", "b"})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if want := "WHERE database_name IN ('a', 'b')"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestUniqueAliasesDisambiguates(t *testing.T) {
	got := UniqueAliases([]string{"/a/chi.duckdb", "/b/chi.duckdb"})
	if got[0] == got[1] {
		t.Errorf("aliases collided: %v", got)
	}
}

func TestConfigYAMLForIncludesEveryBoundDataset(t *testing.T) {
	m, _ := Lookup("corruption")
	b, err := LookupBinding("corruption", "data.cityofchicago.org")
	if err != nil {
		t.Fatalf("LookupBinding: %v", err)
	}
	yaml, err := m.ConfigYAMLFor(b)
	if err != nil {
		t.Fatalf("ConfigYAMLFor: %v", err)
	}
	for _, bd := range b.Concepts {
		if !strings.Contains(yaml, bd.ID) {
			t.Errorf("config missing dataset id %q", bd.ID)
		}
		if !strings.Contains(yaml, "table: "+bd.Table) {
			t.Errorf("config missing table override for %q", bd.Table)
		}
	}
	if !strings.Contains(yaml, "portal: "+b.Portal) {
		t.Error("config missing portal line")
	}
}

// TestConfigYAMLRejectsDatasetlessModes documents that `modes init ranking` is
// meaningless: the mode reads databases you already built.
func TestConfigYAMLRejectsDatasetlessModes(t *testing.T) {
	m, _ := Lookup("ranking")
	if len(m.Concepts) != 0 {
		t.Fatal("ranking should declare no concepts")
	}
	if len(BindingsFor("ranking")) != 0 {
		t.Error("ranking should have no bindings")
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
