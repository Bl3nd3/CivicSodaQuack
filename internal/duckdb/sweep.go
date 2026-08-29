// Copyright (c) 2026 Neomantra Corp

package duckdb

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// SweepResult reports what a sweep reclaimed.
type SweepResult struct {
	StagingTables int
	StagingRows   int64
	AbandonedRuns int
}

// Empty reports whether the sweep found nothing to do, which is the normal case.
func (r SweepResult) Empty() bool {
	return r.StagingTables == 0 && r.AbandonedRuns == 0
}

func (r SweepResult) String() string {
	return fmt.Sprintf("%d staging table(s) holding %d rows, %d unfinished run(s)",
		r.StagingTables, r.StagingRows, r.AbandonedRuns)
}

// SweepAbandoned clears the wreckage a previous run left behind: staging tables
// that never reached their swap, and sync_runs rows that never reached a
// terminal status.
//
// Both are the signature of a process that died rather than failed. The
// strategies drop their own staging table when a sync returns an error, but
// nothing runs when the process is killed, loses power, or is stopped at the
// wrong moment — and the run row stays "running" forever, so the corpus reports
// a sync in flight that no longer exists. Two interrupted million-row syncs left
// 8.2 million orphaned rows across two databases, more than the live data in
// either.
//
// # Why this is safe to run
//
// The caller must hold the portal's advisory write lock, which every path into
// sync.Run does. That lock is what makes the reasoning valid: with it held, no
// other csq process can be mid-sync against this file, so every staging table
// present belongs to a run that is over, and every unfinished run row is
// abandoned rather than in progress. Without the lock this would delete the
// working state of a live sync in another process.
//
// It is deliberately not selective about run IDs. A staging table from the
// current run cannot exist yet — sweeping happens before any dataset starts —
// so "everything in _csq_staging" and "everything from a previous run" are the
// same set, and the simpler rule has no edge case where a stale table survives
// because its run ID looked current.
func (w *Writer) SweepAbandoned(now time.Time) (SweepResult, error) {
	var res SweepResult

	rows, err := w.DB.Query(
		`SELECT table_name, coalesce(estimated_size, 0)
		   FROM duckdb_tables()
		  WHERE schema_name = '_csq_staging'`)
	if err != nil {
		return res, fmt.Errorf("list staging tables: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		var size int64
		if err := rows.Scan(&name, &size); err != nil {
			rows.Close()
			return res, err
		}
		names = append(names, name)
		res.StagingRows += size
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return res, err
	}
	rows.Close()

	for _, name := range names {
		if _, err := w.DB.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS _csq_staging."%s"`, name)); err != nil {
			return res, fmt.Errorf("drop staging.%s: %w", name, err)
		}
		res.StagingTables++
	}

	// A run with no finished_at never wrote a terminal status. Recording it as
	// aborted, with a reason, keeps `research --query failed-runs` honest: a
	// gap in a corpus that reports itself as still-running is a gap nobody
	// investigates.
	out, err := w.DB.Exec(
		`UPDATE _csq.sync_runs
		    SET status = 'aborted',
		        finished_at = $1,
		        error = coalesce(error, 'run did not finish; the process ended before it could record an outcome')
		  WHERE finished_at IS NULL`, now.UTC())
	if err != nil {
		return res, fmt.Errorf("resolve unfinished runs: %w", err)
	}
	if n, err := out.RowsAffected(); err == nil {
		res.AbandonedRuns = int(n)
	}
	return res, nil
}

// MainTableRows returns table name → row count for one attached database's
// main schema.
//
// Shared rather than reimplemented per caller: both the CLI and the web server
// need it to tell "this city cannot answer" from "this city has not synced
// yet", and those two must never disagree about which is which.
//
// estimated_size is exact for a table with no pending changes, which is the
// case for every database this is asked about — they are attached read-only.
func MainTableRows(db *sql.DB, database string) (map[string]int64, error) {
	rows, err := db.Query(fmt.Sprintf(
		`SELECT table_name, coalesce(estimated_size, 0) FROM duckdb_tables()
		  WHERE database_name = '%s' AND schema_name = 'main'`,
		escapeLiteral(database)))
	if err != nil {
		return nil, fmt.Errorf("inventory %s: %w", database, err)
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var name string
		var n int64
		if err := rows.Scan(&name, &n); err != nil {
			return nil, err
		}
		out[name] = n
	}
	return out, rows.Err()
}

func escapeLiteral(s string) string { return strings.ReplaceAll(s, "'", "''") }
