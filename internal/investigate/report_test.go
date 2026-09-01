// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"strings"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
)

// reportWith assembles the minimum a report needs to finalize: a plan of the
// given size, a validation carrying the retention, and the findings that came
// out of it.
//
// Coverage is derived from the findings rather than passed in, because that is
// what the report itself does: a probe that ran and produced nothing is not a
// question that was answered.
func reportWith(total int, retention int, findings ...Finding) *Report {
	v := &Validation{Coverage: []Coverage{}, Notes: []string{}}
	if retention >= 0 {
		v.Confidence = &confidence.Report{Assessed: true, Score: retention}
	}
	r := &Report{
		Plan:       &Plan{Runnable: total, Total: total},
		Validation: v,
		Analysis:   &Analysis{Findings: findings},
	}
	r.finalize()
	return r
}

func supporting() Finding {
	return Finding{Probe: "a", Direction: Down, Supports: true, Change: -0.2}
}

func contradicting() Finding {
	return Finding{Probe: "b", Direction: Up, Supports: false, Change: 0.2}
}

func TestVerdict_SupportsWhenEverythingPointsOneWay(t *testing.T) {
	r := reportWith(2, 100, supporting(), supporting())
	if r.Verdict != VerdictSupports {
		t.Errorf("verdict = %q, want %q", r.Verdict, VerdictSupports)
	}
}

func TestVerdict_MixedWhenIndicatorsDisagree(t *testing.T) {
	r := reportWith(2, 100, supporting(), contradicting())
	if r.Verdict != VerdictMixed {
		t.Errorf("verdict = %q, want %q", r.Verdict, VerdictMixed)
	}
}

func TestVerdict_ContradictedWhenEverythingPointsTheOtherWay(t *testing.T) {
	r := reportWith(2, 100, contradicting())
	if r.Verdict != VerdictContradicts {
		t.Errorf("verdict = %q, want %q", r.Verdict, VerdictContradicts)
	}
}

// The one that matters most: nothing survived, so there is no verdict to
// reach. A tool that could not say this would have to manufacture one of the
// other three.
func TestVerdict_InsufficientWhenEverythingWasWithdrawn(t *testing.T) {
	withdrawn := supporting()
	withdrawn.Withdrawn, withdrawn.WithdrawnBy = true, "local-copy-shortfall"
	r := reportWith(4, 100, withdrawn)
	if r.Verdict != VerdictInsufficient {
		t.Errorf("verdict = %q, want %q", r.Verdict, VerdictInsufficient)
	}
	if !strings.Contains(r.VerdictWhy, "withdrawn") {
		t.Errorf("VerdictWhy = %q, must account for the withdrawal", r.VerdictWhy)
	}
}

func TestVerdict_FlatFindingsDoNotDecideAnything(t *testing.T) {
	flat := Finding{Probe: "c", Direction: Flat, Change: 0.01}
	r := reportWith(1, 100, flat)
	if r.Verdict != VerdictInsufficient {
		t.Errorf("verdict = %q; a flat indicator supports nothing", r.Verdict)
	}
}

// The published identity: confidence is retention times coverage, and nothing
// else. This test exists to fail if anyone adds a term to it.
func TestConfidence_IsRetentionTimesCoverage(t *testing.T) {
	// One of two indicators answered, 80% of the records behind it usable.
	r := reportWith(2, 80, supporting())
	if r.Retention != 80 {
		t.Errorf("retention = %d, want 80", r.Retention)
	}
	if r.Coverage != 50 {
		t.Errorf("coverage = %d, want 50", r.Coverage)
	}
	if r.Confidence != 40 {
		t.Errorf("confidence = %d, want 40 (0.80 × 0.50)", r.Confidence)
	}
}

func TestConfidence_FullEvidenceAndFullCoverageIsFull(t *testing.T) {
	r := reportWith(1, 100, supporting())
	if r.Confidence != 100 {
		t.Errorf("confidence = %d, want 100", r.Confidence)
	}
}

// A number that could not be measured must not render as a zero: the two
// instruct a reader to do opposite things.
func TestConfidence_UnprofiledEvidenceIsNotAssessed(t *testing.T) {
	r := reportWith(2, -1, supporting())
	if r.Assessed {
		t.Fatal("confidence must not claim to be assessed with no retention measured")
	}
	if r.Confidence != 0 {
		t.Errorf("confidence = %d; an unassessed report carries no score", r.Confidence)
	}
}

