// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The JSON path exists so a program can write a mode file. These tests hold it
// to the same contract as the YAML path — same fields, same validation, same
// refusal to ignore a typo — because the moment the two diverge, a mode drafted
// by `csq modes personal` starts meaning something different from the same
// document written by hand.

func writeTemp(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

const jsonMode = `{
  "kind": "mode",
  "name": "json-test",
  "title": "JSON test mode",
  "summary": "A mode declared in JSON",
  "about": "Exists to prove the JSON loader accepts the same document YAML does.",
  "concepts": [
    {"name": "permits", "purpose": "Building permits with a value.",
     "required": ["permit_id", "estimated_cost"], "optional": ["issue_date"]}
  ],
  "queries": [
    {"name": "top-permits", "desc": "Permits by cost.",
     "entity": "permit_id", "measure": "estimated_cost",
     "sql": "SELECT permit_id, estimated_cost FROM {{c:permits}} ORDER BY estimated_cost DESC LIMIT 10"}
  ],
  "caveats": ["An estimated cost is not a final cost."]
}`

func TestLintFile_AcceptsJSONMode(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "m.json", jsonMode)

	kind, name, err := LintFile(path)
	if err != nil {
		t.Fatalf("lint JSON mode: %v", err)
	}
	if kind != "mode" || name != "json-test" {
		t.Errorf("got kind=%q name=%q, want mode/json-test", kind, name)
	}
}

// A mistyped key must fail rather than be dropped. "caveats" silently ignored
// would produce a mode that runs and answers without its limits attached.
func TestLintFile_JSONUnknownKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(jsonMode, `"caveats"`, `"caveat"`, 1)
	path := writeTemp(t, dir, "m.json", body)

	if _, _, err := LintFile(path); err == nil {
		t.Fatal("expected an error for an unknown key, got none")
	}
}

func TestLintFile_JSONRejectsSecondDocument(t *testing.T) {
	dir := t.TempDir()
	path := writeTemp(t, dir, "m.json", jsonMode+"\n"+jsonMode)

	_, _, err := LintFile(path)
	if err == nil {
		t.Fatal("expected an error for two documents in one file")
	}
	if !strings.Contains(err.Error(), "more than one JSON document") {
		t.Errorf("error should name the problem, got: %v", err)
	}
}

// Entity and Measure drive the concentration reading. One without the other is
// an authoring mistake, and accepting it would hide that behind a result that
// simply never reports concentration.
func TestLintFile_EntityWithoutMeasureIsAnError(t *testing.T) {
	dir := t.TempDir()
	body := strings.Replace(jsonMode, `"measure": "estimated_cost",`, "", 1)
	path := writeTemp(t, dir, "m.json", body)

	_, _, err := LintFile(path)
	if err == nil {
		t.Fatal("expected an error when only entity is set")
	}
	if !strings.Contains(err.Error(), "entity") {
		t.Errorf("error should name the field, got: %v", err)
	}
}

// Entity and Measure must survive the load, or a JSON mode silently loses the
// concentration reading its author asked for.
func TestLoadDir_JSONCarriesEntityAndMeasure(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "m.json", jsonMode)
	t.Cleanup(func() { removeMode("json-test") })

	if _, err := LoadDir(dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	m, err := Lookup("json-test")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	q, err := m.Query("top-permits")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if q.Entity != "permit_id" || q.Measure != "estimated_cost" {
		t.Errorf("entity/measure lost: got %q/%q", q.Entity, q.Measure)
	}
}

const jsonBinding = `{
  "kind": "binding",
  "mode": "json-test",
  "portal": "data.example.gov",
  "city": "Example, EX",
  "datasets": {
    "permits": {
      "id": "abcd-1234",
      "table": "permits",
      "name": "Building Permits",
      "rows": 1000,
      "columns": {
        "permit_id": "permit_",
        "estimated_cost": "TRY_CAST(est_cost AS DOUBLE)",
        "issue_date": "try_strptime(issued, '%m/%d/%Y')"
      }
    }
  }
}`

