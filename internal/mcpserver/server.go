// Copyright (c) 2026 Neomantra Corp

package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/version"
)

// queryTimeout is the per-query timeout enforced by query_sql.
const queryTimeout = 30 * time.Second

// Options configures the MCP server.
type Options struct {
	DBs      []DBSpec // resolved (alias, path) pairs; required, non-empty
	HTTPAddr string   // empty means stdio; non-empty switches to HTTP
	// Configs maps portal alias → registered YAML config. Phase 6 write tools
	// (sync_dataset, refresh_catalog) are only registered when len(Configs) > 0.
	// Read tools register regardless.
	Configs map[string]*config.Config
}

// Serve constructs the server, opens pools, registers all tools, and runs the
// chosen transport. Blocks until the context is cancelled or the transport
// returns. Pools are closed before returning.
func Serve(ctx context.Context, opts Options) error {
	// Only portals with a registered config need write access: their write
	// tools reopen the same path in-process, and DuckDB will not hold one file
	// at two access modes. Everything else is opened read-only so a concurrent
	// sync or a second reader is not locked out.
	writable := make(map[string]bool, len(opts.Configs))
	for alias := range opts.Configs {
		writable[alias] = true
	}
	pools, err := OpenPools(opts.DBs, writable)
	if err != nil {
		return err
	}
	defer pools.Close()

	srv, err := buildServer(pools, opts.Configs)
	if err != nil {
		return err
	}

	if opts.HTTPAddr != "" {
		return runHTTP(ctx, srv, opts.HTTPAddr)
	}
	return srv.Run(ctx, &mcp.StdioTransport{})
}

// DatasetList wraps a slice of DatasetSummary so the MCP output schema has
// type "object" (the spec requires the output schema root to be an object).
type DatasetList struct {
	Datasets []DatasetSummary `json:"datasets"`
}

// buildServer creates an *mcp.Server and registers all four read tools (and
// the two write tools when configs is non-empty).
func buildServer(pools *Pools, configs map[string]*config.Config) (*mcp.Server, error) {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "civicsodaquack",
		Version: version.Version,
	}, nil)

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_datasets",
		Description: "List datasets available across attached portal DuckDB files. Use the optional 'portal' or 'category' filters to narrow the result.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args ListDatasetsArgs) (*mcp.CallToolResult, DatasetList, error) {
		out, err := listDatasetsHandler(ctx, pools, args)
		if err != nil {
			return nil, DatasetList{}, err
		}
		return &mcp.CallToolResult{}, DatasetList{Datasets: out}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "describe_dataset",
		Description: "Return columns, last sync info, and tags for one dataset. Pass 'portal' if dataset_id is ambiguous across portals.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args DescribeDatasetArgs) (*mcp.CallToolResult, DatasetDetail, error) {
		out, err := describeDatasetHandler(ctx, pools, args)
		if err != nil {
			return nil, DatasetDetail{}, err
		}
		return &mcp.CallToolResult{}, out, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_datasets",
		Description: "Substring match on dataset name, description, and tags (case-insensitive).",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args SearchDatasetsArgs) (*mcp.CallToolResult, DatasetList, error) {
		out, err := searchDatasetsHandler(ctx, pools, args)
		if err != nil {
			return nil, DatasetList{}, err
		}
		return &mcp.CallToolResult{}, DatasetList{Datasets: out}, nil
	})

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "query_sql",
		Description: "Run a read-only DuckDB SELECT across all attached portals. Cross-portal queries: <alias>.<schema>.<table>. Capped at 1000 rows / 1MB / 30s.",
	}, func(ctx context.Context, req *mcp.CallToolRequest, args QuerySQLArgs) (*mcp.CallToolResult, QuerySQLResult, error) {
		out, err := querySQLHandler(ctx, pools, args, queryTimeout)
		if err != nil {
			return nil, QuerySQLResult{}, err
		}
		// Provide a text-content rendering as well, for clients that don't
		// process structured output.
		body, _ := json.Marshal(out)
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: string(body)}},
		}, out, nil
	})

	// Phase 6: write tools register only when at least one portal has a config.
	if len(configs) > 0 {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "sync_dataset",
			Description: "Sync one dataset by ID for a registered portal. Set full_refresh=true to bootstrap (full-replace) instead of delta. Blocks until the sync finishes.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args SyncDatasetArgs) (*mcp.CallToolResult, SyncDatasetResult, error) {
			out, err := syncDatasetHandler(ctx, configs, args)
			if err != nil {
				return nil, SyncDatasetResult{}, err
			}
			return &mcp.CallToolResult{}, out, nil
		})
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "refresh_catalog",
			Description: "Refetch /api/catalog/v1 for one or all registered portals and upsert _csq.catalog. Per-portal failures don't abort the batch.",
		}, func(ctx context.Context, req *mcp.CallToolRequest, args RefreshCatalogArgs) (*mcp.CallToolResult, RefreshCatalogResultList, error) {
			out, err := refreshCatalogHandler(ctx, configs, args)
			if err != nil {
				return nil, RefreshCatalogResultList{}, err
			}
			return &mcp.CallToolResult{}, out, nil
		})
	}

	return srv, nil
}

// newHTTPHandler returns an http.Handler that serves the given MCP server via
// the SDK's StreamableHTTP transport in stateless mode, which skips the
// session-ID handshake and uses default initialization parameters. This makes
// it easier to call from simple HTTP clients and test harnesses.
func newHTTPHandler(srv *mcp.Server) http.Handler {
	return mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, &mcp.StreamableHTTPOptions{Stateless: true})
}

// runHTTP listens on addr and serves the MCP server over HTTP. Blocks until ctx
// is cancelled or the listener errors.
func runHTTP(ctx context.Context, srv *mcp.Server, addr string) error {
	httpsrv := &http.Server{
		Addr:    addr,
		Handler: newHTTPHandler(srv),
	}

	errCh := make(chan error, 1)
	go func() { errCh <- httpsrv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpsrv.Shutdown(shutdownCtx)
		return nil
	case err := <-errCh:
		if err == http.ErrServerClosed {
			return nil
		}
		return fmt.Errorf("http listen %s: %w", addr, err)
	}
}
