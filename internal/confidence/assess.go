// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
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
		Portal: t.Portal, Concept: t.Concept, Expected: t.ExpectedRows,
		UpstreamUpdated: book.upstreamUpdated, LastSynced: book.okAt,
	}
	if d.Expected == 0 && book.catalogRows != nil {
		d.Expected = *book.catalogRows
	}
	label := t.Table

	// How the data got here, and whether anything went wrong. Diagnostic: it
	// explains a shortfall that completeness has already counted.
	d.Signals = append(d.Signals, syncSignal(book, label))

	types, terr := describeView(ctx, q, t)
	prof, perr := profile(ctx, q, t, types, opts.now())

	if terr != nil || perr != nil {
		// The table could not be read at all, so neither factor of U/E can be
		// measured. Both are Unknown rather than zero: "we could not look" and
		// "we looked and found nothing" call for opposite responses.
		why := firstErr(terr, perr)
		d.Signals = append(d.Signals,
			Signal{Name: SignalCompleteness, Kind: Scored, Level: Unknown, Dataset: label,
				Label: fmt.Sprintf("could not read %s", label), Detail: why},
			Signal{Name: SignalUsability, Kind: Scored, Level: Unknown, Dataset: label,
				Label: fmt.Sprintf("could not examine the columns of %s", label), Detail: why})
	} else {
		d.Held, d.Rows, d.Usable = prof.rows, prof.rows, prof.usable

		completeness := completenessSignal(d.Held, t.ExpectedRows, book.catalogRows, label)
		usability := usabilitySignal(prof, label)
		d.Signals = append(d.Signals, completeness, usability)

		// Record the factorisation so a reader can reconstruct r without
		// re-deriving it from the signal list.
		d.Completeness, d.Usability = 1, 1
		if completeness.Level != Unknown {
			d.Completeness = clamp01(completeness.Score)
		}
		if usability.Level != Unknown {
			d.Usability = clamp01(usability.Score)
		}
		d.Retention = d.Completeness * d.Usability
	}

	// Reported beside R, never folded into it. See the package doc.
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
)

// roleFor classifies a canonical column. Only dates need distinguishing: they
// carry a plausibility condition beyond being non-null. A null identifier is
// just a null column — it drops the row from a join exactly as any other
// missing value drops it from a grouping, and needs no separate rule.
func roleFor(_, dtype string) role {
	if strings.HasPrefix(dtype, "DATE") || strings.HasPrefix(dtype, "TIMESTAMP") {
		return roleDate
	}
	return roleValue
}

// colProfile is what one aggregate scan learned about one column. The counts
// are diagnostic: they name which column is at fault. The score comes from the
// joint count in tableProfile.usable.
type colProfile struct {
	name   string
	role   role
	nulls  int64
	past   int64 // values before minPlausibleDate
	future int64 // values beyond today
}

// tableProfile is the result of the single scan over one dataset.
type tableProfile struct {
	rows int64
	// usable is U: rows in which every column the query reads carries usable
	// information. Measured as one joint condition rather than combined from
	// per-column rates, because nulls in civic data cluster heavily and
	// assuming independence would overstate the loss.
	usable int64
	cols   []colProfile
}

// minPlausibleDate is the floor for a civic date. It is DuckDB's own timestamp
// minimum rather than a chosen cutoff: below it a value cannot be represented,
// let alone be real. Sentinel nulls encoded as year 0001 land here.
const minPlausibleDate = "1677-09-22"

// futureSlack is how far past now a timestamp may sit before it is impossible.
// Permits carry future effective dates and portals disagree about time zones,
// so a couple of days of slack avoids flagging ordinary records.
const futureSlack = "2 DAY"

// profile runs one aggregate scan per dataset, computing the row count, the
// joint usable count, and the per-column diagnosis together. One scan rather
// than one per column is what makes this affordable on the path of an ordinary
// query.
func profile(ctx context.Context, q Queryer, t Target, types map[string]string, now time.Time) (tableProfile, error) {
	var tp tableProfile

	nowLit := now.UTC().Format("2006-01-02 15:04:05")
	seen := map[string]bool{}
	sel := []string{"COUNT(*)"}
	var unusable []string // per-column reasons a row fails to survive

	for _, c := range t.Columns {
		lc := strings.ToLower(c)
		if seen[lc] {
			continue
		}
		dtype, known := types[lc]
		if !known {
			// The binding does not project this column, so the query cannot be
			// reading it. Nothing to examine.
			continue
		}
		seen[lc] = true
		cp := colProfile{name: c, role: roleFor(lc, dtype)}
		qc := `"` + strings.ReplaceAll(c, `"`, `""`) + `"`

		sel = append(sel, fmt.Sprintf("COUNT(*) FILTER (WHERE %s IS NULL)", qc))
		unusable = append(unusable, fmt.Sprintf("%s IS NULL", qc))

		if cp.role == roleDate {
			past := fmt.Sprintf("%s < DATE '%s'", qc, minPlausibleDate)
			future := fmt.Sprintf("%s > (TIMESTAMP '%s' + INTERVAL %s)", qc, nowLit, futureSlack)
			sel = append(sel,
				fmt.Sprintf("COUNT(*) FILTER (WHERE %s)", past),
				fmt.Sprintf("COUNT(*) FILTER (WHERE %s)", future))
			unusable = append(unusable, past, future)
		}
		tp.cols = append(tp.cols, cp)
	}

	// U in one filter: a row survives when none of the failure conditions hold.
	usableExpr := "COUNT(*)"
	if len(unusable) > 0 {
		usableExpr = fmt.Sprintf("COUNT(*) FILTER (WHERE NOT (%s))",
			strings.Join(unusable, " OR "))
	}
	sel = append(sel, usableExpr)

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
		if tp.cols[i].role == roleDate {
			tp.cols[i].past = asInt64(scan[at])
			tp.cols[i].future = asInt64(scan[at+1])
			at += 2
		}
	}
	tp.usable = asInt64(scan[at])
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
