// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"context"
	"fmt"
	"math/big"
	"strconv"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
)

// BaselineWindow is how many periods before the measured one form the
// comparison baseline.
//
// This is a measurement choice, not a coefficient: it says what "before" means,
// and the report always names the exact periods it averaged, so a reader can
// see the window rather than take it on trust. Three years is enough to survive
// one anomalous year and short enough that a decade-old regime does not drag on
// a claim about the present.
const BaselineWindow = 3

// Point is one period of an indicator.
type Point struct {
	Period      int     `json:"period"`
	Value       float64 `json:"value"`
	Denominator float64 `json:"denominator,omitempty"`
	// Indicator is what the finding is measured on: the value itself, or the
	// scaled ratio when the probe declared a denominator.
	Indicator float64 `json:"indicator"`
	// Complete is false for a period the data enters but does not finish.
	// Incomplete periods are charted and never measured.
	Complete bool `json:"complete"`
}

// Finding is one measured movement, and what it would mean.
type Finding struct {
	Probe string `json:"probe"`
	Asks  string `json:"asks"`

	Direction Direction `json:"direction"`
	// Change is the signed fractional movement from baseline to latest.
	Change float64 `json:"change"`

	Latest       float64 `json:"latest"`
	LatestPeriod int     `json:"latest_period"`
	Baseline     float64 `json:"baseline"`
	// BaselineFrom and BaselineTo name the averaged periods, so the comparison
	// can be checked rather than believed.
	BaselineFrom int `json:"baseline_from"`
	BaselineTo   int `json:"baseline_to"`

	Unit string `json:"unit"`
	Per  string `json:"per,omitempty"`

	// Supports is whether this movement is in the direction the plan declared
	// would support the claim — decided before the numbers were read.
	Supports bool `json:"supports"`
	// Headline is the one-line reading a reader sees.
	Headline string `json:"headline"`

	Series  []Point  `json:"series"`
	SQL     string   `json:"sql"`
	Caveats []string `json:"caveats,omitempty"`

	// Withdrawn marks a finding that did not survive the Challenge step. It
	// stays in the report — a withdrawn finding is a result, and deleting it
	// would hide the fact that the investigation looked.
	Withdrawn   bool   `json:"withdrawn"`
	WithdrawnBy string `json:"withdrawn_by,omitempty"`

	Challenges []ChallengeResult `json:"challenges,omitempty"`
}

// Counts reports whether this finding contributes to the verdict.
func (f Finding) Counts() bool { return !f.Withdrawn && f.Direction != Flat }

// Unanswered is a probe that ran but could not produce a finding, and why.
//
// Kept separate from a skipped probe: this one had its data and still could not
// answer, which usually means the series is too short to have a baseline. That
// is a fact about the city's record worth reporting rather than an error.
type Unanswered struct {
	Probe  string `json:"probe"`
	Asks   string `json:"asks"`
	Reason string `json:"reason"`
}

// Analysis is step five: what the indicators actually did.
type Analysis struct {
	Findings   []Finding    `json:"findings"`
	Unanswered []Unanswered `json:"unanswered,omitempty"`
}

// Analyze runs every runnable probe and measures its movement.
//
// Nothing here decides what the movement means for the claim beyond consulting
// the direction the plan already declared. That separation is the reason this
// step can be read as a measurement rather than as an argument.
func Analyze(ctx context.Context, q confidence.Queryer, inv *Investigation,
	plan *Plan, v *Validation) *Analysis {

	a := &Analysis{}
	for _, pp := range plan.Probes {
		if pp.Skipped {
			continue
		}
		series, err := runProbe(ctx, q, pp)
		if err != nil {
			a.Unanswered = append(a.Unanswered, Unanswered{
				Probe: pp.Name, Asks: pp.Asks,
				Reason: "the indicator could not be computed: " + err.Error(),
			})
			continue
		}
		markComplete(series, v, pp)

		f, why := measure(pp, series)
		if why != "" {
			a.Unanswered = append(a.Unanswered, Unanswered{
				Probe: pp.Name, Asks: pp.Asks, Reason: why,
			})
			continue
		}
		f.SQL = pp.SQL
		f.Caveats = pp.probe.Caveats
		a.Findings = append(a.Findings, f)
	}
	return a
}

// runProbe executes one probe and reads its fixed output shape.
func runProbe(ctx context.Context, q confidence.Queryer, pp PlannedProbe) ([]Point, error) {
	rows, err := q.QueryContext(ctx, pp.SQL)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if len(cols) < 2 {
		return nil, fmt.Errorf("probe returned %d columns; it must return period, value"+
			" and optionally denominator", len(cols))
	}
	hasDenom := len(cols) >= 3

	var out []Point
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		period, ok := asFloat(vals[0])
		if !ok {
			continue
		}
		value, ok := asFloat(vals[1])
		if !ok {
			continue
		}
		p := Point{Period: int(period), Value: value, Indicator: value}
		if hasDenom {
			denom, ok := asFloat(vals[2])
			if !ok || denom == 0 {
				// A period with no denominator has no rate. Dropping it is the
				// only honest option: a zero would read as "none of them",
				// which is the opposite of "we cannot say".
				continue
			}
			p.Denominator = denom
			p.Indicator = value / denom * pp.probe.scale()
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no periods returned")
	}
	return out, nil
}

