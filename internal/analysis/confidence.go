// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// ConfidenceTTL is how long a dataset assessment is reused.
//
// The inputs move on a sync's timescale, not a page load's: nothing a profiling
// pass measures can change while the databases are attached READ_ONLY. A short
// TTL is therefore about picking up a Refresh, not about correctness, and it is
// what keeps a browser page that runs six queries against the same three
// datasets from scanning them eighteen times.
const ConfidenceTTL = 5 * time.Minute

type confidenceEntry struct {
	rep *confidence.Report
	at  time.Time
}

type confidenceCache struct {
	mu sync.Mutex
	m  map[string]confidenceEntry
}

func (c *confidenceCache) get(key string) (*confidence.Report, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || time.Since(e.at) > ConfidenceTTL {
		return nil, false
	}
	return e.rep, true
}

func (c *confidenceCache) put(key string, rep *confidence.Report) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]confidenceEntry{}
	}
	c.m[key] = confidenceEntry{rep: rep, at: time.Now()}
}

// invalidate drops every cached assessment. Called when a portal is
// re-attached, since new data is exactly what the cache must not hide.
func (c *confidenceCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = nil
}

// Confidence assesses the data behind one query without running it.
//
// Useful on its own: "should I trust this analysis" is a question worth being
// able to ask before committing to the answer, and for a large corpus the
// profiling pass is much cheaper than the analysis.
func (s *Session) Confidence(ctx context.Context, modeName, queryName string) (*confidence.Report, error) {
	m, err := modes.Lookup(modeName)
	if err != nil {
		return nil, err
	}
	q, err := m.Query(queryName)
	if err != nil {
		return nil, err
	}
	return s.confidenceFor(ctx, m, *q), nil
}

// confidenceFor profiles every dataset the query reads across every attached
// portal that can answer it.
//
// It never fails the caller. A confidence score is commentary on an answer,
// and commentary that can take the answer down with it is worse than no
// commentary — a report that could not be produced says so through Assessed.
func (s *Session) confidenceFor(ctx context.Context, m *modes.Mode, q modes.Query) *confidence.Report {
	targets := s.confidenceTargets(m, q)

	key := confidenceKey(m.Name, q.Name, targets)
	if rep, ok := s.confCache.get(key); ok {
		return rep
	}

	rep := confidence.Assess(ctx, s.host, targets, confidence.Options{})
	rep.Mode, rep.Query = m.Name, q.Name
	s.confCache.put(key, rep)
	return rep
}

// confidenceTargets resolves the datasets one query reads, for each attached
// portal that can actually answer it.
//
// The "can answer it" filter matters: a city excluded from a comparison
// contributes no rows, so profiling its datasets would fold a dataset the
// answer never touched into the score for that answer.
func (s *Session) confidenceTargets(m *modes.Mode, q modes.Query) []confidence.Target {
	if len(m.Concepts) == 0 {
		// Bookkeeping-only modes (research) read _csq, which every csq
		// database carries by construction. There is no synced dataset behind
		// the answer to profile.
		return nil
	}
	bindings, err := s.bindingsFor(m)
	if err != nil {
		return nil
	}
	ports := s.snapshot()

	var out []confidence.Target
	for i, b := range bindings {
		if i >= len(ports) {
			break
		}
		if m.MultiPortal {
			if ok, _ := m.Comparable(q, b); !ok {
				continue
			}
		} else if ok, _ := m.Runnable(q, b); !ok {
			continue
		}
		out = append(out, confidence.TargetsFor(m, q, ports[i].Alias, b)...)
	}
	return out
}

// confidenceKey identifies an assessment by what it actually depends on: the
// query and the datasets and columns it reads. Two queries reading the same
// columns of the same datasets have the same data fitness, and should not be
// profiled twice.
func confidenceKey(mode, query string, targets []confidence.Target) string {
	parts := make([]string, 0, len(targets))
	for _, t := range targets {
		cols := append([]string{}, t.Columns...)
		sort.Strings(cols)
		parts = append(parts, t.Portal+"."+t.Table+"["+strings.Join(cols, ",")+"]")
	}
	sort.Strings(parts)
	return mode + "/" + query + "|" + strings.Join(parts, ";")
}
