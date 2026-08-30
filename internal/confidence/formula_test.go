// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"math/rand"
	"testing"
)

// The formula is a claim the docs make and that is printed beside every
// answer: R is the product of the retentions of the checks that ran. These
// tests hold the implementation to it, so the percent a reader is shown can
// always be reconstructed from the signals shown next to it.

// productOf recomputes R independently of finalize, the way an auditor reading
// the JSON would: multiply the retention of every scored check that ran.
func productOf(sigs []Signal) float64 {
	r := 1.0
	for _, s := range sigs {
		if s.Kind != Scored || s.Level == Unknown {
			continue
		}
		r *= clamp01(s.Score)
	}
	return r
}

func TestReport_ScoreIsExactlyTheProductOfRetentions(t *testing.T) {
	rng := rand.New(rand.NewSource(20260829))
	levels := []Level{Pass, Warn, Fail, Unknown}
	kinds := []Kind{Scored, Diagnostic}

	for trial := 0; trial < 500; trial++ {
		var sigs []Signal
		for n := rng.Intn(8) + 1; n > 0; n-- {
			sigs = append(sigs, Signal{
				Name:  "check",
				Kind:  kinds[rng.Intn(len(kinds))],
				Level: levels[rng.Intn(len(levels))],
				Score: rng.Float64(),
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
		{Signals: []Signal{scored("a", 0.8)}},
		{Signals: []Signal{scored("b", 0.5)}},
	}}
	rep.finalize()

	if rep.Score != 40 { // 0.8 × 0.5
		t.Errorf("Score = %d, want 40", rep.Score)
	}
}

// Every scored retention is a count divided by a count, so R for one dataset
// is exactly U/E — and factors into the two stages a reader acts on.
func TestRetention_FactorsIntoCompletenessTimesUsability(t *testing.T) {
	const E, H, U = 10000, 9000, 8100

	completeness := completenessSignal(H, E, nil, "t")
	usability := usabilitySignal(tableProfile{
		rows: H, usable: U, cols: []colProfile{{name: "c", nulls: H - U}},
	}, "t")

	if got, want := completeness.Score, float64(H)/float64(E); got != want {
		t.Errorf("completeness = %v, want H/E = %v", got, want)
	}
	if got, want := usability.Score, float64(U)/float64(H); got != want {
		t.Errorf("usability = %v, want U/H = %v", got, want)
	}

	rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{completeness, usability}}}}
	rep.finalize()

	// (H/E) × (U/H) = U/E, with no residue from the factorisation.
	if got, want := rep.Score, pctOf(float64(U)/float64(E)); got != want {
		t.Errorf("Score = %d, want U/E = %d", got, want)
	}
	if rep.Score != 81 {
		t.Errorf("Score = %d, want 81", rep.Score)
	}
}

// A transcript of the audited New York report, kept as a test so the worked
// example in the README cannot drift away from the code.
//
//	E = 10,071,507 rows the portal holds
//	H =  5,440,343 rows held locally
//	U =  5,440,343 rows carrying both columns the query reads
//	R = U/E = 0.540172 -> 54%
//
// Freshness (122 days) is reported beside this, never multiplied into it.
func TestReport_ReproducesTheAuditedNewYorkNumbers(t *testing.T) {
	const E, H, U = 10071507, 5440343, 5440343

	rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		completenessSignal(H, E, nil, "nypd_complaints"),
		usabilitySignal(tableProfile{
			rows: H, usable: U, cols: []colProfile{{name: "date", role: roleDate}, {name: "primary_type"}},
		}, "nypd_complaints"),
	}}}}
	rep.finalize()

	if rep.Score != 54 {
		t.Errorf("Score = %d, want 54", rep.Score)
	}
	if rep.Band != BandLow {
		t.Errorf("Band = %q, want %q", rep.Band, BandLow)
	}
	if rep.Coverage != 100 {
		t.Errorf("Coverage = %d, want 100", rep.Coverage)
	}
}

// A transcript of the audited Chicago procurement-type report: 130,626 of
// 185,826 contracts record no procurement type, so 29.7% of the evidence
// survives and the answer rests on that share.
func TestReport_ReproducesTheAuditedChicagoNullCase(t *testing.T) {
	const H, U = 185826, 185826 - 130626

	s := usabilitySignal(tableProfile{
		rows: H, usable: U, cols: []colProfile{{name: "procurement_type", nulls: 130626}},
	}, "contracts")

	if got, want := s.Score, float64(U)/float64(H); got != want {
		t.Errorf("usability = %v, want %v", got, want)
	}
	rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{s}}}}
	rep.finalize()
	if rep.Score != 30 {
		t.Errorf("Score = %d, want 30", rep.Score)
	}
}

// Nothing in the scored path reads a tuning constant, so the score for a given
// set of counts is fixed by the counts alone. This is the guarantee that makes
// two reports comparable, and it is worth asserting directly: the same counts
// must always produce the same number.
func TestScore_DependsOnlyOnTheCounts(t *testing.T) {
	build := func() *Report {
		rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{
			completenessSignal(9000, 10000, nil, "t"),
			usabilitySignal(tableProfile{
				rows: 9000, usable: 8100, cols: []colProfile{{name: "c", nulls: 900}},
			}, "t"),
			// Diagnostics vary freely; none of them may move the result.
			diag(SignalSync, Fail),
			diag(SignalFreshness, Warn),
			diag(SignalLag, Warn),
		}}}}
		rep.finalize()
		return rep
	}
	first, second := build(), build()
	if first.Score != second.Score || first.Score != 81 {
		t.Errorf("scores %d and %d, want both 81", first.Score, second.Score)
	}
}
