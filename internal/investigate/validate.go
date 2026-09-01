// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// Coverage is where one dataset's record actually begins and ends.
//
// This is the single most load-bearing measurement in the package. A civic
// dataset is almost never complete to the day you read it — an extract runs
// monthly, a department is three weeks behind, a sync caught the portal
// mid-publish — and the last period is therefore a fraction of a period. Charted
// naively it is a cliff, and a cliff is exactly what someone looking for a
// story about a city hiding its data expects to find. Measuring where the data
// stops is what turns that cliff back into "the year is not over yet".
type Coverage struct {
	Concept string `json:"concept"`
	Table   string `json:"table"`
	Column  string `json:"column"`

	// First and Last bound the records actually held.
	First string `json:"first,omitempty"`
	Last  string `json:"last,omitempty"`

	// FirstComplete and LastComplete bound the periods covered end to end.
	// Findings are measured only within them.
	//
	// Both ends need guarding for the same reason. A dataset that begins in
	// July holds half a first year, and half a year read as a year makes the
	// following year look like a surge — the mirror image of the part-finished
	// last year making it look like a collapse. Chicago's 311 record starting
	// in December 2018 is exactly this case.
	FirstComplete int `json:"first_complete_period"`
	LastComplete  int `json:"last_complete_period"`

	// PartialFirst and Partial are the periods the data enters but does not
	// cover, zero when the record happens to begin or end on a boundary.
	PartialFirst int `json:"partial_first_period,omitempty"`
	Partial      int `json:"partial_period,omitempty"`

	// Known is false when the extent could not be measured, in which case
	// nothing downstream may assume the series is complete.
	Known bool `json:"known"`
}

// Validation is step four: what the evidence can bear, measured before any
// finding is drawn from it.
type Validation struct {
	Coverage []Coverage `json:"coverage"`
	// Notes are the plain-language limits this step discovered, which become
	// caveats on the verdict.
	Notes []string `json:"notes"`
	// Confidence is the evidence-retention report for the datasets the plan
	// will actually read.
	Confidence *confidence.Report `json:"confidence,omitempty"`
}

// coverageFor returns the coverage of the dataset behind one probe.
func (v *Validation) coverageFor(concept string) (Coverage, bool) {
	for _, c := range v.Coverage {
		if c.Concept == concept {
			return c, true
		}
	}
	return Coverage{}, false
}

