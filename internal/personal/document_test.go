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

	out := MergeMode(existing, drafted)

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

func TestBriefListsEveryColumn(t *testing.T) {
	b := sampleInventory().Brief()
	for _, want := range []string{"contracts", "vendor_name", "award_amount", "DOUBLE", "rsxa-ify5"} {
		if !strings.Contains(b, want) {
			t.Errorf("the inventory the model reads is missing %q:\n%s", want, b)
		}
	}
}
