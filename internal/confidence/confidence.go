// Copyright (c) 2026 Neomantra Corp

// Package confidence scores how far the data behind an answer can be trusted.
//
// Every other part of csq is careful to say what a number cannot support. This
// package makes that judgement computable: given a query and the datasets it
// reads, it profiles those datasets and returns a score with the evidence that
// produced it.
//
// The score answers one question and refuses the neighbouring one. It measures
// **data fitness** — did the sync finish, is the copy complete, is it current,
// are the columns this query reads actually populated, do the dates fall in a
// possible range. It says nothing about whether the finding is true, whether
// the upstream record is accurate, or whether the analysis is the right one.
// A city that under-reports a category will produce an immaculate 95 here.
//
// That distinction is the whole reason the score never travels alone. A Report
// carries the signals that built it and the limits on reading it, and the
// renderers in this package emit all three together. A bare "82%" is exactly
// the kind of number this project exists to stop people from quoting.
//
// # The formula
//
// Reliability is a product, not an average:
//
//	R = ∏ r_i        over every check that was actually performed
//
// Each check returns a retention factor r_i in [floor_i, 1] — the share of the
// answer's trustworthiness that survives it. A check that finds nothing wrong
// returns 1 and leaves R untouched. floor_i is the check's severity, stated as
// the most trust a single defect of that kind can destroy: a failed sync floors
// at 0 (no data, no reliability), stale data floors at 0.5 (old data is
// degraded, never worthless). The floors are the only tuning constants, and
// each one is a sentence in English rather than a coefficient.
//
// Every check maps its measurement to r_i through a documented, monotone
// transfer function — for a rate x of bad rows,
//
//	r = 1 - (1 - floor) × min(1, x / zeroAt)
//
// so r falls linearly from 1 at x=0 to floor at x=zeroAt, and stays there.
//
// # Why a product
//
// A weighted mean was the obvious first choice and it is wrong here, for three
// reasons that a product fixes at once.
//
// It dilutes. Averaged, New York's complaint data scores 90% while holding 54%
// of its rows: six passing checks outvote the one that matters. Multiplied it
// scores 49%, because a check that finds half the data missing removes half the
// reliability no matter how many other checks pass.
//
// It needs caps, and caps introduce cliffs. Rescuing the mean meant clamping
// the score when a critical check failed, which put a 37-point discontinuity
// across a 0.2% change in completeness. R is continuous and monotone in every
// input: more of a defect always lowers the score, by an amount proportional to
// the defect, and no threshold ever moves it in a jump.
//
// It is not comparable with itself. A mean's denominator changes with how many
// checks happened to be measurable, so two 80% scores from different datasets
// need not mean the same thing. A product has no denominator. Adding a check
// that passes leaves R exactly where it was, so R depends only on the defects
// found — never on how many ways they were looked for.
//
// # Uncertainty is reported, not averaged in
//
// A check that could not be run is excluded from the product, which means an
// unmeasured check and a passing one move R identically. That would let a gap
// in the evidence read as a clean bill of health, so the gap is carried
// alongside R rather than inside it: Coverage is the share of applicable checks
// that were actually performed. 95% at full coverage and 95% at half coverage
// are different claims, and no single number can hold both.
//
// # What R is not
//
// It is an index, not a probability. Nothing here is calibrated against known
// outcomes, so R does not estimate the chance that an answer is wrong. What it
// guarantees is consistency: the same defects always produce the same number,
// and two scores built from this catalogue are comparable with each other.
//
// Advisory checks have floor 1. They can cost nothing, so they never move R and
// need no special case in the arithmetic — concentration is reported to the
// reader because one vendor holding 61% of a total is worth knowing, and it is
// not a defect in the data.
package confidence

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Level is a signal's verdict.
type Level string

const (
	// Pass means the property was measured and is sound.
	Pass Level = "pass"
	// Warn means the property was measured and is imperfect but usable.
	Warn Level = "warn"
	// Fail means the property was measured and undermines the answer.
	Fail Level = "fail"
	// Unknown means the property could not be measured. It never scores.
	Unknown Level = "unknown"
)

