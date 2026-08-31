// Copyright (c) 2026 Neomantra Corp

// Package patterns builds modes from analysis shapes rather than from a
// question, so a user can author one with no API key, no network, and no model.
//
// The observation behind it is that most civic analysis is a handful of shapes.
// "Which vendors got the most money", "which contractors pull the most permits",
// "who receives the most contributions" are one shape — rank entities by a
// summed measure — pointed at different columns. A dozen such shapes cover most
// of what people actually ask, and each can be written once, by hand, and
// reviewed.
//
// That has an advantage over drafting beyond cost. The caveats that matter most
// are properties of the *shape*, not of the data: a top-N ranking always hides
// its tail, a free-text entity column always understates concentration because
// the same body appears under several spellings, a count by month is always
// distorted by a partial final month. Written once against the pattern, those
// are reviewed prose rather than something regenerated — and correct every time
// the pattern is used.
//
// A pattern produces exactly the documents `csq modes personal` produces, and
// they go through the same loader, the same read-only guard, and the same
// EXPLAIN check. Nothing downstream can tell the two apart, which is the point:
// the model was always just one way to write the file.
package patterns

import (
	"fmt"
	"sort"
	"strings"
)

// Role names the job a column does in a pattern.
//
// These are the canonical column names a pattern's SQL is written against; the
// generated binding maps each onto whatever the portal actually calls it. That
// is what makes a pattern instantiated on Chicago and one instantiated on NYC
// the same mode rather than two similar ones.
type Role string

const (
	// RoleEntity is the thing each row is about: a vendor, an officer, a
	// department.
	RoleEntity Role = "entity"
	// RoleMeasure is the number being summed. Cast when the column is text.
	RoleMeasure Role = "measure"
	// RoleDate is the event date a trend is grouped by.
	RoleDate Role = "event_date"
	// RoleCategory is a low-cardinality column to split on.
	RoleCategory Role = "category"
	// RoleGroup is the partition a share is computed within — a department,
	// say, when asking what share of *its* spend one vendor took.
	RoleGroup Role = "group_name"
)

// Param is one column a pattern needs from the user.
type Param struct {
	Role Role
	// Flag is the command-line flag that supplies it.
	Flag string
	// Desc explains what to point it at.
	Desc string
	// Required patterns refuse to build without it.
	Required bool
	// Numeric means the column must hold a number; a text column is wrapped in
	// TRY_CAST rather than rejected, since civic portals publish money as text
	// constantly.
	Numeric bool
	// Temporal means the column must hold a date or timestamp. A text column
	// needs --date-format, because guessing between MM/DD/YYYY and DD/MM/YYYY
	// silently mangles half the year.
	Temporal bool
}

// Pattern is one analysis shape.
type Pattern struct {
	Name    string
	Summary string
	About   string
	Params  []Param

	// SQL is the query template. It is written against the canonical role names
	// and refers to its table as {{c:CONCEPT}}, which Build replaces with the
	// concept the user is binding.
	SQL string

	// Entity and Measure name the *output* columns that make a concentration
	// reading possible, as placeholders resolved at build time.
	Entity  string
	Measure string

	// Caveats are the interpretation limits inherent to this shape. They are
	// the reason a pattern is not merely a shortcut: they are written once,
	// reviewed, and correct on every use.
	Caveats []string
}

// ConceptToken is replaced by the concept name when a pattern is instantiated.
const ConceptToken = "{{c:CONCEPT}}"

var registry = []*Pattern{topN, concentration, trend, breakdown, coverage, nameVariants}

// All returns every pattern, in display order.
func All() []*Pattern {
	out := make([]*Pattern, len(registry))
	copy(out, registry)
	return out
}

// Lookup finds a pattern by name.
func Lookup(name string) (*Pattern, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, p := range registry {
		if p.Name == want {
			return p, nil
		}
	}
	return nil, fmt.Errorf("unknown pattern %q (have: %s)", name, strings.Join(Names(), ", "))
}

// Names lists every pattern name.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, p := range registry {
		out = append(out, p.Name)
	}
	sort.Strings(out)
	return out
}

// Param returns the pattern's parameter for a role.
func (p *Pattern) Param(r Role) (Param, bool) {
	for _, pm := range p.Params {
		if pm.Role == r {
			return pm, true
		}
	}
	return Param{}, false
}

// ---------------------------------------------------------------------------
// The patterns themselves.
//
// Every one of them filters NULLs out of the columns it aggregates and divides
// through NULLIF, because the alternative is a query that returns a number
// where it should return nothing.

