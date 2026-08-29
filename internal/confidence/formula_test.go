// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"math"
	"math/rand"
	"testing"
)

// The formula is a claim made in the docs and printed next to every answer:
// R is the product of the retention factors of the checks that ran. These
// tests hold the implementation to it, so the number a reader is shown can
// always be reconstructed from the signals shown beside it.

// productOf recomputes R independently of finalize, the way an auditor reading
// the JSON would: multiply the retention of every check that was performed,
// skipping advisories and unmeasured checks.
func productOf(sigs []Signal) float64 {
	r := 1.0
	for _, s := range sigs {
		if s.Floor >= 1 || s.Level == Unknown {
			continue
		}
		r *= s.Score
	}
	return r
}

func TestReport_ScoreIsExactlyTheProductOfRetentions(t *testing.T) {
	rng := rand.New(rand.NewSource(20260829))
	levels := []Level{Pass, Warn, Fail, Unknown}
	floors := []float64{FloorFatal, FloorRows, FloorDates, FloorFreshness,
		FloorLag, FloorKeys, FloorAdvisory}

	for trial := 0; trial < 500; trial++ {
		var sigs []Signal
		for n := rng.Intn(8) + 1; n > 0; n-- {
			floor := floors[rng.Intn(len(floors))]
			// A retention factor always lies between its floor and 1.
			score := floor + rng.Float64()*(1-floor)
			sigs = append(sigs, Signal{
				Name:  "check",
				Level: levels[rng.Intn(len(levels))],
				Floor: floor,
				Score: score,
			})
		}
		rep := &Report{Datasets: []DatasetReport{{Signals: sigs}}}
		rep.finalize()
		if !rep.Assessed {
			continue // nothing measurable; Score is meaningless by contract
		}

		want := pctOf(productOf(sigs))
		if rep.Score != want {
			t.Fatalf("trial %d: Score = %d, but ∏r = %v gives %d",
				trial, rep.Score, productOf(sigs), want)
		}
	}
}

// The product must run across datasets exactly as it does within one, so a
// query resting on several datasets is scored by one rule rather than two.
func TestReport_ProductRunsAcrossDatasets(t *testing.T) {
	rep := &Report{Datasets: []DatasetReport{
		{Signals: []Signal{sig("a", Warn, 0.8, 0)}},
		{Signals: []Signal{sig("b", Warn, 0.5, 0)}},
	}}
	rep.finalize()

	if rep.Score != 40 { // 0.8 × 0.5
		t.Errorf("Score = %d, want 40", rep.Score)
	}
}

// A transcript of the audited New York report, kept as a test so the worked
// example in the README cannot drift away from the code.
//
//	completeness  5,440,343 held / 10,071,507 expected  -> r = 0.540172
//	freshness     122 days, the 90–365 day band         -> r = 0.907545
//	R = 0.540172 × 0.907545 = 0.490230                  -> 49%
func TestReport_ReproducesTheAuditedNewYorkNumbers(t *testing.T) {
	const held, expected = 5440343, 10071507

	completeness := completenessSignal(held, expected, bookRecord{}, "nypd_complaints")
	if got, want := completeness.Score, float64(held)/float64(expected); got != want {
		t.Fatalf("completeness retention = %v, want the ratio %v", got, want)
	}

	freshness := retain(FloorFreshness, stalenessOf(122))
	if math.Abs(freshness-0.907545) > 1e-6 {
		t.Fatalf("freshness retention = %v, want 0.907545", freshness)
	}

	rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		completeness,
		{Name: SignalFreshness, Level: Warn, Floor: FloorFreshness, Score: freshness},
		sig(SignalSync, Pass, 1, FloorFatal),
		sig(SignalRowIntegrity, Pass, 1, FloorRows),
		sig(SignalNullDensity, Pass, 1, FloorNulls),
		sig(SignalDateRange, Pass, 1, FloorDates),
		sig(SignalLag, Pass, 1, FloorLag),
	}}}}
	rep.finalize()

	if rep.Score != 49 {
		t.Errorf("Score = %d, want 49", rep.Score)
	}
	if rep.Band != BandLow {
		t.Errorf("Band = %q, want %q", rep.Band, BandLow)
	}
	if rep.Coverage != 100 {
		t.Errorf("Coverage = %d, want 100", rep.Coverage)
	}
}

// A defect past its saturation point costs exactly the floor and no more —
// the Chicago procurement_type case, where 70.3% of rows are null against a
// zeroAt of 50%.
func TestReport_ReproducesTheAuditedChicagoFloorCase(t *testing.T) {
	p := tableProfile{rows: 185826, cols: []colProfile{
		{name: "procurement_type", nulls: 130626},
	}}
	s := nullSignal(p, "contracts")

	if s.Score != FloorNulls {
		t.Errorf("retention = %v, want exactly the floor %v", s.Score, FloorNulls)
	}
	rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{s}}}}
	rep.finalize()
	if rep.Score != 30 {
		t.Errorf("Score = %d, want 30", rep.Score)
	}
}
