// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// keysetServer serves rows named row-0000.. in :id order, honouring either
// $offset or a ":id > 'x'" seek, and records the query it was asked for.
func keysetServer(t *testing.T, total int) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		seen = append(seen, r.URL.RawQuery)

		start := 0
		if off := q.Get("$offset"); off != "" {
			fmt.Sscanf(off, "%d", &start)
		}
		if where := q.Get("$where"); where != "" {
			// seek form: ... :id > 'row-000123'
			if i := strings.LastIndex(where, ":id > '"); i >= 0 {
				var n int
				fmt.Sscanf(where[i+len(":id > 'row-"):], "%06d", &n)
				start = n + 1
			}
		}
		limit := 2
		fmt.Sscanf(q.Get("$limit"), "%d", &limit)

		rows := []Row{}
		for i := start; i < start+limit && i < total; i++ {
			rows = append(rows, Row{":id": fmt.Sprintf("row-%06d", i), "n": i})
		}
		_ = json.NewEncoder(w).Encode(rows)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// Keyset paging is the difference between a sync that finishes and one that
// does not: $offset makes the portal scan and discard everything before the
// offset, and NYC stopped serving pages past ~3.1M rows entirely. When the
// stream is ordered by :id, csq must seek rather than offset.
func TestStreamRows_UsesKeysetWhenOrderedByID(t *testing.T) {
	srv, seen := keysetServer(t, 7)
	c := &Client{BatchSize: 2}

	var got []int
	err := c.StreamRowsCtx(context.Background(), "http", strings.TrimPrefix(srv.URL, "http://"),
		"abcd-1234", ":id", "", ":*,*", 0, func(page []Row) error {
			for _, r := range page {
				got = append(got, int(r["n"].(float64)))
			}
			return nil
		})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if len(got) != 7 {
		t.Fatalf("got %d rows, want 7 (%v)", len(got), got)
	}
	for i, n := range got {
		if n != i {
			t.Fatalf("row %d out of order: %v", i, got)
		}
	}
	// The first request has no key yet; every later one must seek, and none
	// may fall back to $offset.
	for i, q := range *seen {
		if i > 0 && !strings.Contains(q, "%3Aid+%3E") {
			t.Errorf("request %d did not seek on :id: %s", i, q)
		}
		if strings.Contains(q, "offset") {
			t.Errorf("request %d used $offset despite :id ordering: %s", i, q)
		}
	}
}

// A user's own $where must survive: the seek is ANDed onto it, not replacing
// it. Losing the caller's filter would silently widen the sync.
func TestStreamRows_KeysetPreservesUserWhere(t *testing.T) {
	srv, seen := keysetServer(t, 5)
	c := &Client{BatchSize: 2}
	err := c.StreamRowsCtx(context.Background(), "http", strings.TrimPrefix(srv.URL, "http://"),
		"abcd-1234", ":id", "date >= '2015-01-01'", ":*,*", 0, func(page []Row) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	for i, q := range *seen {
		if !strings.Contains(q, "date+%3E%3D") {
			t.Errorf("request %d dropped the caller's where: %s", i, q)
		}
	}
}

// Ordering by anything else has no usable key, so the old offset path stands.
func TestStreamRows_FallsBackToOffsetWithoutIDOrder(t *testing.T) {
	srv, seen := keysetServer(t, 5)
	c := &Client{BatchSize: 2}
	err := c.StreamRowsCtx(context.Background(), "http", strings.TrimPrefix(srv.URL, "http://"),
		"abcd-1234", "date", "", ":*,*", 0, func(page []Row) error { return nil })
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if !strings.Contains((*seen)[0], "offset") {
		t.Errorf("expected $offset paging when not ordered by :id: %s", (*seen)[0])
	}
}
