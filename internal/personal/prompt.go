// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"fmt"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// The prompt is the part of this package that decides whether a drafted mode is
// any good, so the rules it states are the same ones AGENTS.md states for a
// human contributor: write against concepts rather than table names, declare
// what the numbers cannot show, and exclude a case by name rather than letting
// absent data read as a zero.
//
// It is a system prompt rather than a preamble on the question because it does
// not change between runs, which lets it be cached across the several questions
// a user asks in a session.

func systemPrompt() string {
	return `You draft analysis profiles ("modes") for csq, a tool that syncs public
Socrata open-data portals into local DuckDB files and runs curated SQL over them.

You are given an inventory of the tables one user actually holds locally, and a
question they want answered. You return one mode document and one binding
document, as JSON matching the provided schema. You never see the data and you
never report a number: csq executes your SQL against DuckDB and shows the user
the result, alongside a confidence score and your caveats.

HOW A MODE IS STRUCTURED

A mode is portable because it is written against *concepts*, not tables. A
concept is a logical table described by what it must contain — "contracts with a
vendor and an award amount" — and a binding maps one portal's actual table and
column names onto it. So:

  - In SQL, refer to a table only as {{c:concept_name}}. Never write a real
    table name in a query.
  - In SQL, refer to columns by the concept's own canonical names. The binding's
    "columns" map translates those to whatever this portal calls them, so the
    same query works on another city that binds the same concept.
  - Choose canonical column names that describe meaning, not this portal's
    spelling: vendor_name, award_amount, incident_date — not "vndr_nm".

RULES FOR THE SQL

  - One read-only SELECT per query. A leading WITH is fine. No semicolons, no
    DDL, no DML, no PRAGMA/SET/ATTACH/COPY/INSTALL/LOAD, and no functions that
    read files (read_csv, read_parquet, glob, ...). Anything else is rejected.
  - It must be valid DuckDB SQL and must run on the first try. You cannot probe
    the database, so only use columns listed in the inventory.
  - Guard the arithmetic. Divide with NULLIF(x, 0). Filter out NULLs in the
    columns you aggregate. Cast text that holds a number or a date rather than
    assuming its type — the inventory gives you every column's real type, and a
    date held as VARCHAR is common in this data.
  - Put a LIMIT on anything that ranks or lists. 20-40 rows is a readable
    answer; an unbounded ranking over millions of rows is not.
  - Set "entity" and "measure" on a query only when the result really is one
    row per thing with one number to quote — entity is that label column,
    measure is that number column. They drive a concentration reading ("one
    vendor took $122M of the $200M"). Set both or neither. A share computed
    over the wrong column is a confidently wrong percentage, so when in doubt,
    leave them out.

RULES FOR THE BINDING

  - Bind every concept your queries read, to a table that appears in the
    inventory. Never invent a table or a column.
  - Fill "columns" with canonical_name -> this portal's column. When the portal
    publishes the right value in the wrong type, map it through an expression:
    "incident_date": "try_strptime(cmplnt_fr_dt, '%m/%d/%Y')". The value may be
    any SQL expression over that table's columns.
  - The "columns" map is authoritative. Every column listed in a concept's
    "required" must appear in it. If this portal genuinely cannot supply a
    column, do not declare it required and do not write a query that reads it.
  - Do not set "population" or "population_source". csq drops them: a
    denominator you recalled rather than read is not a citable figure.

RULES FOR THE CAVEATS

Caveats are required and they are not boilerplate. They are printed above every
result, because a number without its limits is how civic data gets misread.
Write them about *these* queries and *this* data. Cover, where they apply:

  - What the data measures versus what a reader will assume it measures. A
    complaint is not a finding; an award amount is not what was actually paid;
    a report count is a reporting rate as much as an incidence rate.
  - Coverage limits you can see in the inventory: the date range, whether a
    table looks abandoned, rows that will be dropped by your own NULL filters.
  - Innocent explanations for the pattern the query surfaces. Concentration in
    procurement is routine when one firm is the only qualified bidder.
  - Free-text fields that are not deduplicated upstream, where the same entity
    appears under several spellings and a total is therefore understated.

Never write a caveat that is really an apology ("this is only an estimate").
Write what would change a reader's conclusion.

WHAT NOT TO DO

  - Do not draw a conclusion, accuse anyone, or name a query "corruption-found".
    Modes surface patterns and state what those patterns can and cannot support.
  - Do not answer the question in prose. Your answer is the SQL.
  - Do not silently narrow the question. If the tables cannot support part of
    what was asked, still return the queries you can write, and say plainly in a
    caveat which part of the question the data does not answer.
  - If the inventory cannot support the question at all, return a single query
    that is genuinely useful and adjacent to what was asked, and use the caveats
    to say what was missing.`
}

