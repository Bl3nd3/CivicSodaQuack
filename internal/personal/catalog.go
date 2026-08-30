// Copyright (c) 2026 Neomantra Corp

// Package personal turns a question in English into a mode file.
//
// The mode it writes is an ordinary csq mode: concepts, SQL, caveats, a
// binding. It is loaded by the same parser, validated by the same rules, run by
// the same runner, and scored by the same confidence arithmetic as the built-in
// modes. Nothing about a generated mode is privileged, and that is the design —
// the model's output is a text file the user owns, reads, edits, and re-runs,
// not an answer they have to take on trust.
//
// The model never sees a row of data unless the user asks for samples, never
// executes anything, and never reports a number. It reads a schema inventory
// and writes SQL. DuckDB produces every figure the user is shown.
package personal

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Column is one column of a synced table, as DuckDB reports it.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Samples holds a few distinct non-null values, present only when the user
	// asked for them. They make generated filters land on real category
	// spellings instead of plausible guesses.
	Samples []string `json:"samples,omitempty"`
}

// Table is one synced dataset held locally.
type Table struct {
	Name    string   `json:"table"`
	Rows    int64    `json:"rows"`
	Columns []Column `json:"columns"`

	// DatasetID and DatasetName come from _csq.catalog, and are what a binding
	// must record. Without them a generated binding cannot cite its source.
	DatasetID   string `json:"dataset_id,omitempty"`
	DatasetName string `json:"dataset_name,omitempty"`
	Description string `json:"description,omitempty"`
}

// Portal is everything csq knows about one attached database.
type Portal struct {
	Alias  string  `json:"alias"`
	Host   string  `json:"portal"`
	Path   string  `json:"-"`
	Tables []Table `json:"tables"`
}

