// Copyright (c) 2026 Neomantra Corp

// Package confidence measures how much of the evidence behind an answer
// actually survives to support it.
//
// # The formula
//
// For each dataset a query reads, three counts:
//
//	E  rows the portal holds        (the reference count)
//	H  rows held locally
//	U  rows held in which every column the query reads carries usable
//	   information — not null, and for a timestamp, a date that could be real
//
// The dataset's retention is the share of the intended evidence that survives,
// and the query's reliability is the product across its datasets:
//
//	completeness = min(1, H/E)     did the rows arrive
//	usability    = U/H             do they carry what the query reads
//
//	r = completeness × usability             R = ∏ r
//
// which is U/E whenever the local copy is no larger than the reference count.
// It parts company with U/E only when a dataset has grown since it was mapped:
// completeness saturates at 1 rather than exceeding it, so surplus rows cannot
// pay for a defect elsewhere in the product, and the usable share is then
// measured against what is actually held rather than against a stale
// denominator.
//
// # Why this is the whole model
//
// Every term is a count divided by a count. There are no weights, no severity
// coefficients, no saturation points and no thresholds anywhere in the
// arithmetic — nothing to tune, and therefore nothing to tune wrongly. Two
// scores are comparable because they are the same measurement, not because two
// tables of constants happened to agree.
//
// R is not an index. It is a quantity with a plain reading: the share of the
// records the query meant to consult that were actually there and usable. When
// R is 54%, a count taken from that answer rests on 54% of the records the
// portal holds. That sentence is the whole interpretation.
//
// An earlier version of this package scored eight checks using hand-chosen
// severity floors and saturation points — about twenty constants, each
// defensible alone and none of them derived. Six of the eight turned out to be
// the same measurement wearing different clothes: rows that do not survive.
// Stating it once removed every constant with it.
//
// # What is deliberately not scored
//
// Freshness and lag are reported beside R, never folded into it. Staleness
// removes no rows, so it has no reading as evidence loss; and how much
// 122-day-old data matters depends entirely on the question — fine for a 2023
// trend, useless for "what happened last week" — which csq cannot know. Any
// coefficient placed there would be invented, so the age is stated as a fact
// and left to the reader, who knows what they are asking.
//
// Concentration is likewise reported and never scored: one vendor holding most
// of a total is a fact about procurement, not a defect in the data.
//
// # What R does not mean
//
// It measures the evidence, not the truth. A dataset that is complete, current
// and fully populated scores 100% while recording something other than what a
// reader believes it records, or recording it with a bias no count can see. R
// is a ceiling on how much can be known from this corpus, never a statement
// that the finding is correct.
package confidence

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Level is how a check is presented to a reader. It is derived from the
// retention by one rule for every check (see LevelFor) rather than from
// per-check thresholds, so a warning means the same loss whichever check
// raised it.
type Level string

const (
	// Pass means the check cost nothing: the evidence survived it.
	Pass Level = "pass"
	// Warn means the check cost something a reader should see.
	Warn Level = "warn"
	// Fail means the check removed a large share of the evidence.
	Fail Level = "fail"
	// Unknown means the check could not be run. It never scores.
	Unknown Level = "unknown"
)

// Presentation cutoffs. These label an exact number; they take no part in
// computing it, and moving one changes an adjective rather than a score.
const (
	// WarnBelow is the retention under which a check earns a reader's
	// attention rather than a tick.
	WarnBelow = 0.999
	// FailBelow is the retention under which a check has removed enough
	// evidence to be called a failure.
	FailBelow = 0.90
)

// LevelFor derives a check's presentation from what it actually cost.
func LevelFor(retention float64) Level {
	switch {
	case retention >= WarnBelow:
		return Pass
	case retention >= FailBelow:
		return Warn
	default:
		return Fail
	}
}

// Band names a score range in words, so an interface can lead with the
// judgement rather than the arithmetic. Like Level, these are labels on an
// exact number rather than inputs to it.
const (
	BandHigh         = "high"
	BandModerate     = "moderate"
	BandLow          = "low"
	BandInsufficient = "insufficient"
)

// Grade converts a percentage to its band.
func Grade(score int) string {
	switch {
	case score >= 95:
		return BandHigh
	case score >= 80:
		return BandModerate
	case score >= 50:
		return BandLow
	default:
		return BandInsufficient
	}
}

// Kind separates what enters R from what is reported beside it.
type Kind string

