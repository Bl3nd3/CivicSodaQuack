// Copyright (c) 2026 Neomantra Corp

package investigate

import "fmt"

// ChallengeResult is one attempt to explain a finding away.
//
// Every challenge is recorded, including the ones the finding survived. A
// report that printed only the successful attacks would let a reader assume the
// rest were never tried, and "we checked and it held" is the load-bearing half
// of a verdict anyone should act on.
type ChallengeResult struct {
	Name string `json:"name"`
	// Asks is the alternative explanation, phrased as the question a sceptic
	// would put to the finding.
	Asks string `json:"asks"`
	// Survived is true when the finding is still standing after this test.
	Survived bool `json:"survived"`
	// Verdict says what the test found, in one line.
	Verdict string `json:"verdict"`
	// Withdrew marks the challenge that took the finding down.
	Withdrew bool `json:"withdrew"`
}

// Challenge is step six: try to explain each finding away, and withdraw the
// ones that cannot survive it.
//
// A challenge either withdraws a finding or annotates it. Neither applies a
// numeric penalty, and that is deliberate. Scoring a finding down by some
// fraction because an objection is "partly" valid would need a coefficient
// nobody can derive, and would produce a number that looks measured and is not.
// A finding either rests on evidence that survives the objection or it does
// not, and the verdict counts what is left standing.
func Challenge(inv *Investigation, plan *Plan, v *Validation, a *Analysis) {
	for i := range a.Findings {
		f := &a.Findings[i]
		f.Challenges = []ChallengeResult{
			challengePartialPeriod(f, v, plan),
			challengeCoverageShortfall(f, v, plan),
			challengeDenominator(f),
			challengeDiscontinuity(f),
			challengeSparseBaseline(f),
		}
		for _, c := range f.Challenges {
			if c.Withdrew {
				f.Withdrawn, f.WithdrawnBy = true, c.Name
				break
			}
		}
	}
}

// challengePartialPeriod asks whether the measured period had actually
// finished.
//
// Analyze already refuses to measure an incomplete period, so this normally
// confirms rather than withdraws. It stays because the guarantee is worth
// stating explicitly in the output: "the year is not over yet" is the single
// most common reason a civic time series appears to fall off a cliff, and a
// reader deserves to see that it was checked.
func challengePartialPeriod(f *Finding, v *Validation, plan *Plan) ChallengeResult {
	c := ChallengeResult{
		Name: "partial-period",
		Asks: "Is the fall just a period that has not finished yet?",
	}
	concept := conceptOf(plan, f.Probe)
	cov, ok := v.coverageFor(concept)
	if !ok || !cov.Known {
		c.Verdict = "coverage could not be measured, so it cannot be shown that " +
			"the measured period was complete"
		c.Withdrew = true
		return c
	}
	if f.LatestPeriod > cov.LastComplete {
		c.Verdict = fmt.Sprintf("%d is not covered end to end (%s ends %s)",
			f.LatestPeriod, cov.Table, cov.Last)
		c.Withdrew = true
		return c
	}
	c.Survived = true
	if cov.Partial != 0 {
		c.Verdict = fmt.Sprintf("no — %d is complete; the part-finished %d was excluded",
			f.LatestPeriod, cov.Partial)
		return c
	}
	c.Verdict = fmt.Sprintf("no — %d is covered end to end", f.LatestPeriod)
	return c
}

// challengeCoverageShortfall asks whether a fall in published records is really
// a gap in the local copy.
//
// The test is a comparison of two measured quantities rather than a judgement:
// if the share of rows missing from the local copy is at least as large as the
// fall being reported, then the missing rows could account for the entire
// finding, and the finding cannot be distinguished from a sync artifact. That
// is a withdrawal, not a caveat — the alternative explanation is sufficient.
func challengeCoverageShortfall(f *Finding, v *Validation, plan *Plan) ChallengeResult {
	c := ChallengeResult{
		Name: "local-copy-shortfall",
		Asks: "Is the fall really rows missing from the local copy?",
	}
	if v.Confidence == nil || !v.Confidence.Assessed {
		c.Verdict = "the local copy could not be profiled against the portal's count"
		return c
	}
	tables := tablesOf(plan, f.Probe)
	worst, worstTable := 0.0, ""
	for _, d := range v.Confidence.Datasets {
		if !contains(tables, d.Table) {
			continue
		}
		if short := 1 - d.Completeness; short > worst {
			worst, worstTable = short, d.Table
		}
	}
	if f.Direction == Down && worst >= absf(f.Change) {
		c.Verdict = fmt.Sprintf(
			"possibly — %s is %.0f%% short of the portal's count, which is enough "+
				"to account for a %.0f%% fall on its own",
			worstTable, worst*100, absf(f.Change)*100)
		c.Withdrew = true
		return c
	}
	c.Survived = true
	if worst > 0 {
		c.Verdict = fmt.Sprintf(
			"no — the largest local shortfall is %.0f%%, smaller than the %.0f%% movement",
			worst*100, absf(f.Change)*100)
		return c
	}
	c.Verdict = "no — the local copy matches the portal's reference count"
	return c
}

