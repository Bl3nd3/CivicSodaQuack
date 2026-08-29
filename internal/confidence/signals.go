// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// rateSignal turns a "fraction of rows that are wrong" measurement into a
// retention factor through the model's transfer function:
//
//	r = 1 - (1 - floor) × min(1, rate / zeroAt)
//
// r falls linearly from 1 at rate 0 to floor at rate zeroAt, and stays at the
// floor beyond it. Continuous and monotone, so more of a defect always costs
// more, and never in a jump.
//
// The level thresholds are separate from the arithmetic on purpose. passUnder
// and warnUnder decide what the reader is told; floor and zeroAt decide what
// the score does. Conflating them is what produced a signal labelled a failure
// while still scoring 0.99 in the averaged model.
func rateSignal(name, label, detail, dataset string, rate float64,
	passUnder, warnUnder, zeroAt, floor float64) Signal {

	s := Signal{Name: name, Label: label, Detail: detail, Dataset: dataset, Floor: floor}
	switch {
	case rate < passUnder:
		s.Level = Pass
	case rate < warnUnder:
		s.Level = Warn
	default:
		s.Level = Fail
	}
	s.Score = retain(floor, rate/zeroAt)
	return s
}

// retain is the transfer function: severity 0 keeps everything, severity 1 or
// more costs the whole of what this check can take.
func retain(floor, severity float64) float64 {
	// The endpoints are returned exactly rather than computed. 1-(1-0.3)*1
	// lands on 0.30000000000000004 in binary floating point, and a floor that
	// does not compare equal to the constant that declared it is a needless
	// trap for anything that tests or displays it.
	if severity <= 0 {
		return 1
	}
	if severity >= 1 {
		return floor
	}
	return 1 - (1-floor)*severity
}

// syncSignal reports whether the data ever arrived, and cleanly.
//
// This is the gate. Every other measurement on a table describes whatever the
// last run happened to leave behind, so a failed or interrupted sync has to be
// visible before a reader gets as far as the column statistics — an empty
// table profiles beautifully.
func syncSignal(b bookRecord, dataset string) Signal {
	s := Signal{Name: SignalSync, Dataset: dataset, Floor: FloorFatal}

	switch {
	case !b.found || (b.lastStarted == nil && b.okAt == nil):
		s.Level, s.Score = Fail, 0
		s.Label = "never synced — this table has no sync record"
		s.Detail = "The rows below, if any, did not come from a csq sync this database can account for."
		return s

	case b.lastStatus == "running" || (b.lastStarted != nil && b.lastFinished == nil):
		// A run that started and never recorded an end. Either it is in flight
		// or it was killed; from here those look the same, and both mean the
		// table may hold a partial load.
		s.Level, s.Score = Fail, 0.2
		s.Label = "a sync was interrupted and never completed"
		s.Detail = "The table may hold a partial load. Re-run the sync before quoting anything from it."
		return s

	case b.lastStatus != "ok":
		s.Level, s.Score = Fail, 0
		s.Label = "the last sync failed"
		s.Detail = strings.TrimSpace(b.lastError)
		if s.Detail == "" {
			s.Detail = "No error message was recorded."
		}
		if b.okAt != nil {
			s.Score = 0.3
			s.Detail += fmt.Sprintf(" The last successful sync was %s; these rows are from that run.",
				b.okAt.Format("2 Jan 2006"))
		}
		return s
	}

	s.Level, s.Score = Pass, 1
	s.Label = "dataset successfully synced"
	if b.okAt != nil {
		s.Label = fmt.Sprintf("dataset successfully synced on %s", b.okAt.Format("2 Jan 2006"))
	}
	return s
}

