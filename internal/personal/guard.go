// Copyright (c) 2026 Neomantra Corp

package personal

import (
	"fmt"
	"sort"
	"strings"
)

// A generated mode is SQL that a user will run without having written it. The
// READ_ONLY attach already stops it writing to a portal database, but that is
// one layer and it does not cover everything a DuckDB statement can reach:
// COPY ... TO writes a file, INSTALL and LOAD pull in extensions, and
// read_csv('/etc/passwd') pulls a local file into a result set.
//
// So the guard below is a second layer, applied before the SQL is ever saved.
// It is a whitelist of shape (one read-only statement) plus a blacklist of the
// specific escapes that shape still permits. Neither is sufficient alone and
// neither is claimed to be exhaustive — the read-only attach remains the thing
// that actually protects the data.
//
// It runs on hand-written external modes too, for the same reason: by the time
// SQL reaches this package, "who wrote it" is not a property the code can see.

// statementKeywords are the leading words of statements that are not reads.
// A generated query must be a single SELECT, optionally introduced by WITH.
var allowedLeading = map[string]bool{
	"select": true,
	"with":   true,
	"table":  true, // DuckDB's `TABLE t` shorthand for SELECT * FROM t
	"from":   true, // DuckDB's FROM-first syntax
}

// deniedKeywords are statement verbs and directives that must not appear
// anywhere in a generated query, including inside a CTE or a subquery.
var deniedKeywords = []string{
	"attach", "detach", "copy", "export", "import", "install", "load",
	"insert", "update", "delete", "truncate", "merge", "upsert",
	"create", "drop", "alter",
	"pragma", "set", "reset", "call", "checkpoint", "vacuum",
	"begin", "commit", "rollback", "grant", "revoke", "use",
	"prepare", "execute", "deallocate",
}

// Deliberately absent from the list above: REPLACE. DuckDB has both a
// replace(string, from, to) scalar and a `SELECT * REPLACE (...)` projection,
// and both are ordinary read-only SQL. CREATE OR REPLACE is already refused by
// "create", so denying the word would cost real queries and buy nothing.

// deniedFunctions read or write outside the attached databases. A query that
// calls one is not analysing the portal any more.
var deniedFunctions = []string{
	"read_csv", "read_csv_auto", "read_parquet", "read_json", "read_json_auto",
	"read_ndjson", "read_ndjson_auto", "read_text", "read_blob", "read_xlsx",
	"glob", "parquet_scan", "csv_scan", "json_scan",
	"sniff_csv", "delta_scan", "iceberg_scan", "postgres_scan", "mysql_scan",
	"sqlite_scan", "shellfs", "gsheet",
}

// CheckReadOnly reports whether sqlText is a single read-only statement.
//
// The check runs over the SQL with comments and string literals removed, so a
// vendor named "Update Systems Inc" in a WHERE clause is not mistaken for an
// UPDATE statement.
func CheckReadOnly(sqlText string) error {
	stripped, err := stripLiteralsAndComments(sqlText)
	if err != nil {
		return err
	}
	trimmed := strings.TrimSpace(stripped)
	if trimmed == "" {
		return fmt.Errorf("the query is empty")
	}

	// One statement only. A trailing semicolon is fine; anything after it is a
	// second statement, which is the classic way a read smuggles in a write.
	body := strings.TrimSuffix(strings.TrimSpace(trimmed), ";")
	if strings.Contains(body, ";") {
		return fmt.Errorf("the query contains more than one statement; " +
			"a mode query must be a single SELECT")
	}

	lower := strings.ToLower(body)
	fields := strings.Fields(lower)
	if len(fields) == 0 {
		return fmt.Errorf("the query is empty")
	}
	lead := strings.TrimLeft(fields[0], "(")
	if !allowedLeading[lead] {
		return fmt.Errorf("the query starts with %q; a mode query must be a "+
			"read-only SELECT (a leading WITH is fine)", fields[0])
	}

	if kw := findToken(lower, deniedKeywords); kw != "" {
		return fmt.Errorf("the query uses %q, which can modify state; "+
			"a mode query must only read", strings.ToUpper(kw))
	}
	if fn := findCall(lower, deniedFunctions); fn != "" {
		return fmt.Errorf("the query calls %s(), which reads outside the attached "+
			"portal databases; a mode query must read only synced tables", fn)
	}
	return nil
}

// findToken returns the first denied keyword appearing as a whole word.
func findToken(lower string, words []string) string {
	for _, w := range words {
		if containsWord(lower, w) {
			return w
		}
	}
	return ""
}

// findCall returns the first denied function that appears as a call, i.e. the
// name followed by an open parenthesis.
func findCall(lower string, fns []string) string {
	for _, f := range fns {
		idx := 0
		for {
			j := strings.Index(lower[idx:], f)
			if j < 0 {
				break
			}
			start := idx + j
			end := start + len(f)
			if wordBoundary(lower, start, end) && nextNonSpaceIs(lower, end, '(') {
				return f
			}
			idx = end
		}
	}
	return ""
}

func containsWord(s, word string) bool {
	idx := 0
	for {
		j := strings.Index(s[idx:], word)
		if j < 0 {
			return false
		}
		start := idx + j
		end := start + len(word)
		if wordBoundary(s, start, end) {
			return true
		}
		idx = end
	}
}

func wordBoundary(s string, start, end int) bool {
	if start > 0 && isIdentByte(s[start-1]) {
		return false
	}
	if end < len(s) && isIdentByte(s[end]) {
		return false
	}
	return true
}

func nextNonSpaceIs(s string, from int, want byte) bool {
	for i := from; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' {
			continue
		}
		return s[i] == want
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || b == '$' ||
		(b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9')
}

// stripLiteralsAndComments blanks out string literals, quoted identifiers, and
// comments, preserving length and line structure so the remaining text can be
// scanned for keywords without false positives from data.
func stripLiteralsAndComments(s string) (string, error) {
	var out strings.Builder
	out.Grow(len(s))

	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == '-' && i+1 < len(s) && s[i+1] == '-':
			for i < len(s) && s[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < len(s) && s[i+1] == '*':
			end := strings.Index(s[i+2:], "*/")
			if end < 0 {
				return "", fmt.Errorf("the query has an unterminated /* comment")
			}
			i += 2 + end + 2
		case c == '\'' || c == '"':
			quote := c
			i++
			closed := false
			for i < len(s) {
				if s[i] == quote {
					// A doubled quote is an escaped quote, not the end.
					if i+1 < len(s) && s[i+1] == quote {
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				i++
			}
			if !closed {
				return "", fmt.Errorf("the query has an unterminated %c quote", quote)
			}
			// Quoted identifiers still need to keep tables apart, so leave a
			// placeholder token rather than nothing.
			out.WriteString(" x ")
		default:
			out.WriteByte(c)
			i++
		}
	}
	return out.String(), nil
}

// DeniedKeywords returns the guarded keyword list, for documentation and tests.
func DeniedKeywords() []string {
	out := append([]string(nil), deniedKeywords...)
	sort.Strings(out)
	return out
}
