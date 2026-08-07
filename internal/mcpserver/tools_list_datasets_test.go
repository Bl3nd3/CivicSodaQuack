// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
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
	if d.TableName == nil || *d.TableName != "aaaa_0001" {
		t.Errorf("table_name: got %v, want aaaa_0001", d.TableName)
	}
	if !d.Synced {
		t.Error("synced should be true for a successfully synced dataset")
	}
}

// TestListDatasets_NeverSynced_ReportsNoTable is the regression test for P1-2.
// A catalog entry with no successful sync has no table, and the response must
// say so: a synthesised name is indistinguishable from a real one to a consumer
// and turns a discovery-time fact into a query-time error. The previous version
// of this test asserted the synthesised name was correct, which is how the
// behaviour survived.
func TestListDatasets_NeverSynced_ReportsNoTable(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "Crimes", Category: "Safety", Synced: false})
	defer cleanup()

	got, err := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 dataset, got %d", len(got))
	}
	if got[0].RowCount != nil {
		t.Errorf("row_count should be nil for an unsynced dataset; got %v", *got[0].RowCount)
	}
	if got[0].TableName != nil {
		t.Errorf("table_name must be nil for an unsynced dataset; got %q "+
			"(a fabricated name invites the consumer to query a table that does not exist)",
			*got[0].TableName)
	}
	if got[0].Synced {
		t.Error("synced should be false for a dataset with no successful sync")
	}
}

// TestListDatasets_UnsyncedTableNameIsNullInJSON pins the wire format, not just
// the Go value: the field must be present and null so a consumer sees the
// absence, rather than omitted where it could be mistaken for an oversight.
func TestListDatasets_UnsyncedTableNameIsNullInJSON(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "Crimes", Synced: false})
	defer cleanup()

	got, err := listDatasetsHandler(context.Background(), pools, ListDatasetsArgs{})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	b, err := json.Marshal(got[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"table_name":null`, `"row_count":null`, `"synced":false`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("payload missing %s\ngot: %s", want, b)
		}
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