var topN = &Pattern{
	Name:    "top-n",
	Summary: "Rank entities by a summed measure",
	About: "The commonest civic question there is: who got the most. Ranks each " +
		"distinct value of one column, by how many records it has, or by the sum of " +
		"a number when you name one. Both readings of 'the most' are ordinary — the " +
		"most permits is a count, the most money is a sum — so the measure is " +
		"optional and the ranking follows whichever you asked for.",
	Params: []Param{
		{Role: RoleEntity, Flag: "--entity", Desc: "Column naming the thing being ranked (a vendor, a department)", Required: true},
		{Role: RoleMeasure, Flag: "--measure", Desc: "Optional numeric column to sum; without one, rows are counted", Numeric: true},
	},
	Entity:  "entity",
	Measure: "records",
	SQL: `
SELECT entity,
       COUNT(*) AS records
FROM ` + ConceptToken + `
WHERE entity IS NOT NULL
GROUP BY entity
ORDER BY records DESC
LIMIT 25`,
	Caveats: []string{
		"A top-25 ranking hides its tail. The rows not shown may hold most of the " +
			"total, so never describe the leaders as 'most of' anything without checking " +
			"what the full distribution looks like.",
		"Rows where the ranked column is NULL are excluded, and so are rows missing " +
			"the summed column when one is used. The confidence block reports how many " +
			"that was; if it is a large share, the ranking describes a subset you have " +
			"not characterised.",
		"Counting records and summing a value answer different questions. The vendor " +
			"with the most contracts is often not the one paid the most, and which of " +
			"those you meant by 'the most' is not something the data settles.",
		"Entity names in civic data are free text and are rarely deduplicated upstream. " +
			"One organisation appearing as 'ACME INC', 'Acme, Inc.' and 'Acme Incorporated' " +
			"is split across three rows, which understates its true total. Run the " +
			"name-variants pattern on the same column before quoting a figure.",
	},
}

var concentration = &Pattern{
	Name:    "concentration",
	Summary: "Find entities taking an outsized share within a group",
	About: "Computes each entity's share of the total within its group — one vendor's " +
		"share of one department's spend — and surfaces the largest. This is the shape " +
		"behind most procurement reporting, and the one most easily over-read.",
	Params: []Param{
		{Role: RoleGroup, Flag: "--group", Desc: "Column to compute shares within (a department, a category)", Required: true},
		{Role: RoleEntity, Flag: "--entity", Desc: "Column naming the thing taking a share", Required: true},
		{Role: RoleMeasure, Flag: "--measure", Desc: "Numeric column to sum", Required: true, Numeric: true},
	},
	SQL: `
WITH by_entity AS (
  SELECT group_name, entity, SUM(measure) AS amt
  FROM ` + ConceptToken + `
  WHERE measure IS NOT NULL AND group_name IS NOT NULL AND entity IS NOT NULL
  GROUP BY group_name, entity
),
shares AS (
  SELECT group_name, entity, amt,
         SUM(amt) OVER (PARTITION BY group_name) AS group_total
  FROM by_entity
)
SELECT group_name,
       entity,
       ROUND(amt, 2)                                   AS entity_amount,
       ROUND(group_total, 2)                           AS group_total,
       ROUND(100.0 * amt / NULLIF(group_total, 0), 1)  AS pct_of_group
FROM shares
WHERE group_total > 0
ORDER BY pct_of_group DESC, entity_amount DESC
LIMIT 40`,
	Caveats: []string{
		"Concentration is not evidence of wrongdoing. Sole-source and single-bidder " +
			"procurement is lawful and routine for specialised work — some jobs have one " +
			"qualified supplier. Treat a high share as a question for the department, " +
			"never as a finding.",
		"A high share of a small group total is not the same as a high share of a large " +
			"one. Read pct_of_group beside group_total: taking 90% of a $40,000 line is " +
			"unremarkable, taking 90% of a $40M one is a question.",
		"Groups with few entities produce high shares mechanically. A department that " +
			"buys from two suppliers will always show one near 50% or above.",
	},
}

