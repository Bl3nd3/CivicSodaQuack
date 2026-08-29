// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Signal names. Stable slugs: interfaces group and filter on them, and the
// score caps in finalize key off two of them.
const (
	SignalSync          = "sync"
	SignalRowIntegrity  = "row_integrity"
	SignalCompleteness  = "completeness"
	SignalFreshness     = "freshness"
	SignalLag           = "local_lag"
	SignalDateRange     = "date_range"
	SignalNullDensity   = "null_density"
	SignalKeyIntegrity  = "key_integrity"
	SignalConcentration = "concentration"
)

// Target is one dataset a query reads, described in the mode's own vocabulary.
type Target struct {
	// Portal is the alias of the attached database holding the table.
	Portal string
	// Table is the physical DuckDB table name.
	Table string
	// DatasetID is the Socrata 4x4, used to find bookkeeping rows.
	DatasetID string
	// Name is the upstream dataset title.
	Name string
	// Concept is the logical role this dataset plays in the mode.
	Concept string
	// ExpectedRows is the reference row count to measure completeness
	// against. Zero means none is known, which makes completeness Unknown
	// rather than failing.
	ExpectedRows int64

	// View is a SELECT projecting this dataset into the mode's canonical
	// column names. Profiling runs against it rather than against the raw
	// table so that this package speaks the same vocabulary the query does —
	// and so a binding that maps a column through an expression (a text date
	// parsed with try_strptime, say) is profiled as the query will actually
	// see it, not as it happens to be stored.
	View string

	// Columns are the canonical column names this query reads. Only these are
	// profiled: a clean bill of health on columns the query ignores would be
	// an answer to a question nobody asked.
	Columns []string
}

// Queryer is the read side of a *sql.DB. Taking the interface keeps this
// package testable without a live portal database and makes it usable from
// anywhere a read-only session already exists.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// Options tunes an assessment.
type Options struct {
	// Timeout bounds the whole profiling pass. Zero uses DefaultTimeout.
	Timeout time.Duration
	// Now overrides the clock, for tests.
	Now time.Time
}

// DefaultTimeout bounds a profiling pass. Profiling is one aggregate scan per
// dataset, which is cheap in DuckDB but not free on a hundred-million-row
// table, and a confidence score is never worth blocking an answer on.
const DefaultTimeout = 20 * time.Second

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

func (o Options) timeout() time.Duration {
	if o.Timeout <= 0 {
		return DefaultTimeout
	}
	return o.Timeout
}

// Assess profiles every target and returns the report for the query.
//
// It never returns an error for a dataset it could not measure. A portal whose
// bookkeeping is missing, a table that was dropped, a profiling query that
// timed out — each becomes an Unknown signal naming what could not be checked,
// because "we could not verify this" is information the reader needs and an
// error swallowed at this layer would take the whole answer down with it.
func Assess(ctx context.Context, q Queryer, targets []Target, opts Options) *Report {
	started := time.Now()
	rep := &Report{}
	if len(targets) == 0 {
		rep.finalize()
		rep.Elapsed = time.Since(started).Round(time.Millisecond).String()
		return rep
	}

	ctx, cancel := context.WithTimeout(ctx, opts.timeout())
	defer cancel()

	books := loadBookkeeping(ctx, q, targets)

	for _, t := range targets {
		rep.Datasets = append(rep.Datasets, assessOne(ctx, q, t, books[bookKey(t)], opts))
	}

	rep.finalize()
	rep.Elapsed = time.Since(started).Round(time.Millisecond).String()
	return rep
}

// bookRecord is what the _csq schema knows about one dataset.
type bookRecord struct {
	found bool

	lastStatus   string
	lastStarted  *time.Time
	lastFinished *time.Time
	lastError    string

	okAt   *time.Time
	okRows *int64

	upstreamUpdated *time.Time
	catalogRows     *int64
}

func bookKey(t Target) string { return t.Portal + "\x00" + t.DatasetID }

