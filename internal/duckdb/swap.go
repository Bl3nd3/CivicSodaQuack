// Copyright (c) 2026 Neomantra Corp

package duckdb

import "fmt"

// SwapIn replaces main.<target> with the contents of _csq_staging.<stagingName>,
// in a single transaction. The new main table is created via CTAS from the
// staging table, then the staging table is dropped. On success, the prior
// main.<target> is gone and the staging slot is empty.
//
// If primaryKey != "", it is reinstalled on the target after the CTAS
// (which strips constraints).
//
// Note: DuckDB does not support ALTER TABLE ... SET SCHEMA, so we cannot
// rename-and-move the staging table; we copy then drop. The copy stays inside
// the same database file, so it's a fast block-level scan, not a network or
// cross-process move.
func (w *Writer) SwapIn(stagingName, target, primaryKey string) error {
	tx, err := w.DB.Begin()
	if err != nil {
		return fmt.Errorf("begin swap tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS main."%s"`, target)); err != nil {
		return fmt.Errorf("drop main.%s: %w", target, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(
		`CREATE TABLE main."%s" AS SELECT * FROM _csq_staging."%s"`, target, stagingName)); err != nil {
		return fmt.Errorf("ctas main.%s from staging.%s: %w", target, stagingName, err)
	}
	if _, err := tx.Exec(fmt.Sprintf(
		`DROP TABLE _csq_staging."%s"`, stagingName)); err != nil {
		return fmt.Errorf("drop staging.%s: %w", stagingName, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit swap tx: %w", err)
	}

	if primaryKey != "" {
		if _, err := w.DB.Exec(fmt.Sprintf(
			`ALTER TABLE main."%s" ADD PRIMARY KEY ("%s")`, target, primaryKey)); err != nil {
			return fmt.Errorf("install pk on main.%s: %w", target, err)
		}
	}
	return nil
}

// DropStaging removes an abandoned staging table.
//
// SwapIn drops the staging table it consumes, so the only tables this has to
// clean up are those whose run never reached the swap. Nothing else reclaims
// them: each run stages under its own <table>_<runID> name, so a failed run's
// table is not reused by the next one, and it survives until someone drops the
// schema. Two interrupted million-row syncs are enough to leave a database
// carrying hundreds of thousands of rows that no query will ever read.
//
// Errors are the caller's to ignore — this runs on the failure path, where the
// error that got us here is the one worth reporting.
func (w *Writer) DropStaging(stagingName string) error {
	_, err := w.DB.Exec(fmt.Sprintf(`DROP TABLE IF EXISTS _csq_staging."%s"`, stagingName))
	if err != nil {
		return fmt.Errorf("drop staging.%s: %w", stagingName, err)
	}
	return nil
}
