// Copyright (c) 2026 Neomantra Corp

package main

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

const queryUsage = `csq query — run read-only SQL against one or more portal DuckDBs

Usage:
  csq query --db <file> [--db alias=file ...] [options] <SQL>
  csq query --db <file> --file <query.sql> [options]

Options:
  --db          Portal DuckDB to attach (repeatable; 'path' or 'alias=path')
  --file        Read SQL from a file instead of the command line
  --format      table | csv | json | parquet   (default: table)
  --output      Write to this file instead of stdout (required for parquet)
  --limit       Wrap the query in a row cap (0 = no cap)
  --no-header   Omit the header row (csv only)

Every portal is attached READ_ONLY and the query runs inside a read-only
transaction, so this can never modify a database. With exactly one --db that
portal is also made the default, so unqualified table names resolve; with
several, qualify as <alias>.main.<table>.

Examples:
  csq query --db chicago.duckdb "SELECT * FROM _csq.catalog LIMIT 5"
  csq query --db chicago.duckdb --format csv --output out.csv "SELECT * FROM crimes"
  csq query --db a.duckdb --db b.duckdb \
    "SELECT * FROM a.main.x UNION ALL SELECT * FROM b.main.x"
`

// cmdContext is the context used for query execution. Split out so a future
// --timeout flag has one place to hook into.
func cmdContext() context.Context { return context.Background() }

