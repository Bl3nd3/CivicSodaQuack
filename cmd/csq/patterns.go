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
	"github.com/neomantra/CivicSodaQuack/internal/patterns"
	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

const patternsUsage = `csq modes patterns — analysis shapes you can build a mode from

Usage:
  csq modes patterns                 List the patterns
  csq modes patterns show <name>     One pattern's parameters, SQL, and caveats
  csq modes tables --db <file>       The tables and columns you can point one at

A pattern is a reviewed SQL template with holes for the columns you choose.
Building a mode from one needs no API key, no network, and no model — and the
caveats come with the pattern, written once rather than regenerated.
`

const addUsage = `csq modes add — build a mode query from a pattern

Usage:
  csq modes add <pattern> --db <file> --table <name> [role flags] [options]

Options:
  --db FILE            Portal DuckDB the query will run against (required)
  --table NAME         Table to read (required)
  --as NAME            Mode to create or extend (default: personal)
  --concept NAME       Canonical name the table binds to (default: the table name)
  --query NAME         Name for the generated query (default: derived)
  --date-format FMT    strptime layout, when the date column is text
  --portal HOST        Socrata host this DB came from (default: from the filename)
  --city LABEL         Jurisdiction label for the binding (default: the portal)
  --dry-run            Print the generated JSON without saving it
  --run                Run the new query after saving

Examples:
  csq modes tables --db chicago.duckdb
  csq modes add top-n --db chicago.duckdb --table contracts \
      --entity vendor_name --measure award_amount
  csq modes add concentration --db chicago.duckdb --table contracts \
      --group department --entity vendor_name --measure award_amount
  csq modes add trend --db chicago.duckdb --table permits \
      --date issue_date --date-format '%m/%d/%Y'
  csq modes add name-variants --db chicago.duckdb --table contracts \
      --entity vendor_name
  csq modes run personal --db chicago.duckdb
`

func runPatterns(args []string) error {
	if len(args) == 0 {
		return listPatterns()
	}
	switch args[0] {
	case "list":
		return listPatterns()
	case "show":
		return showPattern(args[1:])
	case "-h", "--help", "help":
		fmt.Print(patternsUsage)
		return nil
	default:
		return fmt.Errorf("unknown action %q\n\n%s", args[0], patternsUsage)
	}
}

func listPatterns() error {
	fmt.Printf("Analysis patterns — build a mode without a model or an API key:\n\n")
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	for _, p := range patterns.All() {
		var flags []string
		for _, param := range p.Params {
			f := param.Flag
			if !param.Required {
				f = "[" + f + "]"
			}
			flags = append(flags, f)
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\n", p.Name, p.Summary, strings.Join(flags, " "))
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Printf("\n'csq modes patterns show <name>' for the SQL and the caveats it carries.\n")
	fmt.Printf("'csq modes tables --db <file>' lists the columns you can point one at.\n")
	return nil
}

func showPattern(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: csq modes patterns show <name> (have: %s)",
			strings.Join(patterns.Names(), ", "))
	}
	p, err := patterns.Lookup(args[0])
	if err != nil {
		return err
	}

	fmt.Printf("%s — %s\n%s\n\n", p.Name, p.Summary, strings.Repeat("=", len(p.Name)+len(p.Summary)+3))
	for _, line := range wrapText(p.About, 76) {
		fmt.Printf("%s\n", line)
	}

	fmt.Printf("\nColumns it needs:\n")
	if len(p.Params) == 0 {
		fmt.Printf("  none — it profiles every column in the table\n")
	}
	for _, param := range p.Params {
		req := "required"
		if !param.Required {
			req = "optional"
		}
		fmt.Printf("\n  %s <column>   (%s)\n", param.Flag, req)
		for _, line := range wrapText(param.Desc, 70) {
			fmt.Printf("      %s\n", line)
		}
		switch {
		case param.Numeric:
			fmt.Printf("      must hold a number; a text column is wrapped in TRY_CAST\n")
		case param.Temporal:
			fmt.Printf("      must hold a date; a text column needs --date-format\n")
		}
	}

	if strings.TrimSpace(p.SQL) != "" {
		fmt.Printf("\nSQL (columns shown by role; the binding maps them to yours):\n")
		fmt.Printf("%s\n", strings.TrimSpace(p.SQL))
	}

	fmt.Printf("\nCaveats it carries — printed above every result:\n")
	for _, c := range p.Caveats {
		for i, line := range wrapText(c, 72) {
			if i == 0 {
				fmt.Printf("\n  * %s\n", line)
			} else {
				fmt.Printf("    %s\n", line)
			}
		}
	}
	fmt.Println()
	return nil
}

