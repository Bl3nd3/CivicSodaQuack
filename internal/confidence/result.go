// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"fmt"
	"strconv"
	"strings"
)

// AddConcentration attaches the dominance reading for a result set, when the
// query declared which columns make one possible.
//
// entity names the column identifying each row, measure the numeric column a
// reader will quote. The share is computed over the rows given and labelled as
// such: a top-N result does not carry the information needed to compute a
// share of the whole, and inventing a denominator for it would produce exactly
// the kind of confidently wrong percentage this package exists to prevent.
func AddConcentration(rep *Report, entity, measure string, columns []string, rows [][]any) {
	if rep == nil || measure == "" || len(rows) < 2 {
		return
	}
	mi := indexOf(columns, measure)
	if mi < 0 {
		return
	}
	ei := indexOf(columns, entity)

	var total, top float64
	topLabel := ""
	for _, row := range rows {
		if mi >= len(row) {
			continue
		}
		v, ok := numeric(row[mi])
		if !ok {
			continue
		}
		if v < 0 {
			// A measure that changes sign is not a share of anything. Abandon
			// the reading rather than report a percentage of a mixed total.
			return
		}
		total += v
		if v > top {
			top = v
			if ei >= 0 && ei < len(row) {
				topLabel = displayValue(row[ei])
			}
		}
	}
	if total <= 0 {
		return
	}
	rep.Concentration(entity, topLabel, top/total, len(rows))
}

func indexOf(cols []string, name string) int {
	if name == "" {
		return -1
	}
	for i, c := range cols {
		if strings.EqualFold(c, name) {
			return i
		}
	}
	return -1
}

// numeric coerces a driver value to a float, reporting whether it was a number
// at all. Strings are parsed too: DuckDB surfaces DECIMAL as text through some
// driver paths, and a decimal total is the common case here.
func numeric(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case float32:
		return float64(t), true
	case int64:
		return float64(t), true
	case int32:
		return float64(t), true
	case int:
		return float64(t), true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		return f, err == nil
	}
	return 0, false
}

// displayValue renders a row value as a short label, truncating so one long
// free-text vendor name cannot swallow the block.
func displayValue(v any) string {
	if v == nil {
		return ""
	}
	var s string
	switch t := v.(type) {
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		s = fmt.Sprintf("%v", t)
	}
	s = strings.TrimSpace(s)
	const maxLabel = 48
	if r := []rune(s); len(r) > maxLabel {
		return string(r[:maxLabel-1]) + "\u2026"
	}
	return s
}
