// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
)

// newCSQDB creates an empty but well-formed csq database named after a portal,
// which is how csq derives the portal host for binding lookup.
func newCSQDB(t *testing.T, filename string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), filename)
	w, err := duckdb.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", filename, err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close %s: %v", filename, err)
	}
	return path
}

func TestOpen_DerivesAliasAndPortalFromFilename(t *testing.T) {
	s, err := Open([]DBSpec{{Path: newCSQDB(t, "data.cityofchicago.org.duckdb")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ports := s.Portals()
	if len(ports) != 1 {
		t.Fatalf("got %d portals, want 1", len(ports))
	}
	if ports[0].Portal != "data.cityofchicago.org" {
		t.Errorf("portal = %q", ports[0].Portal)
	}
	// The alias has to be a bare SQL identifier for ATTACH to accept it.
	if ports[0].Alias != "data_cityofchicago_org" {
		t.Errorf("alias = %q", ports[0].Alias)
	}
	// The city label comes from a binding, not the filename.
	if ports[0].City != "Chicago, IL" {
		t.Errorf("city = %q, want the binding's label", ports[0].City)
	}
}

func TestOpen_RejectsMissingFile(t *testing.T) {
	if _, err := Open([]DBSpec{{Path: "/nonexistent/nope.duckdb"}}); err == nil {
		t.Fatal("expected an error for a missing database")
	}
}

func TestOpen_RejectsEmptySpecs(t *testing.T) {
	if _, err := Open(nil); err == nil {
		t.Fatal("expected an error when no database is given")
	}
}

// A mode whose datasets were never synced must be reported unready, not
// reported ready with zero rows. The two are indistinguishable downstream and
// mean opposite things.
func TestModeStatuses_UnsyncedIsNotReady(t *testing.T) {
	s, err := Open([]DBSpec{{Path: newCSQDB(t, "data.cityofchicago.org.duckdb")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sts, err := s.ModeStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]ModeStatus{}
	for _, st := range sts {
		byName[st.Name] = st
	}

	corruption, ok := byName["corruption"]
	if !ok {
		t.Fatal("corruption missing from statuses")
	}
	if !corruption.Applicable {
		t.Error("Chicago is bound to corruption, so it is applicable")
	}
	if corruption.Ready {
		t.Error("no datasets are synced, so corruption must not be ready")
	}
	if corruption.FixCommand == "" {
		t.Error("an unready-but-applicable mode must carry the fix")
	}
	if len(corruption.Datasets) == 0 {
		t.Error("the status should name the datasets it is waiting on")
	}

	// research reads only _csq, which every csq database has by construction.
	if research := byName["research"]; !research.Ready {
		t.Error("research must be ready against any csq database")
	}
}

// A portal with no binding for a mode is a different problem from an unsynced
// one, and must not be offered a "sync this" remedy that would never help.
func TestModeStatuses_UnboundPortalIsNotApplicable(t *testing.T) {
	s, err := Open([]DBSpec{{Path: newCSQDB(t, "datacatalog.cookcountyil.gov.duckdb")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sts, err := s.ModeStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range sts {
		if st.Name != "corruption" {
			continue
		}
		if st.Applicable {
			t.Error("Cook County has no corruption binding; it is not applicable")
		}
		if st.FixCommand != "" {
			t.Error("an unbound portal must not be told to sync")
		}
		if st.Reason == "" {
			t.Error("an unavailable mode must say why")
		}
		return
	}
	t.Fatal("corruption missing from statuses")
}

func TestRun_ResearchWorksOnAnEmptyDatabase(t *testing.T) {
	s, err := Open([]DBSpec{{Path: newCSQDB(t, "data.cityofchicago.org.duckdb")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "research", "coverage-gaps", 10)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(res.Columns) == 0 {
		t.Error("expected columns even when there are no rows")
	}
	if len(res.Caveats) == 0 {
		t.Error("a result must carry its mode's caveats")
	}
	// Never nil: the page iterates these without a guard.
	if res.Rows == nil || res.Excluded == nil {
		t.Error("rows and exclusions must be empty slices, not null")
	}
}

// Querying a mode whose tables are absent must produce the typed error that
// carries a remedy, not a raw DuckDB binder message.
func TestRun_MissingTablesGiveANotSyncedError(t *testing.T) {
	s, err := Open([]DBSpec{{Path: newCSQDB(t, "data.cityofchicago.org.duckdb")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.Run(context.Background(), "corruption", "top-vendors", 10)
	if err == nil {
		t.Fatal("expected an error: nothing is synced")
	}
	var notSynced *NotSyncedError
	if !errors.As(err, &notSynced) {
		t.Fatalf("got %v (%T), want a *NotSyncedError", err, err)
	}
	if notSynced.FixCommand() == "" {
		t.Error("the error must carry the command that fixes it")
	}
}

func TestRun_UnknownModeAndQuery(t *testing.T) {
	s, err := Open([]DBSpec{{Path: newCSQDB(t, "data.cityofchicago.org.duckdb")}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Run(context.Background(), "no-such-mode", "x", 0); err == nil {
		t.Error("expected an error for an unknown mode")
	}
	if _, err := s.Run(context.Background(), "research", "no-such-query", 0); err == nil {
		t.Error("expected an error for an unknown query")
	}
}

// A single-portal mode cannot be run across several databases; saying so is
// better than silently answering for the first one.
func TestRun_SinglePortalModeRefusesMultipleDBs(t *testing.T) {
	s, err := Open([]DBSpec{
		{Path: newCSQDB(t, "data.cityofchicago.org.duckdb")},
		{Path: newCSQDB(t, "data.cityofnewyork.us.duckdb")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.Run(context.Background(), "corruption", "top-vendors", 0); err == nil {
		t.Fatal("expected a refusal for a single-portal mode with two databases")
	}
}
