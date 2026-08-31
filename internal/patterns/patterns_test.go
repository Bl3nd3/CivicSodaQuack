// Copyright (c) 2026 Neomantra Corp

package patterns

import (
	"strings"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

func contractsTable() personal.Table {
	return personal.Table{
		Name: "contracts", Rows: 7,
		DatasetID: "abcd-1234", DatasetName: "City Contracts",
		Columns: []personal.Column{
			{Name: "vendor_nm", Type: "VARCHAR"},
			{Name: "dept", Type: "VARCHAR"},
			{Name: "amt", Type: "VARCHAR"},     // money as text: the common case
			{Name: "awarded", Type: "VARCHAR"}, // date as text
			{Name: "paid", Type: "DOUBLE"},
			{Name: "signed", Type: "DATE"},
		},
	}
}

func buildReq(pattern string, cols map[Role]string) BuildRequest {
	p, _ := Lookup(pattern)
	return BuildRequest{
		Pattern: p, Table: contractsTable(), ModeName: "personal",
		Portal: "data.example.gov", City: "Example, EX", Columns: cols,
	}
}

// Every pattern must satisfy the same rules a hand-written mode does, since a
// pattern's output *is* a mode file. A pattern that ships without caveats, or
// whose template is not read-only, is a defect that would otherwise only
// surface when a user ran it.
func TestEveryPatternIsWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range All() {
		if p.Name == "" || p.Summary == "" || p.About == "" {
			t.Errorf("pattern %q: name, summary and about are all required", p.Name)
		}
		if seen[p.Name] {
			t.Errorf("duplicate pattern name %q", p.Name)
		}
		seen[p.Name] = true

		if len(p.Caveats) == 0 {
			t.Errorf("pattern %q has no caveats; the caveats are the reason a pattern "+
				"is better than an ad-hoc query", p.Name)
		}
		if (p.Entity == "") != (p.Measure == "") {
			t.Errorf("pattern %q sets only one of Entity/Measure", p.Name)
		}
		for _, param := range p.Params {
			if param.Flag == "" || param.Desc == "" {
				t.Errorf("pattern %q: every param needs a flag and a description", p.Name)
			}
			if param.Numeric && param.Temporal {
				t.Errorf("pattern %q: %s cannot be both numeric and temporal",
					p.Name, param.Flag)
			}
		}
		// coverage builds its SQL per-column, so it alone has no template.
		if p.Name == coverage.Name {
			continue
		}
		if !strings.Contains(p.SQL, ConceptToken) {
			t.Errorf("pattern %q does not reference %s, so it would name a table "+
				"directly and only work on one portal", p.Name, ConceptToken)
		}
		if err := personal.CheckReadOnly(strings.ReplaceAll(p.SQL, ConceptToken, "{{c:t}}")); err != nil {
			t.Errorf("pattern %q is not read-only: %v", p.Name, err)
		}
	}
}

// Whatever a pattern emits must load. This runs the generated documents through
// the same validator that reads a hand-written file, which is what guarantees
// the two paths cannot drift.
func TestBuiltModesPassTheLoader(t *testing.T) {
	cases := []struct {
		pattern string
		cols    map[Role]string
		format  string
	}{
		{"top-n", map[Role]string{RoleEntity: "vendor_nm", RoleMeasure: "amt"}, ""},
		{"concentration", map[Role]string{
			RoleGroup: "dept", RoleEntity: "vendor_nm", RoleMeasure: "paid"}, ""},
		{"trend", map[Role]string{RoleDate: "signed"}, ""},
		{"trend", map[Role]string{RoleDate: "awarded", RoleMeasure: "amt"}, "%m/%d/%Y"},
		{"breakdown", map[Role]string{RoleCategory: "dept"}, ""},
		{"breakdown", map[Role]string{RoleCategory: "dept", RoleMeasure: "paid"}, ""},
		{"coverage", map[Role]string{}, ""},
		{"name-variants", map[Role]string{RoleEntity: "vendor_nm"}, ""},
	}

	for _, tc := range cases {
		t.Run(tc.pattern, func(t *testing.T) {
			req := buildReq(tc.pattern, tc.cols)
			req.DateFormat = tc.format
			req.ModeName = "pattern-" + tc.pattern

			draft, err := Build(req)
			if err != nil {
				t.Fatalf("build: %v", err)
			}
			if len(draft.Mode.Queries) != 1 {
				t.Fatalf("want one query, got %d", len(draft.Mode.Queries))
			}
			q := draft.Mode.Queries[0]

			if err := personal.CheckReadOnly(q.SQL); err != nil {
				t.Errorf("generated SQL is not read-only: %v", err)
			}
			refs := modes.ConceptRefs(q.SQL)
			if len(refs) == 0 {
				t.Error("generated SQL names its table directly")
			}
			// Every concept the query reads must be bound, or the mode cannot run.
			for _, r := range refs {
				if _, ok := draft.Binding.Datasets[r]; !ok {
					t.Errorf("query reads {{c:%s}} but the binding does not map it", r)
				}
			}
			if len(draft.Mode.Caveats) == 0 {
				t.Error("generated mode has no caveats")
			}
			// Every required concept column must appear in the binding's map,
			// which is what the loader enforces.
			ds := draft.Binding.Datasets[refs[0]]
			for _, col := range draft.Mode.Concepts[0].Required {
				if _, ok := ds.Columns[col]; !ok {
					t.Errorf("required column %q is not mapped in the binding", col)
				}
			}
		})
	}
}

