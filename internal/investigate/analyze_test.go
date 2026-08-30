// Copyright (c) 2026 Neomantra Corp

package investigate

import "testing"

// probeFor builds a planned probe around a bare Probe, for the measurement
// tests. Nothing here touches a database: measure is arithmetic over a series,
// and testing it through SQL would test DuckDB.
func probeFor(p Probe) PlannedProbe {
	return PlannedProbe{
		Name: p.Name, Asks: p.Asks, Supports: p.Supports,
		Unit: p.Unit, Per: p.Per, Concept: p.Concept,
		PeriodColumn: p.PeriodColumn, probe: p,
	}
}

func countProbe() Probe {
	return Probe{
		Name: "records", Asks: "Are fewer records published?",
		Unit: "records", Supports: Down,
		RiseMeans: "more are published", FallMeans: "fewer are published",
	}
}

func series(vals map[int]float64, complete map[int]bool) []Point {
	// Deterministic order: the measurement depends on the series being
	// ascending, and a map iteration would make this test flaky rather than
	// wrong, which is worse.
	var periods []int
	for p := range vals {
		periods = append(periods, p)
	}
	for i := 0; i < len(periods); i++ {
		for j := i + 1; j < len(periods); j++ {
			if periods[j] < periods[i] {
				periods[i], periods[j] = periods[j], periods[i]
			}
		}
	}
	out := make([]Point, 0, len(periods))
	for _, p := range periods {
		done, ok := complete[p]
		if !ok {
			done = true
		}
		out = append(out, Point{Period: p, Value: vals[p], Indicator: vals[p], Complete: done})
	}
	return out
}

func TestMeasure_ComparesLatestCompleteAgainstTheBaselineMean(t *testing.T) {
	s := series(map[int]float64{2021: 100, 2022: 100, 2023: 100, 2024: 80}, nil)
	f, why := measure(probeFor(countProbe()), s)
	if why != "" {
		t.Fatalf("unmeasurable: %s", why)
	}
	if f.LatestPeriod != 2024 {
		t.Errorf("latest = %d, want 2024", f.LatestPeriod)
	}
	if f.Baseline != 100 {
		t.Errorf("baseline = %v, want the 2021–2023 mean of 100", f.Baseline)
	}
	if f.BaselineFrom != 2021 || f.BaselineTo != 2023 {
		t.Errorf("baseline window = %d–%d, want 2021–2023", f.BaselineFrom, f.BaselineTo)
	}
	if f.Direction != Down {
		t.Errorf("direction = %q, want down", f.Direction)
	}
	if f.Change > -0.19 || f.Change < -0.21 {
		t.Errorf("change = %v, want about -0.20", f.Change)
	}
	// The probe declared that a fall would support the claim, before any of
	// this ran. That declaration is what Supports reads.
	if !f.Supports {
		t.Error("a fall should support a claim whose probe declares Supports: Down")
	}
}

// The baseline is a window, not the whole history: a regime a decade old must
// not drag on a claim about the present.
func TestMeasure_BaselineIsCappedToTheWindow(t *testing.T) {
	s := series(map[int]float64{
		2015: 1, 2016: 1, 2017: 1, 2021: 100, 2022: 100, 2023: 100, 2024: 100,
	}, nil)
	f, why := measure(probeFor(countProbe()), s)
	if why != "" {
		t.Fatalf("unmeasurable: %s", why)
	}
	if f.BaselineFrom != 2021 {
		t.Errorf("baseline starts %d, want 2021 — only %d periods should count",
			f.BaselineFrom, BaselineWindow)
	}
	if f.Direction != Flat {
		t.Errorf("direction = %q; the ancient periods should not have moved this",
			f.Direction)
	}
}

// The whole point of the coverage step: a part-finished period is charted and
// never measured, because a fraction of a year read as a year is a cliff.
func TestMeasure_IncompleteLatestPeriodIsNotMeasured(t *testing.T) {
	s := series(
		map[int]float64{2022: 100, 2023: 100, 2024: 100, 2025: 12},
		map[int]bool{2025: false})
	f, why := measure(probeFor(countProbe()), s)
	if why != "" {
		t.Fatalf("unmeasurable: %s", why)
	}
	if f.LatestPeriod != 2024 {
		t.Fatalf("measured %d — the part-finished 2025 must not be the latest",
			f.LatestPeriod)
	}
	if f.Direction != Flat {
		t.Errorf("direction = %q, want flat; 2025's partial count leaked in", f.Direction)
	}
	// It still has to be in the series, so a reader can see it.
	if len(f.Series) != 4 {
		t.Errorf("series has %d points, want 4 — the partial period is shown, not deleted",
			len(f.Series))
	}
}

func TestMeasure_SmallMovementsReadAsFlat(t *testing.T) {
	s := series(map[int]float64{2022: 100, 2023: 100, 2024: 100, 2025: 102}, nil)
	f, why := measure(probeFor(countProbe()), s)
	if why != "" {
		t.Fatalf("unmeasurable: %s", why)
	}
	if f.Direction != Flat {
		t.Errorf("a 2%% move read as %q, want flat", f.Direction)
	}
	if f.Counts() {
		t.Error("a flat finding must not count toward a verdict")
	}
}