// markComplete flags which periods of a series may be measured.
//
// Two things can disqualify a period, and they are different. Coverage says the
// data does not span it — a part-finished year. The probe's settling lag says
// the data spans it but the field being read has not finished filling in, which
// looks identical in a table and means something entirely different.
//
// When coverage is unknown, no period is marked complete. That looks harsh — it
// can leave an investigation unable to measure anything — but the failure mode
// it prevents is the one that matters: a half-published year read as a collapse
// in publishing, which is precisely the finding this whole package exists to
// avoid producing by accident.
func markComplete(series []Point, v *Validation, pp PlannedProbe) {
	cov, ok := v.coverageFor(pp.Concept)
	if !ok || !cov.Known {
		// Written out rather than returned early. The guarantee that an
		// unmeasured extent licenses no measurement has to hold here, not in
		// whatever built the series — this is the one place that knows.
		for i := range series {
			series[i].Complete = false
		}
		return
	}
	// A field that fills in later is only trustworthy once it has had time to.
	settled := cov.LastComplete - pp.probe.SettlesAfter
	for i := range series {
		series[i].Complete = series[i].Period >= cov.FirstComplete &&
			series[i].Period <= settled
	}
}

// measure computes the movement of one indicator, or explains why it cannot.
func measure(pp PlannedProbe, series []Point) (Finding, string) {
	var complete []Point
	for _, p := range series {
		if p.Complete {
			complete = append(complete, p)
		}
	}
	if len(complete) == 0 {
		return Finding{}, "no period of this indicator is covered end to end, so " +
			"there is nothing that can be compared without comparing a part-year " +
			"against a whole one"
	}
	if len(complete) < 2 {
		return Finding{}, fmt.Sprintf(
			"only one complete period (%d) is held, and a trend needs at least two",
			complete[0].Period)
	}

	latest := complete[len(complete)-1]
	base := complete[:len(complete)-1]
	if len(base) > BaselineWindow {
		base = base[len(base)-BaselineWindow:]
	}
	var sum float64
	for _, p := range base {
		sum += p.Indicator
	}
	baseline := sum / float64(len(base))
	if baseline == 0 {
		return Finding{}, "the baseline periods hold no records, so a change from " +
			"them has no percentage to report"
	}

	change := (latest.Indicator - baseline) / baseline
	f := Finding{
		Probe: pp.Name, Asks: pp.Asks,
		Change: change, Direction: directionOf(change),
		Latest: latest.Indicator, LatestPeriod: latest.Period,
		Baseline: baseline, BaselineFrom: base[0].Period, BaselineTo: base[len(base)-1].Period,
		Unit: pp.Unit, Per: pp.Per,
		Series: series,
	}
	f.Supports = f.Direction == pp.Supports
	f.Headline = headline(pp, f)
	return f, ""
}

func directionOf(change float64) Direction {
	switch {
	case change > MaterialChange:
		return Up
	case change < -MaterialChange:
		return Down
	}
	return Flat
}

// headline reads a movement back in the probe's own words.
func headline(pp PlannedProbe, f Finding) string {
	magnitude := fmt.Sprintf("%.0f%%", absf(f.Change)*100)
	window := fmt.Sprintf("%d vs %s mean", f.LatestPeriod, periodRange(f.BaselineFrom, f.BaselineTo))

	if f.Direction == Flat {
		return fmt.Sprintf("%s held steady (%s, %s)", pp.Unit, magnitude+" change", window)
	}
	verb := "more"
	reading := pp.probe.RiseMeans
	if f.Direction == Down {
		verb, reading = "fewer", pp.probe.FallMeans
	}
	if pp.Per != "" {
		// A rate does not take "more" and "fewer" — those describe counts, and
		// a rate that rose while its numerator fell is exactly the confusion
		// this avoids.
		dir := "higher"
		if f.Direction == Down {
			dir = "lower"
		}
		return fmt.Sprintf("%s %s %s per %s (%s) — %s",
			magnitude, dir, pp.Unit, pp.Per, window, reading)
	}
	return fmt.Sprintf("%s %s %s (%s) — %s", magnitude, verb, pp.Unit, window, reading)
}

func periodRange(from, to int) string {
	if from == to {
		return strconv.Itoa(from)
	}
	return fmt.Sprintf("%d–%d", from, to)
}

func absf(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// asFloat coerces a driver value to a number.
//
// The type list is wider than it looks like it needs to be because DuckDB's
// aggregates do not all return the same width: COUNT gives BIGINT, but SUM over
// an integer promotes to HUGEINT, which arrives as a *big.Int. A probe writing
// SUM(CASE ... END) instead of COUNT(*) FILTER (...) — the same measurement,
// two spellings — would otherwise have every row silently rejected and report
// "no periods returned", which reads as a portal that publishes nothing.
func asFloat(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case int:
		return float64(t), true
	case *big.Int:
		if t == nil {
			return 0, false
		}
		f, _ := new(big.Float).SetInt(t).Float64()
		return f, true
	case big.Int:
		f, _ := new(big.Float).SetInt(&t).Float64()
		return f, true
	case *big.Float:
		if t == nil {
			return 0, false
		}
		f, _ := t.Float64()
		return f, true
	case []byte:
		f, err := strconv.ParseFloat(strings.TrimSpace(string(t)), 64)
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}
