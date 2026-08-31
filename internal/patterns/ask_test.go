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

// Both of the following come from running the router against Chicago's real
// tables, where each produced a query that ran cleanly and reported a number
// that meant nothing. That is the worst failure available to this code — worse
// than refusing — so each has a test.

// Chicago's 311 table has exactly one numeric column, community_area, which is
// a geographic identifier. Filling the trend pattern's *optional* measure with
// it yields a monthly sum of area numbers.
func TestSuggest_OptionalRoleIsNotFilledByTypeAlone(t *testing.T) {
	inv := &personal.Portal{
		Alias: "p", Host: "h",
		Tables: []personal.Table{{
			Name: "requests_311", DatasetName: "311 Service Requests",
			Columns: []personal.Column{
				{Name: "sr_type", Type: "VARCHAR"},
				{Name: "created_date", Type: "TIMESTAMP"},
				{Name: "community_area", Type: "DOUBLE"}, // an id, not a quantity
				{Name: "ward", Type: "DOUBLE"},           // likewise
			},
		}},
	}
	s, err := Suggest("how are 311 requests trending over time?", inv, "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if col, ok := s.Columns[RoleMeasure]; ok {
		t.Errorf("measure should be left unset, got %q — summing it means nothing", col)
	}
}

// Chicago's crimes table has fbi_code, a classification. A question mentioning
// "type" reaches it through the category synonyms, and summing it produces
// totals that look like real numbers.
func TestSuggest_OptionalRoleIgnoresWrongKindOfNameMatch(t *testing.T) {
	inv := &personal.Portal{
		Alias: "p", Host: "h",
		Tables: []personal.Table{{
			Name: "crimes", DatasetName: "Crimes",
			Columns: []personal.Column{
				{Name: "primary_type", Type: "VARCHAR"},
				{Name: "fbi_code", Type: "VARCHAR"}, // a classification, not a measure
				{Name: "date", Type: "TIMESTAMP"},
			},
		}},
	}
	s, err := Suggest("what is the breakdown of crimes by primary type?", inv, "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if s.Columns[RoleCategory] != "primary_type" {
		t.Errorf("category = %q, want primary_type", s.Columns[RoleCategory])
	}
	if col, ok := s.Columns[RoleMeasure]; ok {
		t.Errorf("measure should be left unset, got %q — a code is not a quantity", col)
	}
}

// Chicago's building_permits carries a dozen fee columns. Asked about "fees",
// the one a person means is the unqualified total, not one component.
func TestSuggest_PrefersTheLeastQualifiedName(t *testing.T) {
	inv := &personal.Portal{
		Alias: "p", Host: "h",
		Tables: []personal.Table{{
			Name: "building_permits", DatasetName: "Building Permits",
			Columns: []personal.Column{
				{Name: "permit_type", Type: "VARCHAR"},
				// Declared before total_fee on purpose: order must not decide it.
				{Name: "building_fee_paid", Type: "DOUBLE"},
				{Name: "zoning_fee_paid", Type: "DOUBLE"},
				{Name: "subtotal_paid", Type: "DOUBLE"},
				{Name: "total_fee", Type: "DOUBLE"},
			},
		}},
	}
	s, err := Suggest("which permit types collect the most fees?", inv, "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	if s.Columns[RoleMeasure] != "total_fee" {
		t.Errorf("measure = %q, want total_fee", s.Columns[RoleMeasure])
	}
}

// "The most permits" and "the most money" are both ordinary readings of "the
// most", and they are different questions. A summed measure is attached only
// when the wording reaches for a quantity; otherwise records are counted, which
// is what was asked.
func TestSuggest_CountsUnlessTheQuestionAsksForAQuantity(t *testing.T) {
	counting := []string{
		"how are permits trending over time?",
		"which applicants pull the most permits?",
	}
	for _, q := range counting {
		s, err := Suggest(q, inventory(), "")
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if col, ok := s.Columns[RoleMeasure]; ok {
			t.Errorf("%s: attached measure %q, but the question asks for a count", q, col)
		}
	}

	summing := map[string]string{
		"which applicants pull the most permits by value?": "estimated_cost",
		"which applicants paid the most in permit costs?":  "estimated_cost",
	}
	for q, want := range summing {
		s, err := Suggest(q, inventory(), "")
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if s.Columns[RoleMeasure] != want {
			t.Errorf("%s: measure = %q, want %q", q, s.Columns[RoleMeasure], want)
		}
	}
}

// Ranking or grouping by a raw timestamp yields one row per instant, and a
// boolean yields two. Neither is an answer, and both are reachable through the
// synonym table on Chicago's crimes schema.
func TestSuggest_RejectsTimestampAndBooleanEntities(t *testing.T) {
	inv := &personal.Portal{
		Alias: "p", Host: "h",
		Tables: []personal.Table{{
			Name: "crimes", DatasetName: "Crimes",
			Columns: []personal.Column{
				{Name: "date", Type: "TIMESTAMP"},
				{Name: "arrest", Type: "BOOLEAN"},
				{Name: "primary_type", Type: "VARCHAR"},
			},
		}},
	}
	s, err := Suggest("what are the top crime types?", inv, "")
	if err != nil {
		t.Fatalf("suggest: %v", err)
	}
	switch s.Columns[RoleEntity] {
	case "date", "arrest":
		t.Errorf("entity = %q — ranking by that answers nothing", s.Columns[RoleEntity])
	}
}

// Silently moving to a different table because the obvious one lacks a column
// is the worst outcome available: a question about crimes coming back about
// building permits, correctly labelled and entirely beside the point.
func TestSuggest_RefusesRatherThanSwitchingTables(t *testing.T) {
	inv := &personal.Portal{
		Alias: "p", Host: "h",
		Tables: []personal.Table{
			{
				Name: "crimes", DatasetName: "Crimes",
				Columns: []personal.Column{
					{Name: "primary_type", Type: "VARCHAR"},
					{Name: "district", Type: "VARCHAR"},
				}, // no measure-shaped column at all
			},
			{
				Name: "building_permits", DatasetName: "Building Permits",
				Columns: []personal.Column{
					{Name: "permit_type", Type: "VARCHAR"},
					{Name: "total_fee", Type: "DOUBLE"},
				},
			},
		},
	}
	_, err := Suggest("is any single crime type concentrated in one district?", inv, "")
	if err == nil {
		t.Fatal("expected a refusal rather than a switch to building_permits")
	}
	if !strings.Contains(err.Error(), "crimes") {
		t.Errorf("the refusal should name the table the question was about: %v", err)
	}
	if strings.Contains(err.Error(), "building_permits") {
		t.Errorf("the refusal should not offer an unrelated table: %v", err)
	}
}

// Vocabulary phrases containing a stopword — "by month", "what kind" — must
// still match. Building bigrams after stopword removal silently broke every one
// of them, and these questions matched nothing at all.
func TestSuggest_PhrasesSurviveStopwords(t *testing.T) {
	cases := map[string]string{
		"show me permits by month":          "trend",
		"what kinds of permits are issued?": "breakdown",
	}
	for q, want := range cases {
		s, err := Suggest(q, inventory(), "")
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if s.Pattern.Name != want {
			t.Errorf("%s: pattern = %q, want %q", q, s.Pattern.Name, want)
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
