// Copyright (c) 2026 Neomantra Corp

package duckdb

import (
	"testing"
	"time"
)

func stagingCount(t *testing.T, w *Writer) int {
	t.Helper()
	var n int
	if err := w.DB.QueryRow(
		`SELECT COUNT(*) FROM duckdb_tables() WHERE schema_name = '_csq_staging'`).Scan(&n); err != nil {
		t.Fatalf("count staging: %v", err)
	}
	return n
}

func unfinishedCount(t *testing.T, w *Writer) int {
	t.Helper()
	n, err := w.IncompleteSyncRunCount()
	if err != nil {
		t.Fatalf("incomplete count: %v", err)
	}
	return n
}

// A killed process leaves both kinds of wreckage: a staging table that never
// swapped, and a run row that never reached a terminal status. Neither is
// cleaned by the strategies' error paths, which never execute when the process
// dies rather than fails.
func TestSweepAbandoned_ClearsStagingAndUnfinishedRuns(t *testing.T) {
	w, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.DB.Exec(`CREATE TABLE _csq_staging.crimes_RUN1 (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.DB.Exec(`INSERT INTO _csq_staging.crimes_RUN1 VALUES (1), (2), (3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.DB.Exec(`CREATE TABLE _csq_staging.permits_RUN1 (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.StartSyncRun("RUN1", "aaaa-0001", "crimes", "cfg", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	if got := stagingCount(t, w); got != 2 {
		t.Fatalf("setup: %d staging tables, want 2", got)
	}
	if got := unfinishedCount(t, w); got != 1 {
		t.Fatalf("setup: %d unfinished runs, want 1", got)
	}

	res, err := w.SweepAbandoned(time.Now().UTC())
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if res.StagingTables != 2 {
		t.Errorf("StagingTables = %d, want 2", res.StagingTables)
	}
	if res.StagingRows != 3 {
		t.Errorf("StagingRows = %d, want 3", res.StagingRows)
	}
	if res.AbandonedRuns != 1 {
		t.Errorf("AbandonedRuns = %d, want 1", res.AbandonedRuns)
	}
	if got := stagingCount(t, w); got != 0 {
		t.Errorf("%d staging tables survived the sweep", got)
	}
	if got := unfinishedCount(t, w); got != 0 {
		t.Errorf("%d runs still unfinished after the sweep", got)
	}
}

// An abandoned run must be recorded as aborted with a reason, not left to look
// like it is still going. A corpus that reports a sync in flight that no longer
// exists is a gap nobody investigates.
func TestSweepAbandoned_RecordsAReason(t *testing.T) {
	w, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	if _, err := w.StartSyncRun("RUN1", "aaaa-0001", "crimes", "cfg", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if _, err := w.SweepAbandoned(time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	var status, errMsg string
	var finished *time.Time
	if err := w.DB.QueryRow(
		`SELECT status, coalesce(error, ''), finished_at FROM _csq.sync_runs WHERE run_id = 'RUN1'`,
	).Scan(&status, &errMsg, &finished); err != nil {
		t.Fatal(err)
	}
	if status != "aborted" {
		t.Errorf("status = %q, want aborted", status)
	}
	if finished == nil {
		t.Error("finished_at must be set so the run stops reading as in flight")
	}
	if errMsg == "" {
		t.Error("an aborted run must carry a reason")
	}
}

// A completed run is history, not wreckage.
func TestSweepAbandoned_LeavesFinishedRunsAlone(t *testing.T) {
	w, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	rowid, err := w.StartSyncRun("RUN1", "aaaa-0001", "crimes", "cfg", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.FinishSyncRun(rowid, "ok", 100, "", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	res, err := w.SweepAbandoned(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Empty() {
		t.Errorf("sweep touched a finished run: %s", res)
	}

	var status string
	if err := w.DB.QueryRow(
		`SELECT status FROM _csq.sync_runs WHERE run_id = 'RUN1'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ok" {
		t.Errorf("status = %q, want the original ok", status)
	}
}

// The common case is a clean database, where the sweep must be a no-op that
// reports nothing rather than printing noise on every sync.
func TestSweepAbandoned_CleanDatabaseIsANoop(t *testing.T) {
	w, err := Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	res, err := w.SweepAbandoned(time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Empty() {
		t.Errorf("sweep of a clean database reported %s", res)
	}
}
