// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
)

// writeJSON emits v with a 200.
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// apiError is the error shape the page knows how to render.
//
// Message is written for a reader, not a developer. Fix and FixCommand carry
// the remedy when there is one, so a dead end in the UI can offer a way out
// instead of a stack of DuckDB vocabulary.
type apiError struct {
	Error      string `json:"error"`
	Fix        string `json:"fix,omitempty"`
	FixCommand string `json:"fix_command,omitempty"`
}

func writeError(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)

	out := apiError{Error: err.Error()}

	var notSynced *analysis.NotSyncedError
	if errors.As(err, &notSynced) {
		out.Error = "This analysis has no data yet."
		out.Fix = "Its datasets have not been synced into this database. Run the sync, then reload."
		out.FixCommand = notSynced.FixCommand()
	}
	if errors.Is(err, analysis.ErrNoComparableCity) {
		out.Error = "No attached city can answer this question."
		out.Fix = "Every city was excluded — either it does not publish the data, or it has no population recorded."
	}
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) handlePortals(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"portals": s.sess.Portals()})
}

func (s *Server) handleModes(w http.ResponseWriter, r *http.Request) {
	sts, err := s.sess.ModeStatuses(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"modes": sts})
}

// runRequest names a mode and one of its queries. There is deliberately no
// field for SQL: the browser chooses among the analyses csq ships, and cannot
// author a query of its own.
type runRequest struct {
	Mode  string `json:"mode"`
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var req runRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	res, err := s.sess.Run(r.Context(), req.Mode, req.Query, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, runResponse{Result: res, Chart: chartFor(res)})
}

func (s *Server) handleCatalog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	page, err := s.sess.SearchCatalog(r.Context(),
		strings.TrimSpace(q.Get("q")), strings.TrimSpace(q.Get("category")), limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, page)
}

func (s *Server) handleCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.sess.Categories(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"categories": cats})
}

// runResponse is a result plus the decision about whether it can honestly be
// charted.
//
// That decision is made here, on the server, and shipped to the page as data.
// The alternative — reimplementing "is a bar chart truthful for this shape?" in
// JavaScript — would let the browser and the exported report disagree about the
// same numbers, and the disagreement would surface as a chart that should not
// exist rather than as an error anyone would notice.
type runResponse struct {
	*analysis.Result
	Chart *chartHint `json:"chart"`
}

// chartHint tells the page which columns to draw, using the same rules the
// standalone report applies.
type chartHint struct {
	LabelCol int      `json:"label_col"`
	ValueCol int      `json:"value_col"`
	CityCol  int      `json:"city_col"`
	Series   []string `json:"series"`
}

func chartFor(res *analysis.Result) *chartHint {
	spec := pickChart(res.Columns, res.Rows)
	if spec == nil {
		return nil
	}
	// buildChart applies the series cap and the "two bars minimum" rule; if it
	// declines, the page must not draw one either.
	c := buildChart(res.Columns, res.Rows, spec, 20)
	if c == nil {
		return nil
	}
	return &chartHint{
		LabelCol: spec.labelCol, ValueCol: spec.valueCol,
		CityCol: spec.cityCol, Series: c.Series,
	}
}
