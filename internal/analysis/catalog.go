// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"fmt"
	"strings"
)

// CatalogEntry is one dataset a portal publishes, plus whether it is held
// locally.
type CatalogEntry struct {
	Portal      string `json:"portal"`
	Alias       string `json:"alias"`
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Category    string `json:"category"`
	// RowCount is what the portal reports upstream.
	RowCount *int64 `json:"row_count"`
	// UpdatedAt is the portal's data_updated_at. It moves when a portal
	// republishes unchanged rows, so it is an upper bound on staleness and
	// never evidence of freshness.
	UpdatedAt string `json:"updated_at"`
	// Synced reports whether this dataset has a local table with rows.
	Synced    bool   `json:"synced"`
	LocalRows int64  `json:"local_rows"`
	Table     string `json:"table"`
}

// CatalogPage is one page of search results.
type CatalogPage struct {
	Entries []CatalogEntry `json:"entries"`
	Total   int64          `json:"total"`
	Offset  int            `json:"offset"`
	Limit   int            `json:"limit"`
}

// SearchCatalog lists datasets across attached portals, optionally filtered by
// a free-text query and a category.
//
// The local-vs-upstream distinction is carried on every row on purpose. A
// catalog listing that does not say what you actually hold invites someone to
// query a dataset that was never synced and read the empty result as a finding.
func (s *Session) SearchCatalog(ctx context.Context, q, category string, limit, offset int) (*CatalogPage, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}

	counts, err := s.tableRowCounts(ctx)
	if err != nil {
		return nil, err
	}
	synced, err := s.syncedTables(ctx)
	if err != nil {
		return nil, err
	}

	var wheres []string
	if q != "" {
		// Case-insensitive substring over the fields a person would search by.
		needle := quoteLiteral(strings.ToLower(q))
		wheres = append(wheres, fmt.Sprintf(
			`(lower(name) LIKE '%%%s%%' OR lower(coalesce(description,'')) LIKE '%%%s%%' OR lower(id) LIKE '%%%s%%')`,
			needle, needle, needle))
	}
	if category != "" {
		wheres = append(wheres, fmt.Sprintf(`category = '%s'`, quoteLiteral(category)))
	}
	filter := ""
	if len(wheres) > 0 {
		filter = " WHERE " + strings.Join(wheres, " AND ")
	}

	var unions []string
	for _, p := range s.portals {
		unions = append(unions, fmt.Sprintf(
			`SELECT '%s' AS portal, '%s' AS alias, id, name, coalesce(description,'') AS description,
			        coalesce(category,'') AS category, row_count, updated_at
			 FROM %s._csq.catalog%s`,
			quoteLiteral(p.Portal), quoteLiteral(p.Alias), p.Alias, filter))
	}
	base := strings.Join(unions, "\nUNION ALL\n")

	var total int64
	if err := s.host.QueryRowContext(ctx,
		fmt.Sprintf(`SELECT COUNT(*) FROM (%s)`, base)).Scan(&total); err != nil {
		return nil, fmt.Errorf("count catalog: %w", err)
	}

	listSQL := fmt.Sprintf(
		`SELECT * FROM (%s) ORDER BY name LIMIT %d OFFSET %d`, base, limit, offset)
	rows, err := s.host.QueryContext(ctx, listSQL)
	if err != nil {
		return nil, fmt.Errorf("list catalog: %w", err)
	}
	defer rows.Close()

	page := &CatalogPage{Entries: []CatalogEntry{}, Total: total, Offset: offset, Limit: limit}
	for rows.Next() {
		var e CatalogEntry
		var updated *string
		if err := rows.Scan(&e.Portal, &e.Alias, &e.ID, &e.Name,
			&e.Description, &e.Category, &e.RowCount, &updated); err != nil {
			return nil, err
		}
		if updated != nil {
			e.UpdatedAt = *updated
		}
		if tbl, ok := synced[e.Alias+"."+e.ID]; ok {
			e.Table = tbl
			if n, ok := counts[e.Alias+"."+tbl]; ok && n > 0 {
				e.Synced, e.LocalRows = true, n
			}
		}
		page.Entries = append(page.Entries, e)
	}
	return page, rows.Err()
}

// Categories lists the distinct categories across attached portals, with counts.
func (s *Session) Categories(ctx context.Context) ([]Category, error) {
	var unions []string
	for _, p := range s.portals {
		unions = append(unions, fmt.Sprintf(
			`SELECT coalesce(category,'') AS category FROM %s._csq.catalog`, p.Alias))
	}
	rows, err := s.host.QueryContext(ctx, fmt.Sprintf(
		`SELECT category, COUNT(*) AS n FROM (%s) WHERE category <> ''
		 GROUP BY category ORDER BY n DESC`, strings.Join(unions, " UNION ALL ")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []Category{}
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.Name, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// Category is one catalog category and how many datasets carry it.
type Category struct {
	Name  string `json:"name"`
	Count int64  `json:"count"`
}

// syncedTables maps "<alias>.<dataset id>" → local table name, from the sync
// history rather than from a guess at the table name. csq deliberately stopped
// synthesising table names for unsynced datasets, so the only honest source is
// a run that actually succeeded.
func (s *Session) syncedTables(ctx context.Context) (map[string]string, error) {
	out := map[string]string{}
	for _, p := range s.portals {
		rows, err := s.host.QueryContext(ctx, fmt.Sprintf(
			`SELECT DISTINCT dataset_id, table_name FROM %s._csq.sync_runs
			 WHERE status = 'ok' AND table_name IS NOT NULL`, p.Alias))
		if err != nil {
			return nil, fmt.Errorf("sync history %s: %w", p.Alias, err)
		}
		for rows.Next() {
			var id, tbl string
			if err := rows.Scan(&id, &tbl); err != nil {
				rows.Close()
				return nil, err
			}
			out[p.Alias+"."+id] = tbl
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
