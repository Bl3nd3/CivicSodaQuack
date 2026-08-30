// Copyright (c) 2026 Neomantra Corp

// Package analysis runs mode queries headlessly and returns structured
// results.
//
// The CLI grew its own runner that expanded a query, executed it, and printed
// it in one pass, with the exclusion reporting written directly to stdout. A
// browser UI needs the same decisions — which cities can answer, which were
// excluded and why, whether one qualifying city still counts as a comparison —
// as data rather than as text. Rebuilding that reasoning behind a second
// interface would be the surest way to have the two disagree, and the parts
// most likely to drift are exactly the ones that keep absent data from reading
// as a good result.
//
// So the decisions live here and the interfaces only render them.
package analysis

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// DefaultRowLimit caps rows returned for one query. Matches the MCP server's
// query_sql cap: a browser table and an agent context window suffer the same
// way from an unbounded result.
const DefaultRowLimit = 1000

// DefaultTimeout bounds a single query.
const DefaultTimeout = 30 * time.Second

// ErrNoComparableCity signals that no attached city can answer a query. It is
// not a failure of the query — it means every city was excluded, and the
// caller should say so rather than render an empty table.
var ErrNoComparableCity = errors.New("no attached city can answer this query")

// DBSpec is one portal database to attach.
type DBSpec struct {
	Alias string // SQL identifier; derived from the filename when empty
	Path  string
	// Portal overrides the Socrata host derived from the filename. Needed
	// because csq does not record the portal inside the file.
	Portal string
}

// Portal is one attached database.
type Portal struct {
	Alias  string `json:"alias"`
	Path   string `json:"path"`
	Portal string `json:"portal"` // Socrata host
	City   string `json:"city"`   // from the binding, when one exists
}

// Session holds an in-memory host DB with every portal ATTACHed READ_ONLY.
//
// Read-only attach is what lets this run alongside a csq sync or an MCP server
// holding the same file. Nothing in this package writes.
type Session struct {
	host *sql.DB

	// mu guards portals, which is no longer fixed at construction: the setup
	// flow attaches a portal while the server is serving, so every read of the
	// slice has to take a snapshot rather than range over it directly.
	mu      sync.RWMutex
	portals []Portal

	// confCache memoises confidence assessments. Profiling is a scan per
	// dataset, and a page rendering several analyses over the same corpus would
	// otherwise repeat the same scans for every one of them.
	confCache confidenceCache
}

// snapshot returns a copy of the attached portals, safe to range over without
// holding the lock.
func (s *Session) snapshot() []Portal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Portal, len(s.portals))
	copy(out, s.portals)
	return out
}

// Open attaches every spec READ_ONLY to a fresh in-memory host.
//
// Zero specs is valid and yields an empty session. Someone opening csq for the
// first time has no database at all, and refusing to start without one would
// make the setup flow impossible to reach from the page.
func Open(specs []DBSpec) (*Session, error) {
	paths := make([]string, len(specs))
	for i, s := range specs {
		paths[i] = s.Path
	}
	derived := modes.UniqueAliases(paths)

	host, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open host: %w", err)
	}
	s := &Session{host: host}

	seen := make(map[string]bool, len(specs))
	for i, spec := range specs {
		alias := spec.Alias
		if alias == "" {
			alias = derived[i]
		}
		if seen[alias] {
			host.Close()
			return nil, fmt.Errorf("alias collision on %q; give an explicit alias", alias)
		}
		seen[alias] = true

		if _, err := os.Stat(spec.Path); err != nil {
			host.Close()
			return nil, fmt.Errorf("%s: %w", spec.Path, err)
		}
		if _, err := host.Exec(fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
			quoteLiteral(spec.Path), alias)); err != nil {
			host.Close()
			return nil, fmt.Errorf("attach %s: %w", spec.Path, err)
		}

		portalHost := spec.Portal
		if portalHost == "" {
			portalHost = modes.PortalFromDBPath(spec.Path)
		}
		s.portals = append(s.portals, Portal{
			Alias: alias, Path: spec.Path, Portal: portalHost, City: cityFor(portalHost),
		})
	}
	return s, nil
}

