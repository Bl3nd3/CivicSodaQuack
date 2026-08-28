// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
)

// An export that drops the caveats turns a carefully hedged answer into a bare
// spreadsheet, which is how these numbers get misquoted. CSV has nowhere
// structured to put them, so they lead as comment rows.
func TestExportCSVCarriesCaveats(t *testing.T) {
	rec := get(t, newTestServer(t), "/export/research/coverage-gaps.csv")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Errorf("content-type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "research-coverage-gaps.csv") {
		t.Errorf("content-disposition = %q", cd)
	}

	records, err := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if err != nil {
		t.Fatalf("the export must be valid CSV: %v", err)
	}
	var comments int
	for _, r := range records {
		if len(r) > 0 && strings.HasPrefix(r[0], "# ") {
			comments++
		}
	}
	if comments == 0 {
		t.Error("no caveat rows in the export")
	}
}

func TestExportJSONCarriesCaveats(t *testing.T) {
	rec := get(t, newTestServer(t), "/export/research/coverage-gaps.json")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var res analysis.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("the export must be valid JSON: %v", err)
	}
	if len(res.Caveats) == 0 {
		t.Error("the JSON export must carry the mode's caveats")
	}
}

func TestExportRejectsUnknownFormatAndQuery(t *testing.T) {
	srv := newTestServer(t)
	for path, want := range map[string]int{
		"/export/research/coverage-gaps.xlsx": http.StatusNotFound,
		"/export/research":                    http.StatusNotFound,
		"/export/research/nope.csv":           http.StatusBadRequest,
		"/export/nope/nope.csv":               http.StatusBadRequest,
	} {
		if rec := get(t, srv, path); rec.Code != want {
			t.Errorf("%s: status %d, want %d", path, rec.Code, want)
		}
	}
}

// Large floats must not reach a spreadsheet in scientific notation.
func TestExportValueUsesDecimalNotation(t *testing.T) {
	if got := exportValue(12486464619.2); got != "12486464619.2" {
		t.Errorf("exportValue = %q, want plain decimal", got)
	}
	if got := exportValue(int64(37)); got != "37" {
		t.Errorf("exportValue = %q", got)
	}
}
