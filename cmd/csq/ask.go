// Copyright (c) 2026 Neomantra Corp

package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	flag "github.com/spf13/pflag"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
	"github.com/neomantra/CivicSodaQuack/internal/patterns"
	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

const askUsage = `csq modes ask — build a mode by asking in English

Usage:
  csq modes ask "<question>" --db <file> [options]

csq matches your question against its analysis patterns and the columns of the
tables you hold, shows you what it picked and why, and builds the mode once you
confirm. The matching is keyword scoring over your own schema: it runs entirely
on this machine and makes no network call.

It refuses rather than guesses. A question it cannot place, or a column it
cannot identify, comes back as a message telling you how to say it explicitly
with 'csq modes add'.

Options:
  --db FILE         Portal DuckDB to build against (required)
  --table NAME      Skip table matching and use this one
  --as NAME         Mode to create or extend (default: personal)
  --date-format FMT strptime layout, if the matched date column is text
  --yes             Accept the proposal without confirming
  --dry-run         Show the proposal and the JSON, save nothing
  --run             Run the new query after saving

Examples:
  csq modes ask "which vendors got the most money?" --db chicago.duckdb
  csq modes ask "how are permits trending over time?" --db chicago.duckdb
  csq modes ask "which columns are missing data?" --db chicago.duckdb
  csq modes ask "are any vendor names duplicated?" --db chicago.duckdb
`

func runModeAsk(args []string) error {
	fs := flag.NewFlagSet("modes ask", flag.ContinueOnError)
	var (
		dbPath, table, asName    string
		portal, city, dateFormat string
		assume, dryRun, runNow   bool
	)
	fs.StringVar(&dbPath, "db", "", "Portal DuckDB to build against")
	fs.StringVar(&table, "table", "", "Use this table instead of matching one")
	fs.StringVar(&asName, "as", personal.DefaultModeName, "Mode to create or extend")
	fs.StringVar(&portal, "portal", "", "Socrata host this DB came from")
	fs.StringVar(&city, "city", "", "Jurisdiction label for the binding")
	fs.StringVar(&dateFormat, "date-format", "", "strptime layout for a text date column")
	fs.BoolVar(&assume, "yes", false, "Accept the proposal without confirming")
	fs.BoolVar(&dryRun, "dry-run", false, "Show the proposal, save nothing")
	fs.BoolVar(&runNow, "run", false, "Run the new query after saving")
	if err := fs.Parse(args); err != nil {
		return err
	}

	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		return fmt.Errorf("a question is required\n\n%s", askUsage)
	}
	if dbPath == "" {
		return fmt.Errorf("--db is required: csq matches your question against the tables you hold")
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
	if len(inv.Tables) == 0 {
		return fmt.Errorf("%s holds no synced tables to ask about", dbPath)
	}

	s, err := patterns.Suggest(question, inv, table)
	if err != nil {
		return err
	}

	// Everything the router decided, with the evidence for each choice, before
	// anything is written. A wrong guess has to be visible here — that is the
	// whole difference between this and translating a question into SQL.
	printSuggestion(s, question)

	if s.NeedsDateFormat && dateFormat == "" {
		return fmt.Errorf(
			"the matched date column %q holds text, so csq needs to know its layout.\n"+
				"  Re-run with --date-format, e.g. --date-format '%%m/%%d/%%Y'\n"+
				"  csq will not guess: reading 03/04 as March 4th when it means 4th March\n"+
				"  mislabels a third of the year without erroring.",
			s.Columns[patterns.RoleDate])
	}

	if !assume && !dryRun {
		ok, err := confirmSuggestion(s, dbPath, asName)
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
	}

	return buildFromSuggestion(buildFromSuggestionArgs{
		Suggestion: s, DBPath: dbPath, Alias: alias, Host: host,
		ModeName: asName, Portal: portal, City: city,
		DateFormat: dateFormat, DryRun: dryRun, RunNow: runNow,
	})
}

func printSuggestion(s *patterns.Suggestion, question string) {
	fmt.Fprintf(os.Stderr, "\n  question  %s\n\n", question)
	// Width the columns to the widest value present, so a long column name does
	// not shove the reasoning out of alignment — the reasoning is the part the
	// user is meant to read.
	valWidth := len(s.Pattern.Name)
	if len(s.Table.Name) > valWidth {
		valWidth = len(s.Table.Name)
	}
	for _, col := range s.Columns {
		if len(col) > valWidth {
			valWidth = len(col)
		}
	}

	fmt.Fprintf(os.Stderr, "  %-10s %-*s  %s\n", "pattern", valWidth, s.Pattern.Name,
		bracket(s.Reasons["pattern"]))
	fmt.Fprintf(os.Stderr, "  %-10s %-*s  %s\n", "table", valWidth, s.Table.Name,
		bracket(s.Reasons["table"]))

	for _, param := range s.Pattern.Params {
		col, ok := s.Columns[param.Role]
		if !ok {
			continue
		}
		fmt.Fprintf(os.Stderr, "  %-10s %-*s  %s\n",
			param.Role, valWidth, col, bracket(s.Reasons[string(param.Role)]))
	}

	for _, w := range s.Warnings {
		fmt.Fprintf(os.Stderr, "\n  ! ")
		for i, line := range wrapText(w, 70) {
			if i == 0 {
				fmt.Fprintf(os.Stderr, "%s\n", line)
			} else {
				fmt.Fprintf(os.Stderr, "    %s\n", line)
			}
		}
	}
	if len(s.Alternatives) > 0 {
		fmt.Fprintf(os.Stderr, "\n  other shapes this could have been: %s\n",
			strings.Join(s.Alternatives, ", "))
	}
	fmt.Fprintln(os.Stderr)
}

func bracket(s string) string {
	if s == "" {
		return ""
	}
	return "— " + s
}

// confirmSuggestion asks before writing. 'e' prints the explicit command so a
// user who wants one column changed can edit a line rather than rephrase a
// question and hope.
func confirmSuggestion(s *patterns.Suggestion, dbPath, modeName string) (bool, error) {
	fmt.Fprintf(os.Stderr, "  Use this?  [Y]es  [n]o  [e]dit as a command  ")

	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		// A non-interactive stdin means nobody is there to agree.
		fmt.Fprintf(os.Stderr, "\n  (no input; nothing saved — pass --yes to accept)\n")
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		fmt.Fprintln(os.Stderr)
		return true, nil
	case "e", "edit":
		fmt.Fprintf(os.Stderr, "\n  Adjust and run:\n\n    %s\n\n", s.CommandLine(dbPath, modeName))
		return false, nil
	default:
		fmt.Fprintf(os.Stderr, "\n  Nothing saved. To choose the shape yourself:\n"+
			"    csq modes patterns\n    %s\n\n", s.CommandLine(dbPath, modeName))
		return false, nil
	}
}
