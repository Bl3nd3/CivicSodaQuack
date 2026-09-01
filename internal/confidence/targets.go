// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// TargetsFor resolves the datasets one query reads on one portal.
//
// It answers the question the score depends on: not "what did this mode
// declare" but "what does *this query* actually touch". A mode binding three
// datasets, only one of which a given query reads, must not have its score
// dragged down by a stale dataset the query never opens — and must not be
// flattered by a pristine one either.
//
// The concept indirection makes this exact rather than heuristic. The query
// names its concepts as {{c:name}} and reads them by canonical column name, so
// the datasets and the columns are both recoverable without parsing SQL.
func TargetsFor(m *modes.Mode, q modes.Query, alias string, b *modes.Binding) []Target {
	if m == nil || b == nil {
		return nil
	}
	var out []Target
	for _, name := range modes.ConceptRefs(q.SQL) {
		bd, bound := b.Concepts[name]
		if !bound {
			// Unbound concepts are already reported as an exclusion by the
			// runner, with a reason. Scoring them here would say the same
			// thing twice, in a weaker form.
			continue
		}
		c, ok := m.Concept(name)
		if !ok {
			continue
		}

		t := Target{
			Portal: alias, Table: bd.Table, DatasetID: bd.ID, Name: bd.Name,
			Concept: name, ExpectedRows: bd.Rows,
			View: c.CanonicalView(alias+".main."+bd.Table, bd),
		}
		// Only the columns this query names, required or optional alike.
		//
		// "Required" is a property of the concept, not of the query: a concept
		// requires a column so that *some* of its queries can rely on it, and
		// most queries read a subset. Profiling the rest would let an unrelated
		// gap depress the score for an answer that cannot reach it — top-vendors
		// selects a vendor and an amount, and has no business being marked down
		// because a department column it never opens is patchy.
		for _, col := range append(append([]string{}, c.Required...), c.Optional...) {
			if !modes.QueryReadsColumn(q.SQL, col) {
				continue
			}
			if _, avail := bd.ColumnFor(col); avail {
				t.Columns = append(t.Columns, col)
			}
		}
		out = append(out, t)
	}
	return mergeByDataset(out)
}

// mergeByDataset collapses targets that name the same physical dataset.
//
// R is the product of per-target retentions, so the same table appearing twice
// would square its retention: a dataset holding 60% of its evidence would score
// 36%, describing a loss that was counted once in the data and twice in the
// arithmetic. Nothing in the mode registry produces this today — no binding maps
// two concepts onto one dataset — but nothing forbids it either, and a binding
// is a config change rather than a code change.
//
// The merged target reads the union of the columns, which is what the query
// actually touches: profiling is one joint filter over those columns, so a row
// must survive all of them however many concepts introduced them.
func mergeByDataset(in []Target) []Target {
	if len(in) < 2 {
		return in
	}
	var out []Target
	at := map[string]int{}
	for _, t := range in {
		key := t.Portal + "\x00" + t.DatasetID + "\x00" + t.Table
		i, seen := at[key]
		if !seen {
			at[key] = len(out)
			out = append(out, t)
			continue
		}
		for _, c := range t.Columns {
			if !hasColumn(out[i].Columns, c) {
				out[i].Columns = append(out[i].Columns, c)
			}
		}
	}
	return out
}

func hasColumn(cols []string, c string) bool {
	for _, existing := range cols {
		if strings.EqualFold(existing, c) {
			return true
		}
	}
	return false
}
