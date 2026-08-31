// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"fmt"
	"strings"
)

// DefaultModeName is the mode a built profile lands in unless --as names
// another. "personal" is the user's own mode: whatever they have asked about,
// accumulated as queries they can read and edit.
const DefaultModeName = "personal"

// Draft is a mode and the binding that makes it runnable on one portal.
//
// The name is a holdover from when these documents arrived from a model. They
// are now built from reviewed SQL patterns, and the type survives because what
// it holds — a mode plus its binding, before either is validated or saved — is
// the same unit of work either way.
type Draft struct {
	Mode    *Document `json:"mode"`
	Binding *Document `json:"binding"`
}

// Document is one mode-or-binding file. It mirrors the loader's own shape, so a
// generated file and a hand-written one are the same artefact — see
// modes.DocumentSchema, which describes both.
type Document struct {
	Kind string `json:"kind"`

	Name     string     `json:"name,omitempty"`
	Title    string     `json:"title,omitempty"`
	Summary  string     `json:"summary,omitempty"`
	About    string     `json:"about,omitempty"`
	Concepts []Concept  `json:"concepts,omitempty"`
	Queries  []DocQuery `json:"queries,omitempty"`
	Caveats  []string   `json:"caveats,omitempty"`

	Mode             string                `json:"mode,omitempty"`
	Portal           string                `json:"portal,omitempty"`
	City             string                `json:"city,omitempty"`
	Population       int64                 `json:"population,omitempty"`
	PopulationSource string                `json:"population_source,omitempty"`
	Datasets         map[string]DocDataset `json:"datasets,omitempty"`
	Notes            []string              `json:"notes,omitempty"`
}

// Concept is a logical table the queries read.
type Concept struct {
	Name     string   `json:"name"`
	Purpose  string   `json:"purpose"`
	Required []string `json:"required,omitempty"`
	Optional []string `json:"optional,omitempty"`
}

// DocQuery is one analysis.
type DocQuery struct {
	Name    string `json:"name"`
	Desc    string `json:"desc"`
	SQL     string `json:"sql"`
	Entity  string `json:"entity,omitempty"`
	Measure string `json:"measure,omitempty"`
}

// DocDataset is one concept's binding to a local table.
type DocDataset struct {
	ID      string            `json:"id"`
	Table   string            `json:"table"`
	Name    string            `json:"name"`
	Rows    int64             `json:"rows,omitempty"`
	Notes   string            `json:"notes,omitempty"`
	Columns map[string]string `json:"columns,omitempty"`
}

// MergeMode folds a newly built document into the one already on disk.
//
// The existing file wins every conflict. A user who edited a caveat, renamed a
// query, or corrected a column mapping must not have that work reverted by the
// next question they ask — each run is extending their file, not owning it.
func MergeMode(existing, built *Document) *Document {
	out := *existing
	out.Kind = "mode"

	// Concepts are merged column-wise, not skipped wholesale. A second query
	// against a table the mode already knows commonly needs one more column
	// from it — a date to group by, a category to split on. Keeping only the
	// existing concept would leave that column undeclared, and the query that
	// reads it would then fail to load. The existing *wording* still wins; it
	// is only the column lists that grow.
	out.Concepts = append([]Concept(nil), out.Concepts...)
	byName := map[string]int{}
	for i, c := range out.Concepts {
		byName[c.Name] = i
	}
	for _, c := range built.Concepts {
		i, ok := byName[c.Name]
		if !ok {
			out.Concepts = append(out.Concepts, c)
			byName[c.Name] = len(out.Concepts) - 1
			continue
		}
		existing := out.Concepts[i]
		for _, col := range c.Required {
			existing.Required = appendUnique(existing.Required, col)
		}
		for _, col := range c.Optional {
			// A column already required must not be demoted to optional.
			if !containsString(existing.Required, col) {
				existing.Optional = appendUnique(existing.Optional, col)
			}
		}
		out.Concepts[i] = existing
	}

	taken := map[string]bool{}
	for _, q := range out.Queries {
		taken[q.Name] = true
	}
	for _, q := range built.Queries {
		q.Name = uniqueName(q.Name, taken)
		taken[q.Name] = true
		out.Queries = append(out.Queries, q)
	}

	for _, c := range built.Caveats {
		out.Caveats = appendUnique(out.Caveats, c)
	}
	return &out
}

// MergeBinding folds a newly built binding into one already on disk, on the
// same terms as MergeMode: what is there already stays.
func MergeBinding(existing, built *Document) *Document {
	if existing == nil {
		return built
	}
	out := *existing
	out.Kind = "binding"
	if out.Datasets == nil {
		out.Datasets = map[string]DocDataset{}
	}
	for name, ds := range built.Datasets {
		prev, ok := out.Datasets[name]
		if !ok {
			out.Datasets[name] = ds
			continue
		}
		// Same reasoning as concepts: a new query on a known table usually
		// needs one more column mapped. Dropping the whole new dataset would
		// leave that mapping missing, and because a non-empty columns map is
		// authoritative, the column would read as unavailable rather than as
		// simply unmapped. The user's existing mappings still win one by one.
		if len(ds.Columns) > 0 {
			if prev.Columns == nil {
				prev.Columns = map[string]string{}
			}
			for canonical, expr := range ds.Columns {
				if _, taken := prev.Columns[canonical]; !taken {
					prev.Columns[canonical] = expr
				}
			}
		}
		out.Datasets[name] = prev
	}
	for _, n := range built.Notes {
		out.Notes = appendUnique(out.Notes, n)
	}
	return &out
}

func uniqueName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d", name, i)
		if !taken[candidate] {
			return candidate
		}
	}
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(s)) {
			return list
		}
	}
	return append(list, s)
}
