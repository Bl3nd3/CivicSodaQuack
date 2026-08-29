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
// # The model
//
// Each signal scores in [0,1] and carries a fixed weight. The report score is
// the weighted mean over every signal that could be evaluated, expressed as a
// percentage. Signals that could not be evaluated (Unknown) are excluded from
// both sides of that mean rather than being scored as zero or as one — an
// unmeasurable property is not evidence in either direction, and guessing
// would let a portal that publishes less metadata score higher or lower for
// reasons that have nothing to do with its data.
//
// Two failures then cap the result outright, because a weighted mean is too
// forgiving of them: a dataset that never synced or whose last sync failed
// caps the score at CapFailedSync, and a materially short copy caps it at
// CapIncomplete. A missing dataset must never average out to "moderate".
//
// Advisory signals (Weight 0) are reported but never scored. Concentration is
// the example: one vendor holding 61% of a department's spend is a fact worth
// putting in front of a reader, and it is not a defect in the data.
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

// Score caps a failing signal can impose through Signal.Cap.
//
// CapFailedSync is for failures that mean there is nothing behind the answer
// at all — a sync that never ran, failed, or was interrupted, and a table that
// holds no rows. CapIncomplete is for a copy that arrived but is materially
// short of what the portal holds.
const (
	CapFailedSync = 30
	CapIncomplete = 60
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
	// Score is this signal's contribution in [0,1]. Meaningless when the level
	// is Unknown or the weight is zero.
	Score float64 `json:"score"`
	// Weight is the signal's share of the weighted mean. Zero marks an
	// advisory signal: reported to the reader, excluded from the arithmetic.
	Weight float64 `json:"weight"`
	// Dataset is the table this was measured on; empty for report-level
	// signals derived from the result rows rather than from a table.
	Dataset string `json:"dataset,omitempty"`

	// Cap is the highest score the report may carry when this signal fails.
	// Zero means the signal only contributes to the weighted mean.
	//
	// It lives on the signal rather than in the aggregator because which
	// failures are disqualifying is a property of what was measured, not of
	// the arithmetic. A weighted mean is far too forgiving of a dataset that
	// never arrived: seven clean column checks on an empty table would
	// otherwise average out to "moderate".
	Cap int `json:"cap,omitempty"`
}

// Advisory reports whether this signal is shown but not scored.
func (s Signal) Advisory() bool { return s.Weight == 0 }

// counts reports whether this signal participates in the weighted mean.
func (s Signal) counts() bool { return s.Weight > 0 && s.Level != Unknown }

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

// finalize computes scores and ordering once every signal has been collected.
//
// Ordering is deliberate: the flattened signal list runs passes, then
// advisories, then warnings, then failures. A reader scanning the block sees
// what held up before what did not, which is the order the evidence should be
// weighed in — and the problems land last, where the eye stops.
func (r *Report) finalize() {
	r.Limits = append([]string{}, standardLimits...)

	var sum, weight float64
	capAt := 100
	for i := range r.Datasets {
		d := &r.Datasets[i]
		d.Score, d.Band = scoreSignals(d.Signals)
		for _, s := range d.Signals {
			if s.counts() {
				sum += s.Score * s.Weight
				weight += s.Weight
			}
			if s.Level == Fail && s.Cap > 0 {
				capAt = min(capAt, s.Cap)
			}
		}
		r.Signals = append(r.Signals, d.Signals...)
	}

	if weight == 0 {
		// Nothing measurable. Say so rather than reporting a zero, which reads
		// as "assessed and terrible" instead of "not assessed" — opposite
		// instructions to a reader. Datasets may still be listed, and the
		// renderer distinguishes "nothing to profile" from "profiled nothing"
		// by whether any are present.
		r.Assessed = false
		r.Score, r.Band = 0, BandInsufficient
		sortSignals(r.Signals)
		return
	}

	r.Assessed = true
	r.Score = min(int(sum/weight*100+0.5), capAt)
	r.Band = Grade(r.Score)

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

// scoreSignals computes a weighted mean and band for one signal set.
func scoreSignals(sigs []Signal) (int, string) {
	var sum, weight float64
	capAt := 100
	for _, s := range sigs {
		if s.counts() {
			sum += s.Score * s.Weight
			weight += s.Weight
		}
		if s.Level == Fail && s.Cap > 0 {
			capAt = min(capAt, s.Cap)
		}
	}
	if weight == 0 {
		return 0, BandInsufficient
	}
	n := min(int(sum/weight*100+0.5), capAt)
	return n, Grade(n)
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
