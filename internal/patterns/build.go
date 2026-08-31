// Copyright (c) 2026 Neomantra Corp

package patterns

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

// BuildRequest is one instantiation: a pattern, a table, and the columns that
// fill its roles.
type BuildRequest struct {
	Pattern *Pattern
	// Table is the local table to read, as it appears in the inventory.
	Table personal.Table
	// Concept is the canonical name the table binds to. Defaults to the table
	// name; naming it explicitly is what lets two portals share one mode.
	Concept string
	// Columns maps each role to the portal's column name.
	Columns map[Role]string
	// DateFormat is the strptime format for a text date column. Required when
	// the date column is not already a date or timestamp.
	DateFormat string
	// QueryName overrides the generated query slug.
	QueryName string
	// ModeName is the mode being created or extended.
	ModeName string
	// Portal and City describe the binding.
	Portal string
	City   string
}

// Build turns a pattern and a table into the same pair of documents the model
// would have produced.
//
// Everything the model was trusted to get right is decided here instead: the
// SQL comes from a reviewed template, the casts come from the column types
// DuckDB reported, and the caveats come from the pattern. Nothing is inferred
// from a column's *name* — the user says which column plays which role, because
// a wrong guess about that is exactly the failure this whole path exists to
// avoid.
func Build(req BuildRequest) (*personal.Draft, error) {
	p := req.Pattern
	if p == nil {
		return nil, fmt.Errorf("no pattern given")
	}
	concept := req.Concept
	if concept == "" {
		concept = sanitiseIdent(req.Table.Name)
	}

	cols, err := resolveColumns(req, concept)
	if err != nil {
		return nil, err
	}

	sqlText, entity, measure, err := renderSQL(req, concept)
	if err != nil {
		return nil, err
	}

	// The same guard the drafted path runs. A template should never fail it,
	// and checking anyway is what keeps that true as templates are edited.
	if err := personal.CheckReadOnly(sqlText); err != nil {
		return nil, fmt.Errorf("pattern %q produced SQL that failed the read-only "+
			"check, which is a bug in the pattern: %w", p.Name, err)
	}

	queryName := req.QueryName
	if queryName == "" {
		queryName = defaultQueryName(p, req)
	}

	required, optional := conceptColumns(p, req)

	mode := &personal.Document{
		Kind:    "mode",
		Name:    req.ModeName,
		Title:   fmt.Sprintf("Personal — %s", req.ModeName),
		Summary: "Built from analysis patterns against tables held locally",
		About: "Assembled from csq's analysis patterns with 'csq modes add' or " +
			"'csq modes ask'. Every query here comes from a reviewed SQL template " +
			"pointed at columns of your own tables, so nothing in this file was " +
			"written by a model, and building it required no API key and no network.",
		Concepts: []personal.Concept{{
			Name: concept,
			Purpose: fmt.Sprintf("Records from %s, read as %s.",
				req.Table.Name, describeRoles(req)),
			Required: required,
			Optional: optional,
		}},
		Queries: []personal.DocQuery{{
			Name:    queryName,
			Desc:    p.Summary + " (" + describeRoles(req) + ")",
			SQL:     sqlText,
			Entity:  entity,
			Measure: measure,
		}},
		Caveats: append([]string(nil), p.Caveats...),
	}

	binding := &personal.Document{
		Kind:   "binding",
		Mode:   req.ModeName,
		Portal: req.Portal,
		City:   req.City,
		Datasets: map[string]personal.DocDataset{
			concept: {
				ID:      firstNonEmpty(req.Table.DatasetID, "(local)"),
				Table:   req.Table.Name,
				Name:    firstNonEmpty(req.Table.DatasetName, req.Table.Name),
				Rows:    req.Table.Rows,
				Columns: cols,
			},
		},
	}
	return &personal.Draft{Mode: mode, Binding: binding}, nil
}

