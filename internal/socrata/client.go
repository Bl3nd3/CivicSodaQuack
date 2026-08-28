// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Client is a minimal Socrata Open Data API (SODA2) client.
//
// It is goroutine-unsafe in that the embedded fields should be set before use.
// A zero value is usable (default http.Client, no app token, 5000 batch size).
type Client struct {
	HTTPClient   *http.Client
	AppToken     string
	BatchSize    int           // rows per page; 0 → 5000
	MaxRetries   int           // transient-failure retries; 0 → 8
	RetryWait    time.Duration // initial backoff; 0 → 1s
	MaxRetryWait time.Duration // backoff ceiling; 0 → 30s
}

func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

func (c *Client) batchSize() int {
	if c.BatchSize > 0 {
		return c.BatchSize
	}
	return 5000
}

func (c *Client) maxRetries() int {
	if c.MaxRetries > 0 {
		return c.MaxRetries
	}
	return 8
}

func (c *Client) retryWait() time.Duration {
	if c.RetryWait > 0 {
		return c.RetryWait
	}
	return time.Second
}

func (c *Client) maxRetryWait() time.Duration {
	if c.MaxRetryWait > 0 {
		return c.MaxRetryWait
	}
	return 30 * time.Second
}

// Row is a single JSON object returned by the Socrata rows endpoint.
type Row = map[string]any

// PageHandler receives each page of rows. Return an error to abort the stream.
type PageHandler func(page []Row) error

// StreamRows pages through /resource/{id}.json and invokes handler for each page.
//
// limit caps total rows (0 = unlimited). orderBy is appended to $order (required
// for stable pagination across pages per Socrata docs). whereClause, if set, is
// passed as $where.
func (c *Client) StreamRows(portal, datasetID, orderBy, whereClause string, limit int, handler PageHandler) error {
	base := &url.URL{
		Scheme: "https",
		Host:   portal,
		Path:   fmt.Sprintf("/resource/%s.json", datasetID),
	}

	fetched := 0
	offset := 0
	batch := c.batchSize()

	for {
		remaining := batch
		if limit > 0 && limit-fetched < batch {
			remaining = limit - fetched
		}
		if remaining <= 0 {
			return nil
		}

		q := url.Values{}
		q.Set("$limit", strconv.Itoa(remaining))
		q.Set("$offset", strconv.Itoa(offset))
		if orderBy != "" {
			q.Set("$order", orderBy)
		}
		if whereClause != "" {
			q.Set("$where", whereClause)
		}
		base.RawQuery = q.Encode()

		page, err := c.getPage(base.String())
		if err != nil {
			return err
		}

		if len(page) > 0 {
			if err := handler(page); err != nil {
				return err
			}
		}

		fetched += len(page)
		offset += len(page)

		if len(page) < remaining {
			return nil // short page → end of data
		}
		if limit > 0 && fetched >= limit {
			return nil
		}
	}
}

// retryableError marks a transient failure worth another attempt.
type retryableError struct{ err error }

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

// getPage fetches one page with no cancellation. Retained for StreamRows.
func (c *Client) getPage(fullURL string) ([]Row, error) {
	return c.getPageCtx(context.Background(), fullURL)
}

// getPageCtx fetches one page of rows, retrying transient failures with
// exponential backoff, a ceiling, and jitter.
//
// What counts as transient is the load-bearing decision here. The obvious cases
// are a refused dial, a 429, and a 5xx. The case that matters more in practice
// is a connection reset *part-way through the response body*: the status line
// has already arrived and said 200, so an implementation that decodes straight
// off resp.Body sees only a JSON error and gives up on what is squarely a
// network blip. Multi-hour syncs of million-row datasets hit exactly that, and
// a run that dies at offset 245000 discards every row it had read.
//
// So the body is read to completion inside the retry loop, and a read failure
// there is retried like any other network error. A JSON error on a body that
// arrived intact is *not* retried — that is a malformed response, and asking
// again produces the same bytes.
func (c *Client) getPageCtx(ctx context.Context, fullURL string) ([]Row, error) {
	var lastErr error
	wait := c.retryWait()

	for attempt := 0; attempt <= c.maxRetries(); attempt++ {
		if attempt > 0 {
			if err := sleepCtx(ctx, backoffFor(wait, c.maxRetryWait())); err != nil {
				return nil, err
			}
			wait *= 2
		}

		page, retryAfter, err := c.attemptPage(ctx, fullURL)
		if err == nil {
			return page, nil
		}

		var retryable retryableError
		if !errors.As(err, &retryable) {
			return nil, err // permanent: 4xx, malformed JSON, bad request
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		lastErr = retryable.err
		if retryAfter > 0 {
			wait = retryAfter // server told us how long; honour it
		}
	}
	return nil, fmt.Errorf("exhausted retries after %d attempts: %w", c.maxRetries()+1, lastErr)
}

// attemptPage makes one HTTP attempt. A returned retryableError means try
// again; any other error is permanent. The duration is a server-supplied
// Retry-After, when present.
func (c *Client) attemptPage(ctx context.Context, fullURL string) ([]Row, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	if c.AppToken != "" {
		req.Header.Set("X-App-Token", c.AppToken)
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, retryableError{err} // dial, TLS, reset before headers
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusOK:
		// Read to completion here, not in the decoder, so a mid-body reset is a
		// retryable read error rather than an unretryable decode error.
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, 0, retryableError{fmt.Errorf("read page body: %w", err)}
		}
		var page []Row
		if err := json.Unmarshal(body, &page); err != nil {
			return nil, 0, fmt.Errorf("decode page: %w", err)
		}
		return page, 0, nil

	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, parseRetryAfter(resp.Header.Get("Retry-After")),
			retryableError{fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))}

	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
}

// backoffFor caps wait at max and applies full jitter. Jitter matters because
// syncs run several datasets concurrently against one portal: without it, a
// portal hiccup puts every worker on an identical retry schedule and they
// resynchronise into a burst at exactly the moment it is least wanted.
func backoffFor(wait, max time.Duration) time.Duration {
	if wait > max {
		wait = max
	}
	if wait <= 0 {
		return 0
	}
	return wait/2 + time.Duration(rand.Int63n(int64(wait/2)+1))
}

// parseRetryAfter reads the delay-seconds form of the header. The HTTP-date
// form is ignored: Socrata does not send it, and guessing wrong here delays a
// sync rather than failing it visibly.
func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs < 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// sleepCtx sleeps unless ctx is cancelled first. A plain time.Sleep in the
// backoff path makes Ctrl-C during a 30s wait feel like a hang.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
