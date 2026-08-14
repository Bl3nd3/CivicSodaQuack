// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// A Concept is a logical table a mode needs, described by what it must contain
// rather than by which dataset supplies it. "contracts with a vendor and an
// award amount" is a concept; `rsxa-ify5` is one portal's answer to it.
//
// This indirection is what makes a mode portable. Without it, a mode named
// "police monitoring" is really "Chicago police monitoring", and every new city
// needs a code change rather than a config file.
type Concept struct {
	Name     string   // referenced in SQL as {{c:name}}
	Purpose  string   // why the mode needs it
	Required []string // columns the queries read; a binding missing one is invalid
	Optional []string // columns some queries use when present
}

// BoundDataset is one portal's answer to a Concept.
type BoundDataset struct {
	ID    string // Socrata 4x4 on this portal
	Table string // local DuckDB table name
	Name  string // upstream dataset title
	Rows  int64  // approximate upstream row count, informational
	Notes string // definitional caveats specific to this portal

	// Columns maps a concept's column name to this portal's actual column, so
	// queries can be written once against canonical names. Chicago calls an
	// offence date `date`; NYC calls it `cmplnt_fr_dt`.
	//
	// When non-empty this map is authoritative: a concept column absent from it
	// is treated as unavailable on this portal, and any query needing it
	// excludes the city with a reason. That explicitness matters — assuming a
	// missing column is merely unmapped would let a NULL read as a real value,
	// which for a rate or a share is indistinguishable from a good result.
	//
	// An empty map means the table already uses the concept's own names.
	Columns map[string]string
}

// ColumnFor returns this portal's column for a concept column, and whether it
// is available at all.
func (bd BoundDataset) ColumnFor(conceptCol string) (string, bool) {
	if len(bd.Columns) == 0 {
		return conceptCol, true // identity: the table already uses canonical names
	}
	actual, ok := bd.Columns[conceptCol]
	return actual, ok
}

// CanonicalView builds a SELECT that renames this portal's columns to the
// concept's canonical ones, so every query reads the same shape whichever city
// it runs against.
func (c Concept) CanonicalView(qualifiedTable string, bd BoundDataset) string {
	cols := make([]string, 0, len(c.Required)+len(c.Optional))
	for _, name := range append(append([]string{}, c.Required...), c.Optional...) {
		actual, ok := bd.ColumnFor(name)
		if !ok {
			continue // unavailable here; queries needing it are gated out
		}
		if actual == name {
			cols = append(cols, name)
			continue
		}
		cols = append(cols, fmt.Sprintf("%s AS %s", actual, name))
	}
	if len(cols) == 0 {
		return "SELECT * FROM " + qualifiedTable
	}
	return "SELECT " + strings.Join(cols, ", ") + " FROM " + qualifiedTable
}

// A Binding maps one portal's datasets onto one mode's concepts. Bindings are
// data: adding a city means adding a Binding, not editing a mode.
type Binding struct {
	Mode   string // mode name this binding satisfies
	Portal string // Socrata host
	City   string // human label, e.g. "Chicago, IL"
	// Concepts maps concept name → the dataset fulfilling it. A concept absent
	// here is unbound: queries needing it are skipped with an explanation
	// rather than failing with a binder error.
	Concepts map[string]BoundDataset
	// Notes records portal-wide caveats a user must read before comparing this
	// city with another.
	Notes []string

	// Population is the city's resident count, used to normalise indicators
	// into per-capita rates. Zero means unknown, which excludes this city from
	// any rate comparison rather than silently comparing raw counts — a raw
	// count ranking is a population ranking wearing a disguise.
	Population int64
	// PopulationSource names where Population came from. Required whenever
	// Population is set: a denominator without a citation is not usable in a
	// comparison someone might act on.
	PopulationSource string

	// Source is the file an external binding was loaded from; empty for
	// built-ins. Shown by `csq modes show` so a user can tell which file to edit.
	Source string
}

// bindingRegistry is the set of built-in bindings, keyed by mode then portal.
var bindingRegistry = map[string]map[string]*Binding{}

// registerBinding adds a binding to the registry. Called from per-city files.
func registerBinding(b *Binding) {
	if bindingRegistry[b.Mode] == nil {
		bindingRegistry[b.Mode] = map[string]*Binding{}
	}
	bindingRegistry[b.Mode][b.Portal] = b
}

// BindingsFor returns every registered binding for a mode, portal-sorted.
func BindingsFor(mode string) []*Binding {
	byPortal := bindingRegistry[mode]
	out := make([]*Binding, 0, len(byPortal))
	for _, b := range byPortal {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Portal < out[j].Portal })
	return out
}

