// Copyright (c) 2026 Neomantra Corp

package sync

import (
	"context"
	"testing"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
)

func stagingTableCount(t *testing.T, w *duckdb.Writer) int {
	t.Helper()
	var n int
	err := w.DB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = '_csq_staging'`,
	).Scan(&n)
	if err != nil {
		t.Fatalf("count staging tables: %v", err)
	}
	return n
}

// A bootstrap that dies mid-stream must not leave its partial staging table
// behind. Nothing else reclaims it: the next run stages under a new run ID, so
// the orphan survives every subsequent sync. Two interrupted million-row
// bootstraps against the real Chicago and New York portals left ~500k rows of
// unreachable staging data sitting in the database files.
func TestBootstrapFailure_DropsStagingTable(t *testing.T) {
	w, err := duckdb.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	ds := mkIncrDataset("aaaa-0001", 6, "2026-04-22")
	ds.FailAtOffset = 3
	srv := newFakeSocrata(t, ds)

	strat := &IncrementalStrategy{Portal: fakeHost(srv), Scheme: "http", RunID: "run1"}
	target := DatasetTarget{ID: ds.ID,
		Effective: config.Effective{DatasetID: ds.ID, Table: "crimes", BatchSize: 1}}
	client := &socrata.Client{BatchSize: 1, MaxRetries: 1, RetryWait: time.Millisecond}

	res, _ := strat.Sync(context.Background(), target, client, w, &RecordingReporter{}, 1, 1)
	if res.Status != "failed" {
		t.Fatalf("status: got %q, want failed", res.Status)
	}
	if n := stagingTableCount(t, w); n != 0 {
		t.Fatalf("failed bootstrap left %d staging table(s) behind", n)
	}
}

// Same guarantee for the full-replace path.
func TestFullReplaceFailure_DropsStagingTable(t *testing.T) {
	w, err := duckdb.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	ds := mkIncrDataset("aaaa-0001", 6, "2026-04-22")
	ds.FailAtOffset = 3
	srv := newFakeSocrata(t, ds)

	strat := &FullReplaceStrategy{Portal: fakeHost(srv), Scheme: "http", RunID: "run1"}
	target := DatasetTarget{ID: ds.ID,
		Effective: config.Effective{DatasetID: ds.ID, Table: "crimes", BatchSize: 1}}
	client := &socrata.Client{BatchSize: 1, MaxRetries: 1, RetryWait: time.Millisecond}

	res, _ := strat.Sync(context.Background(), target, client, w, &RecordingReporter{}, 1, 1)
	if res.Status != "failed" {
		t.Fatalf("status: got %q, want failed", res.Status)
	}
	if n := stagingTableCount(t, w); n != 0 {
		t.Fatalf("failed full-replace left %d staging table(s) behind", n)
	}
}

// The success path must still leave the schema clean — SwapIn consumes the
// staging table, and DropStaging on the failure path must not have disturbed
// that.
func TestBootstrapSuccess_LeavesNoStaging(t *testing.T) {
	w, err := duckdb.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if res := runIncr(t, mkIncrDataset("aaaa-0001", 4, "2026-04-22"), w, "run1", nil, ""); res.Status != "ok" {
		t.Fatalf("bootstrap: %v", res.Err)
	}
	if n := stagingTableCount(t, w); n != 0 {
		t.Fatalf("successful bootstrap left %d staging table(s)", n)
	}
}

// The strategies drop their own staging table when a sync returns an error, but
// nothing runs when the process is killed instead. A later run must clear what
// the dead one left, or the orphans accumulate forever — two interrupted
// million-row syncs left 8.2 million unreachable rows across two real databases.
func TestRun_SweepsWreckageFromAKilledRun(t *testing.T) {
	w, err := duckdb.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	// Simulate a process that died mid-sync: staging left behind, and a run row
	// that never reached a terminal status.
	if _, err := w.DB.Exec(`CREATE TABLE _csq_staging.crimes_DEADRUN (x INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := w.StartSyncRun("DEADRUN", "aaaa-0001", "crimes", "cfg", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}

	srv := newFakeSocrata(t, mkDataset("aaaa-0001", 3, 0))
	summary, err := Run(context.Background(), baseCfg(fakeHost(srv)), Deps{
		DB:       w,
		Client:   &socrata.Client{BatchSize: 5, MaxRetries: 1},
		Scheme:   "http",
		Reporter: &RecordingReporter{},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	if summary.Swept.StagingTables != 1 {
		t.Errorf("swept %d staging tables, want the 1 the dead run left", summary.Swept.StagingTables)
	}
	if summary.Swept.AbandonedRuns != 1 {
		t.Errorf("swept %d abandoned runs, want 1", summary.Swept.AbandonedRuns)
	}
	if n := stagingTableCount(t, w); n != 0 {
		t.Errorf("%d staging table(s) remain after a successful run", n)
	}
	if n, _ := w.IncompleteSyncRunCount(); n != 0 {
		t.Errorf("%d run(s) still report themselves unfinished", n)
	}
}

// A dry run inspects; it must not delete another run's working state.
func TestRun_DryRunDoesNotSweep(t *testing.T) {
	w, err := duckdb.Open(":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if _, err := w.DB.Exec(`CREATE TABLE _csq_staging.crimes_OTHER (x INTEGER)`); err != nil {
		t.Fatal(err)
	}

	srv := newFakeSocrata(t, mkDataset("aaaa-0001", 3, 0))
	summary, err := Run(context.Background(), baseCfg(fakeHost(srv)), Deps{
		DB:       w,
		Client:   &socrata.Client{BatchSize: 5, MaxRetries: 1},
		Scheme:   "http",
		Reporter: &RecordingReporter{},
		DryRun:   true,
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !summary.Swept.Empty() {
		t.Errorf("a dry run swept %s", summary.Swept)
	}
	if n := stagingTableCount(t, w); n != 1 {
		t.Errorf("dry run left %d staging tables, want the 1 it found untouched", n)
	}
}