// completenessSignal compares what is held against the reference row count.
//
// The reference is a count recorded when the dataset was mapped, verified
// against the live API at the time. It is not a live count, which makes it a
// tripwire rather than a proof: a large shortfall is strong evidence of a
// truncated sync, while a small difference is what a growing dataset looks
// like. The limits attached to every report say so explicitly.
func completenessSignal(held, expected int64, b bookRecord, dataset string) Signal {
	if expected <= 0 && b.catalogRows != nil {
		expected = *b.catalogRows
	}
	s := Signal{Name: SignalCompleteness, Dataset: dataset, Floor: FloorFatal}

	if expected <= 0 {
		s.Level = Unknown
		s.Label = "expected row count unknown — completeness not checked"
		s.Detail = "Neither the dataset mapping nor the portal catalog records a row count to compare against."
		return s
	}
	if held == 0 {
		// No rows is not "very incomplete", it is nothing at all: there is no
		// data behind the answer, exactly as when the sync never ran.
		s.Level, s.Score = Fail, 0
		s.Label = fmt.Sprintf("no rows held, against %s expected", commas(expected))
		return s
	}

	ratio := float64(held) / float64(expected)
	switch {
	case ratio >= 0.99:
		s.Level, s.Score = Pass, 1
		s.Label = fmt.Sprintf("%s of expected rows present", pct(min1(ratio)))
		if ratio > 1.05 {
			s.Label = fmt.Sprintf("%s rows held, above the %s reference count",
				commas(held), commas(expected))
			s.Detail = "The dataset has grown since it was mapped, which is normal for an active portal."
		}
	case ratio >= 0.90:
		s.Level, s.Score = Warn, ratio
		s.Label = fmt.Sprintf("%s of expected rows present", pct(ratio))
		s.Detail = fmt.Sprintf("%s rows short of the %s reference count.",
			commas(expected-held), commas(expected))
	default:
		// Retention is the ratio itself: a copy holding 54% of the records
		// supports 54% of a count, and the index should say exactly that.
		s.Level, s.Score = Fail, ratio
		s.Label = fmt.Sprintf("only %s of expected rows present", pct(ratio))
		s.Detail = fmt.Sprintf("%s rows short of the %s reference count — treat this copy as truncated.",
			commas(expected-held), commas(expected))
	}
	return s
}

func min1(f float64) float64 {
	if f > 1 {
		return 1
	}
	return f
}

// rowIntegritySignal checks the table against what the last successful sync
// says it wrote.
//
// A shortfall here is a different fault from an incomplete download: the rows
// were fetched and written, and then something removed them or the write did
// not land. It is the signature a killed run leaves behind, and it is invisible
// to a completeness check whenever the reference count is also unavailable.
func rowIntegritySignal(held int64, b bookRecord, dataset string) Signal {
	s := Signal{Name: SignalRowIntegrity, Dataset: dataset, Floor: FloorRows}
	if b.okRows == nil {
		s.Level = Unknown
		s.Label = "no successful sync to check the row count against"
		return s
	}
	written := *b.okRows
	if written <= 0 {
		s.Level = Unknown
		s.Label = "the last successful sync recorded no row count"
		return s
	}
	if held >= written {
		s.Level, s.Score = Pass, 1
		s.Label = fmt.Sprintf("%s rows held, consistent with the last sync", commas(held))
		return s
	}

	rate := float64(written-held) / float64(written)
	return rateSignal(SignalRowIntegrity,
		fmt.Sprintf("%s rows held, but the last sync wrote %s", commas(held), commas(written)),
		"Rows are missing relative to what was written. A sync may have been interrupted partway through applying its results.",
		dataset, rate, 0.001, 0.05, 0.5, FloorRows)
}

// freshnessSignal reports how long ago the portal last changed the data.
//
// This is upstream freshness, not sync recency: csq records both, and they
// answer opposite questions. A dataset synced this morning can still be three
// years stale if the city stopped publishing, and that is the failure mode
// worth catching — a fresh sync of frozen data reads as current to everyone
// who does not check.
func freshnessSignal(b bookRecord, dataset string, now time.Time) Signal {
	s := Signal{Name: SignalFreshness, Dataset: dataset, Floor: FloorFreshness}
	if b.upstreamUpdated == nil {
		s.Level = Unknown
		s.Label = "the portal does not report when this data last changed"
		s.Detail = "Refresh the catalog (csq catalog) to record upstream timestamps."
		return s
	}

	days := int(now.Sub(*b.upstreamUpdated).Hours() / 24)
	if days < 0 {
		days = 0
	}
	s.Score = retain(FloorFreshness, stalenessOf(days))
	switch {
	case days <= 90:
		s.Level = Pass
		s.Label = fmt.Sprintf("portal updated this data %s ago", plural(days, "day"))
	case days <= 365:
		s.Level = Warn
		s.Label = fmt.Sprintf("portal has not updated this data in %s", plural(days, "day"))
	default:
		s.Level = Fail
		s.Label = fmt.Sprintf("portal has not updated this data in %s", plural(days, "day"))
		s.Detail = "Long gaps often mean a dataset was retired or replaced. Check the portal before presenting this as current."
	}
	return s
}

