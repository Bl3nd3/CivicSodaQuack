// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Check names. Stable slugs: interfaces group and filter on them.
const (
	// Scored — the two factors of U/E.
	SignalCompleteness = "completeness" // H/E: did the rows arrive
	SignalUsability    = "usability"    // U/H: do they carry what the query reads

	// Diagnostic — reported beside R, never folded into it.
	SignalSync          = "sync"
	SignalFreshness     = "freshness"
	SignalLag           = "local_lag"
	SignalConcentration = "concentration"
)

// completenessSignal is H/E: the share of the portal's rows that arrived.
//
// Retention is the ratio itself, with no curve applied to it. A copy holding
// 54% of the records supports 54% of a count, and any transformation of that
// number would be a claim about how much incompleteness matters — which
// depends on the question, not on the data.
//
// The reference count E was recorded when the dataset was mapped, verified
// against the live API at the time. It is not a live count, which makes this a
// tripwire rather than a proof: a large shortfall is strong evidence of a
// truncated sync, while a small difference is what a growing dataset looks
// like. Every report says so in its limits.
func completenessSignal(held, expected int64, catalogRows *int64, dataset string) Signal {
	if expected <= 0 && catalogRows != nil {
		expected = *catalogRows
	}
	s := Signal{Name: SignalCompleteness, Kind: Scored, Dataset: dataset, Of: expected}

	if expected <= 0 {
		s.Level = Unknown
		s.Label = "expected row count unknown — completeness not checked"
		s.Detail = "Neither the dataset mapping nor the portal catalog records a row " +
			"count to compare against, so this check is excluded from the score."
		return s
	}
	if held == 0 {
		s.Level, s.Score, s.Lost = Fail, 0, expected
		s.Label = fmt.Sprintf("no rows held, against %s expected", commas(expected))
		return s
	}

	ratio := float64(held) / float64(expected)
	s.Score = clamp01(ratio)
	s.Level = LevelFor(s.Score)
	switch {
	case ratio > 1:
		// More rows than the reference. The dataset grew; nothing is missing,
		// so nothing is lost — the retention clamps to 1 rather than exceeding
		// it, which would let growth pay for a defect elsewhere.
		s.Label = fmt.Sprintf("%s rows held, above the %s reference count",
			commas(held), commas(expected))
		s.Detail = "The dataset has grown since it was mapped, which is normal for an active portal."
	case s.Level == Pass:
		s.Label = fmt.Sprintf("%s of expected rows present", pct(ratio))
	default:
		s.Lost = expected - held
		s.Label = fmt.Sprintf("%s of expected rows present", pct(ratio))
		s.Detail = fmt.Sprintf("%s rows short of the %s reference count.",
			commas(s.Lost), commas(expected))
	}
	return s
}

// usabilitySignal is U/H: the share of held rows carrying what the query reads.
//
// U is measured as a single joint condition — a row survives when *every*
// column the query reads is non-null and every timestamp among them could be
// real — rather than as one rate per column combined arithmetically. The
// difference matters: nulls in civic data cluster heavily, and multiplying
// per-column rates would assume an independence that does not hold and would
// overstate the loss. One SQL filter measures the truth directly.
//
// A query may still emit rows it cannot read, bucketing them as "(unspecified)"
// — honest reporting, not a repair. The information is absent either way, so
// those rows are counted as lost here.
func usabilitySignal(p tableProfile, dataset string) Signal {
	s := Signal{Name: SignalUsability, Kind: Scored, Dataset: dataset, Of: p.rows}

	if p.rows == 0 {
		s.Level, s.Score = Fail, 0
		s.Label = "the table is empty"
		return s
	}
	if len(p.cols) == 0 {
		s.Level = Unknown
		s.Label = "no columns to examine for this query"
		return s
	}

	s.Lost = p.rows - p.usable
	s.Score = clamp01(float64(p.usable) / float64(p.rows))
	s.Level = LevelFor(s.Score)

	if s.Level == Pass {
		if len(p.cols) == 1 {
			s.Label = "the column this query reads is populated in every row"
		} else {
			s.Label = fmt.Sprintf("all %d columns this query reads are populated", len(p.cols))
		}
		return s
	}

	// Name what is actually missing, worst first. The score is the joint loss;
	// this is the diagnosis a reader needs in order to act on it.
	type gap struct {
		name string
		rate float64
		why  string
	}
	var gaps []gap
	for _, c := range p.cols {
		if c.nulls > 0 {
			gaps = append(gaps, gap{c.name, float64(c.nulls) / float64(p.rows), "lack"})
		}
		if bad := c.past + c.future; bad > 0 {
			gaps = append(gaps, gap{c.name, float64(bad) / float64(p.rows),
				"carry an impossible date in"})
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i].rate > gaps[j].rate })

	if len(gaps) > 0 {
		s.Label = fmt.Sprintf("%s of records %s %s",
			pct(gaps[0].rate), gaps[0].why, gaps[0].name)
	} else {
		s.Label = fmt.Sprintf("%s of records are unusable for this query", pct(1-s.Score))
	}

	var parts []string
	for _, g := range gaps {
		if g.name == gaps[0].name && g.why == gaps[0].why {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s %s", pct(g.rate), g.name))
	}
	s.Detail = fmt.Sprintf("%s of %s rows carry every column this query reads.",
		commas(p.usable), commas(p.rows))
	if len(parts) > 0 {
		s.Detail += " Also missing: " + strings.Join(parts, ", ") + "."
	}
	return s
}