// A text money column must be cast, or SUM either errors or silently coerces.
func TestBuild_CastsTextMeasure(t *testing.T) {
	draft, err := Build(buildReq("top-n", map[Role]string{
		RoleEntity: "vendor_nm", RoleMeasure: "amt",
	}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := draft.Binding.Datasets["contracts"].Columns["measure"]
	if !strings.Contains(got, "TRY_CAST") {
		t.Errorf("a VARCHAR measure should be cast, got %q", got)
	}
}

// An already-numeric column must not be wrapped: a needless cast is noise in a
// file the user is meant to read.
func TestBuild_LeavesNumericMeasureAlone(t *testing.T) {
	draft, err := Build(buildReq("top-n", map[Role]string{
		RoleEntity: "vendor_nm", RoleMeasure: "paid",
	}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := draft.Binding.Datasets["contracts"].Columns["measure"]
	if strings.Contains(got, "CAST") {
		t.Errorf("a DOUBLE measure needs no cast, got %q", got)
	}
}

// Guessing between MM/DD and DD/MM mislabels a third of the year without ever
// erroring, so csq refuses rather than picking.
func TestBuild_RefusesTextDateWithoutAFormat(t *testing.T) {
	_, err := Build(buildReq("trend", map[Role]string{RoleDate: "awarded"}))
	if err == nil {
		t.Fatal("a text date column with no --date-format should be refused")
	}
	if !strings.Contains(err.Error(), "--date-format") {
		t.Errorf("the error should name the flag that fixes it: %v", err)
	}
}

func TestBuild_UsesDateFormatWhenGiven(t *testing.T) {
	req := buildReq("trend", map[Role]string{RoleDate: "awarded"})
	req.DateFormat = "%m/%d/%Y"
	draft, err := Build(req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := draft.Binding.Datasets["contracts"].Columns["event_date"]
	if !strings.Contains(got, "try_strptime") || !strings.Contains(got, "%m/%d/%Y") {
		t.Errorf("the date should be parsed with the given format, got %q", got)
	}
}

// A real DATE column needs no parsing, and demanding a format for one would be
// a pointless obstacle.
func TestBuild_AcceptsARealDateColumn(t *testing.T) {
	if _, err := Build(buildReq("trend", map[Role]string{RoleDate: "signed"})); err != nil {
		t.Fatalf("a DATE column should need no format: %v", err)
	}
}

func TestBuild_RejectsUnknownColumn(t *testing.T) {
	_, err := Build(buildReq("top-n", map[Role]string{
		RoleEntity: "nope", RoleMeasure: "amt",
	}))
	if err == nil {
		t.Fatal("an unknown column should be refused")
	}
	// The message must list what is available, or the user cannot act on it.
	if !strings.Contains(err.Error(), "vendor_nm") {
		t.Errorf("the error should list the real columns: %v", err)
	}
}

func TestBuild_RequiresRequiredRoles(t *testing.T) {
	// concentration needs all three; omitting the measure must be refused.
	_, err := Build(buildReq("concentration", map[Role]string{
		RoleGroup: "dept", RoleEntity: "vendor_nm",
	}))
	if err == nil {
		t.Fatal("a missing required role should be refused")
	}
	if !strings.Contains(err.Error(), "--measure") {
		t.Errorf("the error should name the missing flag: %v", err)
	}
}

// top-n without a measure ranks by record count — "the most permits" is as
// ordinary a question as "the most money", and forcing a sum on it is what made
// the router reach for meaningless numeric columns.
func TestBuild_TopNCountsWithoutAMeasure(t *testing.T) {
	draft, err := Build(buildReq("top-n", map[Role]string{RoleEntity: "vendor_nm"}))
	if err != nil {
		t.Fatalf("top-n should build without a measure: %v", err)
	}
	q := draft.Mode.Queries[0]
	if !strings.Contains(q.SQL, "COUNT(*)") {
		t.Errorf("expected a count ranking:\n%s", q.SQL)
	}
	if strings.Contains(q.SQL, "SUM(measure)") {
		t.Errorf("no measure was given, so nothing should be summed:\n%s", q.SQL)
	}
	if q.Measure != "records" {
		t.Errorf("the concentration reading should follow the count, got %q", q.Measure)
	}
	if _, ok := draft.Binding.Datasets["contracts"].Columns["measure"]; ok {
		t.Error("no measure column should be mapped")
	}
}

func TestBuild_TopNSumsWhenGivenAMeasure(t *testing.T) {
	draft, err := Build(buildReq("top-n", map[Role]string{
		RoleEntity: "vendor_nm", RoleMeasure: "paid",
	}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	q := draft.Mode.Queries[0]
	if !strings.Contains(q.SQL, "SUM(measure)") || !strings.Contains(q.SQL, "ORDER BY total DESC") {
		t.Errorf("expected a summed ranking:\n%s", q.SQL)
	}
	// The record count stays: one contract worth $10M and a thousand worth
	// $10M each are different findings.
	if !strings.Contains(q.SQL, "COUNT(*)") {
		t.Errorf("the record count should remain alongside the total:\n%s", q.SQL)
	}
	if q.Measure != "total" {
		t.Errorf("measure = %q, want total", q.Measure)
	}
}

// The optional measure changes the query, and its absence must not leave a
// dangling reference to an unmapped column.
func TestBuild_OptionalMeasureIsTrulyOptional(t *testing.T) {
	draft, err := Build(buildReq("trend", map[Role]string{RoleDate: "signed"}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	q := draft.Mode.Queries[0]
	if strings.Contains(q.SQL, "measure") {
		t.Errorf("without --measure the SQL must not mention it:\n%s", q.SQL)
	}
	if _, ok := draft.Binding.Datasets["contracts"].Columns["measure"]; ok {
		t.Error("without --measure the binding must not map one")
	}
	for _, c := range draft.Mode.Concepts[0].Required {
		if c == "measure" {
			t.Error("measure must not be declared required when it was not supplied")
		}
	}
}

func TestBuild_WithMeasureAddsTheSum(t *testing.T) {
	req := buildReq("trend", map[Role]string{RoleDate: "signed", RoleMeasure: "paid"})
	draft, err := Build(req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	q := draft.Mode.Queries[0]
	if !strings.Contains(q.SQL, "SUM(measure)") {
		t.Errorf("a supplied measure should be summed:\n%s", q.SQL)
	}
	if q.Measure != "total" {
		t.Errorf("the concentration measure should point at the summed column, got %q", q.Measure)
	}
}

// A concept name is what makes two portals share one mode, so it must be
// settable rather than always derived from a local table name.
func TestBuild_HonoursAnExplicitConcept(t *testing.T) {
	req := buildReq("top-n", map[Role]string{RoleEntity: "vendor_nm", RoleMeasure: "paid"})
	req.Concept = "procurement"
	draft, err := Build(req)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(draft.Mode.Queries[0].SQL, "{{c:procurement}}") {
		t.Error("the query should read the named concept")
	}
	if _, ok := draft.Binding.Datasets["procurement"]; !ok {
		t.Error("the binding should map the named concept")
	}
}

// Coverage profiles the table it is given, so it must name every column and
// still be a single read-only statement.
func TestBuild_CoverageProfilesEveryColumn(t *testing.T) {
	draft, err := Build(buildReq("coverage", map[Role]string{}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	sqlText := draft.Mode.Queries[0].SQL
	for _, c := range contractsTable().Columns {
		if !strings.Contains(sqlText, c.Name) {
			t.Errorf("coverage omits column %q", c.Name)
		}
	}
	if err := personal.CheckReadOnly(sqlText); err != nil {
		t.Errorf("coverage SQL is not read-only: %v", err)
	}
}

// Two instantiations in one mode must not collide, or the second silently
// replaces the first.
func TestDefaultQueryNamesAreDistinct(t *testing.T) {
	a, err := Build(buildReq("top-n", map[Role]string{RoleEntity: "vendor_nm", RoleMeasure: "paid"}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	b, err := Build(buildReq("top-n", map[Role]string{RoleEntity: "dept", RoleMeasure: "paid"}))
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if a.Mode.Queries[0].Name == b.Mode.Queries[0].Name {
		t.Errorf("two instantiations produced the same name %q", a.Mode.Queries[0].Name)
	}
}

// A quote in a format string would break out of the SQL literal.
func TestBuild_RejectsQuoteInDateFormat(t *testing.T) {
	req := buildReq("trend", map[Role]string{RoleDate: "awarded"})
	req.DateFormat = "%Y' || (SELECT 1) || '"
	if _, err := Build(req); err == nil {
		t.Fatal("a quote in --date-format should be refused")
	}
}

// Column names are quoted, so a column that needs quoting still works.
func TestBuild_QuotesIdentifiers(t *testing.T) {
	tbl := contractsTable()
	tbl.Columns = append(tbl.Columns, personal.Column{Name: "total amount", Type: "DOUBLE"})
	p, _ := Lookup("top-n")
	draft, err := Build(BuildRequest{
		Pattern: p, Table: tbl, ModeName: "personal", Portal: "p",
		Columns: map[Role]string{RoleEntity: "vendor_nm", RoleMeasure: "total amount"},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := draft.Binding.Datasets["contracts"].Columns["measure"]
	if !strings.Contains(got, `"total amount"`) {
		t.Errorf("a column needing quotes should be quoted, got %q", got)
	}
}
