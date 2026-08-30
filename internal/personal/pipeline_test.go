// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/duckdb/duckdb-go/v2"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// This is the whole personal-mode pipeline with the model taken out: a real
// DuckDB file, a real inventory read off it, the exact JSON documents a draft
// produces, saved through the real loader, planned by DuckDB, and finally
// executed. Only the API call is absent, and what it returns is a Draft — which
// is what the fixture below stands in for.
//
// The value of running it this way is that every step that can reject a
// drafted mode is exercised on real data: it is the difference between
// "the code compiles" and "a generated mode actually runs".

const testPortal = "data.example.gov"

func buildTestDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), testPortal+".duckdb")

	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE SCHEMA IF NOT EXISTS _csq`,
		`CREATE TABLE _csq.catalog (
			id VARCHAR PRIMARY KEY, name VARCHAR NOT NULL, description VARCHAR,
			category VARCHAR, tags JSON, row_count BIGINT,
			updated_at TIMESTAMP, fetched_at TIMESTAMP NOT NULL, raw JSON NOT NULL)`,
		`CREATE TABLE _csq.sync_runs (
			run_id VARCHAR NOT NULL, dataset_id VARCHAR NOT NULL, table_name VARCHAR NOT NULL,
			started_at TIMESTAMP NOT NULL, finished_at TIMESTAMP, status VARCHAR NOT NULL,
			rows_written BIGINT, error VARCHAR, duration_ms BIGINT, config_hash VARCHAR)`,
		`INSERT INTO _csq.catalog VALUES
			('abcd-1234', 'City Contracts', 'Awarded contracts and their values.',
			 'Procurement', '[]', 4, now(), now(), '{}')`,
		`INSERT INTO _csq.sync_runs VALUES
			('run1', 'abcd-1234', 'contracts', now(), now(), 'ok', 4, NULL, 120, 'hash')`,

		// The award amount is VARCHAR on purpose: a portal publishing a number
		// as text is the common case, and it is exactly what the binding's
		// expression mapping exists to absorb.
		`CREATE TABLE contracts (
			vendor_nm VARCHAR, dept VARCHAR, amt VARCHAR, awarded VARCHAR)`,
		`INSERT INTO contracts VALUES
			('Acme Paving',   'STREETS & SAN', '1200000.50', '03/14/2024'),
			('Acme Paving',   'STREETS & SAN', '800000',     '06/02/2024'),
			('Globex Ltd',    'WATER MGMT',    '450000',     '01/09/2024'),
			('Initech Corp',  'STREETS & SAN', '75000',      '11/20/2023')`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("setup %q: %v", s, err)
		}
	}
	return path
}

func attachReadOnly(t *testing.T, path, alias string) *sql.DB {
	t.Helper()
	host, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open host: %v", err)
	}
	t.Cleanup(func() { host.Close() })

	if _, err := host.Exec(fmt.Sprintf("ATTACH '%s' AS %s (READ_ONLY)", path, alias)); err != nil {
		t.Fatalf("attach: %v", err)
	}
	return host
}

func TestDescribeReadsSchemaAndProvenance(t *testing.T) {
	host := attachReadOnly(t, buildTestDB(t), "p")

	inv, err := Describe(host, "p", testPortal)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	tbl, ok := inv.Table("contracts")
	if !ok {
		t.Fatalf("contracts not found; got %v", inv.TableNames())
	}
	if tbl.DatasetID != "abcd-1234" || tbl.DatasetName != "City Contracts" {
		t.Errorf("provenance not picked up: %+v", tbl)
	}
	if len(tbl.Columns) != 4 {
		t.Errorf("want 4 columns, got %d: %+v", len(tbl.Columns), tbl.Columns)
	}
	// The types matter: they are how the model learns that amt needs a cast.
	for _, c := range tbl.Columns {
		if c.Name == "amt" && c.Type != "VARCHAR" {
			t.Errorf("amt should report as VARCHAR, got %q", c.Type)
		}
	}
}

// Samples are what let a generated filter match 'STREETS & SAN' rather than a
// plausible-looking 'Streets and Sanitation'.
func TestSampleColumnsReadsRealValues(t *testing.T) {
	host := attachReadOnly(t, buildTestDB(t), "p")

	inv, err := Describe(host, "p", testPortal)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}
	if err := SampleColumns(host, inv); err != nil {
		t.Fatalf("sample: %v", err)
	}
	tbl, _ := inv.Table("contracts")
	var found bool
	for _, c := range tbl.Columns {
		if c.Name != "dept" {
			continue
		}
		for _, s := range c.Samples {
			if s == "STREETS & SAN" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected the real department spelling in the samples: %+v", tbl.Columns)
	}
}

// draftFor is the fixture standing in for the model's reply: the two documents
// Author would return, using an expression mapping to absorb the VARCHAR money
// column and the text date.
func draftFor(modeName string) *Draft {
	return &Draft{
		Mode: &Document{
			Kind: "mode", Name: modeName,
			Title:   "Test profile",
			Summary: "Contract totals by vendor",
			About:   "Drafted against a synthetic portal to exercise the pipeline.",
			Concepts: []Concept{{
				Name:     "contracts",
				Purpose:  "Awarded contracts with a vendor and a value.",
				Required: []string{"vendor_name", "award_amount"},
				Optional: []string{"department", "award_date"},
			}},
			Queries: []DocQuery{{
				Name: "top-vendors", Desc: "Vendors by total awarded.",
				Entity: "vendor_name", Measure: "total_awarded",
				SQL: `SELECT vendor_name,
       COUNT(*)                    AS contracts,
       ROUND(SUM(award_amount), 2) AS total_awarded
