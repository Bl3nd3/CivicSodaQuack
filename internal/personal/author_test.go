// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"strings"
	"testing"
)

func sampleInventory() *Portal {
	return &Portal{
		Alias: "chi",
		Host:  "data.cityofchicago.org",
		Tables: []Table{{
			Name:        "contracts",
			Rows:        185699,
			DatasetID:   "rsxa-ify5",
			DatasetName: "Contracts",
			Columns: []Column{
				{Name: "vendor_name", Type: "VARCHAR"},
				{Name: "award_amount", Type: "DOUBLE"},
				{Name: "department", Type: "VARCHAR"},
			},
		}},
	}
}

func validDraft() *Draft {
	return &Draft{
		Mode: &Document{
			Kind: "mode", Name: "personal",
			Title: "T", Summary: "S", About: "A",
			Concepts: []Concept{{
				Name: "contracts", Purpose: "Awards by vendor.",
				Required: []string{"vendor_name", "award_amount"},
			}},
			Queries: []DocQuery{{
				Name: "top-vendors", Desc: "Vendors by total.",
				SQL: "SELECT vendor_name, SUM(award_amount) AS total FROM {{c:contracts}} " +
					"GROUP BY vendor_name ORDER BY total DESC LIMIT 25",
			}},
			Caveats: []string{"An award is not a payment."},
		},
		Binding: &Document{
			Kind: "binding", Mode: "personal", Portal: "data.cityofchicago.org",
			City: "Chicago, IL",
			Datasets: map[string]DocDataset{
				"contracts": {
					ID: "rsxa-ify5", Table: "contracts", Name: "Contracts",
					Columns: map[string]string{
						"vendor_name":  "vendor_name",
						"award_amount": "award_amount",
					},
				},
			},
		},
	}
}

func TestDraftCheck_AcceptsAConsistentDraft(t *testing.T) {
	if err := validDraft().check(sampleInventory()); err != nil {
		t.Fatalf("a consistent draft should pass: %v", err)
	}
}

// csq knows the row count and the dataset id better than the model does, so it
// overwrites them rather than trusting a recalled figure.
func TestDraftCheck_FillsProvenanceFromTheInventory(t *testing.T) {
	d := validDraft()
	ds := d.Binding.Datasets["contracts"]
	ds.ID = "wrong-id"
	ds.Rows = 999
	d.Binding.Datasets["contracts"] = ds

	if err := d.check(sampleInventory()); err != nil {
		t.Fatalf("check: %v", err)
	}
	got := d.Binding.Datasets["contracts"]
	if got.ID != "rsxa-ify5" {
		t.Errorf("dataset id should come from the local catalog, got %q", got.ID)
	}
	if got.Rows != 185699 {
		t.Errorf("row count should come from the local catalog, got %d", got.Rows)
	}
}

func TestDraftCheck_RejectsHallucinatedTable(t *testing.T) {
	d := validDraft()
	ds := d.Binding.Datasets["contracts"]
	ds.Table = "contracts_2024"
	d.Binding.Datasets["contracts"] = ds

	err := d.check(sampleInventory())
	if err == nil {
		t.Fatal("binding to a table that is not held should be refused")
	}
	// The message has to say what *is* available, or the user cannot act on it.
	if !strings.Contains(err.Error(), "contracts_2024") || !strings.Contains(err.Error(), "it holds") {
		t.Errorf("error should name the bad table and the real ones, got: %v", err)
	}
}

func TestDraftCheck_RejectsHallucinatedColumn(t *testing.T) {
	d := validDraft()
	d.Binding.Datasets["contracts"].Columns["award_amount"] = "amount_awarded"

	err := d.check(sampleInventory())
	if err == nil {
		t.Fatal("mapping to a column that does not exist should be refused")
	}
	if !strings.Contains(err.Error(), "amount_awarded") {
		t.Errorf("error should name the bad column, got: %v", err)
	}
}

// An expression cannot be checked against the column list; it is left for the
// EXPLAIN pass, which is the only thing that can actually judge it.
func TestDraftCheck_AllowsExpressionMappings(t *testing.T) {
	d := validDraft()
	d.Binding.Datasets["contracts"].Columns["award_amount"] =
		"TRY_CAST(award_amount AS DOUBLE)"

	if err := d.check(sampleInventory()); err != nil {
		t.Fatalf("an expression mapping should be allowed through: %v", err)
	}
}

// A query naming a real table works on exactly one portal, which defeats the
// concept indirection the whole format is built on.
func TestDraftCheck_RejectsQueryWithoutConceptRef(t *testing.T) {
	d := validDraft()
	d.Mode.Queries[0].SQL = "SELECT vendor_name FROM contracts LIMIT 5"

	err := d.check(sampleInventory())
	if err == nil {
		t.Fatal("a query naming a table directly should be refused")
	}
	if !strings.Contains(err.Error(), "{{c:concept}}") {
		t.Errorf("error should teach the fix, got: %v", err)
	}
}

