// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func makeEmptyCSQDB(t *testing.T, path string) {
	t.Helper()
	db, err := openDB(path, false)
	if err != nil {
		t.Fatalf("open seed: %v", err)
	}
	defer db.Close()
	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS _csq`,
		`CREATE TABLE IF NOT EXISTS _csq.catalog (
			id VARCHAR PRIMARY KEY, name VARCHAR NOT NULL, description VARCHAR,
			category VARCHAR, tags JSON, row_count BIGINT, updated_at TIMESTAMP,
			fetched_at TIMESTAMP NOT NULL, raw JSON NOT NULL)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

// TestOpenPools_ReadOnlyByDefault pins the property that matters: a portal
// with no registered config is opened read-only, so it does not take DuckDB's
// exclusive file lock. Without this, serving a portal blocks any concurrent
// sync, second server, or CLI session against the same file.
func TestOpenPools_ReadOnlyByDefault(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.duckdb")
	makeEmptyCSQDB(t, path)

	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	if !pools.Portals["test"].ReadOnly {
		t.Error("pool should be read-only when no config is registered")
	}
	// The engine must actually refuse writes, not merely record a flag.
	if _, err := pools.Portals["test"].DB.Exec(
		`CREATE TABLE should_not_exist (x INT)`); err == nil {
		t.Error("expected the engine to reject a write on a read-only pool")
	}
}

// TestOpenPools_ReadOnlyAllowsSecondProcess pins the contention this fix
// actually removes: while the server holds a read-only pool, a separate process
// can open the same file read-only. Before the fix the pool was read-write, and
// DuckDB's exclusive lock made that impossible — so two servers, or a server
// plus a CLI session, could not share one portal.
//
// What this does NOT enable, despite the obvious hope: a concurrent `csq sync`.
// DuckDB permits many readers or one writer, never both, so a writer is refused
// whichever mode the reader holds. Serving and syncing the same file at once is
// not reachable by changing access modes; it needs snapshot-and-swap or a
// second file.
//
// It must be cross-process. DuckDB's instance cache refuses to hold one file at
// two access modes *within* a process, so a same-process opener fails here no
// matter what the pool does; that in-process constraint is precisely why
// OpenPools takes a writable set rather than always opening read-only. The test
// re-executes itself as a helper to get a genuinely separate process.
func TestOpenPools_ReadOnlyAllowsSecondProcess(t *testing.T) {
	if mode := os.Getenv("CSQ_TEST_HELPER"); mode != "" {
		runPoolsHelper(mode, os.Getenv("CSQ_TEST_HELPER_DB"))
		return // unreachable; runPoolsHelper exits
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "test.duckdb")

	// Seed out-of-process. DuckDB's instance cache keeps a file locked for the
	// lifetime of the process that opened it read-write, even after Close, so
	// seeding here would leave this process holding the very lock the test is
	// trying to prove absent.
	runPoolsHelperProcess(t, "seed", path)

	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	// Stands in for a second server, or a `duckdb -readonly` session, against
	// the same portal the server is serving.
	runPoolsHelperProcess(t, "read", path)

	// And the pool itself still reads fine alongside it.
	var n int
	if err := pools.Portals["test"].DB.QueryRow(
		`SELECT COUNT(*) FROM _csq.catalog`).Scan(&n); err != nil {
		t.Fatalf("pool read while a second reader is attached: %v", err)
	}
}

// runPoolsHelper is the child half of TestOpenPools_ReadOnlyAllowsSecondProcess.
// It never returns; the parent inspects the exit status.
func runPoolsHelper(mode, dbPath string) {
	readOnly := mode == "read"
	db, err := openDB(dbPath, readOnly)
	if err != nil {
		fmt.Fprintf(os.Stderr, "helper %s: open: %v\n", mode, err)
		os.Exit(1)
	}
	defer db.Close()

	switch mode {
	case "seed":
		for _, s := range []string{
			`CREATE SCHEMA IF NOT EXISTS _csq`,
			`CREATE TABLE IF NOT EXISTS _csq.catalog (
				id VARCHAR PRIMARY KEY, name VARCHAR NOT NULL, description VARCHAR,
				category VARCHAR, tags JSON, row_count BIGINT, updated_at TIMESTAMP,
				fetched_at TIMESTAMP NOT NULL, raw JSON NOT NULL)`,
		} {
			if _, err := db.Exec(s); err != nil {
				fmt.Fprintf(os.Stderr, "helper seed: exec: %v\n", err)
				os.Exit(1)
			}
		}
	case "read":
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM _csq.catalog`).Scan(&n); err != nil {
			fmt.Fprintf(os.Stderr, "helper read: query: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "helper: unknown mode %q\n", mode)
		os.Exit(2)
	}
	os.Exit(0)
}

// runPoolsHelperProcess re-executes this test binary as a helper child.
func runPoolsHelperProcess(t *testing.T, mode, dbPath string) {
	t.Helper()
	cmd := exec.Command(os.Args[0],
		"-test.run=^TestOpenPools_ReadOnlyAllowsSecondProcess$")
	cmd.Env = append(os.Environ(),
		"CSQ_TEST_HELPER="+mode,
		"CSQ_TEST_HELPER_DB="+dbPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("helper %q failed (a read-only pool must not block another "+
			"process): %v\n%s", mode, err, out)
	}
}

func TestOpenPools_Success(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.duckdb")
	makeEmptyCSQDB(t, path)

	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()
	if pools.Host == nil {
		t.Error("host DB nil")
	}
	if _, ok := pools.Portals["test"]; !ok {
		t.Errorf("portal 'test' missing")
	}
	if pools.Portals["test"].DB == nil {
		t.Errorf("portal DB nil")
	}
	// Host should see the ATTACHed schema
	var n int
	if err := pools.Host.QueryRow(`SELECT COUNT(*) FROM test._csq.catalog`).Scan(&n); err != nil {
		t.Errorf("query attached: %v", err)
	}
}

func TestOpenPools_MissingFile(t *testing.T) {
	_, err := OpenPools([]DBSpec{{Alias: "x", Path: "/nonexistent/foo.duckdb"}}, nil)
	if err == nil {
		t.Fatal("want error for missing file")
	}
}

func TestOpenPools_NotCSQDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wrong.duckdb")
	// Open without seeding the _csq schema
	db, err := openDB(path, false)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db.Close()

	_, err = OpenPools([]DBSpec{{Alias: "x", Path: path}}, nil)
	if err == nil {
		t.Fatal("want 'not a CivicSodaQuack DuckDB' error")
	}
}

func TestOpenPools_PortalDBReadsWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.duckdb")
	makeEmptyCSQDB(t, path)

	// Writable because a config is registered for this alias; that is the only
	// case where the pool is opened read-write.
	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}},
		map[string]bool{"test": true})
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	if pools.Portals["test"].ReadOnly {
		t.Fatal("pool should be read-write when the alias is in writable")
	}
	defer pools.Close()

	// Write
	_, err = pools.Portals["test"].DB.Exec(
		`INSERT INTO _csq.catalog (id, name, fetched_at, raw)
		 VALUES ('aaaa-0001', 'Test', NOW(), '{}')`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	// Read
	var name string
	err = pools.Portals["test"].DB.QueryRow(
		`SELECT name FROM _csq.catalog WHERE id = 'aaaa-0001'`).Scan(&name)
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "Test" {
		t.Errorf("got %q", name)
	}
}