// LookupBinding finds the binding for a mode and portal.
func LookupBinding(mode, portal string) (*Binding, error) {
	b, ok := bindingRegistry[mode][portal]
	if !ok {
		avail := BindingsFor(mode)
		if len(avail) == 0 {
			return nil, fmt.Errorf("mode %q has no portal bindings", mode)
		}
		names := make([]string, 0, len(avail))
		for _, a := range avail {
			names = append(names, a.Portal)
		}
		return nil, fmt.Errorf(
			"mode %q has no binding for portal %q (have: %s)\n"+
				"  a binding maps this mode's concepts onto one portal's datasets; "+
				"adding a city means adding a binding, not changing the mode",
			mode, portal, strings.Join(names, ", "))
	}
	return b, nil
}

// Concept returns the mode's concept by name.
func (m *Mode) Concept(name string) (Concept, bool) {
	for _, c := range m.Concepts {
		if c.Name == name {
			return c, true
		}
	}
	return Concept{}, false
}

// Unbound lists the mode's concepts this binding does not satisfy.
func (m *Mode) Unbound(b *Binding) []string {
	var out []string
	for _, c := range m.Concepts {
		if _, ok := b.Concepts[c.Name]; !ok {
			out = append(out, c.Name)
		}
	}
	sort.Strings(out)
	return out
}

// conceptRefs extracts the concept names a query reads via {{c:name}}.
func conceptRefs(sqlText string) []string {
	const open = "{{c:"
	var out []string
	rest := sqlText
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return out
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, "}}")
		if j < 0 {
			return out
		}
		name := strings.TrimSpace(rest[:j])
		if name != "" && !contains(out, name) {
			out = append(out, name)
		}
		rest = rest[j+2:]
	}
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// Runnable reports whether every concept a query needs is bound, and if not,
// which are missing. Callers use this to skip a query with an explanation
// instead of surfacing a raw binder error.
func (m *Mode) Runnable(q Query, b *Binding) (bool, []string) {
	var missing []string
	for _, name := range conceptRefs(q.SQL) {
		if _, ok := b.Concepts[name]; !ok {
			missing = append(missing, name)
		}
	}
	return len(missing) == 0, missing
}

// ExpandConcepts substitutes {{c:name}} for the bound table, qualified by the
// portal alias, {{P}} for the alias, and {{POP}} for the city's population.
func ExpandConcepts(sqlText string, alias string, b *Binding) (string, error) {
	return expandWith(sqlText, alias, b, nil)
}

// ExpandConceptsFor is ExpandConcepts with the mode available, so concept
// columns can be renamed to their canonical names for this portal.
func ExpandConceptsFor(m *Mode, sqlText string, alias string, b *Binding) (string, error) {
	return expandWith(sqlText, alias, b, m)
}

func expandWith(sqlText string, alias string, b *Binding, m *Mode) (string, error) {
	for _, name := range conceptRefs(sqlText) {
		bd, ok := b.Concepts[name]
		if !ok {
			return "", fmt.Errorf("concept %q is not bound for portal %s", name, b.Portal)
		}
		qualified := alias + ".main." + bd.Table
		replacement := qualified
		if m != nil {
			if c, ok := m.Concept(name); ok {
				// Wrap in a canonical projection so the query can use the
				// concept's column names whatever this portal calls them.
				replacement = "(" + c.CanonicalView(qualified, bd) + ")"
			}
		}
		sqlText = strings.ReplaceAll(sqlText, "{{c:"+name+"}}", replacement)
	}
	if strings.Contains(sqlText, PlaceholderPopulation) {
		if b.Population <= 0 {
			return "", fmt.Errorf(
				"%s has no population recorded, so per-capita rates cannot be computed; "+
					"add population and population_source to its binding", b.Portal)
		}
		sqlText = strings.ReplaceAll(sqlText, PlaceholderPopulation,
			fmt.Sprintf("%d", b.Population))
	}
	return strings.ReplaceAll(sqlText, PlaceholderPortal, alias), nil
}

// NeedsPopulation reports whether a query normalises by population.
func NeedsPopulation(q Query) bool {
	return strings.Contains(q.SQL, PlaceholderPopulation)
}

// Comparable reports whether this binding can answer the query: every concept
// bound, every column the query reads available on this portal, and a
// population if the query needs one. The reason is returned so the runner can
// say why a city was left out rather than silently dropping it — absent data
// must never read as a good result.
func (m *Mode) Comparable(q Query, b *Binding) (bool, string) {
	if ok, missing := m.Runnable(q, b); !ok {
		return false, "does not publish " + strings.Join(missing, ", ")
	}
	if NeedsPopulation(q) && b.Population <= 0 {
		return false, "no population recorded, so a per-capita rate cannot be computed"
	}
	if missing := m.missingColumns(q, b); len(missing) > 0 {
		return false, "does not record " + strings.Join(missing, ", ")
	}
	return true, ""
}

