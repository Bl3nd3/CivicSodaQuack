// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// SearchDatasetsArgs are the inputs to search_datasets.
type SearchDatasetsArgs struct {
	Query  string `json:"query" jsonschema:"substring to match against name and description; also matches tags case-insensitively"`
	Portal string `json:"portal,omitempty" jsonschema:"optional portal alias filter"`
	Limit  int    `json:"limit,omitempty" jsonschema:"max datasets to return (default 100, max 1000)"`
	Offset int    `json:"offset,omitempty" jsonschema:"skip this many matches; use with the returned total to page"`
}

// searchDatasetsHandler returns datasets whose name or description contain the
// query (case-insensitive substring) or whose tag list contains the query
// (case-insensitive exact match).
func searchDatasetsHandler(ctx context.Context, p *Pools, args SearchDatasetsArgs) ([]DatasetSummary, error) {
	if strings.TrimSpace(args.Query) == "" {
		return nil, fmt.Errorf("query must not be empty")
	}
	all, err := listDatasetsHandler(ctx, p, ListDatasetsArgs{Portal: args.Portal})
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(args.Query)
	out := make([]DatasetSummary, 0, len(all))
	for _, d := range all {
		matched := strings.Contains(strings.ToLower(d.Name), needle)
		if !matched {
			matched = matchesDescriptionOrTag(ctx, p, d, needle)
		}
		if matched {
			out = append(out, d)
		}
	}
	return out, nil
}

// matchesDescriptionOrTag fetches the catalog row's description and tags and
// applies the search rule. Pulled out so the listDatasets path stays cheap.
func matchesDescriptionOrTag(ctx context.Context, p *Pools, d DatasetSummary, needle string) bool {
	pool := p.Portals[d.Portal].DB
	var description string
	var tagsRaw any
	err := pool.QueryRowContext(ctx,
		`SELECT COALESCE(description, ''), tags
		 FROM _csq.catalog WHERE id = $1`, d.DatasetID).Scan(&description, &tagsRaw)
	if err != nil {
		return false
	}
	if strings.Contains(strings.ToLower(description), needle) {
		return true
	}
	// DuckDB returns JSON columns as native Go values; re-marshal to a string
	// then unmarshal as []string. See project_duckdb_json_scan.md.
	if tagsRaw != nil {
		b, err := json.Marshal(tagsRaw)
		if err == nil {
			var tags []string
			if json.Unmarshal(b, &tags) == nil {
				for _, t := range tags {
					if strings.EqualFold(t, needle) {
						return true
					}
				}
			}
		}
	}
	return false
}