FROM {{c:contracts}}
WHERE award_amount IS NOT NULL AND vendor_name IS NOT NULL
GROUP BY vendor_name
ORDER BY total_awarded DESC
LIMIT 25`,
			}},
			Caveats: []string{"An award is not a payment."},
		},
		Binding: &Document{
			Kind: "binding", Mode: modeName, Portal: testPortal, City: "Example, EX",
			Datasets: map[string]DocDataset{
				"contracts": {
					ID: "abcd-1234", Table: "contracts", Name: "City Contracts",
					Columns: map[string]string{
						"vendor_name":  "vendor_nm",
						"award_amount": "TRY_CAST(amt AS DOUBLE)",
						"department":   "dept",
						"award_date":   "try_strptime(awarded, '%m/%d/%Y')",
					},
				},
			},
		},
	}
}

// The end-to-end case: check, save, load, plan, run.
func TestPipeline_DraftIsSavedValidatedAndRuns(t *testing.T) {
	const modeName = "pipeline-test"
	dbPath := buildTestDB(t)
	host := attachReadOnly(t, dbPath, "p")
	dir := t.TempDir()

	inv, err := Describe(host, "p", testPortal)
	if err != nil {
		t.Fatalf("describe: %v", err)
	}

	d := draftFor(modeName)
	if err := d.check(inv); err != nil {
		t.Fatalf("draft check: %v", err)
	}

	paths := PathsFor(dir, modeName, testPortal)
	if _, err := Save(dir, d, paths); err != nil {
		t.Fatalf("save: %v", err)
	}

	// Saving must produce a file the ordinary loader accepts, since that is the
	// only way the mode is ever read again.
	kind, name, err := modes.LintFile(paths.Mode)
	if err != nil {
		t.Fatalf("the saved mode does not lint: %v", err)
	}
	if kind != "mode" || name != modeName {
		t.Errorf("linted as %s/%s", kind, name)
	}

	if problems := VerifyQueries(host, modeName, "p", testPortal, nil); len(problems) > 0 {
		for _, p := range problems {
			t.Errorf("query %s would not plan: %v", p.Query, p.Err)
		}
		t.FailNow()
	}

	// Finally run it, and check the arithmetic survived the expression mapping.
	m, err := modes.Lookup(modeName)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	b, err := modes.LookupBinding(modeName, testPortal)
	if err != nil {
		t.Fatalf("binding: %v", err)
	}
	sqlText, err := modes.ExpandConceptsFor(m, m.Queries[0].SQL, "p", b)
	if err != nil {
		t.Fatalf("expand: %v", err)
	}
	var vendor string
	var count int
	var total float64
	row := host.QueryRow(sqlText)
	if err := row.Scan(&vendor, &count, &total); err != nil {
		t.Fatalf("run: %v", err)
	}
	if vendor != "Acme Paving" || count != 2 {
		t.Errorf("got %s with %d contracts, want Acme Paving with 2", vendor, count)
	}
	// 1200000.50 + 800000 — proof the VARCHAR column was really cast.
	if total != 2000000.50 {
		t.Errorf("total = %v, want 2000000.5", total)
	}
}

// A draft that does not plan must leave nothing behind. Half-saving a broken
// mode would mean the next command fails for a reason the user never caused.
func TestPipeline_RollbackLeavesNoFilesBehind(t *testing.T) {
	const modeName = "rollback-test"
	dbPath := buildTestDB(t)
	host := attachReadOnly(t, dbPath, "p")
	dir := t.TempDir()

	d := draftFor(modeName)
	// A column that survives every static check and only fails when DuckDB
	// binds it — precisely what the EXPLAIN pass is there to catch.
	d.Binding.Datasets["contracts"].Columns["award_amount"] = "TRY_CAST(no_such_col AS DOUBLE)"

	paths := PathsFor(dir, modeName, testPortal)
	rollback, err := Save(dir, d, paths)
	if err != nil {
		t.Fatalf("save should succeed; validation is not planning: %v", err)
	}

	problems := VerifyQueries(host, modeName, "p", testPortal, nil)
	if len(problems) == 0 {
		t.Fatal("planning should have failed on a missing column")
	}
	rollback()

	for _, p := range []string{paths.Mode, paths.Binding} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s should not exist after rollback", p)
		}
	}
}
