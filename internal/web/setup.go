// Copyright (c) 2026 Neomantra Corp

package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// AvailableAnalysis is one analysis a city can answer, and what it would cost
// to hold the data for it.
type AvailableAnalysis struct {
	Mode       string `json:"mode"`
	Title      string `json:"title"`
	Summary    string `json:"summary"`
	Datasets   int    `json:"datasets"`
	ApproxRows int64  `json:"approx_rows"`
	ApproxTime string `json:"approx_time"`
}

// AvailableCity is a portal csq knows how to analyse.
type AvailableCity struct {
	Portal   string              `json:"portal"`
	City     string              `json:"city"`
	Attached bool                `json:"attached"`
	Analyses []AvailableAnalysis `json:"analyses"`
}

// handleAvailable lists the cities csq has bindings for.
//
// This is the answer to "what can I even do with this?", which is the first
// question someone opening the tool with no data has and the one the CLI
// answers worst — `csq modes show` requires already knowing a mode name and a
// portal host.
func (s *Server) handleAvailable(w http.ResponseWriter, r *http.Request) {
	attached := map[string]bool{}
	for _, p := range s.sess.Portals() {
		attached[p.Portal] = true
	}

	byPortal := map[string]*AvailableCity{}
	for _, m := range modes.All() {
		if len(m.Concepts) == 0 {
			continue // reads the _csq schema; nothing to download for it
		}
		for _, b := range modes.BindingsFor(m.Name) {
			c, ok := byPortal[b.Portal]
			if !ok {
				c = &AvailableCity{Portal: b.Portal, City: b.City, Attached: attached[b.Portal]}
				byPortal[b.Portal] = c
			}
			rows := m.ApproxRowsFor(b)
			c.Analyses = append(c.Analyses, AvailableAnalysis{
				Mode: m.Name, Title: m.Title, Summary: m.Summary,
				Datasets: len(b.Concepts), ApproxRows: rows,
				ApproxTime: approxDuration(rows),
			})
		}
	}

	out := make([]*AvailableCity, 0, len(byPortal))
	for _, c := range byPortal {
		sort.Slice(c.Analyses, func(i, j int) bool { return c.Analyses[i].Mode < c.Analyses[j].Mode })
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].City < out[j].City })

	writeJSON(w, map[string]any{"cities": out, "can_setup": s.dataDir != ""})
}

// handleSetup creates a database and config for one city, attaches it, and
// starts downloading the datasets one analysis needs.
//
// The config is written to disk rather than held in memory on purpose: it
// leaves the user with the same YAML `csq modes init` would have produced, so
// whatever they set up here stays drivable from the command line. A UI that
// creates state its own CLI cannot see would be a trap.
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	if s.dataDir == "" {
		writeError(w, http.StatusBadRequest,
			fmt.Errorf("setting up new cities is disabled; restart csq web without --db to enable it"))
		return
	}

	var req struct {
		Portal string `json:"portal"`
		Mode   string `json:"mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	m, err := modes.Lookup(req.Mode)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// Look the binding up rather than trusting the portal string: this is what
	// keeps a request from naming an arbitrary host, and it means the filenames
	// below are derived from csq's own registry, never from user input.
	b, err := modes.LookupBinding(m.Name, req.Portal)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	dbPath := filepath.Join(s.dataDir, b.Portal+".duckdb")
	yamlPath := filepath.Join(s.dataDir, b.Portal+"-"+m.Name+".yaml")

	yamlText, err := m.ConfigYAMLFor(b)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if err := os.WriteFile(yamlPath, []byte(yamlText), 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("write %s: %w", yamlPath, err))
		return
	}

	cfg, err := config.Load(yamlPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("read back %s: %w", yamlPath, err))
		return
	}
	cfg.DB = dbPath

	// Create the database if this is the first analysis for this city; a second
	// analysis for a city already set up reuses it.
	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		dw, err := duckdb.Open(dbPath)
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("create %s: %w", dbPath, err))
			return
		}
		if err := dw.Close(); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("create %s: %w", dbPath, err))
			return
		}
	}

	alias := modes.AliasFor(dbPath)
	if _, already := s.sess.PortalByAlias(alias); !already {
		if _, err := s.sess.Attach(analysis.DBSpec{Alias: alias, Path: dbPath, Portal: b.Portal}); err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	s.cfgMu.Lock()
	s.configs[alias] = cfg
	s.cfgMu.Unlock()

	job, err := s.startSync(alias, m.Name)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{
		"job": job, "db": dbPath, "config": yamlPath, "alias": alias,
	})
}

// approxDuration turns a row count into the phrase a progress display needs
// before it has any progress to show.
//
// Deliberately coarse. csq measured roughly 2,000 rows a second against Cook
// County, but throughput varies with row width, rate limits, and the portal's
// mood, so a precise-looking estimate would be wrong in a way that reads as a
// promise.
func approxDuration(rows int64) string {
	switch {
	case rows <= 0:
		return "unknown"
	case rows < 50_000:
		return "under a minute"
	case rows < 500_000:
		return "a few minutes"
	case rows < 5_000_000:
		return "tens of minutes"
	default:
		return "an hour or more"
	}
}