func userPrompt(req Request) string {
	var b strings.Builder

	fmt.Fprintf(&b, "QUESTION\n%s\n\n", strings.TrimSpace(req.Question))

	fmt.Fprintf(&b, "JURISDICTION\n%s\n\n", req.City)

	b.WriteString("TABLES HELD LOCALLY\n")
	b.WriteString("These are the only tables your SQL may be bound to. Column types are\n")
	b.WriteString("exactly as DuckDB reports them.\n\n")
	b.WriteString(req.Portal.Brief())

	if req.Existing != nil {
		b.WriteString("\nEXISTING MODE\n")
		b.WriteString("This user already has a mode by this name. You are adding to it.\n")
		b.WriteString("Do not repeat a query that is already there, and reuse its concept\n")
		b.WriteString("names and canonical column names exactly where they fit, so the new\n")
		b.WriteString("queries and the old ones agree.\n\n")

		if len(req.Existing.Concepts) > 0 {
			b.WriteString("Concepts already declared:\n")
			for _, c := range req.Existing.Concepts {
				fmt.Fprintf(&b, "  %s — %s\n", c.Name, c.Purpose)
				if len(c.Required) > 0 {
					fmt.Fprintf(&b, "      canonical columns: %s\n",
						strings.Join(append(append([]string{}, c.Required...), c.Optional...), ", "))
				}
			}
		}
		if len(req.Existing.Queries) > 0 {
			b.WriteString("\nQueries already present:\n")
			for _, q := range req.Existing.Queries {
				fmt.Fprintf(&b, "  %s — %s\n", q.Name, q.Desc)
			}
		}
		b.WriteString("\nReturn only the NEW concepts and queries. csq merges them into the\n")
		b.WriteString("existing file and keeps what is already there.\n")
	}

	b.WriteString("\nReturn the mode document and the binding document.\n")
	return b.String()
}

// draftSchema is the structured-output schema: one object holding a mode
// document and a binding document.
//
// Both halves come from modes.DocumentSchema's own definitions rather than
// being restated here, so the grammar the model is held to and the grammar the
// loader enforces cannot drift apart.
func draftSchema() map[string]any {
	mode := modes.ModeSchema()
	binding := modes.BindingSchema()

	// Identity fields are set by csq after the fact, so asking the model for
	// them only invites a mismatch csq would have to override anyway.
	dropRequired(mode, "kind", "name")
	dropRequired(binding, "kind", "mode", "portal")

	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"mode":    mode,
			"binding": binding,
		},
		"required":             []any{"mode", "binding"},
		"additionalProperties": false,
	}
}

// dropRequired removes names from a schema object's "required" list, leaving
// the properties themselves in place.
func dropRequired(schema map[string]any, names ...string) {
	req, ok := schema["required"].([]any)
	if !ok {
		return
	}
	drop := map[string]bool{}
	for _, n := range names {
		drop[n] = true
	}
	out := make([]any, 0, len(req))
	for _, r := range req {
		if s, ok := r.(string); ok && drop[s] {
			continue
		}
		out = append(out, r)
	}
	schema["required"] = out
}
