// Copyright (c) 2026 Neomantra Corp

package patterns

import (
	"strings"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

func inventory() *personal.Portal {
	return &personal.Portal{
		Alias: "p", Host: "data.example.gov",
		Tables: []personal.Table{
			{
				Name: "contracts", Rows: 7,
				DatasetName: "City Contracts", Description: "Awarded contracts and vendors.",
				Columns: []personal.Column{
					{Name: "vendor_name", Type: "VARCHAR"},
					{Name: "department", Type: "VARCHAR"},
					{Name: "award_amount", Type: "VARCHAR"},
					{Name: "awarded_date", Type: "VARCHAR"},
					{Name: "procurement_type", Type: "VARCHAR"},
				},
			},
			{
				Name: "permits", Rows: 5,
				DatasetName: "Building Permits", Description: "Permits issued for construction.",
				Columns: []personal.Column{
					{Name: "applicant", Type: "VARCHAR"},
					{Name: "work_type", Type: "VARCHAR"},
					{Name: "estimated_cost", Type: "DOUBLE"},
					{Name: "issue_date", Type: "DATE"},
				},
			},
		},
	}
}

// The router's whole justification is that the answer space is small enough to
// rank honestly. These pin that it actually lands on the right shape, the right
// table, and — the part easiest to get subtly wrong — the right role per column.
func TestSuggest_RoutesRealQuestions(t *testing.T) {
	cases := []struct {
		question string
		pattern  string
		table    string
		roles    map[Role]string
	}{
		{
			"which vendors got the most money?", "top-n", "contracts",
			map[Role]string{RoleEntity: "vendor_name", RoleMeasure: "award_amount"},
		},
		{
			"how are permits trending over time?", "trend", "permits",
			map[Role]string{RoleDate: "issue_date"},
		},
		{
			"are any vendor names duplicated?", "name-variants", "contracts",
			map[Role]string{RoleEntity: "vendor_name"},
		},
		{
			"what is the breakdown by procurement type?", "breakdown", "contracts",
			map[Role]string{RoleCategory: "procurement_type"},
		},
		{
			// The role-swap case: department is the group, vendor is the entity.
			// Getting these the wrong way round produces a query that runs and
			// answers a different question, which is the worst kind of wrong.
			"which vendors dominate each department's spending?", "concentration", "contracts",
			map[Role]string{
				RoleGroup: "department", RoleEntity: "vendor_name", RoleMeasure: "award_amount",
			},
		},
		{
			"which applicants pull the most permits by value?", "top-n", "permits",
			map[Role]string{RoleEntity: "applicant", RoleMeasure: "estimated_cost"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.question, func(t *testing.T) {
			s, err := Suggest(tc.question, inventory(), "")
			if err != nil {
				t.Fatalf("suggest: %v", err)
			}
			if s.Pattern.Name != tc.pattern {
				t.Errorf("pattern = %q, want %q", s.Pattern.Name, tc.pattern)
			}
			if s.Table.Name != tc.table {
				t.Errorf("table = %q, want %q", s.Table.Name, tc.table)
			}
			for role, want := range tc.roles {
				if got := s.Columns[role]; got != want {
					t.Errorf("%s = %q, want %q", role, got, want)
				}
			}
			// Every choice must carry its evidence, or the user cannot check it.
			for key, why := range s.Reasons {
				if strings.TrimSpace(why) == "" {
					t.Errorf("choice %q has no stated reason", key)
				}
			}
		})
	}
}

// One column must never fill two roles: a share of a group computed by the same
// column that defines the group is always 100%, and always meaningless.
func TestSuggest_NeverReusesAColumn(t *testing.T) {
	s, err := Suggest("which vendors dominate each department's spending?", inventory(), "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	seen := map[string]Role{}
	for role, col := range s.Columns {
		if prev, dup := seen[col]; dup {
			t.Errorf("column %q used for both %s and %s", col, prev, role)
		}
		seen[col] = role
	}
}

// Refusing is the router's most important behaviour: a wrong guess here is a
// query that runs and answers something else.
func TestSuggest_RefusesAnUnmatchableQuestion(t *testing.T) {
	_, err := Suggest("what is the meaning of life", inventory(), "")
	if err == nil {
		t.Fatal("a question matching no pattern should be refused")
	}
	// The message has to teach the vocabulary, or the user cannot recover.
	for _, want := range []string{"top-n", "csq modes patterns"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

func TestSuggest_RefusesWhenNoTableCanAnswer(t *testing.T) {
	// A portal with nothing numeric cannot answer a "most money" question.
	inv := &personal.Portal{
		Alias: "p", Host: "h",
		Tables: []personal.Table{{
			Name: "notes",
			Columns: []personal.Column{
				{Name: "body", Type: "VARCHAR"},
				{Name: "author", Type: "VARCHAR"},
			},
		}},
	}
	// top-n needs a measure; a table of two text columns has one only weakly,
	// so this must either refuse or not claim a numeric column it lacks.
	s, err := Suggest("which authors wrote the most?", inv, "")
	if err != nil {
		return // refusing is the preferred outcome
	}
	if col := s.Columns[RoleMeasure]; col != "" {
		if typ, _ := columnType(s.Table, col); isNumericType(typ) {
			t.Errorf("claimed a numeric measure %q that does not exist", col)
		}
	}
}

// An explicit --table must win over the router's own guess.
func TestSuggest_HonoursAnExplicitTable(t *testing.T) {
	s, err := Suggest("which columns are missing data?", inventory(), "permits")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if s.Table.Name != "permits" {
		t.Errorf("table = %q, want permits", s.Table.Name)
	}
	if !strings.Contains(s.Reasons["table"], "--table") {
		t.Errorf("the reason should credit the flag, got %q", s.Reasons["table"])
	}
}

func TestSuggest_RejectsAnUnknownExplicitTable(t *testing.T) {
	_, err := Suggest("which vendors got the most money?", inventory(), "nope")
	if err == nil {
		t.Fatal("an unknown --table should be refused")
	}
	if !strings.Contains(err.Error(), "contracts") {
		t.Errorf("the error should list the real tables: %v", err)
	}
}

// A text date column has to be flagged, because the router cannot know the
// layout and guessing mislabels a third of the year.
func TestSuggest_FlagsATextDateColumn(t *testing.T) {
	s, err := Suggest("how have contracts trended over time?", inventory(), "contracts")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if s.Columns[RoleDate] != "awarded_date" {
		t.Fatalf("date = %q, want awarded_date", s.Columns[RoleDate])
	}
	if !s.NeedsDateFormat {
		t.Error("a VARCHAR date column should require an explicit format")
	}
}

// A real DATE column needs no format, and demanding one would be an obstacle
// for no benefit.
func TestSuggest_RealDateNeedsNoFormat(t *testing.T) {
	s, err := Suggest("how are permits trending over time?", inventory(), "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if s.NeedsDateFormat {
		t.Error("a DATE column should not require a format")
	}
}

// Asking for the smallest while the pattern ranks largest-first is a real
// mismatch the user must be told about rather than silently served.
func TestSuggest_WarnsOnAscendingIntent(t *testing.T) {
	s, err := Suggest("which vendors got the least money?", inventory(), "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if len(s.Warnings) == 0 {
		t.Fatal("asking for the least should warn that the pattern ranks largest-first")
	}
	if !strings.Contains(strings.Join(s.Warnings, " "), "largest-first") {
		t.Errorf("the warning should explain the direction: %v", s.Warnings)
	}
}

// A suggestion has to be reproducible as an explicit command, since that is how
// a user corrects one column without rephrasing the whole question.
func TestSuggestion_CommandLineIsComplete(t *testing.T) {
	s, err := Suggest("which vendors got the most money?", inventory(), "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	cmd := s.CommandLine("chicago.duckdb", "personal")
	for _, want := range []string{
		"csq modes add", "top-n", "--db chicago.duckdb", "--table contracts",
		"--entity vendor_name", "--measure award_amount",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command line missing %q:\n  %s", want, cmd)
		}
	}
}

// A suggestion must actually build; a router that proposes something the
// builder then rejects is worse than no router.
func TestSuggest_ProducesABuildableRequest(t *testing.T) {
	for _, q := range []string{
		"which vendors got the most money?",
		"how are permits trending over time?",
		"are any vendor names duplicated?",
		"which vendors dominate each department's spending?",
		"what is the breakdown by procurement type?",
	} {
		s, err := Suggest(q, inventory(), "")
		if err != nil {
			t.Fatalf("%s: suggest: %v", q, err)
		}
		req := BuildRequest{
			Pattern: s.Pattern, Table: s.Table, Columns: s.Columns,
			ModeName: "personal", Portal: "data.example.gov",
		}
		if s.NeedsDateFormat {
			req.DateFormat = "%m/%d/%Y"
		}
		draft, err := Build(req)
		if err != nil {
			t.Errorf("%s: the suggestion did not build: %v", q, err)
			continue
		}
		if err := personal.CheckReadOnly(draft.Mode.Queries[0].SQL); err != nil {
			t.Errorf("%s: built SQL is not read-only: %v", q, err)
		}
	}
}

func TestTokeniseIdent_SplitsBothNamingStyles(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"vendor_name", "vendor"},
		{"vendorName", "vendor"},
		{"AwardAmount", "award"},
		{"award_amount", "award"},
	} {
		got := tokeniseIdent(tc.in)
		if len(got) == 0 || got[0] != tc.want {
			t.Errorf("tokeniseIdent(%q) = %v, want it to start with %q", tc.in, got, tc.want)
		}
	}
}

// Plural questions must match singular column names; this is the single
// cheapest way the router earns its keep.
func TestSingular(t *testing.T) {
	for in, want := range map[string]string{
		"vendors": "vendor", "companies": "company", "permits": "permit",
		"addresses": "address", "class": "class",
	} {
		if got := singular(in); got != want {
			t.Errorf("singular(%q) = %q, want %q", in, got, want)
		}
	}
}