// Close releases the host database.
func (s *Session) Close() error {
	if s.host == nil {
		return nil
	}
	return s.host.Close()
}

// Portals returns the attached portals in attach order.
func (s *Session) Portals() []Portal { return s.snapshot() }

// Attach adds a portal to a live session.
//
// Used by the setup flow, which creates a database and then has to make it
// visible without restarting the server.
func (s *Session) Attach(spec DBSpec) (Portal, error) {
	if _, err := os.Stat(spec.Path); err != nil {
		return Portal{}, fmt.Errorf("%s: %w", spec.Path, err)
	}

	alias := spec.Alias
	if alias == "" {
		alias = modes.AliasFor(spec.Path)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.portals {
		if p.Alias == alias {
			return Portal{}, fmt.Errorf("a portal is already attached as %q", alias)
		}
	}
	if _, err := s.host.Exec(fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
		quoteLiteral(spec.Path), alias)); err != nil {
		return Portal{}, fmt.Errorf("attach %s: %w", spec.Path, err)
	}

	portalHost := spec.Portal
	if portalHost == "" {
		portalHost = modes.PortalFromDBPath(spec.Path)
	}
	p := Portal{Alias: alias, Path: spec.Path, Portal: portalHost, City: cityFor(portalHost)}
	s.portals = append(s.portals, p)
	return p, nil
}

// bindingsFor resolves one binding per attached portal for a concept-based
// mode. Modes with no concepts (research) return nil.
func (s *Session) bindingsFor(m *modes.Mode) ([]*modes.Binding, error) {
	if len(m.Concepts) == 0 {
		return nil, nil
	}
	ports := s.snapshot()
	out := make([]*modes.Binding, len(ports))
	for i, p := range ports {
		b, err := modes.LookupBinding(m.Name, p.Portal)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func quoteLiteral(s string) string { return strings.ReplaceAll(s, "'", "''") }

// cityFor finds a human label for a portal host by asking any mode that binds
// it. Bindings carry the city name; the database file only carries a hostname,
// and "data.cityofchicago.org" is not what anyone calls the place.
func cityFor(portalHost string) string {
	for _, m := range modes.All() {
		for _, b := range modes.BindingsFor(m.Name) {
			if b.Portal == portalHost && b.City != "" {
				return b.City
			}
		}
	}
	return ""
}

// Refresh re-attaches one portal so queries see data written since the session
// opened.
//
// A READ_ONLY attachment is a snapshot: DuckDB binds the catalog at ATTACH time
// and a writer in the same process — csq's own sync, for instance — makes
// changes the attached view will never show. Detaching and re-attaching is what
// picks them up, and the alternative (reopening the whole session) would drop
// every other portal for the sake of one.
func (s *Session) Refresh(alias string) error {
	var path string
	for _, p := range s.snapshot() {
		if p.Alias == alias {
			path = p.Path
			break
		}
	}
	if path == "" {
		return fmt.Errorf("no attached portal named %q", alias)
	}

	if _, err := s.host.Exec("DETACH " + alias); err != nil {
		return fmt.Errorf("detach %s: %w", alias, err)
	}
	// From here the portal is missing from the session until the re-attach
	// lands, so a failure has to be reported rather than swallowed — queries
	// against it would otherwise fail with a confusing binder error.
	if _, err := s.host.Exec(fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
		quoteLiteral(path), alias)); err != nil {
		return fmt.Errorf("re-attach %s: %w", alias, err)
	}
	// New data is precisely what a cached assessment must not hide.
	s.confCache.invalidate()
	return nil
}

// PortalByAlias returns the attached portal with this alias.
func (s *Session) PortalByAlias(alias string) (Portal, bool) {
	for _, p := range s.snapshot() {
		if p.Alias == alias {
			return p, true
		}
	}
	return Portal{}, false
}