// stalenessOf maps age to severity in [0,1] across a fixed piecewise curve.
//
// A single exponential would either punish a quarterly dataset or excuse a dead
// one; the breakpoints are where a civic dataset's publication cadence usually
// changes meaning. Severity feeds the transfer function, so at FloorFreshness
// the very stalest data still retains half its reliability — old data is
// degraded, not worthless.
func stalenessOf(days int) float64 {
	switch {
	case days <= 30:
		return 0
	case days <= 90:
		return lerp(float64(days), 30, 90, 0, 0.15)
	case days <= 365:
		return lerp(float64(days), 90, 365, 0.15, 0.45)
	case days <= 1095:
		return lerp(float64(days), 365, 1095, 0.45, 0.75)
	default:
		return 1
	}
}

func lerp(x, x0, x1, y0, y1 float64) float64 {
	if x1 == x0 {
		return y0
	}
	return y0 + (y1-y0)*(x-x0)/(x1-x0)
}

// lagSignal reports that the portal has changed the data since csq last pulled
// it — a provable gap between the local copy and the source, distinct from the
// data simply being old.
func lagSignal(b bookRecord, dataset string) Signal {
	s := Signal{Name: SignalLag, Dataset: dataset, Floor: FloorLag}
	if b.upstreamUpdated == nil || b.okAt == nil {
		s.Level = Unknown
		s.Label = "cannot tell whether this copy is behind the portal"
		return s
	}
	if !b.upstreamUpdated.After(*b.okAt) {
		s.Level, s.Score = Pass, 1
		s.Label = "local copy is current with the portal"
		return s
	}

	behind := int(b.upstreamUpdated.Sub(*b.okAt).Hours() / 24)
	s.Label = fmt.Sprintf("portal has changed this data since the last sync (%s behind)",
		plural(behind, "day"))
	s.Detail = "Re-sync to pick up records published since then; rows added upstream are missing here."
	// Severity rises with how far behind the copy is, saturating at a
	// quarter's worth of missed publishing.
	s.Score = retain(FloorLag, float64(behind)/90)
	switch {
	case behind <= 7:
		s.Level = Warn
	case behind <= 90:
		s.Level = Warn
	default:
		s.Level = Fail
	}
	return s
}

// nullSignal reports the emptiest column the query reads.
//
// One signal per dataset rather than one per column, scored on the worst
// offender and naming it. A query reading eight columns should not be able to
// bury a 40%-empty one under seven clean ticks, and eight separate lines would
// be noise nobody reads.
func nullSignal(p tableProfile, dataset string) Signal {
	if p.rows == 0 {
		return Signal{
			Name: SignalNullDensity, Dataset: dataset, Floor: FloorFatal,
			Level: Fail, Score: 0, Label: "the table is empty",
		}
	}
	if len(p.cols) == 0 {
		return Signal{
			Name: SignalNullDensity, Dataset: dataset, Floor: FloorNulls,
			Level: Unknown, Label: "no columns to profile for this query",
		}
	}

	type offender struct {
		name string
		rate float64
	}
	var worst offender
	var others []offender
	for _, c := range p.cols {
		rate := float64(c.nulls) / float64(p.rows)
		if rate > worst.rate {
			if worst.name != "" {
				others = append(others, worst)
			}
			worst = offender{c.name, rate}
			continue
		}
		if rate >= 0.01 {
			others = append(others, offender{c.name, rate})
		}
	}

	if worst.rate < 0.01 {
		return Signal{
			Name: SignalNullDensity, Dataset: dataset, Floor: FloorNulls,
			Level: Pass, Score: 1,
			Label: fmt.Sprintf("all %d columns this query reads are populated", len(p.cols)),
		}
	}

	detail := ""
	sort.Slice(others, func(i, j int) bool { return others[i].rate > others[j].rate })
	var parts []string
	for _, o := range others {
		if o.rate >= 0.01 {
			parts = append(parts, fmt.Sprintf("%s %s", pct(o.rate), o.name))
		}
	}
	if len(parts) > 0 {
		detail = "Also missing: " + strings.Join(parts, ", ") + "."
	}
	return rateSignal(SignalNullDensity,
		fmt.Sprintf("%s of records lack %s", pct(worst.rate), worst.name),
		detail, dataset, worst.rate, 0.01, 0.10, 0.5, FloorNulls)
}