// Severity floors: the most trust one defect of each kind can destroy.
//
// These are the only tuning constants in the model, and each is a claim in
// English rather than a coefficient. A floor of 0 means the defect is fatal —
// there is nothing behind the answer at all. A floor of 1 means the check is
// advisory and cannot move the index.
const (
	// FloorFatal is for defects that leave no data behind the answer: a sync
	// that never ran, failed, or was interrupted, and a table with no rows.
	FloorFatal = 0.0
	// FloorRows is for rows that arrived and then went missing relative to
	// what the sync recorded writing.
	FloorRows = 0.3
	// FloorNulls is for the emptiest column the query reads.
	FloorNulls = 0.3
	// FloorDates is for timestamps that cannot be real.
	FloorDates = 0.4
	// FloorFreshness is for data the portal stopped updating. Old data is
	// degraded, never worthless, so this check can cost at most half.
	FloorFreshness = 0.5
	// FloorLag is for a local copy behind a portal that has moved on.
	FloorLag = 0.6
	// FloorKeys is for null identifiers, which drop rows out of a join.
	FloorKeys = 0.7
	// FloorAdvisory marks a check that is reported but cannot move the index.
	FloorAdvisory = 1.0
)

// Band names a score range in words, so an interface can lead with the
// judgement rather than with the arithmetic.
const (
	BandHigh         = "high"
	BandModerate     = "moderate"
	BandLow          = "low"
	BandInsufficient = "insufficient"
)

// Signal is one measured property of one dataset, and what it contributes.
type Signal struct {
	// Name is a stable slug, e.g. "freshness". Interfaces group on it.
	Name string `json:"name"`
	// Label is the one-line sentence a reader sees next to a ✓ or ⚠.
	Label string `json:"label"`
	// Detail expands on the label when the reason matters more than the fact.
	Detail string `json:"detail,omitempty"`
	Level  Level  `json:"level"`
	// Score is this check's retention factor r in [Floor, 1]: the share of the
	// answer's trustworthiness that survives it. 1 means the check found
	// nothing wrong and leaves the reliability index untouched. Meaningless
	// when the level is Unknown.
	Score float64 `json:"score"`
	// Floor is the lowest retention this check can return — its severity,
	// stated as the most trust a single defect of this kind can destroy.
	// A fatal check floors at 0; an advisory one floors at 1, which is what
	// makes it unable to move the index without needing a special case.
	Floor float64 `json:"floor"`
	// Dataset is the table this was measured on; empty for report-level
	// signals derived from the result rows rather than from a table.
	Dataset string `json:"dataset,omitempty"`
}

// Advisory reports whether this signal is shown but cannot move the index.
// It is a consequence of the floor, not a flag: a check that can cost nothing
// multiplies by 1.
func (s Signal) Advisory() bool { return s.Floor >= 1 }

// counts reports whether this signal enters the product.
func (s Signal) counts() bool { return !s.Advisory() && s.Level != Unknown }

// retention clamps the signal's score into the range its floor permits, so a
// transfer function cannot return a factor outside the severity it declared.
func (s Signal) retention() float64 {
	r := s.Score
	if r < s.Floor {
		r = s.Floor
	}
	if r > 1 {
		r = 1
	}
	if r < 0 {
		r = 0
	}
	return r
}

// DatasetReport is the assessment of one dataset a query reads.
type DatasetReport struct {
	// Table is the local DuckDB table.
	Table string `json:"table"`
	// DatasetID is the Socrata 4x4 it was synced from.
	DatasetID string `json:"dataset_id,omitempty"`
	// Name is the upstream dataset title.
	Name string `json:"name,omitempty"`
	// Portal is the alias of the attached database holding it.
	Portal string `json:"portal"`
	// Concept is the logical role this dataset plays in the mode.
	Concept string `json:"concept,omitempty"`
	// Rows held locally.
	Rows int64 `json:"rows"`
	// ExpectedRows is the reference count completeness was measured against,
	// zero when none was available.
	ExpectedRows int64 `json:"expected_rows,omitempty"`
	// UpstreamUpdated is when the portal last changed this dataset's data.
	UpstreamUpdated *time.Time `json:"upstream_updated,omitempty"`
	// LastSynced is when csq last pulled it successfully.
	LastSynced *time.Time `json:"last_synced,omitempty"`

	Signals []Signal `json:"signals"`
	Score   int      `json:"score"`
	Band    string   `json:"band"`
}

