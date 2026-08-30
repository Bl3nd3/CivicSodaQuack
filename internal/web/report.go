// Copyright (c) 2026 Neomantra Corp

package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/confidence"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// reportSection is one query's contribution to a report.
type reportSection struct {
	Name     string
	Desc     string
	Columns  []string
	Rows     [][]string
	Chart    *barChart
	Excluded []analysis.Exclusion
	// NotAComparison marks a cross-city query that only one city could answer.
	NotAComparison bool
	Truncated      bool
	// Skipped explains why a query produced nothing, when it did.
	Skipped string
	// Confidence is the data-fitness assessment behind this section. A report
	// is the copy of these numbers most likely to be read by someone who was
	// not there when they were produced, which is exactly when the qualifier
	// has to travel with them.
	Confidence *confidence.Report
}

type reportData struct {
	Title       string
	Mode        string
	About       string
	Caveats     []string
	Portals     []analysis.Portal
	Sections    []reportSection
	GeneratedAt string
	Version     string
}

// handleReport renders every query of a mode into one self-contained HTML file.
//
// Self-contained is the requirement that shapes it: no scripts, no external
// stylesheets, no fonts fetched over the network. The charts are inline SVG and
// the styles are inline CSS, so the file survives being emailed, dropped in a
// shared folder, or opened on a machine with no network — which is the whole
// point of handing someone a report instead of a URL to a server on your laptop.
func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/report/"), ".html")
	if name == "" {
		http.NotFound(w, r)
		return
	}
	m, err := modes.Lookup(name)
	if err != nil {
		writeError(w, http.StatusNotFound, err)
		return
	}

	data := reportData{
		Title:       m.Title,
		Mode:        m.Name,
		About:       m.About,
		Caveats:     m.Caveats,
		Portals:     s.sess.Portals(),
		GeneratedAt: time.Now().Format("2 January 2006, 15:04"),
	}

	for _, q := range m.Queries {
		data.Sections = append(data.Sections, s.section(r.Context(), m, q))
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.URL.Query().Get("download") != "" {
		w.Header().Set("Content-Disposition",
			fmt.Sprintf("attachment; filename=%q", m.Name+"-report.html"))
	}
	if err := reportTmpl.Execute(w, data); err != nil {
		// Headers are already sent; the partial page is more useful than a
		// silent truncation, so log-shaped output goes into the page itself.
		fmt.Fprintf(w, "<!-- report render failed: %v -->", err)
	}
}

func (s *Server) section(ctx context.Context, m *modes.Mode, q modes.Query) reportSection {
	sec := reportSection{Name: q.Name, Desc: q.Desc}

	res, err := s.sess.Run(ctx, m.Name, q.Name, 200)
	if err != nil {
		sec.Skipped = plainRunError(err)
		return sec
	}

	sec.Columns = res.Columns
	sec.Confidence = res.Confidence
	sec.Excluded = res.Excluded
	sec.NotAComparison = res.NotAComparison
	sec.Truncated = res.Truncated

	if spec := pickChart(res.Columns, res.Rows); spec != nil {
		sec.Chart = buildChart(res.Columns, res.Rows, spec, 20)
	}
	for _, row := range res.Rows {
		cells := make([]string, len(row))
		for i, v := range row {
			cells[i] = renderValue(v)
		}
		sec.Rows = append(sec.Rows, cells)
	}
	if len(sec.Rows) == 0 {
		sec.Skipped = "This query returned no rows."
	}
	return sec
}

// plainRunError turns a run failure into a sentence a reader can act on.
func plainRunError(err error) string {
	var notSynced *analysis.NotSyncedError
	switch {
	case errors.As(err, &notSynced):
		return "Skipped — the datasets behind this question have not been synced yet."
	case errors.Is(err, analysis.ErrNoComparableCity):
		return "Skipped — no attached city can answer this question."
	case strings.Contains(err.Error(), "does not publish"):
		return "Skipped — " + err.Error() + "."
	default:
		return "Skipped — " + err.Error()
	}
}

func renderValue(v any) string {
	if v == nil {
		return "—"
	}
	if f, ok := toFloat(v); ok {
		return formatNumber(f)
	}
	return fmt.Sprint(v)
}

var reportTmpl = template.Must(template.New("report").Parse(reportHTML))
