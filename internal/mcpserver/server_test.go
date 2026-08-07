// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
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