const (
	// Scored checks are measured row losses and multiply into R.
	Scored Kind = "scored"
	// Diagnostic checks explain or qualify the result without scoring. They
	// are not a lesser class of evidence — freshness is often the most
	// important line in the block — they are simply not evidence loss.
	Diagnostic Kind = "diagnostic"
)

// Signal is one check, and what it cost.
type Signal struct {
	// Name is a stable slug, e.g. "usability". Interfaces group on it.
	Name string `json:"name"`
	// Label is the one-line sentence a reader sees next to a ✓ or ⚠.
	Label string `json:"label"`
	// Detail expands on the label when the reason matters more than the fact.
	Detail string `json:"detail,omitempty"`
	Level  Level  `json:"level"`
	Kind   Kind   `json:"kind"`

	// Score is the retention: the fraction of rows surviving this check, and
	// the factor it contributes to R. Meaningless for a Diagnostic, and for a
	// Scored check whose level is Unknown.
	Score float64 `json:"score"`

	// Lost and Of are the counts Score was computed from, carried so a reader
	// can reconstruct the arithmetic rather than take it on trust.
	Lost int64 `json:"lost,omitempty"`
	Of   int64 `json:"of,omitempty"`

	// Dataset is the table this was measured on; empty for report-level checks
	// derived from the result rows rather than from a table.
	Dataset string `json:"dataset,omitempty"`
}

// counts reports whether this check contributes a factor to R.
func (s Signal) counts() bool { return s.Kind == Scored && s.Level != Unknown }

// Advisory reports whether this check is shown but never scored.
func (s Signal) Advisory() bool { return s.Kind == Diagnostic }