// runModeTables prints the inventory a pattern is pointed at. It is the
// offline equivalent of what the drafted path shows the model, and the user
// needs it for the same reason: you cannot name a column you cannot see.
func runModeTables(args []string) error {
	fs := flag.NewFlagSet("modes tables", flag.ContinueOnError)
	var dbPath, portal string
	var samples bool
	fs.StringVar(&dbPath, "db", "", "Portal DuckDB to inspect")
	fs.StringVar(&portal, "portal", "", "Socrata host this DB came from")
	fs.BoolVar(&samples, "samples", false, "Also show a few distinct values per text column")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if dbPath == "" {
		return fmt.Errorf("--db is required")
	}
	if portal == "" {
		portal = modes.PortalFromDBPath(dbPath)
	}

	host, alias, err := attachReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer host.Close()

	inv, err := personal.Describe(host, alias, portal)
	if err != nil {
		return err
	}
	if len(inv.Tables) == 0 {
		return fmt.Errorf("%s holds no synced tables", dbPath)
	}
	if samples {
		if err := personal.SampleColumns(host, inv); err != nil {
			return err
		}
	}
	fmt.Print(inv.Brief())
	fmt.Printf("Point a pattern at one of these, e.g.\n")
	fmt.Printf("  csq modes add top-n --db %s --table %s --entity <col> --measure <col>\n",
		dbPath, inv.Tables[0].Name)
	return nil
}

