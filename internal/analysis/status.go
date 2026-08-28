// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// DatasetStatus is one concept of a mode and whether its data has landed.
type DatasetStatus struct {
	Concept string `json:"concept"`
	Table   string `json:"table"`
	ID      string `json:"id"`   // Socrata 4x4
	Name    string `json:"name"` // upstream title
	City    string `json:"city"`
	Present bool   `json:"present"`
	Rows    int64  `json:"rows"`
}

// ModeStatus is a mode plus whether the attached databases can answer it.
//
// The UI leads with this because "this analysis has no data yet" and "this
// analysis found nothing" look identical in a table and mean opposite things.
type ModeStatus struct {
	Name        string          `json:"name"`
	Title       string          `json:"title"`
	Summary     string          `json:"summary"`
	About       string          `json:"about"`
	MultiPortal bool            `json:"multi_portal"`
	QueryCount  int             `json:"query_count"`
	Queries     []QueryInfo     `json:"queries"`
	Caveats     []string        `json:"caveats"`
	Datasets    []DatasetStatus `json:"datasets"`
	// Ready reports that every concept the mode needs has a table with rows.
	Ready bool `json:"ready"`
	// Applicable reports that the attached portals are bound to this mode at
	// all. An unbound portal is a different problem from an unsynced one, and
	// suggesting "sync this" for it would send someone in circles.
	Applicable bool   `json:"applicable"`
	Reason     string `json:"reason"`
	// FixCommand populates the "how do I get this data" affordance.
	FixCommand string `json:"fix_command,omitempty"`
}

// QueryInfo describes one query without exposing its SQL to a listing.
type QueryInfo struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// ModeStatuses reports every registered mode against the attached databases.
func (s *Session) ModeStatuses(ctx context.Context) ([]ModeStatus, error) {
	present, err := s.tableRowCounts(ctx)
	if err != nil {
		return nil, err
	}

	all := modes.All()
	out := make([]ModeStatus, 0, len(all))
	for _, m := range all {
		out = append(out, s.statusFor(ctx, m, present))
	}
	return out, nil
}

func (s *Session) statusFor(ctx context.Context, m *modes.Mode, present map[string]int64) ModeStatus {
	st := ModeStatus{
		Name: m.Name, Title: m.Title, Summary: m.Summary, About: m.About,
		MultiPortal: m.MultiPortal, QueryCount: len(m.Queries),
		Caveats: m.Caveats, Datasets: []DatasetStatus{},
	}
	for _, q := range m.Queries {
		st.Queries = append(st.Queries, QueryInfo{Name: q.Name, Desc: q.Desc})
	}

	// A single-portal mode cannot run against several attached databases.
	ports := s.snapshot()
	if len(ports) == 0 {
		st.Reason = "no data is loaded yet"
		return st
	}
	if !m.MultiPortal && len(ports) != 1 {
		st.Reason = fmt.Sprintf("targets a single portal; %d are attached", len(ports))
		return st
	}

	// Modes with no concepts read the _csq bookkeeping schema, which every csq
	// database carries by construction. They are always ready.
	if len(m.Concepts) == 0 {
		st.Applicable, st.Ready = true, true
		return st
	}

	bindings, err := s.bindingsFor(m)
	if err != nil {
		st.Reason = plainBindingError(err, ports)
		return st
	}
	st.Applicable = true

	ready := true
	for i, b := range bindings {
		alias := ports[i].Alias
		for _, cname := range conceptNames(m) {
			bd, bound := b.Concepts[cname]
			if !bound {
				// A concept this portal does not publish is a property of the
				// city, not a missing sync. Queries needing it are excluded by
				// name at run time; the mode as a whole can still be useful.
				continue
			}
			rows, ok := present[alias+"."+bd.Table]
			ds := DatasetStatus{
				Concept: cname, Table: bd.Table, ID: bd.ID, Name: bd.Name,
				City: b.City, Present: ok && rows > 0, Rows: rows,
			}
			if !ds.Present {
				ready = false
			}
			st.Datasets = append(st.Datasets, ds)
		}
	}
	st.Ready = ready
	if !ready {
		st.Reason = "datasets for this analysis have not been synced yet"
		portal := ""
		if len(ports) > 0 {
			portal = ports[0].Portal
		}
		st.FixCommand = (&NotSyncedError{Mode: m.Name, Portal: portal}).FixCommand()
	}
	return st
}

// tableRowCounts returns "<alias>.<table>" → row count for every attached
// portal's main schema.
//
// duckdb_tables() reports an estimated_size that is exact for a table with no
// pending changes, which is always the case here: every database is attached
// read-only. Counting each table for real would mean a full scan per table on
// every page load.
func (s *Session) tableRowCounts(ctx context.Context) (map[string]int64, error) {
	out := map[string]int64{}
	for _, p := range s.snapshot() {
		rows, err := s.host.QueryContext(ctx, fmt.Sprintf(
			`SELECT table_name, estimated_size FROM duckdb_tables()
			 WHERE database_name = '%s' AND schema_name = 'main'`, quoteLiteral(p.Alias)))
		if err != nil {
			return nil, fmt.Errorf("inventory %s: %w", p.Alias, err)
		}
		for rows.Next() {
			var name string
			var n *int64
			if err := rows.Scan(&name, &n); err != nil {
				rows.Close()
				return nil, err
			}
			var count int64
			if n != nil {
				count = *n
			}
			out[p.Alias+"."+name] = count
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func conceptNames(m *modes.Mode) []string {
	names := make([]string, 0, len(m.Concepts))
	for _, c := range m.Concepts {
		names = append(names, c.Name)
	}
	sort.Strings(names)
	return names
}

// plainBindingError rewrites "no binding for mode X on portal Y" into something
// a non-technical reader can act on.
func plainBindingError(err error, portals []Portal) string {
	hosts := make([]string, 0, len(portals))
	for _, p := range portals {
		hosts = append(hosts, p.Portal)
	}
	if strings.Contains(err.Error(), "no binding") {
		return fmt.Sprintf("not available for %s — no dataset mapping exists for this portal yet",
			strings.Join(hosts, ", "))
	}
	return err.Error()
}
