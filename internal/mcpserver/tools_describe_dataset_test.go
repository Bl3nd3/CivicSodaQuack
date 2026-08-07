// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDescribeDataset_Found(t *testing.T) {
	hwm := time.Date(2026, 4, 23, 5, 0, 0, 0, time.UTC)
	pools, cleanup := openFixturePools(t,
		FixtureDataset{
			ID: "aaaa-0001", Name: "Crimes", Description: "Chicago crimes",
			Category: "Public Safety", Tags: []string{"crime", "311"},
			TableName:  "aaaa_0001",
			ColumnDefs: []string{"socrata_id VARCHAR", "score DOUBLE", "kind VARCHAR"},
			Rows:       []map[string]any{{"socrata_id": "a", "score": 1.0, "kind": "x"}},
			Synced:     true, HWM: hwm,
		})
	defer cleanup()

	got, err := describeDatasetHandler(context.Background(), pools, DescribeDatasetArgs{DatasetID: "aaaa-0001"})
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if got.DatasetID != "aaaa-0001" || got.Description != "Chicago crimes" {
		t.Errorf("got %+v", got)
	}
	if len(got.Tags) != 2 {
		t.Errorf("tags: got %v", got.Tags)
	}
	// socrata_id should be filtered out of user-visible columns
	for _, c := range got.Columns {
		if c.Name == "socrata_id" {
			t.Errorf("socrata_id should be hidden from columns")
		}
	}
	if len(got.Columns) != 2 {
		t.Errorf("user columns: got %d, want 2", len(got.Columns))
	}
	if got.LastSync == nil || got.LastSync.Status != "ok" || got.LastSync.RowsWritten != 1 {
		t.Errorf("last sync: got %+v", got.LastSync)
	}
	if got.HWMUpdatedAt == nil || !got.HWMUpdatedAt.Equal(hwm) {
		t.Errorf("hwm: got %v", got.HWMUpdatedAt)
	}
}

func TestDescribeDataset_NeverSynced(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	got, err := describeDatasetHandler(context.Background(), pools, DescribeDatasetArgs{DatasetID: "aaaa-0001"})
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if got.LastSync != nil {
		t.Errorf("LastSync should be nil")
	}
	if got.HWMUpdatedAt != nil {
		t.Errorf("HWMUpdatedAt should be nil")
	}
	if len(got.Columns) != 0 {
		t.Errorf("Columns should be empty (no table)")
	}
}

func TestDescribeDataset_Unknown(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	_, err := describeDatasetHandler(context.Background(), pools, DescribeDatasetArgs{DatasetID: "zzzz-9999"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("want not-found error, got %v", err)
	}
}

func TestDescribeDataset_AmbiguousAcrossPortals(t *testing.T) {
	dir := t.TempDir()
	a := seedFixtureDB(t, dir, "a.duckdb",
		FixtureDataset{ID: "aaaa-0001", Name: "A's crimes"})
	b := seedFixtureDB(t, dir, "b.duckdb",
		FixtureDataset{ID: "aaaa-0001", Name: "B's crimes"})
	pools, err := OpenPools([]DBSpec{{Alias: "a", Path: a}, {Alias: "b", Path: b}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	_, err = describeDatasetHandler(context.Background(), pools, DescribeDatasetArgs{DatasetID: "aaaa-0001"})
	if err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Errorf("want ambiguous error, got %v", err)
	}
	// Disambiguating with portal works
	got, err := describeDatasetHandler(context.Background(), pools, DescribeDatasetArgs{DatasetID: "aaaa-0001", Portal: "b"})
	if err != nil {
		t.Fatalf("disambiguated: %v", err)
	}
	if got.Name != "B's crimes" {
		t.Errorf("got %q", got.Name)
	}
}

func TestDescribeDataset_UnknownPortal(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	_, err := describeDatasetHandler(context.Background(), pools, DescribeDatasetArgs{DatasetID: "aaaa-0001", Portal: "nope"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("want unknown-portal error mentioning nope, got %v", err)
	}
}
