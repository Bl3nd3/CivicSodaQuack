// Copyright (c) 2026 Neomantra Corp

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	flag "github.com/spf13/pflag"

	"github.com/neomantra/CivicSodaQuack/internal/analysis"
	"github.com/neomantra/CivicSodaQuack/internal/investigate"
)

const investigateUsage = `csq investigate — answer a civic question, and show the working

Usage:
  csq investigate "<question>" --db <portal.duckdb> [options]
  csq investigate --list [--db <portal.duckdb>]

Options:
  --db FILE             Portal DuckDB to investigate (exactly one)
  --investigation NAME  Skip question routing and run this one
  --list                List investigations, with readiness when --db is given
  --json                Emit the whole report as JSON
  --sql                 Print the statement behind each finding
  --working             Print the plan, every challenge, and the dataset profile

An investigation runs seven steps in order — discover, plan, sync, validate,
analyze, challenge, explain — and reports a verdict, a confidence, the findings
behind it, and the reasons it might be wrong. The indicators and the direction
of each that would support the claim are declared before any data is read.

Examples:
  csq investigate "Is Chicago becoming less transparent about policing?" \
      --db data.cityofchicago.org.duckdb
  csq investigate "Is Chicago publishing fewer records?" \
      --db data.cityofchicago.org.duckdb --working --sql
  csq investigate --list --db data.cityofchicago.org.duckdb
`

func runInvestigate(args []string) error {
	fs := flag.NewFlagSet("investigate", flag.ContinueOnError)
	var (
		dbPath   string
		portal   string
		named    string
		asJSON   bool
		showSQL  bool
		working  bool
		listOnly bool
	)
	fs.StringVar(&dbPath, "db", "", "Portal DuckDB to investigate")
	fs.StringVar(&portal, "portal", "", "Socrata host this DB came from (default: derived from filename)")
	fs.StringVar(&named, "investigation", "", "Run this investigation instead of routing the question")
	fs.BoolVar(&asJSON, "json", false, "Emit the report as JSON")
	fs.BoolVar(&showSQL, "sql", false, "Print the SQL behind each finding")
	fs.BoolVar(&working, "working", false, "Print the plan, the challenges, and the dataset profile")
	fs.BoolVar(&listOnly, "list", false, "List investigations and their readiness")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if listOnly {
		return listInvestigations(dbPath, portal, asJSON)
	}

	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		fmt.Print(investigateUsage)
		return fmt.Errorf("a question is required")
	}
	if dbPath == "" {
		return fmt.Errorf("--db is required (the portal database to investigate)")
	}

	sess, err := analysis.Open([]analysis.DBSpec{{Path: dbPath, Portal: portal}})
	if err != nil {
		return err
	}
	defer sess.Close()

	rep, err := sess.Investigate(context.Background(), question,
		investigate.Options{Investigation: named})
	if err != nil {
		return explainInvestigateError(err)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rep)
	}
	investigate.RenderText(os.Stdout, rep, investigate.RenderOptions{
		ShowSQL: showSQL, ShowWorking: working,
	})
	return nil
}

// explainInvestigateError turns the routing failures into advice.
//
// Both of them are the user's question meeting a registry that is deliberately
// finite, which is a different situation from a broken command: csq will not
// invent an analysis it cannot caveat, and saying so plainly is more useful
// than an error code.
func explainInvestigateError(err error) error {
	var ambiguous *investigate.AmbiguousError
	if errors.As(err, &ambiguous) {
		fmt.Fprintf(os.Stderr, "That question could be read two ways:\n\n")
		for _, c := range ambiguous.Candidates {
			inv, lookupErr := investigate.Lookup(c.Name)
			if lookupErr != nil {
				continue
			}
			fmt.Fprintf(os.Stderr, "  %s — %s\n", inv.Name, inv.Claim)
			if len(c.Matched) > 0 {
				fmt.Fprintf(os.Stderr, "      matched: %s\n", strings.Join(c.Matched, ", "))
			}
		}
		fmt.Fprintf(os.Stderr, "\nPick one with --investigation <name>.\n")
		return fmt.Errorf("the question is ambiguous")
	}

	var nomatch *investigate.NoMatchError
	if errors.As(err, &nomatch) {
		fmt.Fprintf(os.Stderr, "No investigation covers that question.\n\n")
		fmt.Fprintf(os.Stderr, "Investigations are curated rather than generated: each one "+
			"carries\nits own indicators and the caveats they need. csq will not assemble "+
			"an\nanalysis it cannot qualify.\n\nAvailable:\n\n")
		for _, inv := range investigate.All() {
			fmt.Fprintf(os.Stderr, "  %s — %s\n", inv.Name, inv.Claim)
		}
		return fmt.Errorf("no investigation matched")
	}
	return err
}

// listInvestigations prints the registry, with readiness when a database is
// given.
func listInvestigations(dbPath, portal string, asJSON bool) error {
	if dbPath == "" {
		if asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(investigate.All())
		}
		fmt.Printf("Available investigations:\n\n")
		for _, inv := range investigate.All() {
			fmt.Printf("  %s\n      claim: %s\n      %d indicators, over the %s mode's datasets\n\n",
				inv.Name, inv.Claim, len(inv.Probes), inv.Mode)
		}
		fmt.Printf("Pass --db <portal.duckdb> to see which can run against your data.\n")
		return nil
	}

	sess, err := analysis.Open([]analysis.DBSpec{{Path: dbPath, Portal: portal}})
	if err != nil {
		return err
	}
	defer sess.Close()

	sts, err := sess.InvestigationStatuses(context.Background())
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(sts)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "INVESTIGATION\tREADY\tINDICATORS\tNOTE")
	for _, st := range sts {
		ready := "yes"
		switch {
		case !st.Applicable:
			ready = "n/a"
		case !st.Ready:
			ready = "no"
		}
		fmt.Fprintf(tw, "%s\t%s\t%d/%d\t%s\n",
			st.Name, ready, st.Runnable, st.Total, st.Reason)
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	for _, st := range sts {
		if st.Readiness.FixCommand != "" {
			fmt.Printf("\nTo make %s runnable:\n  %s\n", st.Name, st.Readiness.FixCommand)
		}
	}
	return nil
}
