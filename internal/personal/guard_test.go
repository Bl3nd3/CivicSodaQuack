// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"strings"
	"testing"
)

// The guard runs on SQL nobody reviewed before it executed. These tests pin
// both directions: what it must refuse, and — just as important — what it must
// not refuse, because a guard that rejects ordinary analytical SQL makes the
// personal mode useless and pushes people to turn it off.

func TestCheckReadOnly_AcceptsOrdinaryAnalyticalSQL(t *testing.T) {
	ok := []struct {
		name string
		sql  string
	}{
		{"plain select", `SELECT vendor_name, SUM(award_amount) AS total
			FROM {{c:contracts}} GROUP BY vendor_name ORDER BY total DESC LIMIT 25`},
		{"leading CTE", `WITH by_vendor AS (
			  SELECT vendor_name, SUM(award_amount) AS amt FROM {{c:contracts}} GROUP BY 1
			)
			SELECT * FROM by_vendor ORDER BY amt DESC LIMIT 10`},
		{"window function", `SELECT department, vendor_name,
			  SUM(amt) OVER (PARTITION BY department) AS dept_total
			FROM {{c:contracts}}`},
		{"trailing semicolon", `SELECT 1 FROM {{c:contracts}};`},
		{"line comment", `-- how much was awarded
			SELECT SUM(award_amount) FROM {{c:contracts}}`},
		{"block comment", `/* totals */ SELECT SUM(award_amount) FROM {{c:contracts}}`},
		{"FROM-first syntax", `FROM {{c:contracts}} SELECT vendor_name`},

		// The denied words are common English, and civic data is full of them.
		// Matching them inside a string literal would refuse real queries.
		{"denied word inside a literal", `SELECT * FROM {{c:contracts}}
			WHERE vendor_name = 'Create Update Systems Inc'`},
		{"denied word inside a quoted identifier", `SELECT "set" FROM {{c:contracts}}`},
		{"denied word as a column substring", `SELECT dataset_id, offset_days,
			created_at, updated_at FROM {{c:contracts}}`},

		// REPLACE is a DuckDB scalar and a projection modifier, not a statement.
		{"replace scalar", `SELECT replace(vendor_name, ',', '') FROM {{c:contracts}}`},
		{"star replace", `SELECT * REPLACE (upper(vendor_name) AS vendor_name)
			FROM {{c:contracts}}`},
	}
	for _, tc := range ok {
		t.Run(tc.name, func(t *testing.T) {
			if err := CheckReadOnly(tc.sql); err != nil {
				t.Errorf("should be allowed, got: %v", err)
			}
		})
	}
}

func TestCheckReadOnly_RefusesEscapes(t *testing.T) {
	bad := []struct {
		name string
		sql  string
		want string
	}{
		{"second statement", `SELECT 1 FROM {{c:t}}; DROP TABLE contracts`, "more than one statement"},
		{"leading DDL", `DROP TABLE contracts`, "read-only"},
		{"insert", `INSERT INTO t VALUES (1)`, "read-only"},

		// COPY ... TO writes a file even under a READ_ONLY attach, which is the
		// specific reason a statement-shape check is not enough on its own.
		{"copy to file", `SELECT * FROM {{c:t}} UNION ALL SELECT * FROM (COPY x TO 'o.csv')`, "COPY"},
		{"attach in a CTE", `WITH a AS (ATTACH 'other.duckdb' AS o) SELECT * FROM {{c:t}}`, "ATTACH"},
		{"install extension", `SELECT 1 FROM {{c:t}} WHERE INSTALL httpfs`, "INSTALL"},
		{"pragma", `PRAGMA database_list`, "read-only"},

		// Reading a local file is not writing, but it is not analysing the
		// portal either — and it lifts file contents into a result set.
		{"read_csv", `SELECT * FROM read_csv('/etc/passwd')`, "read_csv"},
		{"read_parquet in a join", `SELECT * FROM {{c:t}}
			JOIN read_parquet('/tmp/x.parquet') USING (id)`, "read_parquet"},
		{"glob", `SELECT * FROM glob('/**')`, "glob"},

		{"unterminated quote", `SELECT * FROM {{c:t}} WHERE x = 'oops`, "unterminated"},
		{"empty", `   `, "empty"},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckReadOnly(tc.sql)
			if err == nil {
				t.Fatal("should have been refused, but was allowed")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error should mention %q, got: %v", tc.want, err)
			}
		})
	}
}

// A comment must not be able to hide a second statement from the scanner.
func TestCheckReadOnly_CommentsCannotHideStatements(t *testing.T) {
	sql := "SELECT 1 FROM {{c:t}} -- harmless\n; DROP TABLE contracts"
	if err := CheckReadOnly(sql); err == nil {
		t.Fatal("a statement after a comment should still be caught")
	}
}

func TestStripLiteralsPreservesDoubledQuotes(t *testing.T) {
	// 'O''Hare' is one literal containing an apostrophe, not two literals with
	// a stray token between them.
	out, err := stripLiteralsAndComments(`SELECT * FROM t WHERE name = 'O''Hare Update'`)
	if err != nil {
		t.Fatalf("strip: %v", err)
	}
	if strings.Contains(strings.ToLower(out), "update") {
		t.Errorf("literal contents leaked into the scanned text: %q", out)
	}
}