func TestMeasure_RefusesASingleCompletePeriod(t *testing.T) {
	s := series(map[int]float64{2024: 100, 2025: 10}, map[int]bool{2025: false})
	if _, why := measure(probeFor(countProbe()), s); why == "" {
		t.Fatal("expected a refusal: one complete period is not a trend")
	}
}

func TestMeasure_RefusesWhenNoPeriodIsComplete(t *testing.T) {
	s := series(map[int]float64{2024: 100, 2025: 10}, map[int]bool{2024: false, 2025: false})
	_, why := measure(probeFor(countProbe()), s)
	if why == "" {
		t.Fatal("expected a refusal when nothing is covered end to end")
	}
}

// A rate is measured on the ratio, not on the numerator. Getting this wrong
// would report "complaints fell" when complaints rose and arrests rose faster.
func TestMeasure_RateIsMeasuredOnTheRatio(t *testing.T) {
	p := Probe{
		Name: "rate", Asks: "Is the rate falling?", Unit: "complaints",
		Per: "1,000 arrests", Scale: 1000, Supports: Down,
		RiseMeans: "higher", FallMeans: "lower",
	}
	// The numerator doubles while the denominator quadruples: the count rose
	// and the rate halved.
	pts := []Point{
		{Period: 2022, Value: 100, Denominator: 1000, Indicator: 100, Complete: true},
		{Period: 2023, Value: 100, Denominator: 1000, Indicator: 100, Complete: true},
		{Period: 2024, Value: 200, Denominator: 4000, Indicator: 50, Complete: true},
	}
	f, why := measure(probeFor(p), pts)
	if why != "" {
		t.Fatalf("unmeasurable: %s", why)
	}
	if f.Direction != Down {
		t.Errorf("direction = %q, want down — the rate halved though the count rose",
			f.Direction)
	}
	if f.Latest != 50 {
		t.Errorf("latest = %v, want the ratio 50", f.Latest)
	}
}

func TestMarkComplete_UnknownCoverageMarksNothingComplete(t *testing.T) {
	s := series(map[int]float64{2023: 1, 2024: 2}, nil)
	v := &Validation{Coverage: []Coverage{{Concept: "crimes", Known: false}}}
	markComplete(s, v, PlannedProbe{Concept: "crimes"})
	for _, p := range s {
		if p.Complete {
			t.Fatalf("%d marked complete on unknown coverage — an unmeasured extent "+
				"must never license a measurement", p.Period)
		}
	}
}

func TestMarkComplete_BoundsBothEnds(t *testing.T) {
	s := series(map[int]float64{2018: 1, 2019: 2, 2020: 3, 2021: 4}, nil)
	v := &Validation{Coverage: []Coverage{{
		Concept: "sr", Known: true, FirstComplete: 2019, LastComplete: 2020,
	}}}
	markComplete(s, v, PlannedProbe{Concept: "sr"})
	got := map[int]bool{}
	for _, p := range s {
		got[p.Period] = p.Complete
	}
	if got[2018] {
		t.Error("2018 is a part-year at the start and must not count")
	}
	if !got[2019] || !got[2020] {
		t.Error("2019 and 2020 are covered end to end and must count")
	}
	if got[2021] {
		t.Error("2021 is a part-year at the end and must not count")
	}
}

// A field that fills in after the fact is measured only where it has settled.
// Without this, "what share of cases carry an outcome" reports the age of the
// caseload as a disclosure failure.
func TestMarkComplete_SettlingLagHoldsBackRecentPeriods(t *testing.T) {
	s := series(map[int]float64{2021: 1, 2022: 2, 2023: 3, 2024: 4}, nil)
	v := &Validation{Coverage: []Coverage{{
		Concept: "complaints", Known: true, FirstComplete: 2021, LastComplete: 2024,
	}}}
	pp := PlannedProbe{Concept: "complaints", probe: Probe{SettlesAfter: 2}}
	markComplete(s, v, pp)

	got := map[int]bool{}
	for _, p := range s {
		got[p.Period] = p.Complete
	}
	if !got[2021] || !got[2022] {
		t.Error("2021 and 2022 have had time to settle and must be measurable")
	}
	if got[2023] || got[2024] {
		t.Error("2023 and 2024 are inside the settling lag and must not be measured")
	}
	// Held back, not deleted — a reader still sees them.
	if len(s) != 4 {
		t.Errorf("series has %d points, want 4", len(s))
	}
}

func TestMarkComplete_NoLagMeasuresEverythingCovered(t *testing.T) {
	s := series(map[int]float64{2023: 1, 2024: 2}, nil)
	v := &Validation{Coverage: []Coverage{{
		Concept: "crimes", Known: true, FirstComplete: 2023, LastComplete: 2024,
	}}}
	markComplete(s, v, PlannedProbe{Concept: "crimes"})
	for _, p := range s {
		if !p.Complete {
			t.Errorf("%d should be measurable with no settling lag declared", p.Period)
		}
	}
}
