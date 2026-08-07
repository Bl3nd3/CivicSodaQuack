// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"database/sql"
	"fmt"
	"os"

	_ "github.com/duckdb/duckdb-go/v2"
)

// PortalPools holds the *sql.DB handle for one portal file.
// Single pool per portal: DuckDB's per-process instance cache rejects opening
// the same file twice with different access_mode settings. User SQL goes
// through Pools.Host where BEGIN TRANSACTION READ ONLY enforces the read-only
// contract; the per-portal pools only run hardcoded internal queries
// (list/describe/search).
type PortalPools struct {
	Path string
	DB   *sql.DB
	// ReadOnly reports whether this pool was opened with access_mode=read_only,
	// which is the case for every portal without a registered config.
	ReadOnly bool
}

// Pools owns the in-memory host DB plus per-portal pools. The host has each
// portal ATTACHed read-only as <alias>; tools issue queries through Pools.Host
// so cross-portal queries via "<alias>.<schema>.<table>" syntax work.
type Pools struct {
	Host    *sql.DB
	Portals map[string]*PortalPools
}

// OpenPools opens the host and per-portal pools, ATTACHes each portal to the
// host, and verifies each file is a CivicSodaQuack DuckDB.
//
// writable names the aliases that must be opened read-write. Pass the set of
// portals with a registered config: their write tools (sync_dataset,
// refresh_catalog) call duckdb.Open on the same path in-process, and DuckDB's
// instance cache rejects the same file held at two different access modes, so
// a read-only pool there would break them.
//
// Every other portal is opened access_mode=read_only. Those pools only ever
// run hardcoded read queries, and holding them read-only lets a concurrent
// csq sync — or a second server, or the duckdb CLI — open the same file, which
// an exclusive write lock would block for no benefit.
func OpenPools(specs []DBSpec, writable map[string]bool) (*Pools, error) {
	host, err := openDB(":memory:", false)
	if err != nil {
		return nil, fmt.Errorf("open host: %w", err)
	}

	p := &Pools{
		Host:    host,
		Portals: make(map[string]*PortalPools, len(specs)),
	}

	for _, spec := range specs {
		if _, err := os.Stat(spec.Path); err != nil {
			p.Close()
			return nil, fmt.Errorf("--db %s: %w", spec.Path, err)
		}
		readOnly := !writable[spec.Alias]
		db, err := openDB(spec.Path, readOnly)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("open %s: %w", spec.Path, err)
		}
		if err := assertIsCSQDB(db, spec.Path); err != nil {
			db.Close()
			p.Close()
			return nil, err
		}
		// ATTACH read-only to the host
		_, err = host.Exec(fmt.Sprintf(`ATTACH '%s' AS %s (READ_ONLY)`,
			escapeSQLString(spec.Path), spec.Alias))
		if err != nil {
			db.Close()
			p.Close()
			return nil, fmt.Errorf("attach %s as %s: %w", spec.Path, spec.Alias, err)
		}
		p.Portals[spec.Alias] = &PortalPools{Path: spec.Path, DB: db, ReadOnly: readOnly}
	}
	return p, nil
}

// Close closes every pool. Safe to call multiple times.
func (p *Pools) Close() error {
	var firstErr error
	for _, pp := range p.Portals {
		if pp.DB != nil {
			if err := pp.DB.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	if p.Host != nil {
		if err := p.Host.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// openDB opens a *sql.DB for the given path. When readOnly is set the file is
// opened access_mode=read_only, which DuckDB permits concurrently across
// processes; a read-write open takes an exclusive lock on the file.
//
// The host (":memory:") is always read-write — it has to be, so ATTACH can run
// at startup. Engine-level read-only enforcement for user SQL happens there via
// BEGIN TRANSACTION READ ONLY (see querySQLHandler).
func openDB(path string, readOnly bool) (*sql.DB, error) {
	dsn := path
	if readOnly {
		dsn += "?access_mode=read_only"
	}
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// assertIsCSQDB verifies the file has _csq.catalog (the Phase 1 marker).
func assertIsCSQDB(db *sql.DB, path string) error {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM information_schema.tables
		 WHERE table_schema = '_csq' AND table_name = 'catalog'`).Scan(&n)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if n == 0 {
		return fmt.Errorf("no _csq.catalog in %s; not a CivicSodaQuack DuckDB", path)
	}
	return nil
}

// escapeSQLString escapes single quotes so the path can be safely embedded in
// a single-quoted DuckDB string literal.
func escapeSQLString(s string) string {
	return replaceAll(s, "'", "''")
}

func replaceAll(s, old, new string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		if i+len(old) <= len(s) && s[i:i+len(old)] == old {
			out = append(out, new...)
			i += len(old)
			continue
		}
		out = append(out, s[i])
		i++
	}
	return string(out)
}
