// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"strings"
	"testing"
)

// TestCanonicalViewRenamesColumns is the portability guarantee: a query written
// against a concept's column names must work on a portal that calls them
// something else.
func TestCanonicalViewRenamesColumns(t *testing.T) {
	m, _ := Lookup("ranking")
	c, ok := m.Concept("crimes")
	if !ok {
		t.Fatal("ranking should declare a crimes concept")
	}
	nyc, err := LookupBinding("ranking", "data.cityofnewyork.us")
	if err != nil {
		t.Fatalf("LookupBinding: %v", err)
	}
	view := c.CanonicalView("nyc.main.nypd_complaints", nyc.Concepts["crimes"])
	for _, want := range []string{"cmplnt_fr_dt AS date", "ofns_desc AS primary_type"} {
		if !strings.Contains(view, want) {
			t.Errorf("canonical view missing %q\ngot: %s", want, view)
		}
	}
	// arrest is unmapped, so it must not appear at all rather than as NULL.
	if strings.Contains(view, "arrest") {
		t.Errorf("unmapped column should be omitted, not emitted: %s", view)
	}
}

// TestIdentityMappingWhenColumnsOmitted keeps existing bindings working: an
// empty Columns map means the table already uses canonical names.
func TestIdentityMappingWhenColumnsOmitted(t *testing.T) {
	chi, err := LookupBinding("ranking", "data.cityofchicago.org")
	if err != nil {
		t.Fatalf("LookupBinding: %v", err)
	}
	bd := chi.Concepts["crimes"]
	got, ok := bd.ColumnFor("date")
	if !ok || got != "date" {
		t.Errorf("identity mapping expected, got %q ok=%v", got, ok)
	}
}

// TestMissingOptionalColumnExcludesCity is the rule that stops a NULL reading
// as a real value: NYC records no arrest flag, so it must be excluded from
// arrest-share by name rather than showing 0%.
func TestMissingOptionalColumnExcludesCity(t *testing.T) {
	m, _ := Lookup("ranking")
	q, err := m.Query("arrest-share")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	nyc, _ := LookupBinding("ranking", "data.cityofnewyork.us")
	ok, why := m.Comparable(*q, nyc)
	if ok {
		t.Fatal("NYC has no arrest column and must be excluded from arrest-share")
	}
	if !strings.Contains(why, "arrest") {
		t.Errorf("exclusion reason should name the column, got %q", why)
	}

	// Chicago does record it, so it must still qualify.
	chi, _ := LookupBinding("ranking", "data.cityofchicago.org")
	if ok, why := m.Comparable(*q, chi); !ok {
		t.Errorf("Chicago should qualify for arrest-share, got %q", why)
	}
}

// TestBothCitiesComparableOnSharedIndicators confirms the second binding
// actually produces a comparison rather than two exclusions.
func TestBothCitiesComparableOnSharedIndicators(t *testing.T) {
	m, _ := Lookup("ranking")
	chi, _ := LookupBinding("ranking", "data.cityofchicago.org")
	nyc, _ := LookupBinding("ranking", "data.cityofnewyork.us")
	for _, name := range []string{"crime-rate", "311-responsiveness", "311-load", "permit-activity"} {
		q, err := m.Query(name)
		if err != nil {
			t.Fatalf("Query %s: %v", name, err)
		}
		for label, b := range map[string]*Binding{"Chicago": chi, "NYC": nyc} {
			if ok, why := m.Comparable(*q, b); !ok {
				t.Errorf("%s should be comparable on %s, got %q", label, name, why)
			}
		}
	}
}

// TestQueryUsesColumnWordBoundary guards the gate against false positives —
// "date" must not match inside "created_date".
func TestQueryUsesColumnWordBoundary(t *testing.T) {
	if queryUsesColumn("SELECT created_date FROM t", "date") {
		t.Error("'date' should not match inside 'created_date'")
	}
	if !queryUsesColumn("SELECT date FROM t", "date") {
		t.Error("'date' should match as a whole word")
	}
	if !queryUsesColumn("WHERE arrest THEN 1", "arrest") {
		t.Error("'arrest' should match as a whole word")
	}
}
