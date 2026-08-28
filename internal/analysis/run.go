// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// Exclusion records a city left out of a comparison, and why.
//
// This is a first-class result field rather than a log line. A city that does
// not publish crime data must never render as safe, so every consumer is handed
// the exclusions alongside the rows and cannot show one without the other.
type Exclusion struct {
	City   string `json:"city"`
	Reason string `json:"reason"`
}

// Plan is an expanded, ready-to-execute query plus what was decided about it.
type Plan struct {
	SQL      string
	Excluded []Exclusion
	// Cities is how many cities actually contribute rows.
	Cities int
}

// NotAComparison reports whether a cross-city query ended up with a single
// contributing city. The number is still true; calling it a comparison is not.
func (p Plan) NotAComparison() bool { return p.Cities == 1 && len(p.Excluded) > 0 }

// Result is one executed query.
type Result struct {
	Mode     string      `json:"mode"`
	Query    string      `json:"query"`
	Title    string      `json:"title"`
	Desc     string      `json:"desc"`
	SQL      string      `json:"sql"`
	Columns  []string    `json:"columns"`
	Rows     [][]any     `json:"rows"`
	Excluded []Exclusion `json:"excluded"`
	// NotAComparison is set when only one city qualified for a cross-city query.
	NotAComparison bool     `json:"not_a_comparison"`
	Caveats        []string `json:"caveats"`
	// Truncated reports that the row cap was hit, so the table is a prefix.
	Truncated bool   `json:"truncated"`
	Elapsed   string `json:"elapsed"`
}

// PlanQuery expands one query for the attached portals.
//
// The three shapes mirror what the modes themselves declare: a cross-portal
// mode with concepts runs once per city and stacks the results; a single-portal
// mode with concepts runs against its one binding; a mode with no concepts
// (research) reads the _csq schema of every attached database directly.
func (s *Session) PlanQuery(m *modes.Mode, q modes.Query) (*Plan, error) {
	bindings, err := s.bindingsFor(m)
	if err != nil {
		return nil, err
	}
	ports := s.snapshot()
	aliases := make([]string, len(ports))
	for i, p := range ports {
		aliases[i] = p.Alias
	}

	switch {
	case m.MultiPortal && len(m.Concepts) > 0:
		return s.planPerCityUnion(m, q, aliases, bindings)

	case len(bindings) > 0:
		b := bindings[0]
		if ok, missing := m.Runnable(q, b); !ok {
			return nil, fmt.Errorf("%s does not publish %s",
				b.Portal, strings.Join(missing, ", "))
		}
		sqlText, err := modes.ExpandConceptsFor(m, q.SQL, aliases[0], b)
		if err != nil {
			return nil, err
		}
		return &Plan{SQL: sqlText, Cities: 1}, nil

	default:
		sqlText, err := modes.Expand(q.SQL, aliases)
		if err != nil {
			return nil, err
		}
		return &Plan{SQL: sqlText, Cities: len(aliases)}, nil
	}
}

// planPerCityUnion expands a single-city query once per attached city and
// stacks the results with a leading city column.
func (s *Session) planPerCityUnion(m *modes.Mode, q modes.Query,
	aliases []string, bindings []*modes.Binding) (*Plan, error) {

	plan := &Plan{}
	var parts []string
	for i, b := range bindings {
		if ok, why := m.Comparable(q, b); !ok {
			plan.Excluded = append(plan.Excluded, Exclusion{City: b.City, Reason: why})
			continue
		}
		one, err := modes.ExpandConceptsFor(m, q.SQL, aliases[i], b)
		if err != nil {
			return nil, err
		}
		parts = append(parts, fmt.Sprintf("SELECT '%s' AS city, * FROM (%s)",
			quoteLiteral(b.City), one))
	}
	if len(parts) == 0 {
		return nil, ErrNoComparableCity
	}
	plan.Cities = len(parts)
	plan.SQL = strings.Join(parts, "\nUNION ALL\n")
	return plan, nil
}

