// Copyright (c) 2026 Neomantra Corp

package main

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

const modesUsage = `csq modes — curated analysis profiles

Usage:
  csq modes                              List available modes
  csq modes show <mode>                  Datasets, queries, and caveats for one mode
  csq modes init <mode> [--output FILE]  Write a sync config covering the mode's datasets
  csq modes run  <mode> --db <file> [--db ...] [--query NAME] [--limit N]

Examples:
  csq modes
  csq modes show corruption
  csq modes init police --output police.yaml
  csq sync --config police.yaml
  csq modes run police --db data.cityofchicago.org.duckdb --query finding-outcomes
  csq modes run ranking --db chicago.duckdb --db cookcounty.duckdb
`

func runModes(args []string) error {
	if len(args) == 0 {
		return listModes(os.Stdout)
	}
	switch args[0] {
	case "show":
		return showMode(args[1:])
	case "init":
		return initMode(args[1:])
	case "run":
		return runModeQueries(args[1:])
	case "list":
		return listModes(os.Stdout)
	case "-h", "--help", "help":
		fmt.Print(modesUsage)
		return nil
	default:
		return fmt.Errorf("unknown action %q\n\n%s", args[0], modesUsage)
	}
}

// listModes prints the one-line summary of every mode. This is also what a
// bare `csq` invocation advertises, so it is the first thing most users read.
func listModes(out *os.File) error {
	fmt.Fprintf(out, "Available modes:\n\n")
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, m := range modes.All() {
		scope := "single portal"
		if m.MultiPortal {
			scope = "cross-portal"
		}
		fmt.Fprintf(tw, "  %s\t%s\t(%s, %d queries)\n", m.Name, m.Summary, scope, len(m.Queries))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nRun 'csq modes show <mode>' for datasets, queries, and caveats.\n")
	return nil
}

func showMode(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: csq modes show <mode> (have: %s)",
			strings.Join(modes.Names(), ", "))
	}
	m, err := modes.Lookup(args[0])
	if err != nil {
		return err
	}
	out := os.Stdout

	fmt.Fprintf(out, "%s\n%s\n\n", m.Title, strings.Repeat("=", len(m.Title)))
	for _, line := range wrapText(m.About, 76) {
		fmt.Fprintf(out, "%s\n", line)
	}
	if m.Portal != "" {
		fmt.Fprintf(out, "\nPortal: %s\n", m.Portal)
	}

	if len(m.Datasets) > 0 {
		fmt.Fprintf(out, "\nDatasets (%d, ~%s rows total):\n", len(m.Datasets), withCommas(m.ApproxRows()))
		tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		for _, d := range m.Datasets {
			fmt.Fprintf(tw, "  %s\t%s\t%s\n", d.ID, d.Table, withCommas(d.Rows))
		}
		if err := tw.Flush(); err != nil {
			return err
		}
		for _, d := range m.Datasets {
			fmt.Fprintf(out, "\n  %s — %s\n", d.Table, d.Name)
			for _, line := range wrapText(d.Why, 70) {
				fmt.Fprintf(out, "      %s\n", line)
			}
		}
	} else {
		fmt.Fprintf(out, "\nDatasets: none — this mode reads the _csq schema of databases you already have.\n")
	}

	fmt.Fprintf(out, "\nQueries (%d):\n", len(m.Queries))
	for _, q := range m.Queries {
		fmt.Fprintf(out, "\n  %s\n", q.Name)
		for _, line := range wrapText(q.Desc, 70) {
			fmt.Fprintf(out, "      %s\n", line)
		}
	}

	if len(m.Caveats) > 0 {
		fmt.Fprintf(out, "\nCaveats — read before quoting any of this:\n")
		for _, c := range m.Caveats {
			lines := wrapText(c, 72)
			for i, line := range lines {
				if i == 0 {
					fmt.Fprintf(out, "\n  * %s\n", line)
				} else {
					fmt.Fprintf(out, "    %s\n", line)
				}
			}
		}
	}
	fmt.Fprintln(out)
	return nil
}

func initMode(args []string) error {
	fs := flag.NewFlagSet("modes init", flag.ContinueOnError)
	var (
		output string
		force  bool
	)
	fs.StringVar(&output, "output", "", "Write the config here (default: stdout)")
	fs.BoolVar(&force, "force", false, "Overwrite --output if it exists")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: csq modes init <mode> [--output FILE]")
	}
	m, err := modes.Lookup(rest[0])
	if err != nil {
		return err
	}
	yaml, err := m.ConfigYAML()
	if err != nil {
		return err
	}
	if output == "" {
		fmt.Print(yaml)
		return nil
	}
	if _, err := os.Stat(output); err == nil && !force {
		return fmt.Errorf("%s exists; pass --force to overwrite", output)
	}
	if err := os.WriteFile(output, []byte(yaml), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[csq] wrote %s (%d datasets, ~%s rows)\n",
		output, len(m.Datasets), withCommas(m.ApproxRows()))
	fmt.Fprintf(os.Stderr, "[csq] next: csq sync --config %s\n", output)
	return nil
}