// resolveColumns maps each role onto a SQL expression over the portal's own
// columns, casting where the declared type demands it.
//
// This is where a pattern earns its keep against a hand-written mode: civic
// portals publish money as VARCHAR and dates as MM/DD/YYYY text constantly, and
// summing a VARCHAR either errors or, worse, silently coerces. The cast is
// decided from the type DuckDB reports, not from the column's name.
func resolveColumns(req BuildRequest, concept string) (map[string]string, error) {
	out := map[string]string{}

	for _, param := range req.Pattern.Params {
		col, given := req.Columns[param.Role]
		col = strings.TrimSpace(col)
		if col == "" {
			if param.Required {
				return nil, fmt.Errorf("%s is required: %s", param.Flag, param.Desc)
			}
			continue
		}
		if !given {
			continue
		}

		actual, ok := columnType(req.Table, col)
		if !ok {
			return nil, fmt.Errorf("table %q has no column %q.\n  It has: %s",
				req.Table.Name, col, strings.Join(columnNames(req.Table), ", "))
		}

		expr, err := castFor(param, col, actual, req.DateFormat)
		if err != nil {
			return nil, err
		}
		out[string(param.Role)] = expr
	}
	return out, nil
}

// castFor wraps a column so it satisfies the role's type requirement.
func castFor(param Param, col, declared, dateFormat string) (string, error) {
	quoted := quoteIdent(col)

	switch {
	case param.Numeric && !isNumericType(declared):
		// TRY_CAST rather than CAST: one unparseable row must not take down the
		// whole query, and a NULL is then excluded by the template's own filter
		// and counted against the confidence score.
		return fmt.Sprintf("TRY_CAST(%s AS DOUBLE)", quoted), nil

	case param.Temporal && !isTemporalType(declared):
		if strings.TrimSpace(dateFormat) == "" {
			return "", fmt.Errorf(
				"column %q holds %s, not a date, so csq cannot group by month without "+
					"knowing its layout.\n"+
					"  Pass --date-format with a strptime pattern, e.g.\n"+
					"    --date-format '%%m/%%d/%%Y'   for 03/14/2024\n"+
					"    --date-format '%%Y-%%m-%%d'   for 2024-03-14\n"+
					"  csq will not guess: reading 03/04 as March 4th when it means 4th "+
					"March mislabels a third of the year without erroring",
				col, declared)
		}
		if strings.Contains(dateFormat, "'") {
			return "", fmt.Errorf("--date-format must not contain a quote")
		}
		return fmt.Sprintf("try_strptime(CAST(%s AS VARCHAR), '%s')", quoted, dateFormat), nil
	}
	return quoted, nil
}

// renderSQL substitutes the concept into the template and resolves the output
// column names used for the concentration reading.
func renderSQL(req BuildRequest, concept string) (sqlText, entity, measure string, err error) {
	p := req.Pattern

	if p.Name == coverage.Name {
		sqlText, err = buildCoverageSQL(req, concept)
		if err != nil {
			return "", "", "", err
		}
		return sqlText, "", "", nil
	}

	sqlText = strings.ReplaceAll(p.SQL, ConceptToken, "{{c:"+concept+"}}")

	// The trend and breakdown templates gain a summed column only when the
	// optional measure was supplied. Splicing it here keeps one template per
	// shape instead of two that can drift apart.
	if col, ok := req.Columns[RoleMeasure]; ok && strings.TrimSpace(col) != "" {
		switch p.Name {
		case trend.Name:
			sqlText = strings.Replace(sqlText,
				"COUNT(*)                        AS records",
				"COUNT(*)                        AS records,\n"+
					"       ROUND(SUM(measure), 2)          AS total", 1)
			sqlText = strings.Replace(sqlText,
				"WHERE event_date IS NOT NULL",
				"WHERE event_date IS NOT NULL AND measure IS NOT NULL", 1)
			measure = "total"
			entity = "month"
		case breakdown.Name:
			sqlText = strings.Replace(sqlText,
				"       ROUND(100.0 * COUNT(*)",
				"       ROUND(SUM(measure), 2)                                AS total,\n"+
					"       ROUND(100.0 * COUNT(*)", 1)
			measure = "total"
			entity = "category"
		}
	}
	if entity == "" {
		entity, measure = p.Entity, p.Measure
	}
	return sqlText, entity, measure, nil
}