// Run executes one named query of one named mode.
//
// limit caps returned rows; 0 uses DefaultRowLimit. One extra row is fetched so
// the result can report that it was truncated rather than quietly handing back
// a prefix.
func (s *Session) Run(ctx context.Context, modeName, queryName string, limit int) (*Result, error) {
	m, err := modes.Lookup(modeName)
	if err != nil {
		return nil, err
	}
	q, err := m.Query(queryName)
	if err != nil {
		return nil, err
	}
	ports := s.snapshot()
	if len(ports) == 0 {
		return nil, fmt.Errorf("no data is loaded yet")
	}
	if !m.MultiPortal && len(ports) != 1 {
		return nil, fmt.Errorf("mode %q targets a single portal; %d are attached",
			m.Name, len(ports))
	}

	plan, err := s.PlanQuery(m, *q)
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = DefaultRowLimit
	}
	capped := fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", plan.SQL, limit+1)

	started := time.Now()
	cols, rows, err := s.query(ctx, capped)
	if err != nil {
		return nil, annotateMissingTable(err, m, ports)
	}

	res := &Result{
		Mode: m.Name, Query: q.Name, Title: m.Title, Desc: q.Desc,
		SQL: plan.SQL, Columns: cols, Rows: rows,
		Excluded: plan.Excluded, NotAComparison: plan.NotAComparison(),
		Caveats: m.Caveats, Elapsed: time.Since(started).Round(time.Millisecond).String(),
	}
	if len(rows) > limit {
		res.Rows = rows[:limit]
		res.Truncated = true
	}
	if res.Excluded == nil {
		res.Excluded = []Exclusion{}
	}
	return res, nil
}

// query runs SQL inside a read-only transaction on a pinned connection.
//
// Every portal is already attached READ_ONLY, so this is belt-and-braces — but
// it also covers the in-memory host, which has to be writable for ATTACH to
// work at startup. DuckDB spells it "BEGIN TRANSACTION READ ONLY"; there is no
// SET TRANSACTION form.
func (s *Session) query(ctx context.Context, sqlText string) ([]string, [][]any, error) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	conn, err := s.host.Conn(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN TRANSACTION READ ONLY`); err != nil {
		return nil, nil, fmt.Errorf("begin read-only: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(ctx, `ROLLBACK`) }()

	rows, err := conn.QueryContext(ctx, sqlText)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}

	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		for i, v := range vals {
			vals[i] = normalize(v)
		}
		out = append(out, vals)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	if out == nil {
		out = [][]any{}
	}
	return cols, out, nil
}

// normalize converts driver values into something that survives a JSON round
// trip without losing its type. Numbers stay numbers so the UI can chart them
// rather than parsing them back out of strings.
func normalize(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return v
	}
}

// annotateMissingTable turns DuckDB's binder error into the sentence a user can
// act on. A missing table here almost always means the mode's datasets were
// never synced, which is a setup step, not a broken query.
func annotateMissingTable(err error, m *modes.Mode, portals []Portal) error {
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		return err
	}
	portal := ""
	if len(portals) > 0 {
		portal = portals[0].Portal
	}
	return &NotSyncedError{Mode: m.Name, Portal: portal, Err: err}
}

// NotSyncedError means the mode's datasets are not in the database yet.
// Carrying it as a type lets an interface offer the fix — a button, or a
// printed command — instead of showing a binder error.
type NotSyncedError struct {
	Mode   string
	Portal string
	Err    error
}

func (e *NotSyncedError) Error() string {
	return fmt.Sprintf("mode %q has no data in this database yet", e.Mode)
}
func (e *NotSyncedError) Unwrap() error { return e.Err }

// FixCommand is the shell command that resolves this error.
func (e *NotSyncedError) FixCommand() string {
	p := e.Portal
	if p == "" {
		p = "<portal-host>"
	}
	return fmt.Sprintf("csq modes init %s --portal %s --output %s.yaml && csq sync --config %s.yaml",
		e.Mode, p, e.Mode, e.Mode)
}