func runModeQueries(args []string) error {
	fs := flag.NewFlagSet("modes run", flag.ContinueOnError)
	var (
		dbPaths   []string
		queryName string
		limit     int
		quiet     bool
	)
	fs.StringArrayVar(&dbPaths, "db", nil, "Portal DuckDB to attach (repeatable)")
	fs.StringVar(&queryName, "query", "", "Run only this query (default: all)")
	fs.IntVar(&limit, "limit", 0, "Cap rows per query (0 = use the query's own limit)")
	fs.BoolVar(&quiet, "quiet", false, "Suppress caveats and headers")
	if err := fs.Parse(args); err != nil {
		return err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("usage: csq modes run <mode> --db <file> [--db ...]")
	}
	m, err := modes.Lookup(rest[0])
	if err != nil {
		return err
	}
	if len(dbPaths) == 0 {
		return fmt.Errorf("--db is required (at least one portal DuckDB)")
	}
	if !m.MultiPortal && len(dbPaths) != 1 {
		return fmt.Errorf("mode %q targets a single portal; got %d --db flags",
			m.Name, len(dbPaths))
	}

	// Attach every portal READ_ONLY. Read-only attach lets these queries run
	// alongside an MCP server or a sync holding the same file, and nothing here
	// ever writes.
	host, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return fmt.Errorf("open host: %w", err)
	}
	defer host.Close()

	aliases := modes.UniqueAliases(dbPaths)
	for i, path := range dbPaths {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("--db %s: %w", path, err)
		}
		stmt := fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
			strings.ReplaceAll(path, "'", "''"), aliases[i])
		if _, err := host.Exec(stmt); err != nil {
			return fmt.Errorf("attach %s: %w", path, err)
		}
	}

	queries := m.Queries
	if queryName != "" {
		q, err := m.Query(queryName)
		if err != nil {
			return err
		}
		queries = []modes.Query{*q}
	}

	if !quiet && len(m.Caveats) > 0 {
		fmt.Printf("%s\n\n", m.Title)
		fmt.Printf("Caveats:\n")
		for _, c := range m.Caveats {
			lines := wrapText(c, 72)
			for i, line := range lines {
				if i == 0 {
					fmt.Printf("  * %s\n", line)
				} else {
					fmt.Printf("    %s\n", line)
				}
			}
		}
		fmt.Println()
	}

	for _, q := range queries {
		expanded, err := modes.Expand(q.SQL, aliases)
		if err != nil {
			return fmt.Errorf("query %s: %w", q.Name, err)
		}
		if limit > 0 {
			expanded = fmt.Sprintf("SELECT * FROM (%s) LIMIT %d", expanded, limit)
		}
		if !quiet {
			fmt.Printf("── %s ─────────────────────────────────\n", q.Name)
			for _, line := range wrapText(q.Desc, 76) {
				fmt.Printf("%s\n", line)
			}
			fmt.Println()
		}
		if err := runAndPrint(host, expanded); err != nil {
			// A missing table means the mode's datasets were never synced. Say
			// so usefully rather than surfacing a raw binder error.
			fmt.Fprintf(os.Stderr, "  query %s failed: %v\n", q.Name, err)
			if strings.Contains(err.Error(), "does not exist") && m.Portal != "" {
				fmt.Fprintf(os.Stderr,
					"  hint: sync this mode's datasets first —\n"+
						"        csq modes init %s --output %s.yaml && csq sync --config %s.yaml\n",
					m.Name, m.Name, m.Name)
			}
			fmt.Fprintln(os.Stderr)
			continue
		}
		fmt.Println()
	}
	return nil
}

// runAndPrint executes one query and renders the result as an aligned table.
func runAndPrint(db *sql.DB, query string) error {
	rows, err := db.Query(query)
	if err != nil {
		return err
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return err
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	seps := make([]string, len(cols))
	for i, c := range cols {
		seps[i] = strings.Repeat("-", len(c))
	}
	fmt.Fprintln(tw, strings.Join(seps, "\t"))

	n := 0
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		cells := make([]string, len(cols))
		for i, v := range vals {
			cells[i] = renderCell(v)
		}
		fmt.Fprintln(tw, strings.Join(cells, "\t"))
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("(%d rows)\n", n)
	return nil
}

func renderCell(v any) string {
	switch t := v.(type) {
	case nil:
		return "-"
	case []byte:
		return string(t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// wrapText breaks s onto lines of at most width runes, on word boundaries.
func wrapText(s string, width int) []string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return []string{""}
	}
	var lines []string
	cur := fields[0]
	for _, f := range fields[1:] {
		if len(cur)+1+len(f) > width {
			lines = append(lines, cur)
			cur = f
			continue
		}
		cur += " " + f
	}
	return append(lines, cur)
}

// withCommas formats n with thousands separators.
func withCommas(n int64) string {
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