// loadBookkeeping reads sync history and catalog metadata for every target, one
// query per attached portal.
func loadBookkeeping(ctx context.Context, q Queryer, targets []Target) map[string]bookRecord {
	out := map[string]bookRecord{}

	byPortal := map[string][]string{}
	for _, t := range targets {
		if t.DatasetID == "" {
			continue
		}
		byPortal[t.Portal] = append(byPortal[t.Portal], t.DatasetID)
	}

	for portal, ids := range byPortal {
		values := make([]string, 0, len(ids))
		for _, id := range ids {
			values = append(values, "('"+quoteLiteral(id)+"')")
		}
		// Three facts per dataset, joined onto the id list so a dataset with no
		// sync history at all still comes back as a row: the most recent run
		// whatever its outcome, the most recent successful one, and what the
		// portal catalog says. A LEFT JOIN from the ids is what distinguishes
		// "never synced" from "not asked about".
		sqlText := fmt.Sprintf(`
WITH ids(dataset_id) AS (VALUES %s),
latest AS (
  SELECT dataset_id, status, started_at, finished_at, error FROM (
    SELECT dataset_id, status, started_at, finished_at, error,
           ROW_NUMBER() OVER (PARTITION BY dataset_id ORDER BY started_at DESC) AS rn
    FROM %s._csq.sync_runs
  ) WHERE rn = 1
),
lastok AS (
  SELECT dataset_id,
         MAX(started_at)                     AS ok_at,
         ARG_MAX(rows_written, started_at)   AS ok_rows
  FROM %s._csq.sync_runs WHERE status = 'ok' GROUP BY dataset_id
)
SELECT i.dataset_id, l.status, l.started_at, l.finished_at, l.error,
       o.ok_at, o.ok_rows, c.updated_at, c.row_count
FROM ids i
LEFT JOIN latest l USING (dataset_id)
LEFT JOIN lastok o USING (dataset_id)
LEFT JOIN %s._csq.catalog c ON c.id = i.dataset_id`,
			strings.Join(values, ", "), portal, portal, portal)

		rows, err := q.QueryContext(ctx, sqlText)
		if err != nil {
			// No _csq schema, or an unreadable one. Every dataset on this
			// portal degrades to Unknown bookkeeping signals.
			continue
		}
		for rows.Next() {
			var id string
			var status, errMsg sql.NullString
			var started, finished, okAt, upstream sql.NullTime
			var okRows, catRows sql.NullInt64
			if err := rows.Scan(&id, &status, &started, &finished, &errMsg,
				&okAt, &okRows, &upstream, &catRows); err != nil {
				continue
			}
			br := bookRecord{found: true, lastStatus: status.String, lastError: errMsg.String}
			br.lastStarted = timePtr(started)
			br.lastFinished = timePtr(finished)
			br.okAt = timePtr(okAt)
			br.upstreamUpdated = timePtr(upstream)
			if okRows.Valid {
				v := okRows.Int64
				br.okRows = &v
			}
			if catRows.Valid {
				v := catRows.Int64
				br.catalogRows = &v
			}
			out[portal+"\x00"+id] = br
		}
		rows.Close()
	}
	return out
}

func timePtr(t sql.NullTime) *time.Time {
	if !t.Valid {
		return nil
	}
	v := t.Time
	return &v
}

// assessOne profiles a single dataset and builds its signals.
func assessOne(ctx context.Context, q Queryer, t Target, book bookRecord, opts Options) DatasetReport {
	d := DatasetReport{
		Table: t.Table, DatasetID: t.DatasetID, Name: t.Name,
		Portal: t.Portal, Concept: t.Concept, ExpectedRows: t.ExpectedRows,
		UpstreamUpdated: book.upstreamUpdated, LastSynced: book.okAt,
	}
	label := t.Table

	// Sync history first: if the data never landed, everything measured on the
	// table afterwards describes an empty or stale artefact.
	d.Signals = append(d.Signals, syncSignal(book, label))

	types, terr := describeView(ctx, q, t)
	prof, perr := profile(ctx, q, t, types, opts.now())

	switch {
	case terr != nil || perr != nil:
		d.Signals = append(d.Signals, Signal{
			Name: SignalNullDensity, Level: Unknown, Floor: FloorNulls, Dataset: label,
			Label:  fmt.Sprintf("could not profile %s", label),
			Detail: firstErr(terr, perr),
		})
	default:
		d.Rows = prof.rows
		d.Signals = append(d.Signals, completenessSignal(prof.rows, t.ExpectedRows, book, label))
		d.Signals = append(d.Signals, rowIntegritySignal(prof.rows, book, label))
		d.Signals = append(d.Signals, nullSignal(prof, label))
		if s, ok := dateSignal(prof, label); ok {
			d.Signals = append(d.Signals, s)
		}
		if s, ok := keySignal(prof, label); ok {
			d.Signals = append(d.Signals, s)
		}
	}

	d.Signals = append(d.Signals, freshnessSignal(book, label, opts.now()))
	d.Signals = append(d.Signals, lagSignal(book, label))
	return d
}

func firstErr(errs ...error) string {
	for _, e := range errs {
		if e != nil {
			return e.Error()
		}
	}
	return ""
}