// syncSignal reports how the data got here, and whether anything went wrong.
//
// It is a diagnostic, not a score. Its job is to explain a shortfall the
// completeness check has already counted — scoring it too would charge the
// same missing rows twice, and would let a sync that failed at 90% read as
// having delivered nothing at all.
func syncSignal(b bookRecord, dataset string) Signal {
	s := Signal{Name: SignalSync, Kind: Diagnostic, Dataset: dataset}

	switch {
	case !b.found || (b.lastStarted == nil && b.okAt == nil):
		s.Level = Fail
		s.Label = "never synced — this table has no sync record"
		s.Detail = "The rows here, if any, did not come from a csq sync this database can account for."
		return s

	case b.lastStatus == "running" || (b.lastStarted != nil && b.lastFinished == nil):
		s.Level = Fail
		s.Label = "a sync was interrupted and never completed"
		s.Detail = "The table may hold a partial load. Re-run the sync before quoting anything from it."
		return s

	case b.lastStatus != "ok":
		s.Level = Fail
		s.Label = "the last sync failed"
		s.Detail = strings.TrimSpace(b.lastError)
		if s.Detail == "" {
			s.Detail = "No error message was recorded."
		}
		if b.okAt != nil {
			s.Detail += fmt.Sprintf(" The last successful sync was %s; these rows are from that run.",
				b.okAt.Format("2 Jan 2006"))
		}
		return s
	}

	s.Level = Pass
	s.Label = "dataset successfully synced"
	if b.okAt != nil {
		s.Label = fmt.Sprintf("dataset successfully synced on %s", b.okAt.Format("2 Jan 2006"))
	}
	if b.okRows != nil && *b.okRows > 0 {
		s.Detail = fmt.Sprintf("The run recorded writing %s rows.", commas(*b.okRows))
	}
	return s
}

// freshnessSignal reports how long ago the portal last changed the data.
//
// Reported, never scored. Staleness removes no rows, so it has no reading as
// evidence loss, and how much it matters is a property of the question rather
// than of the data: 122-day-old crime data is fine for a 2023 trend and
// useless for last week. Turning that into a coefficient would be csq deciding
// what the reader is asking.
//
// This is upstream freshness, not sync recency — the two answer opposite
// questions. A dataset pulled this morning can still be three years stale if
// the city stopped publishing, and that is the case worth surfacing.
func freshnessSignal(b bookRecord, dataset string, now time.Time) Signal {
	s := Signal{Name: SignalFreshness, Kind: Diagnostic, Dataset: dataset}
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
	// These bands choose a word, not a number. Nothing downstream reads them,
	// so they cannot move the score.
	switch {
	case days <= 90:
		s.Level = Pass
		s.Label = fmt.Sprintf("portal updated this data %s ago", plural(days, "day"))
	case days <= 365:
		s.Level = Warn
		s.Label = fmt.Sprintf("portal has not updated this data in %s", plural(days, "day"))
	default:
		s.Level = Warn
		s.Label = fmt.Sprintf("portal has not updated this data in %s", plural(days, "day"))
		s.Detail = "Long gaps often mean a dataset was retired or replaced. Check the portal before presenting this as current."
	}
	return s
}

// lagSignal reports that the portal has changed the data since csq last pulled
// it — a provable gap between the local copy and the source.
//
// Diagnostic rather than scored: the rows published since the last sync cannot
// be counted from here, so there is no measured loss to multiply. Reporting the
// gap without inventing a size for it is the honest form.
func lagSignal(b bookRecord, dataset string) Signal {
	s := Signal{Name: SignalLag, Kind: Diagnostic, Dataset: dataset}
	if b.upstreamUpdated == nil || b.okAt == nil {
		s.Level = Unknown
		s.Label = "cannot tell whether this copy is behind the portal"
		return s
	}
	if !b.upstreamUpdated.After(*b.okAt) {
		s.Level = Pass
		s.Label = "local copy is current with the portal"
		return s
	}

	behind := int(b.upstreamUpdated.Sub(*b.okAt).Hours() / 24)
	s.Level = Warn
	s.Label = fmt.Sprintf("portal has changed this data since the last sync (%s behind)",
		plural(behind, "day"))
	s.Detail = "Rows published since then are missing here and cannot be counted from " +
		"this copy, so they are not reflected in the score. Re-sync to pick them up."
	return s
}

// Concentration adds the dominance reading for a result.
//
// Diagnostic: dominance is a fact about the world, not a defect in the data.
// Sole-source procurement is legal and common. But a reader looking at a total
// needs to know that most of it is one row before describing it as a pattern.
//
// The share is computed over the rows actually returned, and says so. Inferring
// a global denominator from a top-N result would produce a confidently wrong
// percentage, which is the failure this package exists to prevent.
func (r *Report) Concentration(labelCol string, topLabel string, topShare float64, rowsShown int) {
	if topShare < 0.4 || rowsShown < 2 {
		return
	}
	what := "one row"
	if topLabel != "" {
		what = topLabel
	}
	sig := Signal{
		Name: SignalConcentration, Level: Warn, Kind: Diagnostic,
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
