// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// ListDatasetsArgs are the inputs to the list_datasets MCP tool.
type ListDatasetsArgs struct {
	Portal   string `json:"portal,omitempty" jsonschema:"optional portal alias filter"`
	Category string `json:"category,omitempty" jsonschema:"optional case-insensitive substring on category"`
	Limit    int    `json:"limit,omitempty" jsonschema:"max datasets to return (default 100, max 1000)"`
	Offset   int    `json:"offset,omitempty" jsonschema:"skip this many datasets; use with the returned total to page"`
}

// DatasetSummary is one row in list_datasets / search_datasets results.
//
// A catalog entry is not a queryable table: a portal DuckDB can list thousands
// of datasets it has never synced. Synced says which is which, and TableName is
// null unless a successful sync produced a real table — never a plausible name
// derived from the id, which invites a consumer to query something that does
// not exist. RowCount is likewise emitted as null rather than omitted, so its
// absence is visible instead of inferred.
type DatasetSummary struct {
	DatasetID string  `json:"dataset_id"`
	Portal    string  `json:"portal"`
	Name      string  `json:"name"`
	Category  string  `json:"category,omitempty"`
	Synced    bool    `json:"synced"`
	TableName *string `json:"table_name"`
	RowCount  *int64  `json:"row_count"`
}

// listDatasetsHandler enumerates datasets across the requested portal (or all
// portals) with optional category substring filter.
func listDatasetsHandler(ctx context.Context, p *Pools, args ListDatasetsArgs) ([]DatasetSummary, error) {
	aliases := selectPortals(p, args.Portal)
	out := make([]DatasetSummary, 0, len(aliases)*4)

	for _, alias := range aliases {
		rows, err := queryDatasetsForPortal(ctx, p.Portals[alias].DB, alias)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", alias, err)
		}
		out = append(out, rows...)
	}

	if args.Category != "" {
		needle := strings.ToLower(args.Category)
		filtered := out[:0]
		for _, d := range out {
			if strings.Contains(strings.ToLower(d.Category), needle) {
				filtered = append(filtered, d)
			}
		}
		out = filtered
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Portal != out[j].Portal {
			return out[i].Portal < out[j].Portal
		}
		return out[i].DatasetID < out[j].DatasetID
	})
	return out, nil
}

// selectPortals returns the alias list to scan: a single alias if requested
// (and present), or all aliases otherwise. Unknown alias yields empty result.
func selectPortals(p *Pools, requested string) []string {
	if requested != "" {
		if _, ok := p.Portals[requested]; !ok {
			return nil
		}
		return []string{requested}
	}
	out := make([]string, 0, len(p.Portals))
	for a := range p.Portals {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// queryDatasetsForPortal returns all dataset summaries from one portal pool.
// table_name and row_count come from the most recent status='ok' sync_runs row.
// With no successful sync both stay null and synced is false.
func queryDatasetsForPortal(ctx context.Context, db *sql.DB, alias string) ([]DatasetSummary, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT c.id, c.name, COALESCE(c.category, ''), s.table_name, s.rows_written
		FROM _csq.catalog c
		LEFT JOIN (
			SELECT dataset_id,
			       FIRST(table_name ORDER BY started_at DESC) AS table_name,
			       FIRST(rows_written ORDER BY started_at DESC) AS rows_written
			FROM _csq.sync_runs
			WHERE status = 'ok'
			GROUP BY dataset_id
		) s ON s.dataset_id = c.id
		ORDER BY c.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DatasetSummary
	for rows.Next() {
		var id, name, category string
		var table sql.NullString
		var rowCount sql.NullInt64
		if err := rows.Scan(&id, &name, &category, &table, &rowCount); err != nil {
			return nil, err
		}
		summary := DatasetSummary{
			DatasetID: id,
			Portal:    alias,
			Name:      name,
			Category:  category,
		}
		// Only a successful sync yields a real table. Anything else stays null:
		// a synthesised name here is indistinguishable from a real one to the
		// consumer, and fails at query time instead of at discovery time.
		if table.Valid && table.String != "" {
			t := table.String
			summary.TableName = &t
			summary.Synced = true
		}
		if rowCount.Valid {
			n := rowCount.Int64
			summary.RowCount = &n
		}
		out = append(out, summary)
	}
	return out, rows.Err()
}