// challengeDenominator asks whether a rate moved only because its denominator
// did.
//
// A rate is two numbers, and a reader almost always attributes its movement to
// the numerator — "complaints fell" rather than "arrests rose". When the
// numerator barely moved and the denominator moved a great deal, that reading
// is simply wrong, and the finding is withdrawn rather than footnoted: the
// sentence it would print says something the data does not support.
func challengeDenominator(f *Finding) ChallengeResult {
	c := ChallengeResult{
		Name: "denominator-moved",
		Asks: "Did the rate move only because its denominator did?",
	}
	if f.Per == "" {
		c.Survived = true
		c.Verdict = "not applicable — this is a count, not a rate"
		return c
	}
	num, den, ok := componentChange(f)
	if !ok {
		c.Verdict = "the numerator and denominator could not be compared separately"
		return c
	}
	if absf(num) < MaterialChange && absf(den) > MaterialChange {
		c.Verdict = fmt.Sprintf(
			"yes — the numerator moved %.0f%% while the denominator moved %.0f%%, "+
				"so the rate change is a denominator effect",
			num*100, den*100)
		c.Withdrew = true
		return c
	}
	c.Survived = true
	c.Verdict = fmt.Sprintf("no — the numerator moved %.0f%% and the denominator %.0f%%",
		num*100, den*100)
	return c
}

// componentChange measures the numerator and denominator separately over the
// same window the finding used.
func componentChange(f *Finding) (num, den float64, ok bool) {
	var latest Point
	var base []Point
	for _, p := range f.Series {
		switch {
		case p.Period == f.LatestPeriod:
			latest = p
		case p.Period >= f.BaselineFrom && p.Period <= f.BaselineTo && p.Complete:
			base = append(base, p)
		}
	}
	if latest.Period == 0 || len(base) == 0 {
		return 0, 0, false
	}
	var sumN, sumD float64
	for _, p := range base {
		sumN += p.Value
		sumD += p.Denominator
	}
	baseN, baseD := sumN/float64(len(base)), sumD/float64(len(base))
	if baseN == 0 || baseD == 0 {
		return 0, 0, false
	}
	return (latest.Value - baseN) / baseN, (latest.Denominator - baseD) / baseD, true
}

// challengeDiscontinuity asks whether the series is comparable across the
// window at all.
//
// A single period-over-period jump larger than the movement being reported
// means something changed about how the data was produced — a new reporting
// system, a merged dataset, a definition rewritten — and a baseline drawn
// across that break is an average of two different things. This annotates
// rather than withdraws, because the break may be exactly what the
// investigation is measuring; the reader is told where it is and can judge.
func challengeDiscontinuity(f *Finding) ChallengeResult {
	c := ChallengeResult{
		Name: "series-break",
		Asks: "Is the series comparable across the window it was measured over?",
	}
	var window []Point
	for _, p := range f.Series {
		if p.Period >= f.BaselineFrom && p.Period <= f.LatestPeriod && p.Complete {
			window = append(window, p)
		}
	}
	if len(window) < 3 {
		c.Survived = true
		c.Verdict = "too few periods to look for a break"
		return c
	}
	biggest, at := 0.0, 0
	for i := 1; i < len(window); i++ {
		prev := window[i-1].Indicator
		if prev == 0 {
			continue
		}
		if jump := absf((window[i].Indicator - prev) / prev); jump > biggest {
			biggest, at = jump, window[i].Period
		}
	}
	if biggest > absf(f.Change) && biggest > MaterialChange {
		c.Verdict = fmt.Sprintf(
			"a %.0f%% step at %d is larger than the %.0f%% movement being reported — "+
				"read the baseline as spanning a possible change in how this data is produced",
			biggest*100, at, absf(f.Change)*100)
		return c
	}
	c.Survived = true
	c.Verdict = fmt.Sprintf("no single-period step (largest %.0f%%) exceeds the reported movement",
		biggest*100)
	return c
}

// challengeSparseBaseline asks whether the comparison rests on enough history.
func challengeSparseBaseline(f *Finding) ChallengeResult {
	c := ChallengeResult{
		Name: "sparse-baseline",
		Asks: "Is there enough history behind the baseline to compare against?",
	}
	n := f.BaselineTo - f.BaselineFrom + 1
	if n < BaselineWindow {
		c.Verdict = fmt.Sprintf(
			"the baseline averages %d period(s), not %d — a single unusual year "+
				"carries more of it than intended", n, BaselineWindow)
		return c
	}
	c.Survived = true
	c.Verdict = fmt.Sprintf("no — the baseline averages %d periods", n)
	return c
}

// conceptOf returns the concept a probe's period column belongs to.
func conceptOf(plan *Plan, probe string) string {
	for _, pp := range plan.Probes {
		if pp.Name == probe {
			return pp.Concept
		}
	}
	return ""
}

// tablesOf returns the local tables a probe reads.
func tablesOf(plan *Plan, probe string) []string {
	for _, pp := range plan.Probes {
		if pp.Name == probe {
			return pp.Tables
		}
	}
	return nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