func runQuery(args []string) error {
	fs := flag.NewFlagSet("query", flag.ContinueOnError)
	var (
		dbArgs   []string
		sqlFile  string
		format   string
		output   string
		limit    int
		noHeader bool
	)
	fs.StringArrayVar(&dbArgs, "db", nil, "Portal DuckDB to attach (repeatable)")
	fs.StringVar(&sqlFile, "file", "", "Read SQL from this file")
	fs.StringVar(&format, "format", "table", "Output format: table|csv|json|parquet")
	fs.StringVar(&output, "output", "", "Write to this file instead of stdout")
	fs.IntVar(&limit, "limit", 0, "Cap returned rows (0 = no cap)")
	fs.BoolVar(&noHeader, "no-header", false, "Omit the header row (csv only)")
	fs.Usage = func() { fmt.Fprint(os.Stderr, queryUsage) }

	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(dbArgs) == 0 {
		fmt.Fprint(os.Stderr, queryUsage)
		return fmt.Errorf("--db is required")
	}

	query, err := resolveQueryText(fs.Args(), sqlFile)
	if err != nil {
		return err
	}

	format = strings.ToLower(format)
	switch format {
	case "table", "csv", "json", "parquet":
	default:
		return fmt.Errorf("unknown --format %q (want table, csv, json, or parquet)", format)
	}
	if format == "parquet" && output == "" {
		return fmt.Errorf("--format parquet requires --output (parquet is binary)")
	}

	paths, aliases, err := resolveQueryDBs(dbArgs)
	if err != nil {
		return err
	}

	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return fmt.Errorf("open host: %w", err)
	}
	defer db.Close()

	for i, path := range paths {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("--db %s: %w", path, err)
		}
		if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
			strings.ReplaceAll(path, "'", "''"), aliases[i])); err != nil {
			return fmt.Errorf("attach %s: %w", path, err)
		}
	}
	// With a single portal, make it the default so unqualified names resolve.
	if len(aliases) == 1 {
		if _, err := db.Exec("USE " + aliases[0]); err != nil {
			return fmt.Errorf("use %s: %w", aliases[0], err)
		}
	}

	if limit > 0 {
		query = fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", query, limit)
	}

	// Parquet is written by the engine; everything else streams through Go.
	if format == "parquet" {
		stmt := fmt.Sprintf("COPY (%s) TO '%s' (FORMAT PARQUET)",
			query, strings.ReplaceAll(output, "'", "''"))
		if _, err := db.Exec(stmt); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[csq] wrote %s\n", output)
		return nil
	}

	out := io.Writer(os.Stdout)
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return err
		}
		defer f.Close()
		out = f
	}

	// A read-only transaction is belt-and-braces: every portal is already
	// attached READ_ONLY, but this also blocks writes to the in-memory host.
	// Must run on one pinned connection, and DuckDB spells this
	// "BEGIN TRANSACTION READ ONLY" — there is no SET TRANSACTION form.
	conn, err := db.Conn(cmdContext())
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(cmdContext(), `BEGIN TRANSACTION READ ONLY`); err != nil {
		return fmt.Errorf("begin read-only: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(cmdContext(), `ROLLBACK`) }()

	rows, err := conn.QueryContext(cmdContext(), query)
	if err != nil {
		return err
	}
	defer rows.Close()

	switch format {
	case "csv":
		err = writeCSV(out, rows, !noHeader)
	case "json":
		err = writeJSON(out, rows)
	default:
		err = writeTable(out, rows)
	}
	if err != nil {
		return err
	}
	if output != "" {
		fmt.Fprintf(os.Stderr, "[csq] wrote %s\n", output)
	}
	return nil
}

// resolveQueryText returns the SQL from positional args or --file, requiring
// exactly one source.
func resolveQueryText(positional []string, sqlFile string) (string, error) {
	inline := strings.TrimSpace(strings.Join(positional, " "))
	switch {
	case sqlFile != "" && inline != "":
		return "", fmt.Errorf("pass SQL either positionally or via --file, not both")
	case sqlFile != "":
		b, err := os.ReadFile(sqlFile)
		if err != nil {
			return "", err
		}
		q := strings.TrimSpace(string(b))
		if q == "" {
			return "", fmt.Errorf("%s is empty", sqlFile)
		}
		return strings.TrimSuffix(q, ";"), nil
	case inline != "":
		return strings.TrimSuffix(inline, ";"), nil
	default:
		return "", fmt.Errorf("no SQL given (pass it positionally or with --file)")
	}
}

// resolveQueryDBs splits 'alias=path' / 'path' arguments into parallel slices,
// deriving aliases from filenames where not given.
func resolveQueryDBs(args []string) (paths, aliases []string, err error) {
	var unnamed []string
	explicit := make(map[int]string, len(args))
	for i, raw := range args {
		if j := strings.IndexByte(raw, '='); j >= 0 {
			alias, path := raw[:j], raw[j+1:]
			if alias == "" || path == "" {
				return nil, nil, fmt.Errorf("--db %q: expected alias=path", raw)
			}
			paths = append(paths, path)
			explicit[i] = alias
			unnamed = append(unnamed, "")
			continue
		}
		paths = append(paths, raw)
		unnamed = append(unnamed, raw)
	}
	derived := modes.UniqueAliases(paths)
	aliases = make([]string, len(paths))
	for i := range paths {
		if a, ok := explicit[i]; ok {
			aliases[i] = a
			continue
		}
		aliases[i] = derived[i]
	}
	seen := map[string]bool{}
	for _, a := range aliases {
		if seen[a] {
			return nil, nil, fmt.Errorf("alias collision on %q; pass alias=path to disambiguate", a)
		}
		seen[a] = true
	}
	return paths, aliases, nil
}

func writeTable(out io.Writer, rows *sql.Rows) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	seps := make([]string, len(cols))
	for i, c := range cols {
		seps[i] = strings.Repeat("-", len(c))
	}
	fmt.Fprintln(tw, strings.Join(seps, "\t"))

	n := 0
	err = scanRows(rows, func(vals []any) error {
		cells := make([]string, len(vals))
		for i, v := range vals {
			cells[i] = renderCell(v)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
		n++
		return nil
	})
	if err != nil {
		return err
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "(%d rows)\n", n)
	return nil
}

func writeCSV(out io.Writer, rows *sql.Rows, header bool) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	w := csv.NewWriter(out)
	defer w.Flush()
	if header {
		if err := w.Write(cols); err != nil {
			return err
		}
	}
	return scanRows(rows, func(vals []any) error {
		rec := make([]string, len(vals))
		for i, v := range vals {
			if v == nil {
				rec[i] = ""
				continue
			}
			rec[i] = renderCell(v)
		}
		return w.Write(rec)
	})
}

func writeJSON(out io.Writer, rows *sql.Rows) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	var records []map[string]any
	err = scanRows(rows, func(vals []any) error {
		rec := make(map[string]any, len(cols))
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				rec[cols[i]] = string(b)
				continue
			}
			rec[cols[i]] = v
		}
		records = append(records, rec)
		return nil
	})
	if err != nil {
		return err
	}
	if records == nil {
		records = []map[string]any{}
	}
	return enc.Encode(records)
}

// scanRows iterates rows, handing each row's values to fn.
func scanRows(rows *sql.Rows, fn func([]any) error) error {
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		if err := fn(vals); err != nil {
			return err
		}
	}
	return rows.Err()
}
