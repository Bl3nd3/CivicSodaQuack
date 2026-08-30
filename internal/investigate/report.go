// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"fmt"
	"strings"
	"time"
)

// The four verdicts an investigation can reach.
//
// "Insufficient evidence" is a first-class outcome rather than a failure. Most
// civic questions asked of most portals land there, and a tool that could not
// say so would have to manufacture one of the other three.
const (
	VerdictSupports     = "Evidence supports the claim."
	VerdictContradicts  = "Evidence does not support the claim."
	VerdictMixed        = "Evidence is mixed."
	VerdictInsufficient = "Insufficient evidence."
)

// Report is the whole investigation: the question, every step's working, and
// the verdict that follows from it.
//
// Everything needed to disagree with the conclusion is a field here. The SQL
// that produced each finding, the challenges each survived, the periods
// averaged into each baseline, the datasets profiled and what they were
// profiled against — all of it travels with the verdict rather than being
// summarised away, because a civic finding that cannot be checked is a rumour
// with a percentage attached.
type Report struct {
	Question string    `json:"question"`
	Asked    time.Time `json:"asked"`

	Discovery *Discovery `json:"discovery"`

	Investigation string `json:"investigation"`
	Title         string `json:"title"`
	Claim         string `json:"claim"`
	City          string `json:"city"`
	Portal        string `json:"portal"`

	Plan       *Plan       `json:"plan"`
	Readiness  Readiness   `json:"readiness"`
	Validation *Validation `json:"validation"`
	Analysis   *Analysis   `json:"analysis"`

	Verdict string `json:"verdict"`
	// VerdictWhy counts what the verdict was drawn from.
	VerdictWhy string `json:"verdict_why"`

	// Confidence is the evidence retention multiplied by the share of planned
	// indicators that could be answered, as a percentage. See ConfidenceNote.
	Confidence int `json:"confidence"`
	// Assessed is false when the evidence could not be profiled at all, in
	// which case Confidence is meaningless and must be shown as "not
	// assessed" rather than as a number.
	Assessed bool `json:"confidence_assessed"`
	// Retention and Coverage are the two factors, kept separate so a low score
	// can be read: bad data, or an unanswerable question.
	Retention int `json:"retention"`
	Coverage  int `json:"coverage"`

	// Caveats are every limit that applies to this verdict, measurement-
	// specific first and standing limits last.
	Caveats []string `json:"caveats"`

	// Snapshot names the corpus state this verdict was drawn from.
	Snapshot string `json:"snapshot"`
	// Reproduce is the command that runs this investigation again.
	Reproduce string `json:"reproduce"`
}

// ConfidenceNote states what the confidence figure is, in the words every
// renderer uses.
//
// It is a definition, not a hedge. The number is the share of the evidence the
// investigation set out to consult that was actually there, usable, and
// reached — never a probability that the verdict is correct, which is not a
// thing any amount of profiling can measure.
const ConfidenceNote = "the share of the evidence this investigation set out to " +
	"consult that was present, usable, and reached"

// finalize derives the verdict, the confidence, and the caveats once every
// earlier step has run.
func (r *Report) finalize() {
	r.verdict()
	r.confidence()
	r.caveats()
}

// verdict counts what survived and states the result.
//
// It is a count, not a weighing. Findings are not scored against each other by
// importance, because any importance ranking would be an editorial judgement
// wearing the costume of a calculation — the same reason the ranking mode
// refuses to emit a composite city score.
func (r *Report) verdict() {
	var supporting, against, withdrawn, flat int
	for _, f := range r.Analysis.Findings {
		switch {
		case f.Withdrawn:
			withdrawn++
		case f.Direction == Flat:
			flat++
		case f.Supports:
			supporting++
		default:
			against++
		}
	}

	switch {
	case supporting > 0 && against > 0:
		r.Verdict = VerdictMixed
	case supporting > 0:
		r.Verdict = VerdictSupports
	case against > 0:
		r.Verdict = VerdictContradicts
	default:
		r.Verdict = VerdictInsufficient
	}

	parts := []string{fmt.Sprintf("%d of %d planned indicators produced a measurement",
		len(r.Analysis.Findings), r.Plan.Total)}
	if supporting > 0 {
		parts = append(parts, fmt.Sprintf("%d moved as the plan said would support the claim", supporting))
	}
	if against > 0 {
		parts = append(parts, fmt.Sprintf("%d moved against it", against))
	}
	if flat > 0 {
		parts = append(parts, fmt.Sprintf("%d did not move materially", flat))
	}
	if withdrawn > 0 {
		parts = append(parts, fmt.Sprintf("%d were withdrawn under challenge", withdrawn))
	}
	r.VerdictWhy = strings.Join(parts, "; ") + "."
}

