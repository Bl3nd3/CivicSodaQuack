// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// A disclosure probe reads the very column whose nulls it counts. Profiling
// those nulls as missing evidence would score an indicator that measured every
// record it needed as resting on a fraction of them — Chicago's 73% of open
// cases turned a sound measurement into 7% confidence before this existed.
func TestProfileTargets_ColumnsWhoseAbsenceIsMeasuredAreNotProfiled(t *testing.T) {
	m, err := modes.Lookup("police")
	if err != nil {
		t.Fatal(err)
	}
	b, err := modes.LookupBinding("police", "data.cityofchicago.org")
	if err != nil {
		t.Fatal(err)
	}
	inv, err := Lookup("police-transparency")
	if err != nil {
		t.Fatal(err)
	}
	probe, ok := inv.Probe("outcome-disclosure")
	if !ok {
		t.Fatal("outcome-disclosure is missing")
	}
	q := modes.Query{Name: probe.Name, Desc: probe.Asks, SQL: probe.SQL}

	// Without the exclusion the profile does examine the measured column, so
	// the test is pinning a behaviour change rather than an accident.
	raw := profiledColumns(t, m, q, b, Probe{})
	if !contains(raw, "finding_code") {
		t.Fatalf("expected finding_code among the columns read, got %v", raw)
	}

	got := profiledColumns(t, m, q, b, probe)
	if contains(got, "finding_code") {
		t.Errorf("finding_code is still profiled: %v", got)
	}
	// The period column is never dropped: a row with no date cannot be placed
	// in time, and that is a genuine loss whatever the probe is counting.
	if !contains(got, "complaint_date") {
		t.Errorf("complaint_date must stay in the profile, got %v", got)
	}
}

// A probe that declares nothing keeps every column it reads.
func TestProfileTargets_OrdinaryProbesAreProfiledInFull(t *testing.T) {
	m, err := modes.Lookup("police")
	if err != nil {
		t.Fatal(err)
	}
	b, err := modes.LookupBinding("police", "data.cityofchicago.org")
	if err != nil {
		t.Fatal(err)
	}
	inv, _ := Lookup("police-transparency")
	probe, ok := inv.Probe("case-publication")
	if !ok {
		t.Fatal("case-publication is missing")
	}
	q := modes.Query{Name: probe.Name, Desc: probe.Asks, SQL: probe.SQL}

	got := profiledColumns(t, m, q, b, probe)
	if !contains(got, "complaint_date") {
		t.Errorf("complaint_date should be profiled, got %v", got)
	}
}

func profiledColumns(t *testing.T, m *modes.Mode, q modes.Query,
	b *modes.Binding, probe Probe) []string {
	t.Helper()
	var out []string
	for _, tgt := range profileTargets(m, q, "chi", b, probe) {
		out = append(out, tgt.Columns...)
	}
	return out
}