// Validate measures each dataset's extent and profiles the evidence behind the
// plan.
//
// It never fails the investigation. A coverage probe that cannot run leaves
// Known false, and every consumer treats an unknown extent as a reason to
// doubt a finding rather than as permission to trust it — which is the only
// safe direction for this particular unknown.
func Validate(ctx context.Context, q confidence.Queryer, inv *Investigation,
	plan *Plan, alias string, b *modes.Binding, now time.Time) *Validation {

	v := &Validation{Coverage: []Coverage{}, Notes: []string{}}
	m, err := modes.Lookup(inv.Mode)
	if err != nil {
		return v
	}

	seen := map[string]bool{}
	for _, pp := range plan.Probes {
		if pp.Skipped || pp.Concept == "" || pp.PeriodColumn == "" {
			continue
		}
		key := pp.Concept + "." + pp.PeriodColumn
		if seen[key] {
			continue
		}
		seen[key] = true

		bd, bound := b.Concepts[pp.Concept]
		if !bound {
			continue
		}
		c, ok := m.Concept(pp.Concept)
		if !ok {
			continue
		}
		cov := Coverage{Concept: pp.Concept, Table: bd.Table, Column: pp.PeriodColumn}
		measureExtent(ctx, q, c.CanonicalView(alias+".main."+bd.Table, bd), pp.PeriodColumn, &cov)
		v.Coverage = append(v.Coverage, cov)
	}
	sort.SliceStable(v.Coverage, func(i, j int) bool { return v.Coverage[i].Table < v.Coverage[j].Table })

	for _, cov := range v.Coverage {
		switch {
		case !cov.Known:
			v.Notes = append(v.Notes, fmt.Sprintf(
				"%s: coverage could not be measured, so no period of it can be "+
					"assumed complete", cov.Table))
		case cov.Partial != 0:
			v.Notes = append(v.Notes, fmt.Sprintf(
				"%s ends %s, so %d is incomplete and is excluded from every "+
					"measurement below", cov.Table, cov.Last, cov.Partial))
		}
		if cov.Known && cov.PartialFirst != 0 {
			v.Notes = append(v.Notes, fmt.Sprintf(
				"%s begins %s, so %d is a part-year and is excluded from every "+
					"measurement below", cov.Table, cov.First, cov.PartialFirst))
		}
		// A dataset whose record stops a whole period short of the present is
		// a different problem from a part-finished period, and a louder one.
		if cov.Known && cov.Partial == 0 && cov.LastComplete > 0 &&
			cov.LastComplete < now.Year()-1 {
			v.Notes = append(v.Notes, fmt.Sprintf(
				"%s holds nothing after %d — its record stops %d years before now",
				cov.Table, cov.LastComplete, now.Year()-cov.LastComplete))
		}
	}

	// A probe reading a field that fills in later measures a shorter window
	// than its coverage allows. Saying so matters: the alternative is a report
	// whose series runs to 2024 and whose finding stops at 2022, with nothing
	// explaining the gap.
	for _, pp := range plan.Probes {
		if pp.Skipped || pp.probe.SettlesAfter <= 0 {
			continue
		}
		cov, ok := v.coverageFor(pp.Concept)
		if !ok || !cov.Known {
			continue
		}
		v.Notes = append(v.Notes, fmt.Sprintf(
			"%q reads a field that fills in after the fact, so it is measured "+
				"only to %d — the %d period(s) since are shown and not measured",
			pp.Asks, cov.LastComplete-pp.probe.SettlesAfter, pp.probe.SettlesAfter))
	}

	v.Confidence = confidence.Assess(ctx, q, plan.Targets(), confidence.Options{Now: now})
	if v.Confidence != nil && v.Confidence.FreshnessDays != nil && *v.Confidence.FreshnessDays > 90 {
		v.Notes = append(v.Notes, fmt.Sprintf(
			"the stalest dataset behind this answer was last updated upstream %d days ago",
			*v.Confidence.FreshnessDays))
	}
	return v
}

// measureExtent finds the first and last record in a dataset and works out
// which period was the last one covered end to end.
func measureExtent(ctx context.Context, q confidence.Queryer, view, column string, cov *Coverage) {
	stmt := fmt.Sprintf(`SELECT MIN(%[1]s), MAX(%[1]s) FROM (%[2]s) WHERE %[1]s IS NOT NULL
	  AND %[1]s >= DATE '1990-01-01' AND %[1]s < DATE '2035-01-01'`, column, view)

	rows, err := q.QueryContext(ctx, stmt)
	if err != nil {
		return
	}
	defer rows.Close()
	if !rows.Next() {
		return
	}
	var lo, hi any
	if err := rows.Scan(&lo, &hi); err != nil {
		return
	}
	if rows.Err() != nil {
		return
	}
	first, okLo := asTime(lo)
	last, okHi := asTime(hi)
	if !okLo || !okHi {
		return
	}

	cov.Known = true
	cov.First = first.Format("2006-01-02")
	cov.Last = last.Format("2006-01-02")

	// A year is covered end to end only if the record spans it from the first
	// day to the last. Anything less and the year's count is a fraction of a
	// year being read as a year.
	startOfFirstYear := time.Date(first.Year(), 1, 1, 0, 0, 0, 0, first.Location())
	cov.FirstComplete = first.Year()
	if first.After(startOfFirstYear) {
		cov.FirstComplete = first.Year() + 1
		cov.PartialFirst = first.Year()
	}

	endOfLastYear := time.Date(last.Year(), 12, 31, 0, 0, 0, 0, last.Location())
	cov.LastComplete = last.Year()
	if last.Before(endOfLastYear) {
		cov.LastComplete = last.Year() - 1
		cov.Partial = last.Year()
	}
}

// asTime coerces a driver value to a time. DuckDB hands back time.Time for
// DATE and TIMESTAMP alike, but a binding may map a column through an
// expression, and a NULL from an empty table arrives as nil.
func asTime(v any) (time.Time, bool) {
	switch t := v.(type) {
	case time.Time:
		return t, !t.IsZero()
	case *time.Time:
		if t == nil {
			return time.Time{}, false
		}
		return *t, !t.IsZero()
	case string:
		for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed, true
			}
		}
	}
	return time.Time{}, false
}
