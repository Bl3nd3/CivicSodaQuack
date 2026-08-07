// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServe_RegistersFourTools(t *testing.T) {
	// Construct a Server in isolation (no transport), to verify all four
	// tools register without panicking and the schemas resolve.
	dir := t.TempDir()
	path := seedFixtureDB(t, dir, "test.duckdb",
		FixtureDataset{ID: "aaaa-0001", Name: "X"})
	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	srv, err := buildServer(pools, nil)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	if srv == nil {
		t.Fatal("nil server")
	}
}

func TestServe_HTTPSmoke(t *testing.T) {
	dir := t.TempDir()
	path := seedFixtureDB(t, dir, "test.duckdb",
		FixtureDataset{ID: "aaaa-0001", Name: "X"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Run Serve on an in-process httptest server.
	pools, err := OpenPools([]DBSpec{{Alias: "test", Path: path}}, nil)
	if err != nil {
		t.Fatalf("OpenPools: %v", err)
	}
	defer pools.Close()

	srv, err := buildServer(pools, nil)
	if err != nil {
		t.Fatalf("buildServer: %v", err)
	}
	handler := newHTTPHandler(srv)
	httpsrv := httptest.NewServer(handler)
	defer httpsrv.Close()

	// Send a tools/list JSON-RPC request and assert four tool names appear.
	body := `{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`
	req, _ := http.NewRequestWithContext(ctx, "POST", httpsrv.URL, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}

	buf := make([]byte, 64*1024)
	n, _ := resp.Body.Read(buf)
	got := string(buf[:n])
	for _, name := range []string{"list_datasets", "describe_dataset", "search_datasets", "query_sql"} {
		if !strings.Contains(got, name) {
			t.Errorf("response missing tool %q:\n%s", name, got)
		}
	}
}

func TestPaginate_DefaultsToOnePage(t *testing.T) {
	all := make([]DatasetSummary, 250)
	got := paginate(all, 0, 0)
	if got.Limit != DefaultDatasetLimit || got.Returned != DefaultDatasetLimit {
		t.Errorf("default page: limit=%d returned=%d, want %d",
			got.Limit, got.Returned, DefaultDatasetLimit)
	}
	if got.Total != 250 {
		t.Errorf("total: got %d, want 250", got.Total)
	}
}

func TestPaginate_OffsetAndClamping(t *testing.T) {
	all := make([]DatasetSummary, 10)
	for i := range all {
		all[i].DatasetID = string(rune('a' + i))
	}
	cases := []struct {
		name                  string
		offset, limit         int
		wantReturned, wantOff int
		wantFirst             string
	}{
		{"mid page", 3, 2, 2, 3, "d"},
		{"limit past end", 8, 100, 2, 8, "i"},
		{"offset past end", 50, 5, 0, 10, ""},
		{"negative offset", -5, 3, 3, 0, "a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := paginate(all, tc.offset, tc.limit)
			if got.Returned != tc.wantReturned {
				t.Errorf("returned: got %d, want %d", got.Returned, tc.wantReturned)
			}
			if got.Offset != tc.wantOff {
				t.Errorf("offset: got %d, want %d", got.Offset, tc.wantOff)
			}
			if got.Total != 10 {
				t.Errorf("total: got %d, want 10", got.Total)
			}
			if tc.wantFirst != "" && got.Datasets[0].DatasetID != tc.wantFirst {
				t.Errorf("first: got %q, want %q", got.Datasets[0].DatasetID, tc.wantFirst)
			}
		})
	}
}

// TestPaginate_ExceedingMaxIsClamped keeps a caller asking for everything from
// reproducing the unbounded response that made this tool unusable.
func TestPaginate_ExceedingMaxIsClamped(t *testing.T) {
	all := make([]DatasetSummary, MaxDatasetLimit+500)
	got := paginate(all, 0, 100000)
	if got.Limit != MaxDatasetLimit || got.Returned != MaxDatasetLimit {
		t.Errorf("limit=%d returned=%d, want both %d", got.Limit, got.Returned, MaxDatasetLimit)
	}
}

// TestPaginate_EmptyPageMarshalsAsArray guards against a null datasets field.
func TestPaginate_EmptyPageMarshalsAsArray(t *testing.T) {
	got := paginate(nil, 0, 10)
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(b), `"datasets":[]`) {
		t.Errorf("empty page should marshal as [], got: %s", b)
	}
}
