// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
)

// ExportRowLimit caps an export.
//
// Far above the 1000-row cap the page uses, because the two have different
// jobs: a table on screen stops being readable long before this, while an
// export exists precisely to take the whole result somewhere else. It is still
// bounded — an unbounded export of a multi-million-row table would build the
// whole thing in memory and stall the server for everyone.
const ExportRowLimit = 100000

// handleExport streams one query's result as CSV or JSON.
//
// It runs the same Session.Run the page runs, so an export can never contain
// rows the page would not show, or omit an exclusion the page would report.
// The path is /export/<mode>/<query>.<csv|json>.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/export/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 {
		http.NotFound(w, r)
		return
	}
	mode := parts[0]
	query, format := parts[1], ""
	switch {
	case strings.HasSuffix(query, ".csv"):
		query, format = strings.TrimSuffix(query, ".csv"), "csv"
	case strings.HasSuffix(query, ".json"):
		query, format = strings.TrimSuffix(query, ".json"), "json"
	default:
		http.Error(w, "export as .csv or .json", http.StatusNotFound)
		return
	}

	limit := ExportRowLimit
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < ExportRowLimit {
		limit = v
	}

	res, err := s.sess.Run(r.Context(), mode, query, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	filename := fmt.Sprintf("%s-%s.%s", mode, query, format)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))

	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		// The full result, not just the rows: an export that drops the caveats
		// and the excluded cities turns a carefully hedged answer into a bare
		// spreadsheet, which is how these numbers get misquoted.
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(res)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	cw := csv.NewWriter(w)
	defer cw.Flush()

	// CSV has nowhere structured to put a caveat, so they lead as comment
	// lines. A spreadsheet shows them as text in the first column, which is
	// ugly and unmissable — the right trade when the alternative is a file of
	// numbers with the warnings silently stripped.
	//
	// Each one is padded to the full width. A file whose rows have differing
	// field counts is not valid CSV, and strict parsers reject the whole thing
	// rather than the offending line — which would lose the data as the price
	// of carrying the warnings.
	width := len(res.Columns)
	comment := func(text string) {
		row := make([]string, width)
		if width == 0 {
			row = []string{text}
		} else {
			row[0] = text
		}
		_ = cw.Write(row)
	}
	for _, c := range res.Caveats {
		comment("# " + c)
	}
	for _, x := range res.Excluded {
		comment(fmt.Sprintf("# excluded: %s — %s", x.City, x.Reason))
	}
	if res.NotAComparison {
		comment("# only one city qualified, so this is not a comparison")
	}
	// The confidence block travels with the rows for the same reason the
	// caveats do. A spreadsheet is where a hedged number most easily loses its
	// hedge, and "these figures come from a copy holding 54% of the dataset" is
	// not a footnote a reader can be expected to go back for.
	if c := res.Confidence; c != nil && c.Assessed {
		comment(fmt.Sprintf("# confidence: %d%% (%s) — %s",
			c.Score, c.Band, confidence.Tagline))
		if c.Coverage < 100 {
			comment(fmt.Sprintf("#   %d%% of checks could be run; the score covers only those",
				c.Coverage))
		}
		for _, sig := range c.Problems() {
			comment("#   ! " + sig.Label)
		}
		for _, sig := range c.Unmeasured() {
			comment("#   ? " + sig.Label)
		}
		if line := c.FreshnessLine(); line != "" {
			comment("# " + line)
		}
	}
	// Only when something was actually written above. The confidence block is
	// skipped for an unassessed report, and a stray empty row is read as the
	// header by strict CSV parsers, losing the column names.
	assessed := res.Confidence != nil && res.Confidence.Assessed
	if len(res.Caveats) > 0 || len(res.Excluded) > 0 || res.NotAComparison || assessed {
		comment("")
	}

	_ = cw.Write(res.Columns)
	for _, row := range res.Rows {
		rec := make([]string, len(row))
		for i, v := range row {
			if v == nil {
				rec[i] = ""
				continue
			}
			// Raw values, not the display formatting: thousands separators
			// would make a spreadsheet read the column as text. But plain
			// decimal notation, not Go's default — fmt.Sprint renders a large
			// float as 1.24864646192e+10, which several spreadsheet imports
			// either mangle or leave as a string.
			rec[i] = exportValue(v)
		}
		_ = cw.Write(rec)
	}
}

// exportValue renders one cell for CSV: full precision, decimal notation, no
// separators.
func exportValue(v any) string {
	switch t := v.(type) {
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(t), 'f', -1, 32)
	default:
		return fmt.Sprint(v)
	}
}
