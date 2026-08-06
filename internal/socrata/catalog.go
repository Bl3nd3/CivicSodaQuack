// Copyright (c) 2026 Neomantra Corp

package socrata

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// CatalogEntry is a single dataset as returned by /api/catalog/v1.
type CatalogEntry struct {
	ID          string
	Name        string
	Description string
	Category    string
	Tags        []string
	// RowCount is left nil in Phase 1: Socrata's /api/catalog/v1 does not
	// reliably expose row counts across portals. Callers should tolerate nil.
	RowCount *int64
	// UpdatedAt is when the dataset's *data* last changed upstream, taken from
	// data_updated_at. It deliberately ignores metadata_updated_at: a portal's
	// `updatedAt` is max(data, metadata), so an edit to a description makes a
	// dataset frozen for years look current. Nil when the portal reports none
	// of the candidate fields.
	UpdatedAt *time.Time
	Raw       json.RawMessage
}

// parseCatalogTimestamp returns the first candidate that parses under any known
// layout, so callers can express field precedence by argument order. Returns
// nil when every candidate is empty or unparseable.
func parseCatalogTimestamp(candidates ...string) *time.Time {
	for _, s := range candidates {
		if s == "" {
			continue
		}
		for _, layout := range catalogTimestampLayouts {
			if t, err := time.Parse(layout, s); err == nil {
				return &t
			}
		}
	}
	return nil
}

var catalogTimestampLayouts = []string{
	"2006-01-02T15:04:05.000Z",
	"2006-01-02T15:04:05.000",
	"2006-01-02T15:04:05Z",
	"2006-01-02T15:04:05",
	time.RFC3339,
}

// FetchCatalog returns every dataset the portal reports, following pagination.
func (c *Client) FetchCatalog(portal string) ([]CatalogEntry, error) {
	return c.fetchCatalogScheme(portal, "https")
}

// FetchCatalogScheme is FetchCatalog with an explicit URL scheme (for tests).
func (c *Client) FetchCatalogScheme(portal, scheme string) ([]CatalogEntry, error) {
	return c.fetchCatalogScheme(portal, scheme)
}

// fetchCatalogScheme is the scheme-parameterised form used in tests with httptest.
func (c *Client) fetchCatalogScheme(portal, scheme string) ([]CatalogEntry, error) {
	base := &url.URL{Scheme: scheme, Host: portal, Path: "/api/catalog/v1"}

	var all []CatalogEntry
	offset := 0
	pageSize := c.batchSize()

	for {
		q := url.Values{}
		q.Set("domains", portal)
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))
		base.RawQuery = q.Encode()

		page, total, err := c.getCatalogPage(base.String())
		if err != nil {
			return nil, err
		}
		all = append(all, page...)

		offset += len(page)
		if len(page) == 0 || offset >= total {
			return all, nil
		}
	}
}

type rawCatalogEntry struct {
	Resource struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
		// DataUpdatedAt is what /api/catalog/v1 actually returns for data
		// freshness. UpdatedAt is a fallback for portals that omit it, and
		// RowsUpdatedAt is the field name used by /api/views/{id}.json — kept
		// so a caller feeding this struct a views payload still resolves.
		DataUpdatedAt string `json:"data_updated_at"`
		UpdatedAt     string `json:"updatedAt"`
		RowsUpdatedAt string `json:"rowsUpdatedAt"`
	} `json:"resource"`
	Classification struct {
		DomainCategory string   `json:"domain_category"`
		DomainTags     []string `json:"domain_tags"`
	} `json:"classification"`
}

type catalogResponse struct {
	Results       []json.RawMessage `json:"results"`
	ResultSetSize int               `json:"resultSetSize"`
}

// getCatalogPage performs a single GET with no retry. Catalog fetches are
// one-shot pre-flight calls; errors propagate to the caller (typically the
// sync orchestrator), which can re-invoke FetchCatalog if desired. The row-
// streaming path in getPage has its own 429/5xx retry loop; conflating the
// two would create competing retry scopes.
func (c *Client) getCatalogPage(fullURL string) ([]CatalogEntry, int, error) {
	req, err := http.NewRequest(http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build catalog request: %w", err)
	}
	if c.AppToken != "" {
		req.Header.Set("X-App-Token", c.AppToken)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("catalog request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("catalog HTTP %d: %s", resp.StatusCode, string(body))
	}

	var cr catalogResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, 0, fmt.Errorf("decode catalog: %w", err)
	}

	entries := make([]CatalogEntry, 0, len(cr.Results))
	for _, raw := range cr.Results {
		var r rawCatalogEntry
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, 0, fmt.Errorf("decode catalog entry: %w", err)
		}
		e := CatalogEntry{
			ID:          r.Resource.ID,
			Name:        r.Resource.Name,
			Description: r.Resource.Description,
			Category:    r.Classification.DomainCategory,
			Tags:        r.Classification.DomainTags,
			Raw:         raw,
		}
		e.UpdatedAt = parseCatalogTimestamp(
			r.Resource.DataUpdatedAt,
			r.Resource.UpdatedAt,
			r.Resource.RowsUpdatedAt,
		)
		entries = append(entries, e)
	}
	return entries, cr.ResultSetSize, nil
}
