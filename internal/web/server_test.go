// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
)

// newTestServer builds a csq-shaped database and serves it.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.cityofchicago.org.duckdb")
	w, err := duckdb.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	w.Close()

	srv, err := New(Options{DBs: []analysis.DBSpec{{Path: path}}})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func get(t *testing.T, srv *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestServesAppShell(t *testing.T) {
	rec := get(t, newTestServer(t), "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	for _, want := range []string{"CivicSodaQuack", "/app.js", "/app.css"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("shell missing %q", want)
		}
	}
}

func TestServesEmbeddedAssets(t *testing.T) {
	srv := newTestServer(t)
	for _, path := range []string{"/app.css", "/app.js"} {
		if rec := get(t, srv, path); rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
		}
	}
}

// A mode whose datasets were never synced must report itself as unready and
// carry the command that fixes it. Rendering it as "ready" with an empty table
// would make missing data indistinguishable from a finding of nothing.
func TestModesReportUnsyncedWithAFix(t *testing.T) {
	rec := get(t, newTestServer(t), "/api/modes")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Modes []analysis.ModeStatus `json:"modes"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}

	var corruption, research *analysis.ModeStatus
	for i := range out.Modes {
		switch out.Modes[i].Name {
		case "corruption":
			corruption = &out.Modes[i]
		case "research":
			research = &out.Modes[i]
		}
	}
	if corruption == nil || research == nil {
		t.Fatal("expected corruption and research in the listing")
	}
	if corruption.Ready {
		t.Error("corruption has no synced datasets; it must not report ready")
	}
	if !corruption.Applicable {
		t.Error("Chicago is bound to corruption, so it is applicable but unsynced")
	}
	if corruption.FixCommand == "" {
		t.Error("an unready mode must carry the command that fixes it")
	}
	// research reads only the _csq schema, which every csq database has.
	if !research.Ready {
		t.Error("research must be ready against any csq database")
	}
}

// The browser cannot author SQL. The run endpoint takes names, and an unknown
// name is refused rather than interpreted.
func TestRunRejectsUnknownQuery(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/run",
		strings.NewReader(`{"mode":"research","query":"no-such-query"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestRunRequiresPOST(t *testing.T) {
	if rec := get(t, newTestServer(t), "/api/run"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

// The report must open on a machine with no network: no external stylesheet,
// script, font, or image.
func TestReportIsSelfContained(t *testing.T) {
	rec := get(t, newTestServer(t), "/report/research.html")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, forbidden := range []string{"http://", "https://", "<script"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("report is not self-contained; found %q", forbidden)
		}
	}
	if !strings.Contains(body, "Read before quoting any of this") {
		t.Error("report must lead with the mode's caveats")
	}
}

func TestReportUnknownMode404s(t *testing.T) {
	if rec := get(t, newTestServer(t), "/report/nope.html"); rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// Caveats ship with results, not beside them: a consumer cannot fetch rows
// without also receiving the warnings that make them readable.
func TestRunCarriesCaveats(t *testing.T) {
	srv := newTestServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/run",
		strings.NewReader(`{"mode":"research","query":"coverage-gaps"}`))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	var res analysis.Result
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if len(res.Caveats) == 0 {
		t.Error("a result must carry its mode's caveats")
	}
}
