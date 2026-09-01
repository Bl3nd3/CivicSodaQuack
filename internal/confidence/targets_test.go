// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// Two concepts may legitimately be satisfied by one dataset — a portal that
// publishes contracts and payments in a single table, say. R is the product of
// per-target retentions, so profiling that table twice would square its
// retention and report a loss the data contains once. The merged target must
// read the union of the columns, because U is one joint filter: a row has to
// survive every column the query touches, whichever concept introduced it.
func TestTargetsFor_OneDatasetIsProfiledOnce(t *testing.T) {
	mode := &modes.Mode{
		Name: "test",
		Concepts: []modes.Concept{
			{Name: "spend", Required: []string{"amount"}},
			{Name: "payees", Required: []string{"vendor"}},
		},
	}
	shared := modes.BoundDataset{
		ID: "aaaa-bbbb", Table: "ledger", Name: "City Ledger", Rows: 1000,
		Columns: map[string]string{"amount": "amount", "vendor": "vendor"},
	}
	binding := &modes.Binding{
		Mode:     "test",
		Concepts: map[string]modes.BoundDataset{"spend": shared, "payees": shared},
	}
	q := modes.Query{
		Name: "both",
		SQL:  "SELECT vendor, amount FROM {{c:spend}} JOIN {{c:payees}} USING (vendor)",
	}

	got := TargetsFor(mode, q, "chi", binding)
	if len(got) != 1 {
		t.Fatalf("got %d targets for one dataset, want 1 — its retention would be raised to the power of %d", len(got), len(got))
	}
	if !hasColumn(got[0].Columns, "amount") || !hasColumn(got[0].Columns, "vendor") {
		t.Errorf("merged target reads %v, want both amount and vendor", got[0].Columns)
	}
}

// Distinct datasets must stay distinct: the merge keys on the dataset, not on
// the portal, so a comparison across cities still scores each city's copy.
func TestTargetsFor_DistinctDatasetsSurvive(t *testing.T) {
	mode := &modes.Mode{
		Name: "test",
		Concepts: []modes.Concept{
			{Name: "spend", Required: []string{"amount"}},
			{Name: "payees", Required: []string{"vendor"}},
		},
	}
	binding := &modes.Binding{
		Mode: "test",
		Concepts: map[string]modes.BoundDataset{
			"spend": {ID: "aaaa-bbbb", Table: "contracts", Rows: 10,
				Columns: map[string]string{"amount": "amount"}},
			"payees": {ID: "cccc-dddd", Table: "vendors", Rows: 20,
				Columns: map[string]string{"vendor": "vendor"}},
		},
	}
	q := modes.Query{
		Name: "both",
		SQL:  "SELECT vendor, amount FROM {{c:spend}}, {{c:payees}}",
	}

	if got := TargetsFor(mode, q, "chi", binding); len(got) != 2 {
		t.Errorf("got %d targets for two datasets, want 2", len(got))
	}
}