// DatasetReport is the assessment of one dataset a query reads.
type DatasetReport struct {
	Table     string `json:"table"`
	DatasetID string `json:"dataset_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Portal    string `json:"portal"`
	Concept   string `json:"concept,omitempty"`

	// The three counts the formula is built from.
	Expected int64 `json:"expected"` // E, zero when no reference count exists
	Held     int64 `json:"held"`     // H
	Usable   int64 `json:"usable"`   // U

	// The factorisation, for reporting. Retention is the product of the two,
	// which equals U/E unless Completeness saturated (see the package doc).
	Completeness float64 `json:"completeness"` // min(1, H/E)
	Usability    float64 `json:"usability"`    // U/H
	Retention    float64 `json:"retention"`    // completeness × usability

	// Rows is Held under the name interfaces already display it by.
	Rows int64 `json:"rows"`

	UpstreamUpdated *time.Time `json:"upstream_updated,omitempty"`
	LastSynced      *time.Time `json:"last_synced,omitempty"`
	// AgeDays is days since UpstreamUpdated, measured against the assessment's
	// clock rather than recomputed on read. Nil when the portal reports no
	// timestamp.
	AgeDays *int `json:"age_days,omitempty"`

	Signals []Signal `json:"signals"`
	Score   int      `json:"score"`
	Band    string   `json:"band"`
}

// FreshnessDays returns whole days since the portal last changed this
// dataset's data, and whether that is known at all.
//
// The age is the one computed during assessment, not one recomputed now: the
// signal line and this must not disagree, and they would for any caller that
// pins the clock — a report could otherwise read "updated 3 days ago" above
// "Source freshness: 700 days".
func (d DatasetReport) FreshnessDays() (int, bool) {
	if d.AgeDays == nil {
		return 0, false
	}
	return *d.AgeDays, true
}

// Clone returns a deep copy, so a cached report can be handed to a caller that
// will append to it.
//
// Reports are memoised per query, and a caller may add a result-level signal
// (concentration) to what it receives. Without this, that append lands on the
// shared cached value: the signal accumulates on every run, and two concurrent
// HTTP handlers mutate and sort one slice while a third marshals it.
func (r *Report) Clone() *Report {
	if r == nil {
		return nil
	}
	out := *r
	out.Signals = append([]Signal(nil), r.Signals...)
	out.Limits = append([]string(nil), r.Limits...)
	out.Datasets = make([]DatasetReport, len(r.Datasets))
	for i, d := range r.Datasets {
		d.Signals = append([]Signal(nil), d.Signals...)
		out.Datasets[i] = d
	}
	if r.FreshnessDays != nil {
		n := *r.FreshnessDays
		out.FreshnessDays = &n
	}
	return &out
}

// Report is the assessment for one query.
type Report struct {
	Mode  string `json:"mode,omitempty"`
	Query string `json:"query,omitempty"`

	// Score is R as a percentage: the share of the evidence the query meant to
	// read that survived to support the answer.
	Score int    `json:"score"`
	Band  string `json:"band"`

	Datasets []DatasetReport `json:"datasets"`
	Signals  []Signal        `json:"signals"`

	// FreshnessDays is the age of the stalest dataset the query reads, because
	// a join is only as current as its oldest input. Reported, never scored.
	FreshnessDays *int `json:"freshness_days,omitempty"`

	// Coverage is the share of scored checks that could actually be run. A
	// check that could not run leaves the product untouched, which makes it
	// indistinguishable from one that cost nothing; this is what keeps a gap
	// in the evidence from reading as a clean result.
	Coverage int `json:"coverage"`

	// Limits states what this number does not mean.
	Limits []string `json:"limits"`

	// Assessed is false when nothing could be measured, in which case Score is
	// meaningless and interfaces must say "not assessed" rather than show a
	// zero — the two instruct a reader to do opposite things.
	Assessed bool   `json:"assessed"`
	Elapsed  string `json:"elapsed,omitempty"`
}

// standardLimits is attached to every report. These are not caveats about the
// civic data — the modes carry those — but about the number itself.
var standardLimits = []string{
	"This measures how much of the evidence survives, not whether the answer is " +
		"true. Data that is complete, current and fully populated can still record " +
		"something other than what you think it records.",
	"Completeness is measured against a reference row count recorded when the " +
		"dataset was mapped, not against a live count from the portal. A dataset " +
		"that grew or was revised upstream will not match it exactly.",
	"Only the columns this query reads are examined. A high score says nothing " +
		"about the rest of the table.",
	"Freshness is reported but never scored. How much a stale dataset matters " +
		"depends on the question being asked, which csq cannot know — read the age " +
		"beside the score and judge it yourself.",
}

// finalize computes R and the display ordering once every check is collected.
func (r *Report) finalize() {
	r.Limits = append([]string{}, standardLimits...)

	product := 1.0
	measured, unmeasured := 0, 0
	for i := range r.Datasets {
		d := &r.Datasets[i]
		d.Score, d.Band = scoreSignals(d.Signals)
		for _, s := range d.Signals {
			if s.Kind != Scored {
				continue
			}
			if s.Level == Unknown {
				unmeasured++
				continue
			}
			measured++
			product *= clamp01(s.Score)
		}
		r.Signals = append(r.Signals, d.Signals...)
	}

	if measured == 0 {
		r.Assessed = false
		r.Score, r.Band, r.Coverage = 0, BandInsufficient, 0
		sortSignals(r.Signals)
		return
	}

	r.Assessed = true
	r.Score = pctOf(product)
	r.Band = Grade(r.Score)
	r.Coverage = pctOf(float64(measured) / float64(measured+unmeasured))

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

// scoreSignals computes R and its band for one dataset's checks.
func scoreSignals(sigs []Signal) (int, string) {
	product := 1.0
	measured := 0
	for _, s := range sigs {
		if !s.counts() {
			continue
		}
		measured++
		product *= clamp01(s.Score)
	}
	if measured == 0 {
		return 0, BandInsufficient
	}
	n := pctOf(product)
	return n, Grade(n)
}

func clamp01(f float64) float64 {
	if f < 0 {
		return 0
	}
	if f > 1 {
		return 1
	}
	return f
}

// pctOf rounds a factor in [0,1] to a percentage.
//
// A non-zero product never rounds down to 0: "0%" is reserved for "there is
// nothing behind this answer at all", which is a different statement from a
// very poor score.
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

// levelRank orders checks for display: what held up, then what could not be
// checked, then what to read, then what failed. Problems land last, where the
// eye stops.
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

// Problems returns every warning and failure, advisories included.
func (r *Report) Problems() []Signal {
	var out []Signal
	for _, s := range r.Signals {
		if s.Level == Warn || s.Level == Fail {
			out = append(out, s)
		}
	}
	return out
}

// Confirmations returns the checks that cost nothing.
func (r *Report) Confirmations() []Signal {
	var out []Signal
	for _, s := range r.Signals {
		if s.Level == Pass {
			out = append(out, s)
		}
	}
	return out
}

// Unmeasured returns the checks that could not be run. "We did not check" and
// "we checked and it was fine" are different claims, and collapsing them is
// how a gap becomes a guarantee.
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
