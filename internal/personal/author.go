// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/llm"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// DefaultModeName is the mode a drafted profile lands in unless --as names
// another. "personal" is the user's own mode: whatever they have asked about,
// accumulated as queries they can read and edit.
const DefaultModeName = "personal"

// Request is one authoring run.
type Request struct {
	// Question is what the user asked, in their own words.
	Question string
	// ModeName is the mode to create or extend.
	ModeName string
	// Portal is the inventory the queries must run against.
	Portal *Portal
	// City labels the jurisdiction in the binding.
	City string
	// Existing is the current mode document, when one is already on disk.
	// Queries are appended to it rather than replacing it.
	Existing *Document
}

// Draft is what the model returned, after parsing and local checks.
type Draft struct {
	Mode    *Document `json:"mode"`
	Binding *Document `json:"binding"`
}

// Document is one mode-or-binding file. It mirrors the loader's own shape so a
// drafted file and a hand-written one are the same artefact — see
// modes.DocumentSchema, which is what the model is constrained to.
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

// Concept is a logical table the drafted queries read.
type Concept struct {
	Name     string   `json:"name"`
	Purpose  string   `json:"purpose"`
	Required []string `json:"required,omitempty"`
	Optional []string `json:"optional,omitempty"`
}

// DocQuery is one drafted analysis.
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

// GeneratedCaveat is added to every drafted mode and never removed.
//
// A reader looking at a table of numbers cannot tell which SQL a person wrote
// and which a model did, and the difference changes how much checking the
// numbers deserve. Every other caveat in a mode is the author's judgement;
// this one is a fact about the file's provenance, so csq asserts it rather
// than asking the model to remember.
const GeneratedCaveat = "These queries were drafted by a language model from the column " +
	"names of your local tables, then run unchanged by csq. The model did not see the data " +
	"and did not produce any number here — but it did choose which columns to trust, and it " +
	"can misread what a column means. Read the SQL under 'csq modes show' before quoting a " +
	"result, and treat a surprising figure as a reason to check the query first."

// Author asks the model for a mode and a binding, then checks what came back.
//
// The returned draft has been parsed, had its identity fields forced to match
// what the caller asked for, and had every query's SQL checked as read-only. It
// has not yet been validated by the loader or executed — see Save and Verify.
func Author(ctx context.Context, c *llm.Client, req Request) (*Draft, error) {
	if strings.TrimSpace(req.Question) == "" {
		return nil, fmt.Errorf("no question given")
	}
	if req.Portal == nil || len(req.Portal.Tables) == 0 {
		return nil, fmt.Errorf(
			"this database holds no synced tables, so there is nothing to write a mode against.\n" +
				"  Sync something first, e.g. 'csq modes init corruption --output c.yaml && csq sync --config c.yaml'")
	}

	raw, err := c.JSON(ctx, llm.JSONRequest{
		System: systemPrompt(),
		User:   userPrompt(req),
		Schema: draftSchema(),
	})
	if err != nil {
		return nil, err
	}

	var d Draft
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("the model's draft did not parse: %w", err)
	}
	if d.Mode == nil || d.Binding == nil {
		return nil, fmt.Errorf("the model's draft was missing its mode or its binding")
	}

	// Identity is csq's to set, not the model's. Forcing these removes a whole
	// class of failure — a draft naming the wrong portal, or a binding pointing
	// at a mode that does not exist — without another round trip.
	d.Mode.Kind = "mode"
	d.Mode.Name = req.ModeName
	d.Binding.Kind = "binding"
	d.Binding.Mode = req.ModeName
	d.Binding.Portal = req.Portal.Host
	if strings.TrimSpace(d.Binding.City) == "" {
		d.Binding.City = req.City
	}

	// A population the model recalled from training is a denominator with no
	// citation, and a per-capita rate computed from it is confidently wrong.
	// The loader would reject it anyway; dropping it here says why.
	d.Binding.Population = 0
	d.Binding.PopulationSource = ""

	if err := d.check(req.Portal); err != nil {
		return nil, err
	}
	d.Mode.Caveats = appendUnique(d.Mode.Caveats, GeneratedCaveat)

	if req.Existing != nil {
		d.Mode = mergeMode(req.Existing, d.Mode)
	}
	return &d, nil
}