// A column map may hold an SQL expression, not just a column name. That escape
// hatch is what lets a portal publishing a date as text bind at all, so the
// JSON path has to carry it through to BoundDataset.Columns intact.
func TestLoadDir_JSONBindingCarriesColumnExpressions(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "mode.json", jsonMode)
	writeTemp(t, dir, "binding.json", jsonBinding)
	t.Cleanup(func() { removeMode("json-test"); removeBinding("json-test") })

	if _, err := LoadDir(dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	b, err := LookupBinding("json-test", "data.example.gov")
	if err != nil {
		t.Fatalf("lookup binding: %v", err)
	}
	bd := b.Concepts["permits"]
	if got := bd.Columns["estimated_cost"]; got != "TRY_CAST(est_cost AS DOUBLE)" {
		t.Errorf("expression mapping lost: %q", got)
	}

	// The expression must reach the generated SQL verbatim, aliased to the
	// canonical name — that is the whole mechanism.
	m, _ := Lookup("json-test")
	c, _ := m.Concept("permits")
	view := c.CanonicalView("p.main.permits", bd)
	if !strings.Contains(view, "TRY_CAST(est_cost AS DOUBLE) AS estimated_cost") {
		t.Errorf("canonical view lost the expression: %s", view)
	}
}

// A non-empty column map is authoritative, so omitting a required column is not
// "unmapped" — it is a portal that cannot satisfy the concept, and every query
// reading it would project a column that is not there.
func TestLoadDir_BindingOmittingRequiredColumnFails(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "mode.json", jsonMode)
	body := strings.Replace(jsonBinding,
		`"estimated_cost": "TRY_CAST(est_cost AS DOUBLE)",`, "", 1)
	writeTemp(t, dir, "binding.json", body)
	t.Cleanup(func() { removeMode("json-test"); removeBinding("json-test") })

	_, err := LoadDir(dir)
	if err == nil {
		t.Fatal("expected an error for a missing required column")
	}
	if !strings.Contains(err.Error(), "estimated_cost") {
		t.Errorf("error should name the missing column, got: %v", err)
	}
}

// An external mode replaces a built-in of the same name. That is what makes the
// personal mode work: the stub ships in the binary, and the drafted file takes
// its place with no special case anywhere downstream.
func TestLoadDir_ExternalModeReplacesPersonalStub(t *testing.T) {
	before, err := Lookup("personal")
	if err != nil {
		t.Fatalf("personal should be built in: %v", err)
	}
	if before.Source != "" {
		t.Fatalf("built-in personal should have no Source, got %q", before.Source)
	}
	stubQueries := len(before.Queries)

	dir := t.TempDir()
	body := strings.Replace(jsonMode, `"name": "json-test"`, `"name": "personal"`, 1)
	path := writeTemp(t, dir, "personal.json", body)
	t.Cleanup(func() { restoreMode(before) })

	if _, err := LoadDir(dir); err != nil {
		t.Fatalf("load: %v", err)
	}
	after, err := Lookup("personal")
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if after.Source != path {
		t.Errorf("replaced mode should record its file, got %q", after.Source)
	}
	if len(after.Queries) == stubQueries && stubQueries != 1 {
		t.Errorf("stub was not replaced: still %d queries", len(after.Queries))
	}
	if len(after.Concepts) != 1 || after.Concepts[0].Name != "permits" {
		t.Errorf("replaced mode lost its concepts: %+v", after.Concepts)
	}
}

// The printed schema is the contract a hand author reads and the model is held
// to, so it has to actually describe the document the loader accepts.
func TestSchemaJSONDescribesBothDocuments(t *testing.T) {
	s, err := SchemaJSON()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(s), &doc); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	variants, ok := doc["oneOf"].([]any)
	if !ok || len(variants) != 2 {
		t.Fatalf("schema should offer a mode and a binding, got %T", doc["oneOf"])
	}
	for _, want := range []string{"caveats", "queries", "concepts", "datasets", "columns"} {
		if !strings.Contains(s, `"`+want+`"`) {
			t.Errorf("schema never mentions %q", want)
		}
	}
	// Caveats are required by the loader; a schema that made them optional
	// would invite drafts the loader then rejects.
	mode := ModeSchema()
	req, _ := mode["required"].([]any)
	var hasCaveats bool
	for _, r := range req {
		if r == "caveats" {
			hasCaveats = true
		}
	}
	if !hasCaveats {
		t.Error("caveats must be required in the schema, as the loader requires them")
	}
}

// removeMode/removeBinding/restoreMode undo registry mutations between tests.
// The registry is package-level state, and these tests deliberately write to it.
func removeMode(name string) {
	out := registry[:0]
	for _, m := range registry {
		if m.Name != name {
			out = append(out, m)
		}
	}
	registry = out
}

func removeBinding(mode string) { delete(bindingRegistry, mode) }

func restoreMode(m *Mode) {
	for i, existing := range registry {
		if existing.Name == m.Name {
			registry[i] = m
			return
		}
	}
	registry = append(registry, m)
}
