// Copyright (c) 2026 Neomantra Corp

package sync

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
)

// IncrementalStrategy implements WriteStrategy. It auto-detects bootstrap vs.
// delta based on _csq.dataset_state and the live table's socrata_id column,
// then either bootstraps via a staging-swap (Phase-1-style with HWM tracking)
// or performs a delta upsert pass.
//
// `mode: full_replace` in the per-dataset YAML override forces the bootstrap
// path on every run.
type IncrementalStrategy struct {
	Portal string
	Scheme string // "" defaults to "https"
	RunID  string
}

func (s *IncrementalStrategy) scheme() string {
	if s.Scheme != "" {
		return s.Scheme
	}
	return "https"
}

func (s *IncrementalStrategy) Sync(
	ctx context.Context,
	target DatasetTarget,
	client *socrata.Client,
	w *duckdb.Writer,
	prog ProgressReporter,
	idx, total int,
) (DatasetResult, error) {
	state, err := w.ReadDatasetState(target.ID)
	if err != nil {
		return failResult(target, "failed", fmt.Errorf("read dataset_state: %w", err)), nil
	}

	useBootstrap, err := shouldBootstrap(state, target, w)
	if err != nil {
		return failResult(target, "failed", err), nil
	}

	if useBootstrap {
		return s.bootstrap(ctx, target, client, w, prog, idx, total)
	}
	return s.delta(ctx, target, client, w, prog, idx, total, state)
}

func shouldBootstrap(state *duckdb.DatasetState, target DatasetTarget, w *duckdb.Writer) (bool, error) {
	if target.Effective.Mode == "full_replace" {
		return true, nil
	}
	if state == nil {
		return true, nil
	}
	hasPK, err := tableHasSocrataIDPK(w, target.Effective.Table)
	if err != nil {
		return false, fmt.Errorf("check pk on %s: %w", target.Effective.Table, err)
	}
	return !hasPK, nil
}