// dateSignal reports timestamps that cannot be real.
//
// Civic date columns routinely carry typos that put a record centuries away,
// and nulls encoded as sentinel dates. Either one silently wrecks a time
// series, and both are cheap to catch here — the modes' own caveats tell
// readers to range-check every date column, so this is that check, run.
func dateSignal(p tableProfile, dataset string) (Signal, bool) {
	if p.rows == 0 {
		return Signal{}, false
	}
	var total int64
	var cols int
	worstName, worstBad := "", int64(0)
	for _, c := range p.cols {
		if c.role != roleDate {
			continue
		}
		cols++
		bad := c.past + c.future
		total += bad
		if bad > worstBad {
			worstBad, worstName = bad, c.name
		}
	}
	if cols == 0 {
		return Signal{}, false
	}

	if total == 0 {
		return Signal{
			Name: SignalDateRange, Dataset: dataset, Floor: FloorDates,
			Level: Pass, Score: 1,
			Label: fmt.Sprintf("dates within a possible range across %s",
				plural(cols, "date column")),
		}, true
	}

	rate := float64(total) / float64(p.rows*int64(cols))
	return rateSignal(SignalDateRange,
		fmt.Sprintf("%s impossible dates in %s", commas(total), worstName),
		"Values before 1677 or in the future. Bound the time range explicitly rather than trusting MIN and MAX.",
		dataset, rate, 0.0001, 0.005, 0.05, FloorDates), true
}

// keySignal reports whether the identifiers a query joins or groups on are
// usable. A null key does not join, and its rows vanish from the answer
// without appearing anywhere as excluded.
func keySignal(p tableProfile, dataset string) (Signal, bool) {
	if p.rows == 0 {
		return Signal{}, false
	}
	var keys int
	worstName, worstRate := "", 0.0
	var cardinality string
	for _, c := range p.cols {
		if c.role != roleKey {
			continue
		}
		keys++
		rate := float64(c.nulls) / float64(p.rows)
		if rate >= worstRate {
			worstRate, worstName = rate, c.name
		}
		if cardinality == "" && c.distinct > 0 {
			cardinality = fmt.Sprintf("%s distinct %s across %s rows",
				commas(c.distinct), c.name, commas(p.rows))
		}
	}
	if keys == 0 {
		return Signal{}, false
	}

	if worstRate < 0.001 {
		return Signal{
			Name: SignalKeyIntegrity, Dataset: dataset, Floor: FloorKeys,
			Level: Pass, Score: 1,
			Label:  fmt.Sprintf("%s consistent", plural(keys, "identifier")),
			Detail: cardinality,
		}, true
	}
	return rateSignal(SignalKeyIntegrity,
		fmt.Sprintf("%s of records have no %s", pct(worstRate), worstName),
		"Rows with a null identifier drop out of any join or grouping on it, without being reported as excluded.",
		dataset, worstRate, 0.001, 0.05, 0.3, FloorKeys), true
}

// Concentration adds the advisory signal for a dominant row in a result.
//
// It is advisory — Weight 0, never scored — because dominance is a fact about
// the world, not a defect in the data. Sole-source procurement is legal and
// common. But a reader looking at a total needs to know that most of it is one
// row before they describe it as a pattern, so it belongs in the same block as
// everything else they are being asked to weigh.
//
// The share is computed over the rows actually returned, and says so. Inferring
// a global denominator from a top-N result would produce a confidently wrong
// percentage, which is precisely the failure this package exists to prevent.
func (r *Report) Concentration(labelCol string, topLabel string, topShare float64, rowsShown int) {
	if topShare <= 0 || rowsShown < 2 {
		return
	}
	level := Pass
	if topShare >= 0.4 {
		level = Warn
	}
	if level == Pass {
		return
	}
	what := "one row"
	if topLabel != "" {
		what = topLabel
	}
	sig := Signal{
		Name: SignalConcentration, Level: level, Floor: FloorAdvisory, Score: 1,
		Label: fmt.Sprintf("%s accounts for %s of the total shown", what, pct(topShare)),
		Detail: fmt.Sprintf("Measured across the %d rows returned, not the whole dataset. "+
			"Concentration is routine in specialised procurement and is a question, not a finding.", rowsShown),
	}
	if labelCol != "" {
		sig.Detail = "Grouped by " + labelCol + ". " + sig.Detail
	}
	r.Signals = append(r.Signals, sig)
	sortSignals(r.Signals)
}

func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