var trend = &Pattern{
	Name:    "trend",
	Summary: "Count or sum by month over time",
	About: "Groups records by month, optionally summing a measure, so a level can be " +
		"distinguished from a change. The shape behind 'is this getting worse'.",
	Params: []Param{
		{Role: RoleDate, Flag: "--date", Desc: "Date or timestamp column to group by", Required: true, Temporal: true},
		{Role: RoleMeasure, Flag: "--measure", Desc: "Optional numeric column to sum alongside the count", Numeric: true},
	},
	SQL: `
SELECT DATE_TRUNC('month', event_date) AS month,
       COUNT(*)                        AS records
FROM ` + ConceptToken + `
WHERE event_date IS NOT NULL
GROUP BY month
ORDER BY month`,
	Caveats: []string{
		"The first and last months in the range are almost always partial, and will " +
			"read as a collapse or a surge that is really the edge of the data. Check the " +
			"range before describing any trend.",
		"A count over time measures reporting as much as it measures incidence. A rise " +
			"can mean more events, a new intake system, a reporting campaign, or a " +
			"backfill — the data cannot distinguish them.",
		"Records with no date are excluded. If the portal backfills dates late, recent " +
			"months are undercounted in a way that looks like a genuine decline.",
	},
}

var breakdown = &Pattern{
	Name:    "breakdown",
	Summary: "Count and share by category",
	About: "Splits records across a categorical column with each category's share of " +
		"the whole. The orienting query worth running before any deeper question.",
	Params: []Param{
		{Role: RoleCategory, Flag: "--category", Desc: "Low-cardinality column to split on (a type, a status)", Required: true},
		{Role: RoleMeasure, Flag: "--measure", Desc: "Optional numeric column to sum per category", Numeric: true},
	},
	Entity:  "category",
	Measure: "records",
	SQL: `
SELECT COALESCE(CAST(category AS VARCHAR), '(not recorded)') AS category,
       COUNT(*)                                              AS records,
       ROUND(100.0 * COUNT(*)
             / NULLIF(SUM(COUNT(*)) OVER (), 0), 1)          AS pct_of_records
FROM ` + ConceptToken + `
GROUP BY category
ORDER BY records DESC
LIMIT 50`,
	Caveats: []string{
		"Blank and missing categories are shown as '(not recorded)' rather than dropped. " +
			"A large '(not recorded)' share means the breakdown describes only the " +
			"classified subset, and the pattern in the rest is unknown.",
		"Categories are recorded by the agency for its own purposes, and their meaning " +
			"changes over time without the label changing. A category's share moving may " +
			"reflect a reclassification rather than anything in the world.",
	},
}

var coverage = &Pattern{
	Name:    "coverage",
	Summary: "Profile how populated a table's columns are",
	About: "Counts NULL and blank values per column. The due-diligence query that " +
		"belongs before trusting any other result, since a column that is 60% empty " +
		"will quietly halve every total computed from it.",
	Params: []Param{},
	SQL:    "", // built per-column; see Build
	Caveats: []string{
		"A populated column is not a correct one. This counts presence, not accuracy: " +
			"a column full of zeroes, placeholder dates, or 'UNKNOWN' reads as complete here.",
		"Blank strings are counted as missing alongside NULL, because a portal that " +
			"writes '' where it means 'no value' is common and the two mean the same " +
			"thing to a reader.",
	},
}

var nameVariants = &Pattern{
	Name:    "name-variants",
	Summary: "Find one entity recorded under several spellings",
	About: "Groups values that differ only in case, spacing, or punctuation, so the " +
		"same organisation split across several spellings is visible. Run this before " +
		"quoting any total from a free-text name column.",
	Params: []Param{
		{Role: RoleEntity, Flag: "--entity", Desc: "Free-text name column to check", Required: true},
	},
	SQL: `
WITH normalised AS (
  SELECT entity,
         TRIM(LOWER(REGEXP_REPLACE(CAST(entity AS VARCHAR), '[^a-zA-Z0-9]+', ' ', 'g'))) AS key
  FROM ` + ConceptToken + `
  WHERE entity IS NOT NULL AND TRIM(CAST(entity AS VARCHAR)) <> ''
)
SELECT key                              AS normalised_name,
       COUNT(DISTINCT entity)           AS spellings,
       COUNT(*)                         AS records,
       STRING_AGG(DISTINCT entity, ' | ') AS variants
FROM normalised
GROUP BY key
HAVING COUNT(DISTINCT entity) > 1
ORDER BY spellings DESC, records DESC
LIMIT 40`,
	Caveats: []string{
		"Matching on case and punctuation alone is deliberately conservative. It will " +
			"not catch abbreviations, misspellings, or a renamed subsidiary, so this is a " +
			"floor on the duplication present, never a count of it.",
		"Two genuinely different bodies can normalise to the same string. Read the " +
			"variants column before merging anything — this surfaces candidates for a " +
			"human to judge, it does not decide.",
	},
}
