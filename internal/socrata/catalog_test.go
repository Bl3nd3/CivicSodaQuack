// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestFetchCatalog_Paginates(t *testing.T) {
	total := 5
	pageSize := 2
	mux := http.NewServeMux()
	mux.HandleFunc("/api/catalog/v1", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		limit, _ := strconv.Atoi(q.Get("limit"))
		results := []map[string]any{}
		for i := offset; i < offset+limit && i < total; i++ {
			results = append(results, map[string]any{
				"resource": map[string]any{
					"id":          "abcd-000" + strconv.Itoa(i),
					"name":        "Dataset " + strconv.Itoa(i),
					"description": "desc",
					// Mirrors what /api/catalog/v1 actually returns: no
					// rowsUpdatedAt, and updatedAt later than data_updated_at
					// because it folds in metadata edits.
					"data_updated_at":     "2024-01-15T00:00:00.000Z",
					"updatedAt":           "2025-06-30T00:00:00.000Z",
					"metadata_updated_at": "2025-06-30T00:00:00.000Z",
				},
				"classification": map[string]any{
					"domain_category": "Public Safety",
					"domain_tags":     []string{"crime"},
				},
			})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"results":       results,
			"resultSetSize": total,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	host := strings.TrimPrefix(srv.URL, "http://")
	c := &Client{BatchSize: pageSize}
	entries, err := c.fetchCatalogScheme(host, "http")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(entries) != total {
		t.Fatalf("got %d entries, want %d", len(entries), total)
	}
	if entries[0].ID != "abcd-0000" {
		t.Errorf("first id: got %q, want abcd-0000", entries[0].ID)
	}
	if entries[0].Category != "Public Safety" {
		t.Errorf("category: got %q, want %q", entries[0].Category, "Public Safety")
	}
	if len(entries[0].Tags) != 1 || entries[0].Tags[0] != "crime" {
		t.Errorf("tags: got %v", entries[0].Tags)
	}
	// data_updated_at wins over updatedAt. Reading updatedAt here would report
	// 2025 for a dataset whose data has not changed since 2024, which is how a
	// frozen dataset comes to look current.
	if entries[0].UpdatedAt == nil {
		t.Fatal("UpdatedAt: got nil, want non-nil")
	}
	if got := entries[0].UpdatedAt.Year(); got != 2024 {
		t.Errorf("UpdatedAt year: got %d, want 2024 (data_updated_at, not updatedAt)", got)
	}
	if len(entries[0].Raw) == 0 {
		t.Error("Raw: expected non-empty JSON")
	}
}

// TestCatalogTimestampPrecedence pins the field order directly, so a future
// edit that reorders the candidates fails here rather than silently reporting
// metadata edits as data freshness.
func TestCatalogTimestampPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		data       string
		updated    string
		rowsUpdate string
		wantYear   int
		wantNil    bool
	}{
		{name: "prefers data_updated_at", data: "2024-01-15T00:00:00.000Z",
			updated: "2025-06-30T00:00:00.000Z", wantYear: 2024},
		{name: "falls back to updatedAt", updated: "2025-06-30T00:00:00.000Z", wantYear: 2025},
		{name: "falls back to rowsUpdatedAt (views payload)",
			rowsUpdate: "2023-03-01T00:00:00.000", wantYear: 2023},
		{name: "nil when all empty", wantNil: true},
		{name: "skips unparseable and continues", data: "not-a-date",
			updated: "2025-06-30T00:00:00.000Z", wantYear: 2025},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseCatalogTimestamp(tc.data, tc.updated, tc.rowsUpdate)
			if tc.wantNil {
				if got != nil {
					t.Fatalf("got %v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("got nil, want year %d", tc.wantYear)
			}
			if got.Year() != tc.wantYear {
				t.Errorf("got year %d, want %d", got.Year(), tc.wantYear)
			}
		})
	}
}

// TestFetchCatalog_LiveResponseShape guards against the fixture drift that hid
// this bug: a hand-written fixture asserted a key (`rowsUpdatedAt`) the real
// /api/catalog/v1 never returns, so the parser passed its test while producing
// a NULL updated_at for every dataset on every portal. This replays a trimmed
// real response instead.
func TestFetchCatalog_LiveResponseShape(t *testing.T) {
	const realPage = `{
	  "results": [
	    {
	      "resource": {
	        "name": "Building Permits",
	        "id": "ydr8-5enu",
	        "description": "Building permits issued by the City of Chicago.",
	        "createdAt": "2011-09-30T12:00:08.000Z",
	        "data_updated_at": "2026-08-06T12:19:20.000Z",
	        "metadata_updated_at": "2026-05-29T18:56:10.000Z",
	        "updatedAt": "2026-08-06T12:19:20.000Z"
	      },
	      "classification": {"domain_category": "Buildings", "domain_tags": ["permits"]}
	    },
	    {
	      "resource": {
	        "name": "Lobbyist Registry 2010",
	        "id": "2ft4-4uik",
	        "description": "Frozen dataset whose metadata was edited years later.",
	        "createdAt": "2011-08-21T02:47:57.000Z",
	        "data_updated_at": "2011-08-21T02:47:57.000Z",
	        "metadata_updated_at": "2015-10-23T20:57:41.000Z",
	        "updatedAt": "2015-10-23T20:57:41.000Z"
	      },
	      "classification": {"domain_category": "Ethics", "domain_tags": []}
	    }
	  ],
	  "resultSetSize": 2
	}`

	mux := http.NewServeMux()
	mux.HandleFunc("/api/catalog/v1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(realPage))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := &Client{BatchSize: 100}
	entries, err := c.fetchCatalogScheme(strings.TrimPrefix(srv.URL, "http://"), "http")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.UpdatedAt == nil {
			t.Errorf("%s: UpdatedAt is nil against a real response shape", e.ID)
		}
	}
	// The frozen dataset must report 2011, not the 2015 metadata edit.
	if got := entries[1].UpdatedAt; got != nil && got.Year() != 2011 {
		t.Errorf("%s: UpdatedAt year %d, want 2011 — metadata edits must not "+
			"make a frozen dataset look current", entries[1].ID, got.Year())
	}
}