// missingColumns lists concept columns this query reads that the portal does
// not provide. Only optional columns can be missing — a binding lacking a
// required column is a broken binding, caught at load time.
func (m *Mode) missingColumns(q Query, b *Binding) []string {
	var missing []string
	for _, name := range conceptRefs(q.SQL) {
		c, ok := m.Concept(name)
		if !ok {
			continue
		}
		bd, ok := b.Concepts[name]
		if !ok {
			continue
		}
		for _, col := range c.Optional {
			if !queryUsesColumn(q.SQL, col) {
				continue
			}
			if _, avail := bd.ColumnFor(col); !avail {
				missing = append(missing, col)
			}
		}
	}
	sort.Strings(missing)
	return missing
}

// queryUsesColumn reports whether the SQL references col as a whole word.
func queryUsesColumn(sqlText, col string) bool {
	lower := strings.ToLower(sqlText)
	target := strings.ToLower(col)
	for i := 0; ; {
		j := strings.Index(lower[i:], target)
		if j < 0 {
			return false
		}
		start := i + j
		end := start + len(target)
		beforeOK := start == 0 || !isIdentByte(lower[start-1])
		afterOK := end == len(lower) || !isIdentByte(lower[end])
		if beforeOK && afterOK {
			return true
		}
		i = end
	}
}

func isIdentByte(b byte) bool {
	return b == '_' || (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// PortalFromDBPath derives the Socrata host a database was synced from. csq
// does not record the portal inside the file, so the filename is the only
// signal available — the same convention `csq snapshot` uses. Callers should
// let the user override it.
func PortalFromDBPath(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".duckdb")
}

// ConfigYAMLFor renders a sync config for one portal's binding of this mode.
func (m *Mode) ConfigYAMLFor(b *Binding) (string, error) {
	if len(b.Concepts) == 0 {
		return "", fmt.Errorf("binding for %s has no datasets", b.Portal)
	}
	// Emit in concept order so the file reads like the mode's structure.
	type row struct {
		concept string
		bd      BoundDataset
	}
	var rows []row
	var total int64
	for _, c := range m.Concepts {
		if bd, ok := b.Concepts[c.Name]; ok {
			rows = append(rows, row{c.Name, bd})
			total += bd.Rows
		}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "# Generated by: csq modes init %s --portal %s\n", m.Name, b.Portal)
	fmt.Fprintf(&sb, "# %s — %s\n#\n", m.Title, b.City)
	for _, line := range wrap(m.About, 74) {
		fmt.Fprintf(&sb, "# %s\n", line)
	}
	if missing := m.Unbound(b); len(missing) > 0 {
		fmt.Fprintf(&sb, "#\n# Concepts this portal does not publish: %s\n",
			strings.Join(missing, ", "))
		fmt.Fprintf(&sb, "# Queries needing them are skipped rather than guessed at.\n")
	}
	if len(b.Notes) > 0 {
		fmt.Fprintf(&sb, "#\n# Portal notes:\n")
		for _, n := range b.Notes {
			for i, line := range wrap(n, 70) {
				if i == 0 {
					fmt.Fprintf(&sb, "#   - %s\n", line)
				} else {
					fmt.Fprintf(&sb, "#     %s\n", line)
				}
			}
		}
	}
	fmt.Fprintf(&sb, "#\n# Interpretation caveats:\n")
	for _, c := range m.Caveats {
		for i, line := range wrap(c, 70) {
			if i == 0 {
				fmt.Fprintf(&sb, "#   - %s\n", line)
			} else {
				fmt.Fprintf(&sb, "#     %s\n", line)
			}
		}
	}

	fmt.Fprintf(&sb, "\nportal: %s\n", b.Portal)
	fmt.Fprintf(&sb, "# app_token: ${SOCRATA_APP_TOKEN}   # anonymous works but is rate-limited\n")
	fmt.Fprintf(&sb, "concurrency: 4\non_error: continue\n\n")
	fmt.Fprintf(&sb, "defaults:\n  batch_size: 10000\n  order_by: \":id\"\n\n")
	fmt.Fprintf(&sb, "include:\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "  - id: %s    # %s → %s (~%s rows)\n",
			r.bd.ID, r.concept, r.bd.Name, commas(r.bd.Rows))
	}
	fmt.Fprintf(&sb, "\noverrides:\n")
	for _, r := range rows {
		fmt.Fprintf(&sb, "  %s:\n    table: %s\n", r.bd.ID, r.bd.Table)
	}
	return sb.String(), nil
}

// ApproxRowsFor totals the upstream rows a binding will sync.
func (m *Mode) ApproxRowsFor(b *Binding) int64 {
	var n int64
	for _, bd := range b.Concepts {
		n += bd.Rows
	}
	return n
}
