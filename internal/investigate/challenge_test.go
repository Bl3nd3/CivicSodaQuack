// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
)

// planWith builds the minimum plan the challenges read: a probe name, its
// concept, and the tables behind it.
func planWith(probe, concept string, tables ...string) *Plan {
	return &Plan{Probes: []PlannedProbe{{
		Name: probe, Concept: concept, Tables: tables, Skipped: false,
	}}}
}

func validationWith(cov Coverage, ds ...confidence.DatasetReport) *Validation {
	v := &Validation{Coverage: []Coverage{cov}}
	if len(ds) > 0 {
		v.Confidence = &confidence.Report{Assessed: true, Datasets: ds}
	}
	return v
}

func challengeNamed(f Finding, name string) ChallengeResult {
	for _, c := range f.Challenges {
		if c.Name == name {
			return c
		}
	}
	return ChallengeResult{Name: "(absent)"}
}

// The headline case: a local copy two thirds short of the portal can account
// for a 7% fall on its own, so the fall is not evidence of anything.
func TestChallenge_ShortLocalCopyWithdrawsAFall(t *testing.T) {
	f := Finding{
		Probe: "records", Direction: Down, Change: -0.07,
		LatestPeriod: 2024, BaselineFrom: 2021, BaselineTo: 2023,
	}
	v := validationWith(
		Coverage{Concept: "crimes", Table: "crimes", Known: true, LastComplete: 2024},
		confidence.DatasetReport{Table: "crimes", Completeness: 0.34})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	got := a.Findings[0]
	if !got.Withdrawn {
		t.Fatal("a 66% shortfall must withdraw a 7% fall — the missing rows explain it")
	}
	if got.WithdrawnBy != "local-copy-shortfall" {
		t.Errorf("withdrawn by %q, want local-copy-shortfall", got.WithdrawnBy)
	}
}

// A shortfall smaller than the movement cannot account for it, so the finding
// stands — with the shortfall stated.
func TestChallenge_SmallShortfallDoesNotWithdraw(t *testing.T) {
	f := Finding{
		Probe: "records", Direction: Down, Change: -0.40,
		LatestPeriod: 2024, BaselineFrom: 2021, BaselineTo: 2023,
	}
	v := validationWith(
		Coverage{Concept: "crimes", Table: "crimes", Known: true, LastComplete: 2024},
		confidence.DatasetReport{Table: "crimes", Completeness: 0.98})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	if a.Findings[0].Withdrawn {
		t.Fatal("a 2% shortfall cannot account for a 40% fall")
	}
	if c := challengeNamed(a.Findings[0], "local-copy-shortfall"); !c.Survived {
		t.Errorf("challenge should record survival, got %+v", c)
	}
}

// A rise is not explained by missing rows: rows you do not have cannot inflate
// a count. The challenge must not fire on it.
func TestChallenge_ShortfallDoesNotWithdrawARise(t *testing.T) {
	f := Finding{
		Probe: "records", Direction: Up, Change: 0.07,
		LatestPeriod: 2024, BaselineFrom: 2021, BaselineTo: 2023,
	}
	v := validationWith(
		Coverage{Concept: "crimes", Table: "crimes", Known: true, LastComplete: 2024},
		confidence.DatasetReport{Table: "crimes", Completeness: 0.34})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	if a.Findings[0].Withdrawn {
		t.Fatal("missing rows cannot explain a rise")
	}
}

// Coverage that could not be measured means the measured period cannot be
// shown complete, and the finding goes.
func TestChallenge_UnknownCoverageWithdraws(t *testing.T) {
	f := Finding{Probe: "records", Direction: Down, Change: -0.30, LatestPeriod: 2024}
	v := validationWith(Coverage{Concept: "crimes", Table: "crimes", Known: false})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	if !a.Findings[0].Withdrawn {
		t.Fatal("unmeasured coverage must withdraw the finding")
	}
	if a.Findings[0].WithdrawnBy != "partial-period" {
		t.Errorf("withdrawn by %q, want partial-period", a.Findings[0].WithdrawnBy)
	}
}

