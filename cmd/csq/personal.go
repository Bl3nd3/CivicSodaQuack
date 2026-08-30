// Copyright (c) 2026 Neomantra Corp

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	flag "github.com/spf13/pflag"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/neomantra/CivicSodaQuack/internal/llm"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

const personalUsage = `csq modes personal — draft a mode from a question

Usage:
  csq modes personal "<question>" --db <file> [flags]

Shows a language model the tables you hold locally — their names, columns, and
types — and asks it to draft the concepts, SQL, and interpretation caveats that
answer your question. The draft is saved as JSON in your modes directory and
becomes an ordinary csq mode: run it, read it, edit it, or delete a query.

The model never sees your rows unless you pass --samples, never executes
anything, and never reports a number. csq runs the SQL and shows you the result.

Flags:
  --db FILE           Portal DuckDB to write the mode against (required)
  --portal HOST       Socrata host this DB came from (default: from the filename)
  --city LABEL        Jurisdiction label for the binding (default: from the portal)
  --as NAME           Save as this mode instead of "personal"
  --samples           Also show the model a few distinct values from low-cardinality
                      text columns. Sends that data to the Anthropic API; it makes
                      generated filters match how a portal actually spells a category
  --model ID          Claude model (default: ` + llm.DefaultModel + `, or CSQ_LLM_MODEL)
  --effort LEVEL      low|medium|high|xhigh|max (default: ` + llm.DefaultEffort + `, or CSQ_LLM_EFFORT)
  --dry-run           Print the drafted JSON without saving it
  --run               Run the new queries immediately after saving
  --yes               Do not ask before contacting the API

Examples:
  csq modes personal "which vendors got the most money last year?" \
      --db data.cityofchicago.org.duckdb
  csq modes personal "where are 311 requests slowest to close?" \
      --db data.cityofchicago.org.duckdb --samples --run
  csq modes personal "which precincts report the most complaints?" \
      --db data.cityofnewyork.us.duckdb --as nypd-watch
`

func runPersonalMode(args []string) error {
	fs := flag.NewFlagSet("modes personal", flag.ContinueOnError)
	var (
		dbPath  string
		portal  string
		city    string
		asName  string
		model   string
		effort  string
		samples bool
		dryRun  bool
		runNow  bool
		assume  bool
	)
	fs.StringVar(&dbPath, "db", "", "Portal DuckDB to write the mode against")
	fs.StringVar(&portal, "portal", "", "Socrata host this DB came from")
	fs.StringVar(&city, "city", "", "Jurisdiction label for the binding")
	fs.StringVar(&asName, "as", personal.DefaultModeName, "Save as this mode name")
	fs.StringVar(&model, "model", "", "Claude model id")
	fs.StringVar(&effort, "effort", "", "Reasoning effort: low|medium|high|xhigh|max")
	fs.BoolVar(&samples, "samples", false, "Show the model sample values from text columns")
	fs.BoolVar(&dryRun, "dry-run", false, "Print the draft without saving")
	fs.BoolVar(&runNow, "run", false, "Run the new queries after saving")
	fs.BoolVar(&assume, "yes", false, "Do not ask before contacting the API")
	if err := fs.Parse(args); err != nil {
		return err
	}

	question := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if question == "" {
		return fmt.Errorf("a question is required\n\n%s", personalUsage)
	}
	if dbPath == "" {
		return fmt.Errorf("--db is required: a mode is written against the tables you hold")
	}
	if asName != strings.ToLower(asName) || strings.ContainsAny(asName, " \t/") {
		return fmt.Errorf("--as %q must be lowercase with no spaces or slashes", asName)
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("--db %s: %w", dbPath, err)
	}

	if portal == "" {
		portal = modes.PortalFromDBPath(dbPath)
	}
	if city == "" {
		city = portal
	}

	// READ_ONLY for the same reason every other read path uses it: nothing here
	// writes, and a generated query must not be the first thing that could.
	host, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		return fmt.Errorf("open host: %w", err)
	}
	defer host.Close()

	alias := modes.AliasFor(dbPath)
	attach := fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)",
		strings.ReplaceAll(dbPath, "'", "''"), alias)
	if _, err := host.Exec(attach); err != nil {
		return fmt.Errorf("attach %s: %w", dbPath, err)
	}

	fmt.Fprintf(os.Stderr, "[csq] reading the schema of %s\n", dbPath)
	inv, err := personal.Describe(host, alias, portal)
	if err != nil {
		return err
	}
	if len(inv.Tables) == 0 {
		return fmt.Errorf("%s holds no synced tables, so there is nothing to write a "+
			"mode against.\n  Sync a dataset first — 'csq modes init corruption "+
			"--output c.yaml && csq sync --config c.yaml'", dbPath)
	}
	if samples {
		fmt.Fprintf(os.Stderr, "[csq] sampling low-cardinality text columns\n")
		if err := personal.SampleColumns(host, inv); err != nil {
			return err
		}
	}

	dir := modesDir()
	paths := personal.PathsFor(dir, asName, portal)
	existing, err := personal.LoadExisting(paths.Mode)
	if err != nil {
		return err
	}

	// Contacting an external API is the one step here the user may not have
	// meant to take, so it is stated plainly and confirmed by default.
	if err := confirmAPICall(inv, asName, samples, assume, dryRun); err != nil {
		return err
	}

	client, err := llm.New(llm.Options{Model: model, Effort: effort})
	if err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "[csq] asking %s to draft %q (effort %s)\n",
		client.Model(), asName, client.Effort())

	draft, err := personal.Author(context.Background(), client, personal.Request{
		Question: question,
		ModeName: asName,
		Portal:   inv,
		City:     city,
		Existing: existing,
	})
	if err != nil {
		return err
	}

	newQueries := draftedQueryNames(existing, draft)

	if dryRun {
		return printDraft(draft)
	}

	// Everything below is the part that decides whether the draft is kept.
	existingBinding, err := personal.LoadExisting(paths.Binding)
	if err != nil {
		return err
	}
	draft.Binding = personal.MergeBinding(existingBinding, draft.Binding)

	rollback, err := personal.Save(dir, draft, paths)
	if err != nil {
		return err
	}

	if problems := personal.VerifyQueries(host, asName, alias, portal, newQueries); len(problems) > 0 {
		rollback()
		fmt.Fprintf(os.Stderr, "\n[csq] the draft was discarded: %d quer%s would not run\n",
			len(problems), plural(len(problems), "y", "ies"))
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "  %s\n      %v\n", p.Query, p.Err)
		}
		return fmt.Errorf("nothing was saved; ask again, or add --samples so the model " +
			"can see how the columns are actually populated")
	}

	fmt.Fprintf(os.Stderr, "\n[csq] saved %s\n", paths.Mode)
	fmt.Fprintf(os.Stderr, "[csq] saved %s\n", paths.Binding)
	fmt.Fprintf(os.Stderr, "[csq] added %d quer%s: %s\n",
		len(newQueries), plural(len(newQueries), "y", "ies"),
		strings.Join(sortedKeys(newQueries), ", "))
	fmt.Fprintf(os.Stderr, "\n  read it:  csq modes show %s\n", asName)
	fmt.Fprintf(os.Stderr, "  run it:   csq modes run %s --db %s\n", asName, dbPath)
	fmt.Fprintf(os.Stderr, "  edit it:  %s\n\n", paths.Mode)

	if !runNow {
		return nil
	}
	// Run only what was just added, so a session of questions does not re-run
	// the whole accumulated mode each time.
	for _, name := range sortedKeys(newQueries) {
		if err := runModeQueries([]string{asName, "--db", dbPath, "--query", name}); err != nil {
			return err
		}
	}
	return nil
}

