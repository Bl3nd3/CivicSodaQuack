// Copyright (c) 2026 Neomantra Corp

package sync

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fakeDataset is one dataset the fake server will serve.
type fakeDataset struct {
	ID      string
	Name    string
	Columns []map[string]string // each has fieldName + dataTypeName
	Rows    []map[string]any
	// FailAtOffset: if > 0, return 500 when $offset >= FailAtOffset
	FailAtOffset int
}

func newFakeSocrata(t *testing.T, datasets ...fakeDataset) *httptest.Server {
	t.Helper()
	byID := map[string]fakeDataset{}
	for _, d := range datasets {
		byID[d.ID] = d
	}
	mux := http.NewServeMux()

	// /api/catalog/v1
	mux.HandleFunc("/api/catalog/v1", func(w http.ResponseWriter, r *http.Request) {
		results := make([]map[string]any, 0, len(datasets))
		for _, d := range datasets {
			results = append(results, map[string]any{
				"resource": map[string]any{
					"id":            d.ID,
					"name":          d.Name,
					"description":   "",
					"rowsUpdatedAt": "2024-01-15T00:00:00.000",
				},
				"classification": map[string]any{
					"domain_category": "Test",
					"domain_tags":     []string{"test"},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":       results,
			"resultSetSize": len(datasets),
		})
	})

	// /api/views/{id}.json
	mux.HandleFunc("/api/views/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/views/"), ".json")
		d, ok := byID[id]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		cols := make([]map[string]string, 0, len(d.Columns))
		cols = append(cols, d.Columns...)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": d.ID, "name": d.Name, "columns": cols,
		})
	})

	// /resource/{id}.json
	mux.HandleFunc("/resource/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/resource/"), ".json")
		d, ok := byID[id]
		if !ok {
			http.Error(w, "not found", 404)
			return
		}
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("$offset"))
		limit, _ := strconv.Atoi(q.Get("$limit"))
		selectClause := q.Get("$select")
		whereClause := q.Get("$where")
		includeSystem := selectClause == ":*,*"

		// csq pages by seeking on :id rather than by $offset (see
		// StreamRowsCtx), so the seek term is split off here and the row it
		// names becomes the effective start of the page. Modelling this is
		// what keeps FailAtOffset meaningful: without it the fake would serve
		// page one for ever and no paging test would exercise anything.
		userWhere, seekID := splitKeysetSeek(whereClause)
		whereClause = userWhere

		// Apply $where filter if present
		filtered := d.Rows
		if whereClause != "" {
			cutoff, ok := parseSimpleGreaterThan(whereClause)
			if !ok {
				http.Error(w, "fake portal: unsupported $where: "+whereClause, 400)
				return
			}
			filtered = filtered[:0:0]
			for _, row := range d.Rows {
				ts, ok := row[":updated_at"].(string)
				if !ok {
					continue
				}
				if ts > cutoff { // string compare on ISO-8601 is order-preserving
					filtered = append(filtered, row)
				}
			}
		}

		// A seek overrides $offset: resume just past the named row.
		if seekID != "" {
			offset = len(filtered) // unknown id => nothing left to serve
			for i, row := range filtered {
				if row[":id"] == seekID {
					offset = i + 1
					break
				}
			}
		}
		if d.FailAtOffset > 0 && offset >= d.FailAtOffset {
			http.Error(w, "synthetic failure", 500)
			return
		}

		// Page slice
		end := offset + limit
		if end > len(filtered) {
			end = len(filtered)
		}
		if offset > len(filtered) {
			offset = len(filtered)
		}
		page := filtered[offset:end]

		// Strip or include system fields per $select
		out := make([]map[string]any, 0, len(page))
		for i, row := range page {
			cleaned := map[string]any{}
			for k, v := range row {
				if strings.HasPrefix(k, ":") {
					if includeSystem {
						cleaned[k] = v
					}
					continue
				}
				cleaned[k] = v
			}
			if includeSystem {
				if _, has := cleaned[":id"]; !has {
					cleaned[":id"] = fmt.Sprintf("%s-row-%d", d.ID, offset+i)
				}
			}
			out = append(out, cleaned)
		}
		_ = json.NewEncoder(w).Encode(out)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// fakeHost returns the host:port of an httptest.Server (strips "http://").
func fakeHost(srv *httptest.Server) string {
	return strings.TrimPrefix(srv.URL, "http://")
}

// makeRows is a small helper to generate n rows of the given shape.
func makeRows(n int, mk func(i int) map[string]any) []map[string]any {
	out := make([]map[string]any, n)
	for i := 0; i < n; i++ {
		out[i] = mk(i)
	}
	return out
}

// parseSimpleGreaterThan recognises the single Phase 2 predicate shape:
//
//	<col> > '<value>'   (with surrounding whitespace ignored)
//
// Returns the value if matched. Anything else returns ok=false.
// splitKeysetSeek separates a keyset seek from the caller's own predicate.
// csq emits either ":id > 'x'" or "(<user where>) AND :id > 'x'".
func splitKeysetSeek(where string) (userWhere, seekID string) {
	re := regexp.MustCompile(`^\s*(?:\((.*)\)\s+AND\s+)?:id\s*>\s*'([^']*)'\s*$`)
	m := re.FindStringSubmatch(where)
	if m == nil {
		return where, ""
	}
	return m[1], m[2]
}

func parseSimpleGreaterThan(where string) (string, bool) {
	re := regexp.MustCompile(`^\s*[A-Za-z_:][A-Za-z0-9_:]*\s*>\s*'([^']*)'\s*$`)
	m := re.FindStringSubmatch(where)
	if m == nil {
		return "", false
	}
	return m[1], true
}