// buildCoverageSQL writes one UNION ALL arm per column, since a column list is
// not something SQL can iterate over.
func buildCoverageSQL(req BuildRequest, concept string) (string, error) {
	cols := columnNames(req.Table)
	if len(cols) == 0 {
		return "", fmt.Errorf("table %q reports no columns", req.Table.Name)
	}
	// Bounded so a 200-column table does not produce an unreadable query.
	const maxCols = 60
	if len(cols) > maxCols {
		cols = cols[:maxCols]
	}

	var arms []string
	for _, c := range cols {
		q := quoteIdent(c)
		arms = append(arms, fmt.Sprintf(
			"SELECT '%s' AS column_name,\n"+
				"       COUNT(*) AS rows_total,\n"+
				"       COUNT(*) FILTER (WHERE %s IS NOT NULL "+
				"AND TRIM(CAST(%s AS VARCHAR)) <> '') AS rows_populated\n"+
				"FROM {{c:%s}}",
			strings.ReplaceAll(c, "'", "''"), q, q, concept))
	}

	return fmt.Sprintf(`
WITH profile AS (
%s
)
SELECT column_name,
       rows_total,
       rows_populated,
       ROUND(100.0 * rows_populated / NULLIF(rows_total, 0), 1) AS pct_populated
FROM profile
ORDER BY pct_populated ASC, column_name`, strings.Join(arms, "\nUNION ALL\n")), nil
}

// conceptColumns splits the roles into the concept's required and optional
// column lists. A role the pattern marks optional and the user did not supply
// appears in neither: declaring a column the binding does not map would make
// the mode unloadable.
func conceptColumns(p *Pattern, req BuildRequest) (required, optional []string) {
	for _, param := range p.Params {
		col, ok := req.Columns[param.Role]
		if !ok || strings.TrimSpace(col) == "" {
			continue
		}
		if param.Required {
			required = append(required, string(param.Role))
			continue
		}
		optional = append(optional, string(param.Role))
	}
	sort.Strings(required)
	sort.Strings(optional)
	return required, optional
}

// defaultQueryName derives a readable slug from the pattern and the columns it
// was pointed at, so several instantiations in one mode stay distinguishable.
func defaultQueryName(p *Pattern, req BuildRequest) string {
	parts := []string{p.Name}
	for _, r := range []Role{RoleEntity, RoleCategory, RoleGroup, RoleDate} {
		if col, ok := req.Columns[r]; ok && strings.TrimSpace(col) != "" {
			parts = append(parts, sanitiseIdent(col))
			break
		}
	}
	if len(parts) == 1 {
		parts = append(parts, sanitiseIdent(req.Table.Name))
	}
	return strings.Join(parts, "-")
}

func describeRoles(req BuildRequest) string {
	var parts []string
	for _, param := range req.Pattern.Params {
		if col, ok := req.Columns[param.Role]; ok && strings.TrimSpace(col) != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", param.Role, col))
		}
	}
	if len(parts) == 0 {
		return "all columns"
	}
	return strings.Join(parts, ", ")
}

func columnType(t personal.Table, name string) (string, bool) {
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return c.Type, true
		}
	}
	return "", false
}

func columnNames(t personal.Table) []string {
	out := make([]string, 0, len(t.Columns))
	for _, c := range t.Columns {
		out = append(out, c.Name)
	}
	return out
}

func isNumericType(t string) bool {
	u := strings.ToUpper(t)
	for _, n := range []string{"INT", "DECIMAL", "NUMERIC", "DOUBLE", "FLOAT", "REAL", "HUGEINT"} {
		if strings.Contains(u, n) {
			return true
		}
	}
	return false
}

func isTextType(t string) bool {
	u := strings.ToUpper(t)
	return strings.Contains(u, "VARCHAR") || strings.Contains(u, "CHAR") ||
		u == "TEXT" || u == "STRING"
}

func isTemporalType(t string) bool {
	u := strings.ToUpper(t)
	return strings.Contains(u, "DATE") || strings.Contains(u, "TIMESTAMP")
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

// sanitiseIdent reduces a name to something usable as a concept or slug.
func sanitiseIdent(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-' || r == ' ':
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "records"
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}
