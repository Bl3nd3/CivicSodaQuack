// Copyright (c) 2026 Neomantra Corp

// Package web serves the csq browser interface.
//
// The CLI is the complete interface to csq and stays that way. This package
// exists because a terminal is a barrier for people who have every other
// qualification to read civic data — the analyses here are aimed at reporters,
// researchers, and residents, and requiring them to know what a --db flag is
// filters the audience by the wrong criterion.
//
// Two constraints shape the design:
//
// The browser cannot send SQL. Every endpoint takes a mode name and a query
// name, and the SQL is the one the mode declares. That removes the injection
// surface a local SQL console would open, and it means what a person runs in
// the browser is exactly what the CLI runs — including the caveats.
//
// Caveats and exclusions travel with results, never beside them. The API
// returns them in the same payload as the rows, and the page has no code path
// that renders a table without them.
package web

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/config"
)

// Options configures the server.
type Options struct {
	// DBs are the portal databases to attach, read-only.
	DBs []analysis.DBSpec
	// Addr is the listen address. Defaults to loopback.
	Addr string
	// DataDir is where the setup flow creates new databases and configs. Empty
	// disables setup, which is the case whenever --db was given explicitly:
	// someone who named their databases did not ask csq to invent more.
	DataDir string
	// Configs maps a portal alias to its sync config. A portal without one is
	// read-only in the UI: it can be explored and analysed, but not downloaded
	// into, exactly as csq mcp gates its write tools.
	Configs map[string]*config.Config
	// Out receives startup messages.
	Out *os.File
}

// DefaultAddr binds loopback only. The server has no authentication because it
// is a single-user local tool; binding a wider interface would publish an
// unauthenticated read surface over whatever data has been synced, so the
// default must be the safe one and widening it must be deliberate.
const DefaultAddr = "127.0.0.1:8080"

// Server holds the analysis session and the HTTP mux.
type Server struct {
	sess    *analysis.Session
	mux     *http.ServeMux
	out     *os.File
	addr    string
	dataDir string

	// cfgMu guards configs, which the setup flow writes while handlers read it.
	cfgMu   sync.RWMutex
	configs map[string]*config.Config
	syncs   *syncManager
}

// New opens the databases and builds the routes.
func New(opts Options) (*Server, error) {
	sess, err := analysis.Open(opts.DBs)
	if err != nil {
		return nil, err
	}
	out := opts.Out
	if out == nil {
		out = os.Stderr
	}
	cfgs := opts.Configs
	if cfgs == nil {
		cfgs = map[string]*config.Config{}
	}
	s := &Server{
		sess: sess, mux: http.NewServeMux(), out: out, addr: opts.Addr,
		dataDir: opts.DataDir, configs: cfgs, syncs: newSyncManager(),
	}
	s.routes()
	return s, nil
}

// Close stops any running download and releases the database session.
func (s *Server) Close() error {
	s.syncs.stop()
	return s.sess.Close()
}

// Handler exposes the mux for testing.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	static, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic(fmt.Sprintf("embed assets: %v", err)) // build-time guarantee
	}
	fileServer := http.FileServer(http.FS(static))

	s.mux.HandleFunc("/api/portals", s.handlePortals)
	s.mux.HandleFunc("/api/modes", s.handleModes)
	s.mux.HandleFunc("/api/run", s.handleRun)
	s.mux.HandleFunc("/api/catalog", s.handleCatalog)
	s.mux.HandleFunc("/api/categories", s.handleCategories)
	s.mux.HandleFunc("/api/available", s.handleAvailable)
	s.mux.HandleFunc("/api/setup", s.handleSetup)
	s.mux.HandleFunc("/api/sync", s.handleSyncStart)
	s.mux.HandleFunc("/api/sync/status", s.handleSyncStatus)
	s.mux.HandleFunc("/api/sync/stop", s.handleSyncStop)
	s.mux.HandleFunc("/api/sync/events", s.handleSyncEvents)
	s.mux.HandleFunc("/report/", s.handleReport)
	s.mux.HandleFunc("/export/", s.handleExport)

	s.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			// No caching on the shell: a rebuilt binary must not serve a stale
			// page out of the browser cache.
			w.Header().Set("Cache-Control", "no-store")
			b, err := fs.ReadFile(static, "index.html")
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			w.Write(b)
			return
		}
		fileServer.ServeHTTP(w, r)
	})
}

// Serve runs the HTTP server until ctx is cancelled.
func (s *Server) Serve(ctx context.Context) error {
	addr := DefaultAddr
	if a := strings.TrimSpace(s.addr); a != "" {
		addr = a
	}

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	url := "http://" + ln.Addr().String()
	fmt.Fprintf(s.out, "\n  csq is running — open %s in your browser\n", url)
	for _, p := range s.sess.Portals() {
		label := p.City
		if label == "" {
			label = p.Portal
		}
		fmt.Fprintf(s.out, "    • %s  (%s)\n", label, p.Path)
	}
	if len(s.sess.Portals()) == 0 {
		fmt.Fprintf(s.out,
			"\n  No data yet — the page will walk you through picking a city.\n")
	} else if len(s.configs) == 0 && s.dataDir == "" {
		fmt.Fprintf(s.out,
			"\n  Read-only: pass --config <portal.yaml> to download data from the page.\n")
	}
	if !isLoopback(ln.Addr()) {
		fmt.Fprintf(s.out,
			"\n  warning: %s is reachable from other machines and csq has no\n"+
				"  authentication. Anyone who can reach it can read every synced dataset.\n",
			ln.Addr().String())
	}
	fmt.Fprintf(s.out, "\n  Press Ctrl-C to stop.\n\n")

	srv := &http.Server{
		Handler:           s.mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		// Cancel a download before dropping the listener. The sync orchestrator
		// treats cancellation as an abort and cleans up its staging table;
		// letting the process die mid-write instead would leave that table
		// stranded in the database, which nothing else reclaims.
		s.syncs.stop()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func isLoopback(a net.Addr) bool {
	host, _, err := net.SplitHostPort(a.String())
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