// confidence multiplies evidence retention by plan coverage.
//
// Both factors are counts over counts and neither introduces a constant, so
// the product keeps a plain reading: of the records this investigation meant to
// consult, this share was present, usable, and actually reached. Retention
// answers "was the data there"; coverage answers "did we get to ask the
// question". An investigation that reads pristine data to answer one of four
// questions has not earned the confidence of one that answered all four, and
// multiplying is what says so.
func (r *Report) confidence() {
	cov := r.answered()
	r.Coverage = pct(cov)

	rep := r.Validation.Confidence
	if rep == nil || !rep.Assessed {
		// Retention unknown. The confidence package is emphatic that an
		// unmeasured check must never render as a zero, and the same holds
		// here: coverage alone is not a confidence figure, because it says
		// nothing about whether the data behind the answered questions was
		// any good.
		r.Assessed = false
		r.Confidence = 0
		return
	}
	r.Retention = rep.Score
	r.Assessed = true
	r.Confidence = pct(float64(rep.Score) / 100 * cov)
}

// caveats assembles every limit on this verdict, run-specific first.
//
// Nothing is truncated. A caveat list long enough to be inconvenient is a
// property of civic data rather than a presentation problem, and the export
// paths in this project already refuse to hand over rows without them.
func (r *Report) caveats() {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	// What this run measured about this corpus, which is the part a reader
	// cannot get from the documentation.
	for _, n := range r.Validation.Notes {
		add(n)
	}
	for _, f := range r.Analysis.Findings {
		if f.Withdrawn {
			add(fmt.Sprintf("%q was withdrawn: %s", f.Asks, withdrawalReason(f)))
			continue
		}
		for _, c := range f.Challenges {
			if !c.Survived && !c.Withdrew {
				add(fmt.Sprintf("%s: %s", f.Probe, c.Verdict))
			}
		}
	}
	for _, u := range r.Analysis.Unanswered {
		add(fmt.Sprintf("%q went unanswered: %s", u.Asks, u.Reason))
	}
	for _, pp := range r.Plan.Probes {
		if pp.Skipped {
			add(fmt.Sprintf("%q was not run — %s", pp.Asks, pp.Reason))
		}
	}
	// Standing limits: the indicators that produced findings, then the
	// investigation's own, then the city's.
	for _, f := range r.Analysis.Findings {
		if f.Withdrawn {
			continue
		}
		for _, c := range f.Caveats {
			add(c)
		}
	}
	r.Caveats = out
}

// answered is the share of planned indicators that produced a measurement.
//
// It counts findings rather than runnable probes, because those differ and the
// difference is exactly the case that would flatter the score: a probe whose
// tables are present and whose SQL expands cleanly can still return a series too
// short to compare, and reporting it as "answered" would credit the
// investigation for a question it did not settle.
//
// A withdrawn finding still counts here. The evidence behind it was reached,
// read and measured; the withdrawal is a judgement about what the measurement
// supports, which is the verdict's business rather than the confidence's.
func (r *Report) answered() float64 {
	if r.Plan == nil || r.Plan.Total == 0 {
		return 0
	}
	return float64(len(r.Analysis.Findings)) / float64(r.Plan.Total)
}

// withdrawalReason finds the challenge that took a finding down.
func withdrawalReason(f Finding) string {
	for _, c := range f.Challenges {
		if c.Withdrew {
			return c.Verdict
		}
	}
	return "it did not survive challenge"
}

// AddCaveats appends limits from outside this package — the investigation's
// own, and the ones the city's binding carries — after the run-specific ones.
//
// They arrive last on purpose. A reader who stops after three bullets should
// have read what this run discovered rather than what is true of every run.
func (r *Report) AddCaveats(cs ...string) {
	seen := map[string]bool{}
	for _, c := range r.Caveats {
		seen[c] = true
	}
	for _, c := range cs {
		c = strings.TrimSpace(c)
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		r.Caveats = append(r.Caveats, c)
	}
}

// Surviving returns the findings that still stand.
func (r *Report) Surviving() []Finding {
	var out []Finding
	for _, f := range r.Analysis.Findings {
		if !f.Withdrawn {
			out = append(out, f)
		}
	}
	return out
}

// Withdrawn returns the findings challenge took down.
func (r *Report) Withdrawn() []Finding {
	var out []Finding
	for _, f := range r.Analysis.Findings {
		if f.Withdrawn {
			out = append(out, f)
		}
	}
	return out
}

// pct rounds a factor in [0,1] to a percentage, never rounding a non-zero
// value down to zero — "0%" is reserved for "there is nothing here at all",
// which is a different statement from "very little".
func pct(f float64) int {
	if f <= 0 {
		return 0
	}
	n := int(f*100 + 0.5)
	if n < 1 {
		return 1
	}
	if n > 100 {
		return 100
	}
	return n
}
