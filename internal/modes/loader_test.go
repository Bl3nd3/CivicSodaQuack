// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return p
}

const validBinding = `
kind: binding
mode: police
portal: data.example.gov
city: "Example, EX"
datasets:
  complaints:
    id: aaaa-1111
    table: complaints
    name: "Complaint Cases"
    rows: 100
notes:
  - "Example publishes no officer breakdown."
`

func TestLintFile_ValidBinding(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "b.yaml", validBinding)
	kind, name, err := LintFile(p)
	if err != nil {
		t.Fatalf("LintFile: %v", err)
	}
	if kind != "binding" || !strings.Contains(name, "data.example.gov") {
		t.Errorf("got kind=%q name=%q", kind, name)
	}
}

// TestLintFile_UnknownKeyIsAnError is the point of KnownFields(true): a typo in
// a config a non-programmer wrote must fail loudly, not be silently dropped.
func TestLintFile_UnknownKeyIsAnError(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "b.yaml", strings.Replace(validBinding, "city:", "citty:", 1))
	_, _, err := LintFile(p)
	if err == nil {
		t.Fatal("expected an error for an unknown key")
	}
	if !strings.Contains(err.Error(), "citty") {
		t.Errorf("error should name the offending key, got: %v", err)
	}
}

func TestLintFile_MissingKind(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "b.yaml", "mode: police\nportal: x\n")
	_, _, err := LintFile(p)
	if err == nil || !strings.Contains(err.Error(), "kind") {
		t.Errorf("expected a 'kind' error, got: %v", err)
	}
}

// TestLintFile_ModeRequiresCaveats keeps the caveat requirement enforced for
// externally authored modes, matching what the built-in registry test enforces.
func TestLintFile_ModeRequiresCaveats(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "m.yaml", `
kind: mode
name: budgets
title: "Budget analysis"
summary: "Spend by department"
about: "Departmental appropriations."
concepts:
  - name: appropriations
    purpose: "Budget lines by department"
queries:
  - name: by-dept
    desc: "Spend by department"
    sql: "SELECT department FROM {{c:appropriations}}"
`)
	_, _, err := LintFile(p)
	if err == nil || !strings.Contains(err.Error(), "caveats") {
		t.Errorf("a mode without caveats must fail; got: %v", err)
	}
}

func TestLintFile_QueryReferencingUndeclaredConcept(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "m.yaml", `
kind: mode
name: budgets
title: "Budget analysis"
summary: "Spend"
about: "Appropriations."
concepts:
  - name: appropriations
    purpose: "Budget lines"
queries:
  - name: by-dept
    desc: "Spend by department"
    sql: "SELECT * FROM {{c:contracts}}"
caveats:
  - "Awarded is not spent."
`)
	_, _, err := LintFile(p)
	if err == nil || !strings.Contains(err.Error(), "contracts") {
		t.Errorf("expected an undeclared-concept error naming 'contracts'; got: %v", err)
	}
}

// TestLintFile_QueryMustUseConceptRef stops an author hardcoding a table name,
// which would silently make their mode single-city.
func TestLintFile_QueryMustUseConceptRef(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "m.yaml", `
kind: mode
name: budgets
title: "Budget analysis"
summary: "Spend"
about: "Appropriations."
concepts:
  - name: appropriations
    purpose: "Budget lines"
queries:
  - name: by-dept
    desc: "Spend by department"
    sql: "SELECT * FROM appropriations"
caveats:
  - "Awarded is not spent."
`)
	_, _, err := LintFile(p)
	if err == nil || !strings.Contains(err.Error(), "{{c:name}}") {
		t.Errorf("expected guidance to use a concept ref; got: %v", err)
	}
}

func TestLoadDir_MissingDirIsNotAnError(t *testing.T) {
	loaded, err := LoadDir(filepath.Join(t.TempDir(), "nope"))
	if err != nil {
		t.Errorf("a missing modes dir should be silent, got: %v", err)
	}
	if len(loaded) != 0 {
		t.Errorf("expected nothing loaded, got %v", loaded)
	}
}

func TestLoadDir_RegistersBinding(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "example.yaml", validBinding)

	loaded, err := LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("expected 1 file loaded, got %v", loaded)
	}
	b, err := LookupBinding("police", "data.example.gov")
	if err != nil {
		t.Fatalf("binding not registered: %v", err)
	}
	if b.City != "Example, EX" || b.Source == "" {
		t.Errorf("binding fields wrong: %+v", b)
	}
	// Partial bindings are legitimate: the mode must report what is missing
	// rather than the binding being rejected.
	m, _ := Lookup("police")
	if un := m.Unbound(b); len(un) == 0 {
		t.Error("expected unbound concepts for this partial binding")
	}
	delete(bindingRegistry["police"], "data.example.gov")
}

// TestLoadDir_ModeBeforeBindingRegardlessOfFilename covers the two-pass load:
// a binding in a-file.yaml must find a mode defined in z-file.yaml.
func TestLoadDir_ModeBeforeBindingRegardlessOfFilename(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a-binding.yaml", `
kind: binding
mode: zoning
portal: data.example.gov
city: "Example, EX"
datasets:
  permits: {id: aaaa-2222, table: permits, name: Permits, rows: 5}
`)
	writeFile(t, dir, "z-mode.yaml", `
kind: mode
name: zoning
title: "Zoning"
summary: "Permits"
about: "Permit activity."
concepts:
  - name: permits
    purpose: "Issued permits"
queries:
  - name: by-year
    desc: "Permits per year"
    sql: "SELECT * FROM {{c:permits}}"
caveats:
  - "Permit counts are not construction volume."
`)
	if _, err := LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if _, err := Lookup("zoning"); err != nil {
		t.Errorf("external mode not registered: %v", err)
	}
	if _, err := LookupBinding("zoning", "data.example.gov"); err != nil {
		t.Errorf("binding to an external mode not registered: %v", err)
	}
	// Clean up so other tests see the built-in registry.
	for i, m := range registry {
		if m.Name == "zoning" {
			registry = append(registry[:i], registry[i+1:]...)
			break
		}
	}
	delete(bindingRegistry, "zoning")
}

func TestLoadDir_BindingToUnknownConceptFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "b.yaml", `
kind: binding
mode: police
portal: data.bad.gov
city: "Bad, EX"
datasets:
  not_a_concept: {id: aaaa-3333, table: t, name: N}
`)
	_, err := LoadDir(dir)
	if err == nil || !strings.Contains(err.Error(), "not_a_concept") {
		t.Errorf("expected an unknown-concept error; got: %v", err)
	}
	delete(bindingRegistry["police"], "data.bad.gov")
}