// FreshnessDays returns whole days since the portal last changed this
// dataset's data, and whether that is known at all.
func (d DatasetReport) FreshnessDays() (int, bool) {
	if d.UpstreamUpdated == nil {
		return 0, false
	}
	return int(time.Since(*d.UpstreamUpdated).Hours() / 24), true
}

// Report is the confidence assessment for one query.
type Report struct {
	// Mode and Query name what was assessed.
	Mode  string `json:"mode,omitempty"`
	Query string `json:"query,omitempty"`

	// Score is the weighted mean as a percentage, after caps.
	Score int `json:"score"`
	// Band is Score in words.
	Band string `json:"band"`
	// Headline is the one-line summary of what the score is about.
	Headline string `json:"headline,omitempty"`

	// Datasets is the per-dataset detail, in the order the query reads them.
	Datasets []DatasetReport `json:"datasets"`
	// Signals is every signal from every dataset plus any report-level ones,
	// flattened for rendering: passes first, then warnings, then failures.
	Signals []Signal `json:"signals"`

	// FreshnessDays is the age of the *stalest* dataset the query reads,
	// because a join is only as current as its oldest input. Nil when no
	// dataset reports an upstream timestamp.
	FreshnessDays *int `json:"freshness_days,omitempty"`

	// Limits states what this score does not mean. It is populated on every
	// report and rendered with it; a fitness score read as a truth score is
	// the specific misuse this package has to design against.
	Limits []string `json:"limits"`

	// Coverage is the share of applicable checks that could actually be
	// performed, as a percentage. It is reported beside Score rather than
	// folded into it: an unmeasured check leaves the product untouched, so
	// without this a gap in the evidence would be indistinguishable from a
	// clean result. 95% at full coverage and 95% at half coverage are
	// different claims.
	Coverage int `json:"coverage"`

	// Assessed is false when no dataset could be profiled at all, in which
	// case Score is meaningless and interfaces must say "not assessed" rather
	// than render a zero.
	Assessed bool `json:"assessed"`
	// Elapsed is how long the profiling pass took.
	Elapsed string `json:"elapsed,omitempty"`
}

// standardLimits is attached to every report. These are not caveats about the
// civic data — the modes carry those — but about the score itself.
var standardLimits = []string{
	"This scores whether the data is fit to be queried, not whether the answer " +
		"is true. Well-formed data can still be wrong, biased, or measuring " +
		"something other than what you think.",
	"Completeness is measured against a reference row count recorded when the " +
		"dataset was mapped, not against a live count from the portal. A dataset " +
		"that grew or was revised upstream will not match it exactly.",
	"Only the columns this query reads are profiled. A clean score says nothing " +
		"about the rest of the table.",
	"Freshness is the portal's own data_updated_at. It moves when a portal " +
		"republishes unchanged rows, so a recent timestamp is evidence that " +
		"something was published, not that the data changed. Read the dataset's " +
		"description before calling anything current.",
}

// Grade converts a percentage to its band.
func Grade(score int) string {
	switch {
	case score >= 85:
		return BandHigh
	case score >= 65:
		return BandModerate
	case score >= 40:
		return BandLow
	default:
		return BandInsufficient
	}
}