// The reading a rate finding prints is about the numerator. When the numerator
// did not move and the denominator did, that sentence is wrong.
func TestChallenge_RateMovedOnlyByItsDenominator(t *testing.T) {
	f := Finding{
		Probe: "rate", Per: "1,000 arrests", Direction: Down, Change: -0.50,
		LatestPeriod: 2024, BaselineFrom: 2023, BaselineTo: 2023,
		Series: []Point{
			{Period: 2023, Value: 100, Denominator: 1000, Indicator: 100, Complete: true},
			{Period: 2024, Value: 101, Denominator: 2000, Indicator: 50.5, Complete: true},
		},
	}
	v := validationWith(Coverage{Concept: "complaints", Table: "copa", Known: true, LastComplete: 2024})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("rate", "complaints", "copa"), v, a)

	if !a.Findings[0].Withdrawn {
		t.Fatal("a rate that moved only because arrests doubled must be withdrawn")
	}
	if a.Findings[0].WithdrawnBy != "denominator-moved" {
		t.Errorf("withdrawn by %q, want denominator-moved", a.Findings[0].WithdrawnBy)
	}
}

// A count has no denominator, and the challenge must say so rather than
// quietly reporting a survival it did not test.
func TestChallenge_DenominatorIsNotApplicableToCounts(t *testing.T) {
	f := Finding{
		Probe: "records", Direction: Down, Change: -0.30,
		LatestPeriod: 2024, BaselineFrom: 2021, BaselineTo: 2023,
	}
	v := validationWith(Coverage{Concept: "crimes", Table: "crimes", Known: true, LastComplete: 2024})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	c := challengeNamed(a.Findings[0], "denominator-moved")
	if !c.Survived {
		t.Errorf("a count should pass the denominator challenge, got %+v", c)
	}
}

// A step change inside the window makes the baseline an average of two
// different regimes. That is a caveat, not a withdrawal: the step may be the
// very thing being measured.
func TestChallenge_SeriesBreakIsNotedNotWithdrawn(t *testing.T) {
	f := Finding{
		Probe: "records", Direction: Down, Change: -0.10,
		LatestPeriod: 2024, BaselineFrom: 2021, BaselineTo: 2023,
		Series: []Point{
			{Period: 2021, Indicator: 10, Complete: true},
			{Period: 2022, Indicator: 100, Complete: true}, // tenfold step
			{Period: 2023, Indicator: 100, Complete: true},
			{Period: 2024, Indicator: 90, Complete: true},
		},
	}
	v := validationWith(Coverage{Concept: "crimes", Table: "crimes", Known: true, LastComplete: 2024})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	if a.Findings[0].Withdrawn {
		t.Fatal("a series break annotates; it must not withdraw")
	}
	c := challengeNamed(a.Findings[0], "series-break")
	if c.Survived {
		t.Errorf("the break should be reported as unresolved, got %+v", c)
	}
}

// Every challenge is recorded, including the ones nothing was found in. A
// reader who sees only the failures assumes the rest were never tried.
func TestChallenge_EveryChallengeIsRecorded(t *testing.T) {
	f := Finding{
		Probe: "records", Direction: Down, Change: -0.30,
		LatestPeriod: 2024, BaselineFrom: 2021, BaselineTo: 2023,
		Series: []Point{
			{Period: 2021, Indicator: 100, Complete: true},
			{Period: 2022, Indicator: 100, Complete: true},
			{Period: 2023, Indicator: 100, Complete: true},
			{Period: 2024, Indicator: 70, Complete: true},
		},
	}
	v := validationWith(
		Coverage{Concept: "crimes", Table: "crimes", Known: true, LastComplete: 2024},
		confidence.DatasetReport{Table: "crimes", Completeness: 1})

	a := &Analysis{Findings: []Finding{f}}
	Challenge(nil, planWith("records", "crimes", "crimes"), v, a)

	got := a.Findings[0]
	if got.Withdrawn {
		t.Fatalf("a clean 30%% fall should stand, withdrawn by %q", got.WithdrawnBy)
	}
	want := []string{"partial-period", "local-copy-shortfall", "denominator-moved",
		"series-break", "sparse-baseline"}
	if len(got.Challenges) != len(want) {
		t.Fatalf("got %d challenges, want %d", len(got.Challenges), len(want))
	}
	for i, name := range want {
		if got.Challenges[i].Name != name {
			t.Errorf("challenge %d = %q, want %q", i, got.Challenges[i].Name, name)
		}
		if got.Challenges[i].Verdict == "" {
			t.Errorf("challenge %q recorded no verdict", name)
		}
	}
}
