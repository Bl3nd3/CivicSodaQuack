// Copyright (c) 2026 Neomantra Corp

package sync

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
)

// WriteStrategy owns how one dataset's rows land in DuckDB.
type WriteStrategy interface {
	Sync(
		ctx context.Context,
		target DatasetTarget,
		client *socrata.Client,
		w *duckdb.Writer,
		prog ProgressReporter,
		idx, total int,
	) (DatasetResult, error)
}

// FullReplaceStrategy writes into _csq_staging.<table>_<runID>, then swaps
// the staging table into place as main.<table>.
// Scheme defaults to "https" when empty (tests override with "http").
type FullReplaceStrategy struct {
	Portal string
	Scheme string
	RunID  string
}

func (s *FullReplaceStrategy) scheme() string {
	if s.Scheme != "" {
		return s.Scheme
	}
	return "https"
}

func (s *FullReplaceStrategy) Sync(
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

	// 1. Fetch metadata from the scheme we were configured with.
	meta, err := fetchMetadata(ctx, client, s.scheme(), s.Portal, target.ID)
	if err != nil {
		return fail(result, "failed", fmt.Errorf("fetch metadata: %w", err)), nil
	}

	// 2. Filter columns honoring SkipColumns.
	cols := filterColumns(meta.Columns, target.Effective.SkipColumns)

	// 3. Build schema keyed to the staging table name.
	stagingName := target.Effective.Table + "_" + s.RunID
	schema := duckdb.BuildSchema(stagingName, cols)

	// 4. Create the staging table in _csq_staging.
	if _, err := w.DB.ExecContext(ctx, schema.CreateTableSQLIn("_csq_staging")); err != nil {
		return fail(result, "failed", fmt.Errorf("create staging: %w", err)), nil
	}

	// 5. Stream rows, inserting into _csq_staging.<stagingName>.
	var rowsWritten int64
	err = streamInto(ctx, client, s.scheme(), s.Portal, target, w, schema, &rowsWritten, prog, idx, total)
	if err != nil {
		// The partial staging table is unreachable from here on: the next run
		// stages under a fresh run ID, so leaving it only grows the file.
		_ = w.DropStaging(stagingName)
		if ctx.Err() != nil {
			return fail(result, "aborted", ctx.Err()), nil
		}
		return fail(result, "failed", err), nil
	}

	// 6. Swap into main.
	if err := w.SwapIn(stagingName, target.Effective.Table, ""); err != nil {
		_ = w.DropStaging(stagingName)
		return fail(result, "failed", fmt.Errorf("swap: %w", err)), nil
	}

	result.Status = "ok"
	result.RowsWritten = rowsWritten
	result.FinishedAt = time.Now().UTC()
	return result, nil
}

func fail(res DatasetResult, status string, err error) DatasetResult {
	res.Status = status
	res.Err = err
	res.FinishedAt = time.Now().UTC()
	return res
}

func fetchMetadata(ctx context.Context, c *socrata.Client, scheme, portal, id string) (*socrata.DatasetMetadata, error) {
	u := &url.URL{Scheme: scheme, Host: portal, Path: "/api/views/" + id + ".json"}
	return c.FetchMetadataURL(ctx, u.String())
}

func filterColumns(cols []socrata.Column, skip []string) []socrata.Column {
	if len(skip) == 0 {
		return cols
	}
	skipSet := make(map[string]struct{}, len(skip))
	for _, s := range skip {
		skipSet[s] = struct{}{}
	}
	out := cols[:0:0]
	for _, c := range cols {
		if _, drop := skipSet[c.FieldName]; drop {
			continue
		}
		out = append(out, c)
	}
	return out
}

func streamInto(
	ctx context.Context,
	client *socrata.Client,
	scheme, portal string,
	target DatasetTarget,
	w *duckdb.Writer,
	schema duckdb.TableSchema,
	rowsWritten *int64,
	prog ProgressReporter,
	idx, total int,
) error {
	return client.StreamRowsCtx(ctx, scheme, portal, target.ID,
		target.Effective.OrderBy, target.Effective.Where, "",
		target.Effective.Limit,
		func(page []socrata.Row) error {
			if err := w.InsertRowsInto("_csq_staging", schema, page); err != nil {
				return err
			}
			*rowsWritten += int64(len(page))
			prog.DatasetProgress(idx, total, target, *rowsWritten)
			return nil
		},
	)
}