// Coverage alone is not a confidence figure. An investigation that answered
// every question against unmeasurable data has not earned one.
func TestConfidence_CoverageAloneIsNotConfidence(t *testing.T) {
	r := reportWith(4, -1, supporting())
	if r.Assessed {
		t.Error("full coverage over unprofiled data must not read as assessed")
	}
}

// A withdrawn finding is a result. Deleting it would make the survivors look
// like everything the investigation found.
func TestCaveats_WithdrawnFindingsAreReported(t *testing.T) {
	withdrawn := supporting()
	withdrawn.Asks = "Are fewer records published?"
	withdrawn.Withdrawn = true
	withdrawn.Challenges = []ChallengeResult{{
		Name: "local-copy-shortfall", Withdrew: true,
		Verdict: "crimes is 66% short of the portal's count",
	}}
	r := reportWith(1, 100, withdrawn)

	joined := strings.Join(r.Caveats, "\n")
	if !strings.Contains(joined, "Are fewer records published?") {
		t.Errorf("the withdrawn question is missing from the caveats:\n%s", joined)
	}
	if !strings.Contains(joined, "66% short") {
		t.Errorf("the withdrawal reason is missing from the caveats:\n%s", joined)
	}
	if len(r.Surviving()) != 0 {
		t.Error("a withdrawn finding must not be reported as surviving")
	}
	if len(r.Withdrawn()) != 1 {
		t.Error("a withdrawn finding must still be retrievable")
	}
}

// A skipped indicator is a question the investigation did not test. Reporting a
// verdict without saying so would be a claim it did not earn.
func TestCaveats_SkippedIndicatorsAreReported(t *testing.T) {
	r := &Report{
		Plan: &Plan{Runnable: 1, Total: 2, Probes: []PlannedProbe{{
			Name: "b", Asks: "Is the rate falling?", Skipped: true,
			Reason: "not synced yet: arrests",
		}}},
		Validation: &Validation{Confidence: &confidence.Report{Assessed: true, Score: 100}},
		Analysis:   &Analysis{Findings: []Finding{supporting()}},
	}
	r.finalize()

	joined := strings.Join(r.Caveats, "\n")
	if !strings.Contains(joined, "Is the rate falling?") {
		t.Errorf("the skipped question is missing from the caveats:\n%s", joined)
	}
	if !strings.Contains(joined, "not synced yet") {
		t.Errorf("the skip reason is missing from the caveats:\n%s", joined)
	}
}

func TestAddCaveats_AppendsWithoutDuplicating(t *testing.T) {
	r := reportWith(1, 100, supporting())
	before := len(r.Caveats)
	r.AddCaveats("a limit", "a limit", "")
	if len(r.Caveats) != before+1 {
		t.Errorf("got %d caveats, want %d — duplicates and blanks must be dropped",
			len(r.Caveats), before+1)
	}
}

// A probe can run cleanly and still settle nothing — a series too short to
// compare, for instance. Counting it as answered would credit the
// investigation for a question it did not resolve.
func TestConfidence_ProbesThatRanButAnsweredNothingDoNotCount(t *testing.T) {
	r := &Report{
		// Four indicators planned, all runnable, one of which measured.
		Plan:       &Plan{Runnable: 4, Total: 4},
		Validation: &Validation{Confidence: &confidence.Report{Assessed: true, Score: 100}},
		Analysis: &Analysis{
			Findings: []Finding{supporting()},
			Unanswered: []Unanswered{
				{Probe: "b", Asks: "?", Reason: "only one complete period"},
				{Probe: "c", Asks: "?", Reason: "only one complete period"},
				{Probe: "d", Asks: "?", Reason: "only one complete period"},
			},
		},
	}
	r.finalize()

	if r.Coverage != 25 {
		t.Errorf("coverage = %d%%, want 25%% — three indicators settled nothing",
			r.Coverage)
	}
	if r.Confidence != 25 {
		t.Errorf("confidence = %d, want 25 (1.00 × 0.25)", r.Confidence)
	}
	if !strings.Contains(r.VerdictWhy, "1 of 4") {
		t.Errorf("VerdictWhy = %q, should say 1 of 4 produced a measurement", r.VerdictWhy)
	}
}