// check applies the constraints the schema cannot express: that SQL is
// read-only, that concepts referenced are declared and bound, and that bound
// tables actually exist locally.
func (d *Draft) check(p *Portal) error {
	if len(d.Mode.Queries) == 0 {
		return fmt.Errorf("the model returned no queries")
	}

	declared := map[string]bool{}
	for _, c := range d.Mode.Concepts {
		declared[c.Name] = true
	}

	for _, q := range d.Mode.Queries {
		if err := CheckReadOnly(q.SQL); err != nil {
			return fmt.Errorf("drafted query %q was rejected: %w", q.Name, err)
		}
		refs := modes.ConceptRefs(q.SQL)
		if len(refs) == 0 {
			return fmt.Errorf("drafted query %q names its tables directly instead of "+
				"using {{c:concept}}, so it would only ever work on this one portal", q.Name)
		}
		for _, r := range refs {
			if !declared[r] {
				return fmt.Errorf("drafted query %q reads {{c:%s}}, which the draft never "+
					"declares as a concept", q.Name, r)
			}
			if _, ok := d.Binding.Datasets[r]; !ok {
				return fmt.Errorf("drafted query %q reads {{c:%s}}, which the draft's "+
					"binding never maps to a table", q.Name, r)
			}
		}
	}

	// Every bound table must be one csq actually holds. A hallucinated table
	// name would otherwise surface much later as a DuckDB binder error in the
	// middle of a result.
	for concept, ds := range d.Binding.Datasets {
		t, ok := p.Table(ds.Table)
		if !ok {
			return fmt.Errorf("the draft binds concept %q to table %q, which this database "+
				"does not have (it holds: %s)",
				concept, ds.Table, strings.Join(p.TableNames(), ", "))
		}
		// Fill in the facts csq knows better than the model does.
		ds.Rows = t.Rows
		if t.DatasetID != "" {
			ds.ID = t.DatasetID
		}
		if t.DatasetName != "" {
			ds.Name = t.DatasetName
		}
		if err := checkColumns(concept, ds, t); err != nil {
			return err
		}
		d.Binding.Datasets[concept] = ds
	}
	return nil
}

// checkColumns verifies that every plain column mapping names a real column.
//
// A mapping may also be an SQL expression, which cannot be checked this way and
// is left for the verification pass to execute. The distinction is drawn by
// looking for anything that is not a bare identifier.
func checkColumns(concept string, ds DocDataset, t Table) error {
	have := map[string]bool{}
	for _, c := range t.Columns {
		have[strings.ToLower(c.Name)] = true
	}
	for canonical, expr := range ds.Columns {
		if !isBareIdentifier(expr) {
			continue // an expression; the EXPLAIN pass will judge it
		}
		if !have[strings.ToLower(expr)] {
			return fmt.Errorf("the draft maps %s.%s to column %q, which table %q does not "+
				"have", concept, canonical, expr, t.Name)
		}
	}
	return nil
}

func isBareIdentifier(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isIdentByte(s[i]) {
			return false
		}
	}
	return true
}

// mergeMode folds a new draft into the document already on disk.
//
// The existing file wins every conflict. A user who edited a caveat, renamed a
// query, or corrected a column mapping must not have that work reverted by the
// next question they ask — the model is extending their file, not owning it.
func mergeMode(existing, drafted *Document) *Document {
	out := *existing
	out.Kind = "mode"

	seenConcept := map[string]bool{}
	for _, c := range out.Concepts {
		seenConcept[c.Name] = true
	}
	for _, c := range drafted.Concepts {
		if !seenConcept[c.Name] {
			out.Concepts = append(out.Concepts, c)
			seenConcept[c.Name] = true
		}
	}

	taken := map[string]bool{}
	for _, q := range out.Queries {
		taken[q.Name] = true
	}
	for _, q := range drafted.Queries {
		q.Name = uniqueName(q.Name, taken)
		taken[q.Name] = true
		out.Queries = append(out.Queries, q)
	}

	for _, c := range drafted.Caveats {
		out.Caveats = appendUnique(out.Caveats, c)
	}
	return &out
}

// MergeBinding folds a drafted binding into one already on disk, on the same
// terms as mergeMode: what is there already stays.
func MergeBinding(existing, drafted *Document) *Document {
	if existing == nil {
		return drafted
	}
	out := *existing
	out.Kind = "binding"
	if out.Datasets == nil {
		out.Datasets = map[string]DocDataset{}
	}
	for name, ds := range drafted.Datasets {
		if _, ok := out.Datasets[name]; !ok {
			out.Datasets[name] = ds
		}
	}
	for _, n := range drafted.Notes {
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

func appendUnique(list []string, s string) []string {
	for _, existing := range list {
		if strings.EqualFold(strings.TrimSpace(existing), strings.TrimSpace(s)) {
			return list
		}
	}
	return append(list, s)
}
