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
