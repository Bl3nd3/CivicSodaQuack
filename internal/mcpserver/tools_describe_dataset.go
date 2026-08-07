// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DescribeDatasetArgs are the inputs to the describe_dataset MCP tool.
type DescribeDatasetArgs struct {
	DatasetID string `json:"dataset_id" jsonschema:"4x4 Socrata id"`
	Portal    string `json:"portal,omitempty" jsonschema:"required only when dataset_id appears in multiple portals"`
}

// DatasetDetail is the output of describe_dataset. Embeds DatasetSummary fields.
type DatasetDetail struct {
	DatasetSummary
	Description  string       `json:"description,omitempty"`
	Tags         []string     `json:"tags,omitempty"`
	Columns      []ColumnInfo `json:"columns"`
	LastSync     *SyncInfo    `json:"last_sync,omitempty"`
	HWMUpdatedAt *time.Time   `json:"hwm_updated_at,omitempty"`
}

// ColumnInfo names a single user-visible DuckDB column.
type ColumnInfo struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SyncInfo summarises the last successful sync_runs row.
type SyncInfo struct {
	RunID       string    `json:"run_id"`
	StartedAt   time.Time `json:"started_at"`
	Status      string    `json:"status"`
	RowsWritten int64     `json:"rows_written"`
	DurationMs  int64     `json:"duration_ms"`
}

// describeDatasetHandler returns the merged catalog + columns + last-sync detail
// for the requested dataset. Errors when the id is not found or when it is
// ambiguous across portals and no portal is specified.
func describeDatasetHandler(ctx context.Context, p *Pools, args DescribeDatasetArgs) (DatasetDetail, error) {
	if args.Portal != "" {
		if _, ok := p.Portals[args.Portal]; !ok {
			return DatasetDetail{}, fmt.Errorf("portal %q not attached", args.Portal)
		}
	}

	matches, err := findDatasetPortals(ctx, p, args.DatasetID, args.Portal)
	if err != nil {
		return DatasetDetail{}, err
	}
	if len(matches) == 0 {
		return DatasetDetail{}, fmt.Errorf("dataset %q not found", args.DatasetID)
	}
	if len(matches) > 1 {
		return DatasetDetail{}, fmt.Errorf("ambiguous dataset_id %q present in portals %s; pass portal=",
			args.DatasetID, strings.Join(matches, ", "))
	}
	alias := matches[0]
	return loadDetail(ctx, p, alias, args.DatasetID)
}

// findDatasetPortals returns the portals that contain the given dataset_id,
// optionally restricted to a single portal.
func findDatasetPortals(ctx context.Context, p *Pools, id, portal string) ([]string, error) {
	if portal != "" {
		exists, err := datasetExists(ctx, p.Portals[portal].DB, id)
		if err != nil {
			return nil, err
		}
		if exists {
			return []string{portal}, nil
		}
		return nil, nil
	}
	var out []string
	for _, alias := range sortedPortals(p) {
		exists, err := datasetExists(ctx, p.Portals[alias].DB, id)
		if err != nil {
			return nil, err
		}
		if exists {
			out = append(out, alias)
		}
	}
	return out, nil
}

func datasetExists(ctx context.Context, db *sql.DB, id string) (bool, error) {
	var n int
	err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM _csq.catalog WHERE id = $1`, id).Scan(&n)
	return n > 0, err
}

func sortedPortals(p *Pools) []string {
	out := make([]string, 0, len(p.Portals))
	for a := range p.Portals {
		out = append(out, a)
	}
	// stable order
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// loadDetail builds the full DatasetDetail for one (alias, id) pair.
func loadDetail(ctx context.Context, p *Pools, alias, id string) (DatasetDetail, error) {
	pool := p.Portals[alias].DB
	d := DatasetDetail{}
	d.DatasetID = id
	d.Portal = alias

	var name, description, category string
	var tagsAny any
	err := pool.QueryRowContext(ctx,
		`SELECT name, COALESCE(description, ''), COALESCE(category, ''), tags
		 FROM _csq.catalog WHERE id = $1`, id).Scan(&name, &description, &category, &tagsAny)
	if err != nil {
		return d, fmt.Errorf("read catalog: %w", err)
	}
	d.Name = name
	d.Description = description
	d.Category = category
	// DuckDB returns JSON columns as native Go types, not strings; re-marshal to decode.
	if tagsAny != nil {
		if b, err := json.Marshal(tagsAny); err == nil {
			var tags []string
			if err := json.Unmarshal(b, &tags); err == nil {
				d.Tags = tags
			}
		}
	}

	// last successful sync_runs row
	var runID, status string
	var startedAt time.Time
	var rowsWritten, duration sql.NullInt64
	var tableName sql.NullString
	err = pool.QueryRowContext(ctx,
		`SELECT run_id, table_name, started_at, status, rows_written, duration_ms
		 FROM _csq.sync_runs
		 WHERE dataset_id = $1 AND status = 'ok'
		 ORDER BY started_at DESC LIMIT 1`, id).Scan(
		&runID, &tableName, &startedAt, &status, &rowsWritten, &duration)
	if err != nil && err != sql.ErrNoRows {
		return d, fmt.Errorf("read sync_runs: %w", err)
	}
	if err == nil {
		d.LastSync = &SyncInfo{
			RunID:       runID,
			StartedAt:   startedAt,
			Status:      status,
			RowsWritten: rowsWritten.Int64,
			DurationMs:  duration.Int64,
		}
		if rowsWritten.Valid {
			n := rowsWritten.Int64
			d.RowCount = &n
		}
		if tableName.Valid && tableName.String != "" {
			t := tableName.String
			d.TableName = &t
			d.Synced = true
		}
	}

	// HWM
	var hwm sql.NullTime
	err = pool.QueryRowContext(ctx,
		`SELECT hwm_updated_at FROM _csq.dataset_state WHERE dataset_id = $1`, id).Scan(&hwm)
	if err != nil && err != sql.ErrNoRows {
		return d, fmt.Errorf("read dataset_state: %w", err)
	}
	if hwm.Valid {
		t := hwm.Time
		d.HWMUpdatedAt = &t
	}

	// Columns from information_schema, excluding socrata_id. An unsynced
	// dataset has no table to describe, so report an empty column list rather
	// than probing a name that was never created.
	d.Columns = []ColumnInfo{}
	if d.TableName != nil {
		cols, err := readColumns(ctx, pool, *d.TableName)
		if err != nil {
			return d, err
		}
		d.Columns = cols
	}

	return d, nil
}

func readColumns(ctx context.Context, db *sql.DB, table string) ([]ColumnInfo, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT column_name, data_type FROM information_schema.columns
		 WHERE table_schema = 'main' AND table_name = $1
		 ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("read columns for %s: %w", table, err)
	}
	defer rows.Close()
	var out []ColumnInfo
	for rows.Next() {
		var name, typ string
		if err := rows.Scan(&name, &typ); err != nil {
			return nil, err
		}
		if name == "socrata_id" {
			continue
		}
		out = append(out, ColumnInfo{Name: name, Type: typ})
	}
	return out, rows.Err()
}
