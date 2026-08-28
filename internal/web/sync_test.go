// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
)

func newServerWithConfig(t *testing.T, withConfig bool) *Server {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.cityofchicago.org.duckdb")
	w, err := duckdb.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	w.Close()

	opts := Options{DBs: []analysis.DBSpec{{Path: path}}}
	if withConfig {
		opts.Configs = map[string]*config.Config{
			"data_cityofchicago_org": {Portal: "data.cityofchicago.org", DB: path},
		}
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv
}

func post(t *testing.T, srv *Server, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

// Opening the UI on a database must never be enough on its own to start writing
// to it. Downloading requires an explicitly paired --config, the same gate the
// MCP server puts on its write tools.
func TestSyncRefusedWithoutAConfig(t *testing.T) {
	srv := newServerWithConfig(t, false)

	rec := get(t, srv, "/api/sync/status")
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if status.Enabled {
		t.Error("no config was given; downloading must report itself disabled")
	}

	rec = post(t, srv, "/api/sync", `{"mode":"corruption"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "--config") {
		t.Errorf("the refusal must name the flag that enables it: %s", rec.Body.String())
	}
}

func TestSyncStatusReportsEnabledWithAConfig(t *testing.T) {
	rec := get(t, newServerWithConfig(t, true), "/api/sync/status")
	var status struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &status); err != nil {
		t.Fatal(err)
	}
	if !status.Enabled {
		t.Error("a paired config must enable downloading")
	}
}

// A mode that reads only the _csq bookkeeping schema has nothing to download,
// and offering to sync it would send someone after data that does not exist.
func TestSyncRefusesModesWithNoDatasets(t *testing.T) {
	rec := post(t, newServerWithConfig(t, true), "/api/sync", `{"mode":"research"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "needs no download") {
		t.Errorf("unexpected message: %s", rec.Body.String())
	}
}

func TestSyncRefusesUnknownMode(t *testing.T) {
	if rec := post(t, newServerWithConfig(t, true), "/api/sync", `{"mode":"nope"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
}

func TestSyncRequiresPOST(t *testing.T) {
	if rec := get(t, newServerWithConfig(t, true), "/api/sync"); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status %d, want 405", rec.Code)
	}
}

// datasetIDsForMode is what makes "download this data" fetch the three datasets
// an analysis needs instead of the portal's entire config.
func TestDatasetIDsForMode(t *testing.T) {
	ids, err := datasetIDsForMode("corruption", "data.cityofchicago.org")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 {
		t.Fatalf("got %d ids, want the 3 corruption concepts: %v", len(ids), ids)
	}
	for _, id := range ids {
		if len(id) != 9 || id[4] != '-' {
			t.Errorf("%q is not a Socrata 4x4", id)
		}
	}

	if _, err := datasetIDsForMode("corruption", "datacatalog.cookcountyil.gov"); err == nil {
		t.Error("Cook County has no corruption binding; expected an error")
	}
}

// Two concurrent writers to one DuckDB file cannot both succeed, so a second
// request is refused up front rather than failing partway through a download.
func TestOnlyOneSyncAtATime(t *testing.T) {
	srv := newServerWithConfig(t, true)

	now := time.Now()
	srv.syncs.mu.Lock()
	srv.syncs.current = &Job{ID: "in-flight", State: JobRunning, Started: now}
	srv.syncs.mu.Unlock()

	rec := post(t, srv, "/api/sync", `{"mode":"corruption"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "already running") {
		t.Errorf("unexpected message: %s", rec.Body.String())
	}

	// A finished job must not block the next one.
	srv.syncs.mu.Lock()
	srv.syncs.current.State = JobDone
	srv.syncs.mu.Unlock()
	if rec := post(t, srv, "/api/sync", `{"mode":"research"}`); !strings.Contains(rec.Body.String(), "needs no download") {
		t.Errorf("a finished job must not block a new one: %s", rec.Body.String())
	}
}

// A slow browser must not be able to stall a sync by not draining its stream.
func TestPublishDoesNotBlockOnASlowSubscriber(t *testing.T) {
	m := newSyncManager()
	ch := m.subscribe()
	defer m.unsubscribe(ch)

	m.mu.Lock()
	m.current = &Job{ID: "j", State: JobRunning}
	m.mu.Unlock()

	done := make(chan struct{})
	go func() {
		// publish is documented as being called with the lock held, which is
		// how every production caller reaches it.
		for i := 0; i < 1000; i++ { // far more than the channel buffer
			m.mu.Lock()
			m.publish()
			m.mu.Unlock()
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publish blocked on a subscriber that never reads")
	}
}