// finalize computes the index and ordering once every check has been collected.
//
// Ordering is deliberate: the flattened signal list runs passes, then
// advisories, then warnings, then failures. A reader scanning the block sees
// what held up before what did not, which is the order the evidence should be
// weighed in — and the problems land last, where the eye stops.
func (r *Report) finalize() {
	r.Limits = append([]string{}, standardLimits...)

	product := 1.0
	measured, unknown := 0, 0
	for i := range r.Datasets {
		d := &r.Datasets[i]
		d.Score, d.Band = scoreSignals(d.Signals)
		for _, s := range d.Signals {
			if s.Advisory() {
				continue
			}
			if s.Level == Unknown {
				unknown++
				continue
			}
			measured++
			product *= s.retention()
		}
		r.Signals = append(r.Signals, d.Signals...)
	}

	if measured == 0 {
		// Nothing measurable. Say so rather than reporting a zero, which reads
		// as "assessed and terrible" instead of "not assessed" — opposite
		// instructions to a reader. Datasets may still be listed, and the
		// renderer distinguishes "nothing to profile" from "profiled nothing"
		// by whether any are present.
		r.Assessed = false
		r.Score, r.Band, r.Coverage = 0, BandInsufficient, 0
		sortSignals(r.Signals)
		return
	}

	r.Assessed = true
	r.Score = pctOf(product)
	r.Band = Grade(r.Score)
	// Coverage travels beside the index rather than inside it. A check that
	// could not be run is excluded from the product, which makes it
	// indistinguishable from one that passed; saying how much of the intended
	// scrutiny actually happened is what stops that from reading as a clean
	// bill of health.
	r.Coverage = pctOf(float64(measured) / float64(measured+unknown))

	// The stalest input governs the report's freshness.
	for _, d := range r.Datasets {
		if days, ok := d.FreshnessDays(); ok {
			if r.FreshnessDays == nil || days > *r.FreshnessDays {
				n := days
				r.FreshnessDays = &n
			}
		}
	}
	sortSignals(r.Signals)
}

// scoreSignals computes the index and band for one signal set.
func scoreSignals(sigs []Signal) (int, string) {
	product := 1.0
	measured := 0
	for _, s := range sigs {
		if !s.counts() {
			continue
		}
		measured++
		product *= s.retention()
	}
	if measured == 0 {
		return 0, BandInsufficient
	}
	n := pctOf(product)
	return n, Grade(n)
}

// pctOf rounds a factor in [0,1] to a percentage.
//
// A non-zero product never rounds down to 0: "0%" is the report's way of
// saying there is nothing behind the answer at all, and a very poor score is a
// different statement from a fatal one.
func pctOf(f float64) int {
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

// levelRank orders signals for display. Advisories sit between passes and
// warnings: they are not problems, but they are things to read.
func levelRank(s Signal) int {
	switch s.Level {
	case Pass:
		return 0
	case Unknown:
		return 1
	case Warn:
		if s.Advisory() {
			return 2
		}
		return 3
	case Fail:
		return 4
	}
	return 5
}

func sortSignals(sigs []Signal) {
	sort.SliceStable(sigs, func(i, j int) bool {
		return levelRank(sigs[i]) < levelRank(sigs[j])
	})
}

// Problems returns the signals a reader must not miss: every warning and
// failure, advisories included.
func (r *Report) Problems() []Signal {
	var out []Signal
	for _, s := range r.Signals {
		if s.Level == Warn || s.Level == Fail {
			out = append(out, s)
		}
	}
	return out
}

// Confirmations returns the signals that held up.
func (r *Report) Confirmations() []Signal {
	var out []Signal
	for _, s := range r.Signals {
		if s.Level == Pass {
			out = append(out, s)
		}
	}
	return out
}

// Unmeasured returns the signals that could not be evaluated. These are worth
// surfacing separately: "we did not check" and "we checked and it was fine"
// are different claims, and collapsing them is how a gap becomes a guarantee.
func (r *Report) Unmeasured() []Signal {
	var out []Signal
	for _, s := range r.Signals {
		if s.Level == Unknown {
			out = append(out, s)
		}
	}
	return out
}

// FreshnessLine renders the source-freshness footer, or "" when unknown.
func (r *Report) FreshnessLine() string {
	if r.FreshnessDays == nil {
		return ""
	}
	d := *r.FreshnessDays
	switch {
	case d <= 0:
		return "Source freshness: updated today"
	case d == 1:
		return "Source freshness: 1 day"
	default:
		return fmt.Sprintf("Source freshness: %d days", d)
	}
}

// pct formats a ratio as a percentage with one decimal, trimming a trailing
// ".0" so "100%" does not render as "100.0%".
func pct(f float64) string {
	s := fmt.Sprintf("%.1f", f*100)
	return strings.TrimSuffix(s, ".0") + "%"
}

// commas formats n with thousands separators.
func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	if len(s) > 3 {
		var out []byte
		for i, c := range []byte(s) {
			if i > 0 && (len(s)-i)%3 == 0 {
				out = append(out, ',')
			}
			out = append(out, c)
		}
		s = string(out)
	}
	if neg {
		return "-" + s
	}
	return s
}
