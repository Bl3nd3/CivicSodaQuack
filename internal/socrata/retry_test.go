// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fastClient retries hard but sleeps negligibly, so tests exercise the retry
// logic without paying its backoff.
func fastClient() *Client {
	return &Client{BatchSize: 2, MaxRetries: 5, RetryWait: time.Millisecond, MaxRetryWait: 2 * time.Millisecond}
}

// A connection reset part-way through the response body is the failure that
// killed the Chicago and New York ranking syncs: the status line said 200, so
// the old client saw only "decode page: read tcp ...: connection reset" and
// returned without retrying. The body must be read inside the retry loop for
// this to be recoverable.
func TestGetPage_RetriesTruncatedBody(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Content-Length", "512") // promise more than we send
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`[{"a":"1"}`)) // truncated, then hang up
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			panic(http.ErrAbortHandler) // kill the connection mid-body
		}
		_, _ = w.Write([]byte(`[{"a":"1"},{"a":"2"}]`))
	}))
	defer srv.Close()

	page, err := fastClient().getPageCtx(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("expected recovery from a mid-body reset, got %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("want 2 rows after retry, got %d", len(page))
	}
	if got := atomic.LoadInt32(&attempts); got != 2 {
		t.Fatalf("want 2 attempts, got %d", got)
	}
}

// Malformed JSON that arrived intact is not a network problem. Retrying it
// re-fetches identical bytes, so it must fail on the first attempt.
func TestGetPage_DoesNotRetryMalformedJSON(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		_, _ = w.Write([]byte(`{"not":"an array"}`))
	}))
	defer srv.Close()

	if _, err := fastClient().getPageCtx(context.Background(), srv.URL); err == nil {
		t.Fatal("want an error for malformed JSON")
	} else if !strings.Contains(err.Error(), "decode page") {
		t.Fatalf("want a decode error, got %v", err)
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("malformed JSON must not be retried; got %d attempts", got)
	}
}

func TestGetPage_Retries429ThenSucceeds(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`[{"a":"1"}]`))
	}))
	defer srv.Close()

	page, err := fastClient().getPageCtx(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("want success after 429s, got %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("want 1 row, got %d", len(page))
	}
}

// A 4xx is the client's fault and will not fix itself.
func TestGetPage_DoesNotRetry4xx(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "bad SoQL", http.StatusBadRequest)
	}))
	defer srv.Close()

	if _, err := fastClient().getPageCtx(context.Background(), srv.URL); err == nil {
		t.Fatal("want an error for HTTP 400")
	}
	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("4xx must not be retried; got %d attempts", got)
	}
}

// Cancelling during backoff must return promptly rather than serving out the
// remaining sleep.
func TestGetPage_CancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	c := &Client{MaxRetries: 5, RetryWait: 10 * time.Second, MaxRetryWait: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()

	start := time.Now()
	_, err := c.getPageCtx(ctx, srv.URL)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("cancellation took %v; backoff is not context-aware", elapsed)
	}
}

func TestBackoffFor_CapsAndJitters(t *testing.T) {
	max := 30 * time.Second
	for _, wait := range []time.Duration{time.Second, time.Minute, 10 * time.Minute} {
		want := wait
		if want > max {
			want = max
		}
		for i := 0; i < 50; i++ {
			got := backoffFor(wait, max)
			if got < want/2 || got > want {
				t.Fatalf("backoffFor(%v, %v) = %v, want within [%v, %v]", wait, max, got, want/2, want)
			}
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := map[string]time.Duration{
		"":                              0,
		"5":                             5 * time.Second,
		"0":                             0,
		"-3":                            0,
		"Wed, 21 Oct 2026 07:28:00 GMT": 0, // HTTP-date form is deliberately ignored
	}
	for in, want := range cases {
		if got := parseRetryAfter(in); got != want {
			t.Errorf("parseRetryAfter(%q) = %v, want %v", in, got, want)
		}
	}
}