func TestDraftCheck_RejectsNonReadOnlySQL(t *testing.T) {
	d := validDraft()
	d.Mode.Queries[0].SQL = "SELECT * FROM {{c:contracts}}; DROP TABLE contracts"

	if err := d.check(sampleInventory()); err == nil {
		t.Fatal("a second statement should be refused")
	}
}

func TestDraftCheck_RejectsUnboundConcept(t *testing.T) {
	d := validDraft()
	d.Mode.Concepts = append(d.Mode.Concepts, Concept{Name: "lobbying", Purpose: "p"})
	d.Mode.Queries = append(d.Mode.Queries, DocQuery{
		Name: "lobby", Desc: "d", SQL: "SELECT * FROM {{c:lobbying}} LIMIT 5",
	})

	err := d.check(sampleInventory())
	if err == nil {
		t.Fatal("a concept with no dataset should be refused")
	}
	if !strings.Contains(err.Error(), "binding never maps") {
		t.Errorf("error should say the binding is missing, got: %v", err)
	}
}

// Merging is what makes the personal mode accumulate. A user's edits are the
// point of the file being on disk, so a later question must never revert them.
func TestMergeMode_KeepsWhatTheUserAlreadyHas(t *testing.T) {
	existing := &Document{
		Kind: "mode", Name: "personal",
		Title: "My title", Summary: "My summary", About: "My about",
		Concepts: []Concept{{Name: "contracts", Purpose: "Edited by hand."}},
		Queries: []DocQuery{{
			Name: "top-vendors", Desc: "My own edited version.",
			SQL: "SELECT 1 FROM {{c:contracts}}",
		}},
		Caveats: []string{"My own caveat."},
	}
	drafted := &Document{
		Kind: "mode",
		// The model does not get to rename the user's mode or rewrite its prose.
		Title: "Model title", Summary: "Model summary", About: "Model about",
		Concepts: []Concept{
			{Name: "contracts", Purpose: "Model's wording."},
			{Name: "permits", Purpose: "New concept."},
		},
		Queries: []DocQuery{
			{Name: "top-vendors", Desc: "A colliding name.", SQL: "SELECT 2 FROM {{c:contracts}}"},
			{Name: "by-department", Desc: "Genuinely new.", SQL: "SELECT 3 FROM {{c:contracts}}"},
		},
		Caveats: []string{"My own caveat.", "A new caveat."},
	}

	out := mergeMode(existing, drafted)

	if out.Title != "My title" || out.About != "My about" {
		t.Error("the user's prose should survive a merge")
	}
	if len(out.Concepts) != 2 || out.Concepts[0].Purpose != "Edited by hand." {
		t.Errorf("existing concept wording should win: %+v", out.Concepts)
	}
	if len(out.Queries) != 3 {
		t.Fatalf("want 3 queries after merge, got %d", len(out.Queries))
	}
	if out.Queries[0].Desc != "My own edited version." {
		t.Error("an existing query must not be overwritten by a colliding draft")
	}
	// The colliding draft is kept under a fresh name rather than dropped: the
	// user asked for it, and silently discarding it would look like a no-op.
	if out.Queries[1].Name != "top-vendors-2" {
		t.Errorf("collision should be renamed, got %q", out.Queries[1].Name)
	}
	if len(out.Caveats) != 2 {
		t.Errorf("caveats should be unioned, not duplicated: %v", out.Caveats)
	}
}

func TestMergeBinding_KeepsExistingDatasets(t *testing.T) {
	existing := &Document{
		Kind: "binding", Datasets: map[string]DocDataset{
			"contracts": {ID: "user-id", Table: "contracts", Name: "Hand-edited"},
		},
	}
	drafted := &Document{
		Kind: "binding", Datasets: map[string]DocDataset{
			"contracts": {ID: "model-id", Table: "contracts", Name: "Model version"},
			"permits":   {ID: "new-id", Table: "permits", Name: "New"},
		},
	}

	out := MergeBinding(existing, drafted)
	if out.Datasets["contracts"].ID != "user-id" {
		t.Error("an existing dataset mapping should win over a redraft")
	}
	if _, ok := out.Datasets["permits"]; !ok {
		t.Error("a genuinely new dataset should be added")
	}
}

// The provenance caveat is a fact about the file, not a judgement call, so it
// is asserted by csq rather than left to the model to remember.
func TestGeneratedCaveatIsAlwaysPresent(t *testing.T) {
	got := appendUnique([]string{"something else"}, GeneratedCaveat)
	if len(got) != 2 || got[1] != GeneratedCaveat {
		t.Fatalf("caveat not appended: %v", got)
	}
	// Appending twice must not duplicate it across repeated questions.
	again := appendUnique(got, GeneratedCaveat)
	if len(again) != 2 {
		t.Errorf("caveat duplicated on a second run: %v", again)
	}
}

func TestBriefListsEveryColumn(t *testing.T) {
	b := sampleInventory().Brief()
	for _, want := range []string{"contracts", "vendor_name", "award_amount", "DOUBLE", "rsxa-ify5"} {
		if !strings.Contains(b, want) {
			t.Errorf("the inventory the model reads is missing %q:\n%s", want, b)
		}
	}
}