// confirmAPICall states what is about to leave the machine, and to where.
func confirmAPICall(inv *personal.Portal, modeName string, samples, assume, dryRun bool) error {
	what := "the names, columns, and types of your local tables"
	if samples {
		what = "the names, columns, and types of your local tables, " +
			"plus a few distinct values from low-cardinality text columns"
	}
	fmt.Fprintf(os.Stderr,
		"\n[csq] csq is about to send %s\n"+
			"      for %d table%s to the Anthropic API, to draft the %q mode.\n"+
			"      No rows of data are sent%s, and nothing is executed remotely.\n",
		what, len(inv.Tables), plural(len(inv.Tables), "", "s"), modeName,
		map[bool]string{true: " beyond those sample values", false: ""}[samples])
	if dryRun {
		fmt.Fprintf(os.Stderr, "      --dry-run: the draft will be printed, not saved.\n")
	}
	if assume {
		fmt.Fprintln(os.Stderr)
		return nil
	}
	fmt.Fprintf(os.Stderr, "\n      Continue? [y/N] ")

	var answer string
	if _, err := fmt.Fscanln(os.Stdin, &answer); err != nil {
		// An empty line, EOF, or a non-interactive stdin all mean "no".
		answer = ""
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		fmt.Fprintln(os.Stderr)
		return nil
	}
	return fmt.Errorf("cancelled; pass --yes to skip this prompt")
}

// draftedQueryNames reports which queries this run added, so verification and
// the optional --run touch only the new work.
func draftedQueryNames(existing *personal.Document, d *personal.Draft) map[string]bool {
	had := map[string]bool{}
	if existing != nil {
		for _, q := range existing.Queries {
			had[q.Name] = true
		}
	}
	out := map[string]bool{}
	for _, q := range d.Mode.Queries {
		if !had[q.Name] {
			out[q.Name] = true
		}
	}
	return out
}

func printDraft(d *personal.Draft) error {
	for _, doc := range []any{d.Mode, d.Binding} {
		body, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(body))
	}
	fmt.Fprintf(os.Stderr,
		"[csq] --dry-run: nothing saved. Save the JSON above into your modes directory\n"+
			"      (csq modes where) to use it, or re-run without --dry-run.\n")
	return nil
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
