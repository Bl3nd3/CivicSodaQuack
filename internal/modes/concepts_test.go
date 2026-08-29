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

// TestBindingMayMapAnExpression covers the wrong-type escape hatch. NYC's DOB
// permit extract publishes issuance_date as text holding MM/DD/YYYY, so the
// binding maps it through try_strptime. Without that, permit-activity does not
// merely return odd numbers -- it fails to run, because DuckDB refuses to
// compare a VARCHAR against a DATE and will not take date_part of one.
//
// The bug this locks down was invisible to every existing test: the mapping was
// a valid column name, the mode was well-formed, and the failure only appeared
// against real rows from the live portal.
func TestBindingMayMapAnExpression(t *testing.T) {
	m, _ := Lookup("ranking")
	c, ok := m.Concept("building_permits")
	if !ok {
		t.Fatal("ranking should declare a building_permits concept")
	}
	nyc, err := LookupBinding("ranking", "data.cityofnewyork.us")
	if err != nil {
		t.Fatalf("LookupBinding: %v", err)
	}
	view := c.CanonicalView("nyc.main.dob_permits", nyc.Concepts["building_permits"])

	const want = "try_strptime(issuance_date, '%m/%d/%Y') AS issue_date"
	if !strings.Contains(view, want) {
		t.Errorf("expression mapping not emitted verbatim\nwant substring: %s\ngot: %s", want, view)
	}
	// A bare column reference here is the regression: it type-errors at runtime.
	if strings.Contains(view, "issuance_date AS issue_date") {
		t.Errorf("issuance_date mapped as a bare column; it is text, not a date: %s", view)
	}
}

// A concept can be bound and its table never synced. Without a presence check
// a cross-city query dies on a DuckDB binder error naming the missing table,
// taking every other city's answer down with it — so the city that has the data
// gets no answer because of the city that does not.
func TestComparableWith_ExcludesUnsyncedTables(t *testing.T) {
	m, err := Lookup("ranking")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LookupBinding("ranking", "data.cityofnewyork.us")
	if err != nil {
		t.Fatal(err)
	}
	q, err := m.Query("311-load")
	if err != nil {
		t.Fatal(err)
	}

	// Bound, and in principle answerable.
	if ok, why := m.Comparable(*q, b); !ok {
		t.Fatalf("expected the binding alone to look answerable, got %q", why)
	}

	// But the table is not held locally.
	none := func(string) bool { return false }
	ok, why := m.ComparableWith(*q, b, none)
	if ok {
		t.Fatal("a city whose table is absent must be excluded")
	}
	if !strings.Contains(why, "has not synced") {
		t.Errorf("reason = %q; must say the data is unsynced, not unpublished", why)
	}
}

// The two exclusion reasons have different remedies and must stay
// distinguishable: a city that does not publish an indicator will never have
// it, while a city that has not synced one is a command away from it.
func TestComparableWith_KeepsUnpublishedDistinctFromUnsynced(t *testing.T) {
	m, _ := Lookup("ranking")
	b, err := LookupBinding("ranking", "data.cityofnewyork.us")
	if err != nil {
		t.Fatal(err)
	}
	q, err := m.Query("arrest-share")
	if err != nil {
		t.Fatal(err)
	}

	// Every table present; NYC still cannot answer, because it records no
	// arrest flag. The reason must reflect that, not the presence check.
	all := func(string) bool { return true }
	ok, why := m.ComparableWith(*q, b, all)
	if ok {
		t.Fatal("NYC publishes no arrest flag; it must be excluded")
	}
	if strings.Contains(why, "has not synced") {
		t.Errorf("reason = %q; an unpublished column must not read as unsynced", why)
	}
	if !strings.Contains(why, "does not record") {
		t.Errorf("reason = %q, want the unpublished-column wording", why)
	}
}

// A city holding everything the query needs is comparable, and a nil lookup
// means "presence unknown" rather than "absent" — an inspection failure must
// not masquerade as missing data and silently drop a city.
func TestComparableWith_PresentAndUnknown(t *testing.T) {
	m, _ := Lookup("ranking")
	b, err := LookupBinding("ranking", "data.cityofchicago.org")
	if err != nil {
		t.Fatal(err)
	}
	q, err := m.Query("311-load")
	if err != nil {
		t.Fatal(err)
	}

	all := func(string) bool { return true }
	if ok, why := m.ComparableWith(*q, b, all); !ok {
		t.Errorf("Chicago holds 311; got excluded with %q", why)
	}
	if ok, why := m.ComparableWith(*q, b, nil); !ok {
		t.Errorf("a nil lookup must not exclude anyone; got %q", why)
	}
}
