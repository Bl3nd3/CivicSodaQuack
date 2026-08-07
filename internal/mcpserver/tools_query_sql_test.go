// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestQuerySQL_HappySelect(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{
			ID: "aaaa-0001", Name: "Crimes",
			TableName:  "crimes",
			ColumnDefs: []string{"socrata_id VARCHAR", "score DOUBLE"},
			Rows: []map[string]any{
				{"socrata_id": "a", "score": 1.0},
				{"socrata_id": "b", "score": 2.0},
			},
		})
	defer cleanup()

	got, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `SELECT socrata_id, score FROM test.main.crimes ORDER BY socrata_id`}, time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if got.RowCount != 2 || got.Truncated {
		t.Errorf("rowcount=%d truncated=%v", got.RowCount, got.Truncated)
	}
	if len(got.Columns) != 2 || got.Columns[0] != "socrata_id" {
		t.Errorf("columns: %v", got.Columns)
	}
	if len(got.Rows) != 2 {
		t.Errorf("rows: got %d", len(got.Rows))
	}
}

func TestQuerySQL_RejectsWrites(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	_, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `CREATE TABLE main.evil (x INT)`}, time.Second)
	if err == nil {
		t.Fatal("CREATE TABLE should be rejected")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "read") {
		t.Errorf("error should mention read-only: %v", err)
	}
}

func TestQuerySQL_TruncatesByRowCap(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	// Use a generate_series query to make 2000 synthetic rows
	got, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `SELECT * FROM range(0, 2000)`}, 5*time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got.Truncated {
		t.Errorf("expected truncated=true at 1000-row cap")
	}
	if got.RowCount != 1000 {
		t.Errorf("rowcount: got %d, want 1000", got.RowCount)
	}
	if got.Note == "" {
		t.Errorf("expected a note explaining truncation")
	}
}

func TestQuerySQL_Timeout(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	// 1ms timeout against a query that takes longer than that to return any rows
	_, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `SELECT * FROM range(0, 100000000)`}, time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("want timeout error, got %v", err)
	}
}

func TestQuerySQL_CrossPortal(t *testing.T) {
	dir := t.TempDir()
	a := seedFixtureDB(t, dir, "a.duckdb",
		FixtureDataset{
			ID: "aaaa-0001", Name: "Aw",
			TableName: "items", ColumnDefs: []string{"id VARCHAR"},
			Rows: []map[string]any{{"id": "x"}},
		})
	b := seedFixtureDB(t, dir, "b.duckdb",
		FixtureDataset{
			ID: "bbbb-0001", Name: "Bw",
			TableName: "items", ColumnDefs: []string{"id VARCHAR"},
			Rows: []map[string]any{{"id": "x"}},
		})
	pools, err := OpenPools([]DBSpec{{Alias: "a", Path: a}, {Alias: "b", Path: b}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	got, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `SELECT a.id FROM a.main.items a JOIN b.main.items b ON a.id = b.id`}, time.Second)
	if err != nil {
		t.Fatalf("cross-portal: %v", err)
	}
	if got.RowCount != 1 {
		t.Errorf("want 1 row, got %d", got.RowCount)
	}
}

func TestQuerySQL_TruncatesByByteCap(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	// Generate a small number of rows but with very large per-row payload
	// so the byte cap (1MB) trips before the row cap (1000).
	// repeat('x', N) produces an N-char string. 50 rows * ~30KB each = ~1.5MB.
	got, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `SELECT i AS n, repeat('x', 30000) AS payload FROM range(0, 50) t(i)`}, 5*time.Second)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !got.Truncated {
		t.Errorf("expected truncated=true at byte cap")
	}
	if got.RowCount >= 50 {
		t.Errorf("expected fewer than 50 rows due to byte cap; got %d", got.RowCount)
	}
	if got.Note == "" {
		t.Errorf("expected a note explaining truncation")
	}
}

func TestQuerySQL_ParseErrorReturnsDuckDBMessage(t *testing.T) {
	pools, cleanup := openFixturePools(t,
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	defer cleanup()

	_, err := querySQLHandler(context.Background(), pools,
		QuerySQLArgs{SQL: `THIS IS NOT VALID SQL AT ALL`}, time.Second)
	if err == nil {
		t.Fatal("want parse error")
	}
	// Must propagate the DuckDB error (not wrap it as a generic message)
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "parser") && !strings.Contains(msg, "syntax") {
		t.Errorf("error should reflect DuckDB parser/syntax message; got: %v", err)
	}
}
