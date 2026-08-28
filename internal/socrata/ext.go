// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

// FetchMetadataURL is like FetchMetadata but takes a full URL. Used by callers
// that need to control the scheme (e.g. httptest).
func (c *Client) FetchMetadataURL(ctx context.Context, fullURL string) (*DatasetMetadata, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build metadata request: %w", err)
	}
	if c.AppToken != "" {
		req.Header.Set("X-App-Token", c.AppToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, fmt.Errorf("metadata request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("metadata HTTP %d: %s", resp.StatusCode, string(body))
	}
	var md DatasetMetadata
	if err := json.NewDecoder(resp.Body).Decode(&md); err != nil {
		return nil, fmt.Errorf("decode metadata: %w", err)
	}
	return &md, nil
}

// StreamRowsCtx is a context-aware, scheme-parameterised version of StreamRows.
// Cancellation via ctx aborts mid-request and mid-backoff, not only between
// pages. selectClause, if non-empty, is sent as $select; pass ":*,*" to include
// Socrata system fields (:id, :updated_at).
func (c *Client) StreamRowsCtx(
	ctx context.Context,
	scheme, portal, datasetID, orderBy, whereClause, selectClause string,
	limit int,
	handler PageHandler,
) error {
	base := &url.URL{Scheme: scheme, Host: portal, Path: "/resource/" + datasetID + ".json"}

	fetched := 0
	offset := 0
	batch := c.batchSize()

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

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
		if selectClause != "" {
			q.Set("$select", selectClause)
		}
		base.RawQuery = q.Encode()

		page, err := c.getPageCtx(ctx, base.String())
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
			return nil
		}
		if limit > 0 && fetched >= limit {
			return nil
		}
	}
}