// Describe inventories one attached database: its tables, their row counts,
// their columns, and the upstream dataset each came from.
//
// Everything here is metadata. The one exception is column samples, which read
// actual values and are therefore opt-in — see SampleColumns.
func Describe(db *sql.DB, alias, host string) (*Portal, error) {
	p := &Portal{Alias: alias, Host: host}

	rows, err := db.Query(`
SELECT table_name, COALESCE(estimated_size, 0)
FROM duckdb_tables()
WHERE database_name = ? AND schema_name = 'main'
ORDER BY table_name`, alias)
	if err != nil {
		return nil, fmt.Errorf("inventory %s: %w", alias, err)
	}
	byName := map[string]*Table{}
	for rows.Next() {
		var t Table
		if err := rows.Scan(&t.Name, &t.Rows); err != nil {
			rows.Close()
			return nil, err
		}
		p.Tables = append(p.Tables, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range p.Tables {
		byName[p.Tables[i].Name] = &p.Tables[i]
	}
	if len(p.Tables) == 0 {
		return p, nil
	}

	if err := loadColumns(db, alias, byName); err != nil {
		return nil, err
	}
	// Provenance is best-effort: a database synced by an older csq, or one
	// assembled by hand, still has usable tables. A mode authored without
	// dataset ids is worse — the binding cannot cite its source — but it is far
	// better than refusing to work at all.
	_ = loadProvenance(db, alias, byName)

	return p, nil
}

func loadColumns(db *sql.DB, alias string, byName map[string]*Table) error {
	rows, err := db.Query(`
SELECT table_name, column_name, data_type
FROM duckdb_columns()
WHERE database_name = ? AND schema_name = 'main'
ORDER BY table_name, column_index`, alias)
	if err != nil {
		return fmt.Errorf("read columns for %s: %w", alias, err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, col, typ string
		if err := rows.Scan(&table, &col, &typ); err != nil {
			return err
		}
		if t, ok := byName[table]; ok {
			t.Columns = append(t.Columns, Column{Name: col, Type: typ})
		}
	}
	return rows.Err()
}

// loadProvenance attaches the upstream dataset id and title to each table, via
// the sync run that wrote it.
func loadProvenance(db *sql.DB, alias string, byName map[string]*Table) error {
	q := fmt.Sprintf(`
SELECT r.table_name, r.dataset_id,
       COALESCE(c.name, ''), COALESCE(c.description, '')
FROM %s._csq.sync_runs r
LEFT JOIN %s._csq.catalog c ON c.id = r.dataset_id
WHERE r.status = 'ok'
QUALIFY ROW_NUMBER() OVER (PARTITION BY r.table_name ORDER BY r.started_at DESC) = 1`,
		alias, alias)
	rows, err := db.Query(q)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var table, id, name, desc string
		if err := rows.Scan(&table, &id, &name, &desc); err != nil {
			return err
		}
		if t, ok := byName[table]; ok {
			t.DatasetID, t.DatasetName, t.Description = id, name, desc
		}
	}
	return rows.Err()
}

// maxSampleValues caps how many distinct values are read per column. A handful
// is enough to show the model how a category is spelled; more is data transfer
// with no added signal.
const maxSampleValues = 8

// sampleCardinalityCeiling bounds which columns are worth sampling. A column
// with thousands of distinct values is a free-text or identifier field, and a
// sample of it teaches the model nothing about how to filter on it.
const sampleCardinalityCeiling = 40

// SampleColumns fills in a few distinct values for low-cardinality text columns.
//
// This is the only part of csq that reads data on the model's behalf, so it is
// opt-in and the caller says so in the output. The payoff is real: without it a
// generated filter says department = 'Streets and Sanitation' where the portal
// actually writes 'STREETS & SAN', and the query returns zero rows that look
// like a finding.
func SampleColumns(db *sql.DB, p *Portal) error {
	for ti := range p.Tables {
		t := &p.Tables[ti]
		if t.Rows == 0 {
			continue
		}
		for ci := range t.Columns {
			c := &t.Columns[ci]
			if !isTextType(c.Type) {
				continue
			}
			vals, err := sampleColumn(db, p.Alias, t.Name, c.Name)
			if err != nil {
				// One unreadable column must not abort an inventory; the model
				// simply gets no samples for it.
				continue
			}
			c.Samples = vals
		}
	}
	return nil
}

func sampleColumn(db *sql.DB, alias, table, column string) ([]string, error) {
	// Bounded by both the distinct ceiling and the row scan: on a 40M-row table
	// an unbounded COUNT(DISTINCT) is a minute of wall clock for a hint.
	q := fmt.Sprintf(`
WITH scanned AS (
  SELECT %s AS v FROM %s.main.%s WHERE %s IS NOT NULL LIMIT 50000
),
distinct_vals AS (SELECT DISTINCT v FROM scanned LIMIT %d)
SELECT v FROM distinct_vals ORDER BY v LIMIT %d`,
		quoteIdent(column), quoteIdent(alias), quoteIdent(table), quoteIdent(column),
		sampleCardinalityCeiling+1, sampleCardinalityCeiling+1)

	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var v sql.NullString
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		if v.Valid {
			out = append(out, v.String)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Over the ceiling means free text or an identifier: no useful sample.
	if len(out) > sampleCardinalityCeiling {
		return nil, nil
	}
	if len(out) > maxSampleValues {
		out = out[:maxSampleValues]
	}
	return out, nil
}

func isTextType(t string) bool {
	u := strings.ToUpper(t)
	return strings.Contains(u, "VARCHAR") || strings.Contains(u, "CHAR") ||
		u == "TEXT" || u == "STRING"
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// Brief renders the inventory as the compact text the model reads.
//
// It is written for a reader with no access to the database: every table, its
// size, and every column with its type, because a column the model cannot see
// is a column it will invent.
func (p *Portal) Brief() string {
	var b strings.Builder
	fmt.Fprintf(&b, "portal: %s\n", p.Host)
	fmt.Fprintf(&b, "tables held locally: %d\n\n", len(p.Tables))

	tables := append([]Table(nil), p.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Rows > tables[j].Rows })

	for _, t := range tables {
		fmt.Fprintf(&b, "TABLE %s  (%s rows)\n", t.Name, commas(t.Rows))
		if t.DatasetID != "" {
			fmt.Fprintf(&b, "  dataset_id: %s\n", t.DatasetID)
		}
		if t.DatasetName != "" {
			fmt.Fprintf(&b, "  dataset_name: %s\n", t.DatasetName)
		}
		if d := strings.TrimSpace(t.Description); d != "" {
			fmt.Fprintf(&b, "  description: %s\n", truncate(collapse(d), 300))
		}
		fmt.Fprintf(&b, "  columns:\n")
		for _, c := range t.Columns {
			fmt.Fprintf(&b, "    %s %s", c.Name, c.Type)
			if len(c.Samples) > 0 {
				fmt.Fprintf(&b, "  e.g. %s", strings.Join(quoteAll(c.Samples), ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// TableNames lists the local tables, for error messages that need to say what
// was actually available.
func (p *Portal) TableNames() []string {
	out := make([]string, 0, len(p.Tables))
	for _, t := range p.Tables {
		out = append(out, t.Name)
	}
	sort.Strings(out)
	return out
}

// Table returns the named table.
func (p *Portal) Table(name string) (Table, bool) {
	for _, t := range p.Tables {
		if strings.EqualFold(t.Name, name) {
			return t, true
		}
	}
	return Table{}, false
}

func quoteAll(vals []string) []string {
	out := make([]string, 0, len(vals))
	for _, v := range vals {
		out = append(out, "'"+truncate(collapse(v), 60)+"'")
	}
	return out
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func commas(n int64) string {
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