// describeView returns the DuckDB type of each canonical column, by asking
// DuckDB to describe the projection rather than inferring from the raw table.
// A binding may map a column through an expression, and the expression's type
// is the one the query will actually see.
func describeView(ctx context.Context, q Queryer, t Target) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, "DESCRIBE "+t.View)
	if err != nil {
		return nil, fmt.Errorf("describe %s: %w", t.Table, err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		// DESCRIBE returns column_name, column_type first; the rest vary by
		// version and are not needed here.
		if len(vals) >= 2 {
			out[strings.ToLower(asString(vals[0]))] = strings.ToUpper(asString(vals[1]))
		}
	}
	return out, rows.Err()
}

func asString(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// role classifies a canonical column for profiling purposes.
type role int

const (
	roleValue role = iota
	roleDate
	roleKey
)

func roleFor(name, dtype string) role {
	switch {
	case strings.HasPrefix(dtype, "DATE"), strings.HasPrefix(dtype, "TIMESTAMP"):
		return roleDate
	case name == "socrata_id" || strings.HasSuffix(name, "_id"):
		return roleKey
	}
	return roleValue
}

// colProfile is what one aggregate scan learned about one column.
type colProfile struct {
	name     string
	role     role
	nulls    int64
	past     int64 // values before minPlausibleYear
	future   int64 // values beyond today
	distinct int64
}

// tableProfile is the result of the single scan over one dataset.
type tableProfile struct {
	rows int64
	cols []colProfile
}

// minPlausibleDate is the floor for a civic date. Nothing in an open-data
// portal legitimately predates it, and sentinel nulls encoded as year 0001 or
// 1900-01-01 are common enough to be worth catching by name.
const minPlausibleDate = "1677-09-22"

// futureSlack is how far past now a timestamp may sit before it is impossible.
// Permits are issued with future effective dates and portals disagree about
// time zones, so a day or two of slack avoids flagging normal records.
const futureSlack = "2 DAY"

// profile runs one aggregate scan per dataset, computing every column check at
// once. One scan rather than one per column is what makes this affordable to
// run on the path of an ordinary query.
func profile(ctx context.Context, q Queryer, t Target, types map[string]string, now time.Time) (tableProfile, error) {
	var tp tableProfile

	seen := map[string]bool{}
	sel := []string{"COUNT(*)"}
	for _, c := range t.Columns {
		lc := strings.ToLower(c)
		if seen[lc] {
			continue
		}
		dtype, known := types[lc]
		if !known {
			// The binding does not project this column, so the query cannot be
			// reading it. Nothing to profile.
			continue
		}
		seen[lc] = true
		cp := colProfile{name: c, role: roleFor(lc, dtype)}
		qc := `"` + strings.ReplaceAll(c, `"`, `""`) + `"`

		sel = append(sel, fmt.Sprintf("COUNT(*) FILTER (WHERE %s IS NULL)", qc))
		switch cp.role {
		case roleDate:
			sel = append(sel,
				fmt.Sprintf("COUNT(*) FILTER (WHERE %s < DATE '%s')", qc, minPlausibleDate),
				fmt.Sprintf("COUNT(*) FILTER (WHERE %s > (TIMESTAMP '%s' + INTERVAL %s))",
					qc, now.UTC().Format("2006-01-02 15:04:05"), futureSlack))
		case roleKey:
			sel = append(sel, fmt.Sprintf("COUNT(DISTINCT %s)", qc))
		}
		tp.cols = append(tp.cols, cp)
	}

	rows, err := q.QueryContext(ctx,
		"SELECT "+strings.Join(sel, ", ")+" FROM ("+t.View+")")
	if err != nil {
		return tp, fmt.Errorf("profile %s: %w", t.Table, err)
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return tp, err
		}
		return tp, fmt.Errorf("profile %s: no result", t.Table)
	}

	scan := make([]any, len(sel))
	ptrs := make([]any, len(sel))
	for i := range scan {
		ptrs[i] = &scan[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return tp, err
	}

	tp.rows = asInt64(scan[0])
	at := 1
	for i := range tp.cols {
		tp.cols[i].nulls = asInt64(scan[at])
		at++
		switch tp.cols[i].role {
		case roleDate:
			tp.cols[i].past = asInt64(scan[at])
			tp.cols[i].future = asInt64(scan[at+1])
			at += 2
		case roleKey:
			tp.cols[i].distinct = asInt64(scan[at])
			at++
		}
	}
	return tp, rows.Err()
}

func asInt64(v any) int64 {
	switch t := v.(type) {
	case int64:
		return t
	case int32:
		return int64(t)
	case int:
		return int64(t)
	case float64:
		return int64(t)
	case nil:
		return 0
	}
	return 0
}

func quoteLiteral(s string) string { return strings.ReplaceAll(s, "'", "''") }