// tableHasSocrataIDPK reports whether main.<table> exists AND has a socrata_id column.
// (PK presence is implied by the socrata_id column; we don't introspect constraints.)
func tableHasSocrataIDPK(w *duckdb.Writer, table string) (bool, error) {
	var n int
	err := w.DB.QueryRow(
		`SELECT COUNT(*) FROM information_schema.columns
		 WHERE table_schema = 'main' AND table_name = $1 AND column_name = 'socrata_id'`,
		table,
	).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// bootstrap streams the full dataset into _csq_staging.<table>_<runID> with the
// socrata_id PK installed, swaps it into main, then writes dataset_state with
// the observed max(:updated_at). Mirrors FullReplaceStrategy.Sync but tracks
// HWM during streaming so we don't have to reread the source.
func (s *IncrementalStrategy) bootstrap(
	ctx context.Context,
	target DatasetTarget,
	client *socrata.Client,
	w *duckdb.Writer,
	prog ProgressReporter,
	idx, total int,
) (DatasetResult, error) {
	started := time.Now().UTC()
	prog.DatasetStart(idx, total, target)
	result := DatasetResult{Target: target, StartedAt: started}

	hwmCol := target.Effective.HWMColumn
	if hwmCol == "" {
		hwmCol = ":updated_at"
	}

	meta, err := fetchMetadata(ctx, client, s.scheme(), s.Portal, target.ID)
	if err != nil {
		return failResult(target, "failed", fmt.Errorf("fetch metadata: %w", err)), nil
	}
	cols := filterColumns(meta.Columns, target.Effective.SkipColumns)
	stagingName := target.Effective.Table + "_" + s.RunID
	schema := duckdb.BuildSchemaWithSocrataID(stagingName, cols)

	if _, err := w.DB.ExecContext(ctx, schema.CreateTableSQLIn("_csq_staging")); err != nil {
		return failResult(target, "failed", fmt.Errorf("create staging: %w", err)), nil
	}

	var rowsWritten int64
	var maxHWM *time.Time
	err = client.StreamRowsCtx(ctx, s.scheme(), s.Portal, target.ID,
		target.Effective.OrderBy, target.Effective.Where, ":*,*",
		target.Effective.Limit,
		func(page []socrata.Row) error {
			if err := w.InsertRowsInto("_csq_staging", schema, page); err != nil {
				return err
			}
			for _, row := range page {
				if t := extractRowHWM(row, hwmCol); t != nil {
					if maxHWM == nil || t.After(*maxHWM) {
						maxHWM = t
					}
				}
			}
			rowsWritten += int64(len(page))
			prog.DatasetProgress(idx, total, target, rowsWritten)
			return nil
		},
	)
	if err != nil {
		// See FullReplaceStrategy: a staging table whose run never swapped is
		// dead weight that nothing else reclaims.
		_ = w.DropStaging(stagingName)
		if ctx.Err() != nil {
			return failResult(target, "aborted", ctx.Err()), nil
		}
		return failResult(target, "failed", err), nil
	}

	if err := w.SwapIn(stagingName, target.Effective.Table, schema.PrimaryKey); err != nil {
		_ = w.DropStaging(stagingName)
		return failResult(target, "failed", fmt.Errorf("swap: %w", err)), nil
	}

	now := time.Now().UTC()
	stateRow := duckdb.DatasetState{
		DatasetID:         target.ID,
		HWMUpdatedAt:      maxHWM,
		LastFullReplaceAt: &now,
		LastRunID:         s.RunID,
		HWMColumn:         hwmCol,
	}

	result.Status = "ok"
	result.RowsWritten = rowsWritten
	result.FinishedAt = time.Now().UTC()

	if err := w.UpsertDatasetState(stateRow); err != nil {
		// Data is in main but state didn't land. Surface as ok with err set;
		// sync_runs records the message. Next run will re-bootstrap (idempotent).
		result.Err = fmt.Errorf("write dataset_state: %w", err)
	}
	return result, nil
}

func (s *IncrementalStrategy) delta(
	ctx context.Context,
	target DatasetTarget,
	client *socrata.Client,
	w *duckdb.Writer,
	prog ProgressReporter,
	idx, total int,
	state *duckdb.DatasetState,
) (DatasetResult, error) {
	started := time.Now().UTC()
	prog.DatasetStart(idx, total, target)
	result := DatasetResult{Target: target, StartedAt: started}

	hwmCol := state.HWMColumn
	if hwmCol == "" {
		hwmCol = ":updated_at"
	}

	// Fetch metadata + check drift before any writes.
	meta, err := fetchMetadata(ctx, client, s.scheme(), s.Portal, target.ID)
	if err != nil {
		return failResult(target, "failed", fmt.Errorf("fetch metadata: %w", err)), nil
	}
	cols := filterColumns(meta.Columns, target.Effective.SkipColumns)
	wantSchema := duckdb.BuildSchemaWithSocrataID(target.Effective.Table, cols)

	diffs, err := duckdb.DiffSchema(wantSchema, w.DB, "main", target.Effective.Table)
	if err != nil {
		return failResult(target, "failed", fmt.Errorf("diff schema: %w", err)), nil
	}
	if len(diffs) > 0 {
		return failResult(target, "failed", schemaDriftError(target.Effective.Table, diffs)), nil
	}

	// Build $where = "<hwm> > 'TS'", AND-combined with target.Effective.Where if set.
	// Strict `>` (not `>=`): we store the exact max we observed, so any row updated
	// at the same millisecond as our HWM after we read it would be missed. Acceptable
	// in practice (Socrata timestamps are millisecond-resolution and bursts at the
	// exact same instant are rare). A future phase could switch to `>=` and rely on
	// PK-upsert idempotency to dedupe.
	whereClause := ""
	if state.HWMUpdatedAt != nil {
		whereClause = fmt.Sprintf("%s > '%s'", hwmCol, state.HWMUpdatedAt.UTC().Format("2006-01-02T15:04:05.000"))
	}
	if target.Effective.Where != "" {
		if whereClause != "" {
			whereClause = "(" + whereClause + ") AND (" + target.Effective.Where + ")"
		} else {
			whereClause = target.Effective.Where
		}
	}

	// Compound order: hwmCol then :id, for stable pagination across same-timestamp rows.
	orderBy := target.Effective.OrderBy
	if orderBy == "" {
		orderBy = hwmCol + ",:id"
	}

	var rowsWritten int64
	maxHWM := state.HWMUpdatedAt
	pageIdx := 0
	err = client.StreamRowsCtx(ctx, s.scheme(), s.Portal, target.ID,
		orderBy, whereClause, ":*,*", target.Effective.Limit,
		func(page []socrata.Row) error {
			if err := w.UpsertRows("main", wantSchema, page); err != nil {
				return err
			}
			for _, row := range page {
				if t := extractRowHWM(row, hwmCol); t != nil {
					if maxHWM == nil || t.After(*maxHWM) {
						maxHWM = t
					}
				}
			}
			rowsWritten += int64(len(page))
			pageIdx++
			if n := target.Effective.CheckpointEveryNPages; n > 0 && pageIdx%n == 0 {
				// Best-effort mid-stream HWM checkpoint. A failure here is logged
				// but not fatal: the page's data has already been committed
				// (UpsertRows above), and the next checkpoint or the final
				// HWM-on-success write will catch up.
				_ = w.UpsertDatasetState(duckdb.DatasetState{
					DatasetID:         target.ID,
					HWMUpdatedAt:      maxHWM,
					LastFullReplaceAt: state.LastFullReplaceAt,
					LastRunID:         s.RunID,
					HWMColumn:         hwmCol,
				})
			}
			prog.DatasetProgress(idx, total, target, rowsWritten)
			return nil
		},
	)
	if err != nil {
		if ctx.Err() != nil {
			return failResult(target, "aborted", ctx.Err()), nil
		}
		return failResult(target, "failed", err), nil
	}

	// Stream succeeded — persist new HWM (preserving last_full_replace_at).
	state.HWMUpdatedAt = maxHWM
	state.LastRunID = s.RunID
	if err := w.UpsertDatasetState(*state); err != nil {
		// Mirror bootstrap behavior: data is in main; surface as failed-state-write.
		result.Status = "ok"
		result.RowsWritten = rowsWritten
		result.FinishedAt = time.Now().UTC()
		result.Err = fmt.Errorf("write dataset_state: %w", err)
		return result, nil
	}

	result.Status = "ok"
	result.RowsWritten = rowsWritten
	result.FinishedAt = time.Now().UTC()
	return result, nil
}

func schemaDriftError(table string, diffs []duckdb.SchemaDiff) error {
	parts := make([]string, 0, len(diffs))
	for _, d := range diffs {
		switch d.Kind {
		case "added":
			parts = append(parts, fmt.Sprintf("%s added", d.Column))
		case "removed":
			parts = append(parts, fmt.Sprintf("%s removed", d.Column))
		case "retyped":
			parts = append(parts, fmt.Sprintf("%s retyped from %s to %s", d.Column, d.Have, d.Want))
		}
	}
	return fmt.Errorf("schema drift on %s: %s; set mode: full_replace in YAML to rebootstrap",
		table, strings.Join(parts, ", "))
}

func extractRowHWM(row socrata.Row, hwmCol string) *time.Time {
	v, ok := row[hwmCol]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return nil
	}
	for _, layout := range []string{
		"2006-01-02T15:04:05.000",
		"2006-01-02T15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// failResult is a small wrapper around fail() for the incremental strategy.
func failResult(target DatasetTarget, status string, err error) DatasetResult {
	res := DatasetResult{Target: target, StartedAt: time.Now().UTC()}
	res.Status = status
	res.Err = err
	res.FinishedAt = time.Now().UTC()
	return res
}
