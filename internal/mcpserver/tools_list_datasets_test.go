// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"sort"
	"testing"
	"time"
)

func openFixturePools(t *testing.T, datasets ...FixtureDataset) (*Pools, func()) {
	t.Helper()
	dir := t.TempDir()
	path := seedFixtureDB(t, dir, "test.duckdb", datasets...)
	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	return pools, func() { pools.Close() }
}

func TestListDatasets_Empty(t *testing.T) {
	pools, cleanup := openFixturePools(t)
	defer cleanup()

	got, err := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d, want 0", len(got))
	}
}

func TestListDatasets_OnePortal(t *testing.T) {
	hwm := time.Date(2026, 4, 23, 0, 0, 0, 0, time.UTC)
	pools, cleanup := openFixturePools(t,
		FixtureDataset{
			ID: "aaaa-0001", Name: "Crimes", Category: "Public Safety",
			TableName:  "aaaa_0001",
			ColumnDefs: []string{"socrata_id VARCHAR", "score DOUBLE"},
			Rows:       []map[string]any{{"socrata_id": "a", "score": 1.0}, {"socrata_id": "b", "score": 2.0}},
			Synced:     true, HWM: hwm,
		})
	defer cleanup()

	got, err := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1, got %d", len(got))
	}
	d := got[0]
	if d.DatasetID != "aaaa-0001" || d.Portal != "test" || d.Name != "Crimes" {
		t.Errorf("dataset summary wrong: %+v", d)
	}
	if d.RowCount == nil || *d.RowCount != 2 {
		t.Errorf("rowcount: got %v, want 2", d.RowCount)
	}
	if d.TableName != "aaaa_0001" {
		t.Errorf("table_name: got %q", d.TableName)
	}
}

func TestListDatasets_NeverSynced_RowCountNil(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "Crimes", Category: "Safety", Synced: false})
	defer cleanup()

	got, _ := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{})
	if len(got) != 1 || got[0].RowCount != nil {
		t.Errorf("RowCount should be nil for un-synced dataset; got %+v", got)
	}
	// Fallback table name from id
	if got[0].TableName != "aaaa_0001" {
		t.Errorf("fallback table_name wrong: %q", got[0].TableName)
	}
}

func TestListDatasets_PortalFilter(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "A"},
		FixtureDataset{ID: "bbbb-0002", Name: "B"})
	defer cleanup()

	got, _ := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{Portal: "missing"})
	if len(got) != 0 {
		t.Errorf("portal=missing should return empty, got %d", len(got))
	}
	got, _ = listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{Portal: "test"})
	if len(got) != 2 {
		t.Errorf("portal=test should return 2, got %d", len(got))
	}
}

func TestListDatasets_CategoryFilterCaseInsensitive(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "A", Category: "Public Safety"},
		FixtureDataset{ID: "bbbb-0002", Name: "B", Category: "Parks"})
	defer cleanup()

	got, _ := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{Category: "safety"})
	if len(got) != 1 || got[0].DatasetID != "aaaa-0001" {
		ids := sort.StringSlice{}
		for _, d := range got {
			ids = append(ids, d.DatasetID)
		}
		t.Errorf("got %v, want [aaaa-0001]", []string(ids))
	}
}

func TestListDatasets_TwoPortals(t *testing.T) {
	dir := t.TempDir()
	a := seedFixtureDB(t, dir, "a.duckdb",
		FixtureDataset{ID: "aaaa-0001", Name: "A1"})
	b := seedFixtureDB(t, dir, "b.duckdb",
		FixtureDataset{ID: "bbbb-0002", Name: "B1"})
	pools, err := OpenPools([]DBSpec{{Alias: "a", Path: a}, {Alias: "b", Path: b}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	got, err := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 datasets, got %d", len(got))
	}
	// Find each by id and confirm portal alias is correct on each row
	gotByID := map[string]string{}
	for _, d := range got {
		gotByID[d.DatasetID] = d.Portal
	}
	if gotByID["aaaa-0001"] != "a" {
		t.Errorf("aaaa-0001 portal: got %q, want a", gotByID["aaaa-0001"])
	}
	if gotByID["bbbb-0002"] != "b" {
		t.Errorf("bbbb-0002 portal: got %q, want b", gotByID["bbbb-0002"])
	}
}