func runModeAdd(args []string) error {
	fs := flag.NewFlagSet("modes add", flag.ContinueOnError)
	var (
		dbPath, table, asName, concept, queryName string
		portal, city, dateFormat                  string
		entity, measure, date, category, group    string
		dryRun, runNow                            bool
	)
	fs.StringVar(&dbPath, "db", "", "Portal DuckDB the query runs against")
	fs.StringVar(&table, "table", "", "Table to read")
	fs.StringVar(&asName, "as", personal.DefaultModeName, "Mode to create or extend")
	fs.StringVar(&concept, "concept", "", "Canonical name the table binds to")
	fs.StringVar(&queryName, "query", "", "Name for the generated query")
	fs.StringVar(&portal, "portal", "", "Socrata host this DB came from")
	fs.StringVar(&city, "city", "", "Jurisdiction label for the binding")
	fs.StringVar(&dateFormat, "date-format", "", "strptime layout for a text date column")
	fs.StringVar(&entity, "entity", "", "Column naming the thing being ranked")
	fs.StringVar(&measure, "measure", "", "Numeric column to sum")
	fs.StringVar(&date, "date", "", "Date column to group by")
	fs.StringVar(&category, "category", "", "Categorical column to split on")
	fs.StringVar(&group, "group", "", "Column to compute shares within")
	fs.BoolVar(&dryRun, "dry-run", false, "Print the generated JSON without saving")
	fs.BoolVar(&runNow, "run", false, "Run the new query after saving")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return fmt.Errorf("a pattern is required (have: %s)\n\n%s",
			strings.Join(patterns.Names(), ", "), addUsage)
	}
	p, err := patterns.Lookup(rest[0])
	if err != nil {
		return err
	}
	if dbPath == "" {
		return fmt.Errorf("--db is required: a mode is built against tables you hold")
	}
	if table == "" {
		return fmt.Errorf("--table is required; 'csq modes tables --db %s' lists them", dbPath)
	}
	if asName != strings.ToLower(asName) || strings.ContainsAny(asName, " \t/") {
		return fmt.Errorf("--as %q must be lowercase with no spaces or slashes", asName)
	}
	if portal == "" {
		portal = modes.PortalFromDBPath(dbPath)
	}
	if city == "" {
		city = portal
	}

	host, alias, err := attachReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer host.Close()

	inv, err := personal.Describe(host, alias, portal)
	if err != nil {
		return err
	}
	tbl, ok := inv.Table(table)
	if !ok {
		return fmt.Errorf("%s has no table %q.\n  It holds: %s",
			dbPath, table, strings.Join(inv.TableNames(), ", "))
	}

	draft, err := patterns.Build(patterns.BuildRequest{
		Pattern:    p,
		Table:      tbl,
		Concept:    concept,
		ModeName:   asName,
		Portal:     portal,
		City:       city,
		QueryName:  queryName,
		DateFormat: dateFormat,
		Columns: map[patterns.Role]string{
			patterns.RoleEntity:   entity,
			patterns.RoleMeasure:  measure,
			patterns.RoleDate:     date,
			patterns.RoleCategory: category,
			patterns.RoleGroup:    group,
		},
	})
	if err != nil {
		return err
	}

	dir := modesDir()
	paths := personal.PathsFor(dir, asName, portal)

	// Merge into whatever is already there, on the same terms as the drafted
	// path: the user's file wins every conflict.
	existingMode, err := personal.LoadExisting(paths.Mode)
	if err != nil {
		return err
	}
	newQueries := map[string]bool{}
	for _, q := range draft.Mode.Queries {
		newQueries[q.Name] = true
	}
	if existingMode != nil {
		for _, q := range existingMode.Queries {
			delete(newQueries, q.Name)
		}
		draft.Mode = personal.MergeMode(existingMode, draft.Mode)
	}
	existingBinding, err := personal.LoadExisting(paths.Binding)
	if err != nil {
		return err
	}
	draft.Binding = personal.MergeBinding(existingBinding, draft.Binding)

	if dryRun {
		return printDraft(draft)
	}

	rollback, err := personal.Save(dir, draft, paths)
	if err != nil {
		return err
	}
	if problems := personal.VerifyQueries(host, asName, alias, portal, newQueries); len(problems) > 0 {
		rollback()
		fmt.Fprintf(os.Stderr, "\n[csq] nothing was saved: the generated query would not run\n")
		for _, pr := range problems {
			fmt.Fprintf(os.Stderr, "  %s\n      %v\n", pr.Query, pr.Err)
		}
		return fmt.Errorf("check the columns you named against 'csq modes tables --db %s'", dbPath)
	}

	fmt.Fprintf(os.Stderr, "[csq] added %s to %s\n", strings.Join(sortedKeys(newQueries), ", "), paths.Mode)
	fmt.Fprintf(os.Stderr, "[csq] binding      %s\n", paths.Binding)
	fmt.Fprintf(os.Stderr, "\n  read it:  csq modes show %s\n", asName)
	fmt.Fprintf(os.Stderr, "  run it:   csq modes run %s --db %s\n\n", asName, dbPath)

	if !runNow {
		return nil
	}
	for _, name := range sortedKeys(newQueries) {
		if err := runModeQueries([]string{asName, "--db", dbPath, "--query", name}); err != nil {
			return err
		}
	}
	return nil
}

// attachReadOnly opens an in-memory host and attaches one portal read-only.
func attachReadOnly(dbPath string) (*sql.DB, string, error) {
	if _, err := os.Stat(dbPath); err != nil {
		return nil, "", fmt.Errorf("--db %s: %w", dbPath, err)
	}
	host, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return nil, "", fmt.Errorf("open host: %w", err)
	}
	alias := modes.AliasFor(dbPath)
	stmt := fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
		strings.ReplaceAll(dbPath, "'", "''"), alias)
	if _, err := host.Exec(stmt); err != nil {
		host.Close()
		return nil, "", fmt.Errorf("attach %s: %w", dbPath, err)
	}
	return host, alias, nil
}
