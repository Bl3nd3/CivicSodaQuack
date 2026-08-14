// Copyright (c) 2026 Neomantra Corp

package main

import (
	"database/sql"
	"errors"
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
  csq modes show <mode>                  Concepts, portals, queries, and caveats
  csq modes init <mode> [--portal HOST] [--output FILE]
  csq modes run  <mode> --db <file> [--db ...] [--query NAME] [--limit N]
  csq modes lint <file.yaml> ...         Check an external mode or binding file
  csq modes where                        Show where external modes are loaded from

Modes and bindings can be added as YAML in ~/.csq/modes/ (override with
--modes-dir or CSQ_MODES_DIR) without rebuilding csq. Run 'csq modes lint'
on a file to check it before use.

Examples:
  csq modes
  csq modes show corruption
  csq modes init police --output police.yaml
  csq sync --config police.yaml
  csq modes run police --db data.cityofchicago.org.duckdb --query finding-outcomes
  csq modes run ranking --db chicago.duckdb --db cookcounty.duckdb
`

func runModes(args []string) error {
	args = extractModesDirFlag(args)
	loadExternalModes()
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
	case "lint":
		return lintModeFiles(args[1:])
	case "where":
		return showModesDir(os.Stdout)
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
	if m.Source != "" {
		fmt.Fprintf(out, "Defined in: %s\n\n", m.Source)
	}
	for _, line := range wrapText(m.About, 76) {
		fmt.Fprintf(out, "%s\n", line)
	}
	if len(m.Concepts) > 0 {
		fmt.Fprintf(out, "\nConcepts this mode needs (%d):\n", len(m.Concepts))
		for _, c := range m.Concepts {
			fmt.Fprintf(out, "\n  %s\n", c.Name)
			for _, line := range wrapText(c.Purpose, 70) {
				fmt.Fprintf(out, "      %s\n", line)
			}
			if len(c.Required) > 0 {
				fmt.Fprintf(out, "      requires: %s\n", strings.Join(c.Required, ", "))
			}
		}

		bindings := modes.BindingsFor(m.Name)
		fmt.Fprintf(out, "\nPortals bound (%d):\n", len(bindings))
		if len(bindings) == 0 {
			fmt.Fprintf(out, "  none yet — add one to internal/modes/ to support a city\n")
		}
		for _, b := range bindings {
			fmt.Fprintf(out, "\n  %s  (%s)  ~%s rows\n",
				b.Portal, b.City, withCommas(m.ApproxRowsFor(b)))
			if b.Source != "" {
				fmt.Fprintf(out, "      from: %s\n", b.Source)
			}
			tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
			for _, c := range m.Concepts {
				bd, ok := b.Concepts[c.Name]
				if !ok {
					fmt.Fprintf(tw, "      %s\t(not published by this portal)\t\n", c.Name)
					continue
				}
				fmt.Fprintf(tw, "      %s\t%s\t%s\n", c.Name, bd.ID, bd.Table)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
			for _, n := range b.Notes {
				for i, line := range wrapText(n, 66) {
					if i == 0 {
						fmt.Fprintf(out, "      note: %s\n", line)
					} else {
						fmt.Fprintf(out, "            %s\n", line)
					}
				}
			}
		}
	} else {
		fmt.Fprintf(out, "\nConcepts: none — this mode reads the _csq schema of databases you already have.\n")
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
	var portal string
	fs.StringVar(&output, "output", "", "Write the config here (default: stdout)")
	fs.StringVar(&portal, "portal", "", "Socrata host to bind (default: the only bound portal)")
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
	b, err := pickBinding(m, portal)
	if err != nil {
		return err
	}
	yaml, err := m.ConfigYAMLFor(b)
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
	fmt.Fprintf(os.Stderr, "[csq] wrote %s — %s, %d datasets, ~%s rows\n",
		output, b.Portal, len(b.Concepts), withCommas(m.ApproxRowsFor(b)))
	if missing := m.Unbound(b); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "[csq] note: %s does not publish %s; "+
			"queries needing those will be skipped\n",
			b.Portal, strings.Join(missing, ", "))
	}
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
	var portalOverride string
	fs.StringArrayVar(&dbPaths, "db", nil, "Portal DuckDB to attach (repeatable)")
	fs.StringVar(&portalOverride, "portal", "", "Socrata host this DB came from (default: derived from filename)")
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

	// Concept-based modes need a binding per attached portal. csq does not
	// record the portal inside the file, so it is derived from the filename;
	// --portal overrides it for a single database.
	bindings := make([]*modes.Binding, len(dbPaths))
	if len(m.Concepts) > 0 {
		for i, path := range dbPaths {
			want := modes.PortalFromDBPath(path)
			if portalOverride != "" && len(dbPaths) == 1 {
				want = portalOverride
			}
			b, err := modes.LookupBinding(m.Name, want)
			if err != nil {
				return err
			}
			bindings[i] = b
		}
	}
	var binding *modes.Binding
	if len(m.Concepts) > 0 {
		binding = bindings[0]
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
		var expanded string
		switch {
		case m.MultiPortal && len(m.Concepts) > 0:
			expanded, err = buildPerCityUnion(m, q, aliases, bindings, quiet)
			if err == errNoComparableCity {
				continue
			}
		case binding != nil:
			ok, missing := m.Runnable(q, binding)
			if !ok {
				fmt.Fprintf(os.Stderr,
					"  skipping %s — %s does not publish: %s\n\n",
					q.Name, binding.Portal, strings.Join(missing, ", "))
				continue
			}
			expanded, err = modes.ExpandConcepts(q.SQL, aliases[0], binding)
		default:
			expanded, err = modes.Expand(q.SQL, aliases)
		}
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
			if strings.Contains(err.Error(), "does not exist") && binding != nil {
				fmt.Fprintf(os.Stderr,
					"  hint: sync this mode's datasets first —\n"+
						"        csq modes init %s --portal %s --output %s.yaml && csq sync --config %s.yaml\n",
					m.Name, binding.Portal, m.Name, m.Name)
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

// pickBinding resolves the binding to use: the requested portal, or the only
// one registered when a mode has exactly one.
func pickBinding(m *modes.Mode, portal string) (*modes.Binding, error) {
	if len(m.Concepts) == 0 {
		return nil, fmt.Errorf(
			"mode %q has no datasets to sync; it reads the _csq schema of databases you already have",
			m.Name)
	}
	if portal != "" {
		return modes.LookupBinding(m.Name, portal)
	}
	bindings := modes.BindingsFor(m.Name)
	switch len(bindings) {
	case 0:
		return nil, fmt.Errorf("mode %q has no portal bindings", m.Name)
	case 1:
		return bindings[0], nil
	default:
		names := make([]string, 0, len(bindings))
		for _, b := range bindings {
			names = append(names, b.Portal)
		}
		return nil, fmt.Errorf("mode %q is bound for several portals; pass --portal (have: %s)",
			m.Name, strings.Join(names, ", "))
	}
}

// lintModeFiles validates external mode/binding files without registering them.
func lintModeFiles(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: csq modes lint <file.yaml> [file.yaml ...]")
	}
	var bad int
	for _, path := range args {
		kind, name, err := modes.LintFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  FAIL  %s\n        %v\n", path, err)
			bad++
			continue
		}
		fmt.Printf("  ok    %s  (%s: %s)\n", path, kind, name)
	}
	if bad > 0 {
		return fmt.Errorf("%d of %d file(s) failed", bad, len(args))
	}
	return nil
}

// showModesDir reports where external modes are read from and what was found.
func showModesDir(out *os.File) error {
	dir := modesDir()
	fmt.Fprintf(out, "External modes directory:\n  %s\n\n", dir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		fmt.Fprintf(out, "It does not exist yet. Create it and drop in a YAML file:\n")
		fmt.Fprintf(out, "  mkdir -p %s\n\n", dir)
		fmt.Fprintf(out, "Override the location with --modes-dir or CSQ_MODES_DIR.\n")
		return nil
	}
	var external int
	for _, m := range modes.All() {
		if m.Source != "" {
			fmt.Fprintf(out, "  mode     %-12s %s\n", m.Name, m.Source)
			external++
		}
		for _, b := range modes.BindingsFor(m.Name) {
			if b.Source != "" {
				fmt.Fprintf(out, "  binding  %-12s %s → %s\n", m.Name, b.Portal, b.Source)
				external++
			}
		}
	}
	if external == 0 {
		fmt.Fprintf(out, "No external modes or bindings loaded from it yet.\n")
	}
	return nil
}

// modesDir resolves where external modes are loaded from, in precedence order:
// the --modes-dir flag (captured into modesDirOverride), CSQ_MODES_DIR, then
// ~/.csq/modes.
func modesDir() string {
	if modesDirOverride != "" {
		return modesDirOverride
	}
	if d := os.Getenv("CSQ_MODES_DIR"); d != "" {
		return d
	}
	return modes.DefaultModesDir()
}

// modesDirOverride is set from the --modes-dir flag before dispatch.
var modesDirOverride string

// loadExternalModes registers any user-supplied modes and bindings. A failure
// is reported but not fatal: one broken file should not make csq unusable, and
// the message names the file so it can be fixed or removed.
func loadExternalModes() {
	if _, err := modes.LoadDir(modesDir()); err != nil {
		fmt.Fprintf(os.Stderr, "[csq] warning: %v\n", err)
		fmt.Fprintf(os.Stderr, "[csq] continuing with built-in modes only; "+
			"run 'csq modes lint <file>' to check it\n")
	}
}

// extractModesDirFlag pulls --modes-dir out of the argument list before
// dispatch, so every subcommand honours it without repeating the flag.
func extractModesDirFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--modes-dir" && i+1 < len(args):
			modesDirOverride = args[i+1]
			i++
		case strings.HasPrefix(args[i], "--modes-dir="):
			modesDirOverride = strings.TrimPrefix(args[i], "--modes-dir=")
		default:
			out = append(out, args[i])
		}
	}
	return out
}

// errNoComparableCity signals that no attached city can answer a query, so the
// runner should move on rather than emit an empty table.
var errNoComparableCity = errors.New("no comparable city")

// buildPerCityUnion expands a single-city query once per attached city and
// stacks the results with a leading city column.
//
// Cities that cannot answer are excluded *by name with a reason*. That
// reporting is the point: silently dropping a city would make absent data look
// like a good result, which for a crime or service comparison is the most
// consequential way this tool could mislead someone.
func buildPerCityUnion(m *modes.Mode, q modes.Query, aliases []string,
	bindings []*modes.Binding, quiet bool) (string, error) {

	var parts []string
	var excluded []string
	for i, b := range bindings {
		if ok, why := m.Comparable(q, b); !ok {
			excluded = append(excluded, fmt.Sprintf("%s (%s)", b.City, why))
			continue
		}
		one, err := modes.ExpandConcepts(q.SQL, aliases[i], b)
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf(
			"SELECT '%s' AS city, * FROM (%s)",
			strings.ReplaceAll(b.City, "'", "''"), one))
	}

	if len(excluded) > 0 && !quiet {
		fmt.Printf("  excluded from this comparison:\n")
		for _, e := range excluded {
			fmt.Printf("    - %s\n", e)
		}
		fmt.Println()
	}
	if len(parts) == 0 {
		fmt.Fprintf(os.Stderr, "  skipping %s — no attached city can answer it\n\n", q.Name)
		return "", errNoComparableCity
	}
	if len(parts) == 1 && len(bindings) > 1 && !quiet {
		fmt.Printf("  note: only one city qualifies, so this is not a comparison.\n\n")
	}
	return strings.Join(parts, "\nUNION ALL\n"), nil
}
