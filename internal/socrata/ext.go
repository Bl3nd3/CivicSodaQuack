// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// socrataIDPattern is the shape of a Socrata :id -- row-abcd-efgh_ijkl. An id
// that does not match is not embedded in a predicate; the stream falls back to
// $offset rather than risk a malformed or injectable $where.
var socrataIDPattern = regexp.MustCompile(`^[A-Za-z0-9_~.-]+$`)

// selectsSocrataID reports whether the projection will return :id, without
// which there is no key to seek on. The default projection omits system fields.
func selectsSocrataID(selectClause string) bool {
	return strings.Contains(selectClause, ":*") || strings.Contains(selectClause, ":id")
}

// socrataIDOf pulls the :id out of a row, rejecting anything unusable.
func socrataIDOf(row Row) (string, bool) {
	v, ok := row[":id"]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	if !ok || s == "" || !socrataIDPattern.MatchString(s) {
		return "", false
	}
	return s, true
}

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

	// Keyset pagination. $offset makes the portal scan and discard every row
	// before the offset, so page cost grows with depth. Measured against
	// Chicago's crimes dataset, one 5,000-row page took 5s at offset 0 and 36s
	// at offset 2,500,000; NYC's complaint dataset stopped answering at all
	// past roughly 3.1M, which is a sync that cannot finish rather than one
	// that is merely slow. Seeking on the ordering key keeps every page the
	// same cost: the page that took 36s by offset takes 4.4s by keyset.
	//
	// This applies only when the stream is ordered by :id and :id is in the
	// projection -- csq's default. Anything else keeps the old $offset path,
	// as does a row whose :id we cannot safely quote.
	useKeyset := orderBy == ":id" && selectsSocrataID(selectClause)
	lastID := ""

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
		where := whereClause
		if useKeyset {
			if lastID != "" {
				seek := ":id > '" + lastID + "'"
				if where == "" {
					where = seek
				} else {
					where = "(" + where + ") AND " + seek
				}
			}
		} else {
			q.Set("$offset", strconv.Itoa(offset))
		}
		if orderBy != "" {
			q.Set("$order", orderBy)
		}
		if where != "" {
			q.Set("$where", where)
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
		if useKeyset && len(page) > 0 {
			id, ok := socrataIDOf(page[len(page)-1])
			if !ok {
				// No usable key: drop to $offset for the rest of this stream.
				// Continuing without a seek would refetch the same page for
				// ever, which is worse than paging slowly.
				useKeyset = false
			} else {
				lastID = id
			}
		}
		if len(page) < remaining {
			return nil
		}
		if limit > 0 && fetched >= limit {
			return nil
		}
	}
}
