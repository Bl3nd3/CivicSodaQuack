// Copyright (c) 2026 Neomantra Corp

package web

import (
	"fmt"
	"html/template"
	"math"
	"strconv"
	"strings"
)

// Chart colours are the validated defaults from the data-viz reference palette.
// Categorical slots 1 and 2 clear every adjacent gate in both modes; the single
// -series case uses the sequential blue, since one series is a magnitude
// comparison rather than an identity one.
const (
	seriesLight1 = "#2a78d6" // blue
	seriesLight2 = "#eb6834" // orange
)

// barChart is a horizontal bar chart built as inline SVG.
//
// Horizontal because the labels are names — vendors, complaint categories,
// request types — and a vertical column chart would either truncate them or
// rotate them to 45 degrees, which is a legibility tax paid on every read.
type barChart struct {
	Title  string
	Bars   []bar
	Series []string // legend entries; empty for a single series
}

type bar struct {
	Label  string
	Value  float64
	Text   string // pre-formatted value
	Series int    // index into Series
}

// chartSpec records which columns a chart can be built from.
type chartSpec struct {
	labelCol int
	valueCol int
	cityCol  int // -1 when the result is not a cross-city comparison
}

// pickChart decides whether a result can honestly be drawn as a bar chart.
//
// The rule is deliberately narrow: exactly one text column to name the bars and
// at least one numeric column to size them. A result with several numeric
// columns of different units is the case a dual-axis chart is invented for, and
// a dual-axis chart is the single most misleading form in common use — so those
// stay tables.
func pickChart(cols []string, rows [][]any) *chartSpec {
	if len(rows) == 0 || len(cols) < 2 {
		return nil
	}
	spec := &chartSpec{labelCol: -1, valueCol: -1, cityCol: -1}
	for i, c := range cols {
		if strings.EqualFold(c, "city") {
			spec.cityCol = i
			break
		}
	}

	numeric := []int{}
	text := []int{}
	for i := range cols {
		if i == spec.cityCol {
			continue
		}
		switch {
		case columnIsNumeric(rows, i):
			numeric = append(numeric, i)
		case columnIsText(rows, i):
			text = append(text, i)
		}
	}
	if len(text) != 1 || len(numeric) == 0 {
		return nil
	}
	spec.labelCol = text[0]
	spec.valueCol = measureColumn(rows, numeric)
	return spec
}

// measureColumn picks which numeric column the bars should show.
//
// Taking the first one is wrong often enough to matter: "top vendors" returns
// (vendor, contracts, total_awarded, avg_award) and is ordered by total value,
// so charting the first numeric column would draw contract *counts* against
// rows sorted by dollars — bars in visibly jumbled order next to a table that
// is correctly sorted, which reads as a broken chart at best and a different
// claim at worst.
//
// A mode query orders by the measure it is about, so the sorted column is the
// measure. Preferring a monotonic column finds it without needing to parse the
// SQL, and falling back to the first numeric column keeps unsorted results
// chartable.
func measureColumn(rows [][]any, numeric []int) int {
	for _, i := range numeric {
		if isMonotonic(rows, i) {
			return i
		}
	}
	return numeric[0]
}

// isMonotonic reports whether a column never changes direction, which is what a
// column an ORDER BY produced looks like. Nulls are skipped rather than treated
// as breaks: a trailing null in an aggregate should not disqualify the measure.
func isMonotonic(rows [][]any, i int) bool {
	var prev float64
	var seen, up, down bool
	for _, r := range rows {
		if i >= len(r) || r[i] == nil {
			continue
		}
		v, ok := toFloat(r[i])
		if !ok {
			return false
		}
		if seen {
			switch {
			case v > prev:
				up = true
			case v < prev:
				down = true
			}
			if up && down {
				return false
			}
		}
		prev, seen = v, true
	}
	// A single value, or a column of one repeated value, is not evidence of a
	// sort — treat it as not the measure so the fallback applies.
	return seen && (up || down)
}

func columnIsNumeric(rows [][]any, i int) bool {
	seen := false
	for _, r := range rows {
		if i >= len(r) || r[i] == nil {
			continue
		}
		if _, ok := toFloat(r[i]); !ok {
			return false
		}
		seen = true
	}
	return seen
}

// columnIsText reports whether every non-null value is a string.
//
// A string that happens to look like a number ("2024") still counts as text.
// It is the label the reader reads along the axis — a year, a ward, a beat —
// and excluding it would leave a per-year comparison with no label column and
// silently demote a perfectly chartable result to a table.
func columnIsText(rows [][]any, i int) bool {
	seen := false
	for _, r := range rows {
		if i >= len(r) || r[i] == nil {
			continue
		}
		if _, ok := r[i].(string); !ok {
			return false
		}
		seen = true
	}
	return seen
}

func toFloat(v any) (float64, bool) {
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
	default:
		return 0, false
	}
}

// buildChart assembles a chart from a result, capped at maxBars rows.
func buildChart(cols []string, rows [][]any, spec *chartSpec, maxBars int) *barChart {
	if spec == nil {
		return nil
	}
	c := &barChart{Title: cols[spec.valueCol]}
	seriesIdx := map[string]int{}

	for _, r := range rows {
		if len(c.Bars) >= maxBars {
			break
		}
		v, ok := toFloat(r[spec.valueCol])
		if !ok {
			continue
		}
		label, _ := r[spec.labelCol].(string)
		b := bar{Label: label, Value: v, Text: formatNumber(v)}
		if spec.cityCol >= 0 {
			city, _ := r[spec.cityCol].(string)
			idx, seen := seriesIdx[city]
			if !seen {
				// Two cities is the documented safe span for categorical slots;
				// beyond that the chart is dropped in favour of the table
				// rather than generating hues that collapse under CVD.
				if len(c.Series) >= 2 {
					return nil
				}
				idx = len(c.Series)
				seriesIdx[city] = idx
				c.Series = append(c.Series, city)
			}
			b.Series = idx
			b.Label = label + " · " + city
		}
		c.Bars = append(c.Bars, b)
	}
	if len(c.Bars) < 2 {
		return nil // a one-bar bar chart is a stat tile pretending to be a chart
	}
	return c
}

// SVG renders the chart. Bars carry direct labels, so identity never rests on
// colour alone, and every report also prints the full table underneath.
func (c *barChart) SVG() template.HTML {
	const (
		rowH    = 30
		gap     = 2 // surface gap between adjacent fills
		labelW  = 240
		valueW  = 110
		barMaxW = 360
		padTop  = 8
		radius  = 4
	)
	height := padTop + len(c.Bars)*rowH
	width := labelW + barMaxW + valueW

	max := 0.0
	for _, b := range c.Bars {
		if math.Abs(b.Value) > max {
			max = math.Abs(b.Value)
		}
	}
	if max == 0 {
		max = 1
	}

	var sb strings.Builder
	fmt.Fprintf(&sb,
		`<svg viewBox="0 0 %d %d" width="100%%" height="%d" role="img" class="chart">`,
		width, height, height)

	for i, b := range c.Bars {
		y := padTop + i*rowH
		w := (math.Abs(b.Value) / max) * float64(barMaxW)
		if w < 2 {
			w = 2 // a nonzero value must never render as nothing
		}
		fill := seriesLight1
		if b.Series == 1 {
			fill = seriesLight2
		}
		cls := "bar-1"
		if b.Series == 1 {
			cls = "bar-2"
		}
		fmt.Fprintf(&sb,
			`<text x="%d" y="%d" class="bar-label" text-anchor="end">%s</text>`,
			labelW-10, y+rowH/2+4, template.HTMLEscapeString(truncate(b.Label, 34)))
		fmt.Fprintf(&sb,
			`<rect x="%d" y="%d" width="%.2f" height="%d" rx="%d" class="%s" fill="%s"><title>%s: %s</title></rect>`,
			labelW, y+gap, w, rowH-gap*2, radius, cls, fill,
			template.HTMLEscapeString(b.Label), template.HTMLEscapeString(b.Text))
		fmt.Fprintf(&sb,
			`<text x="%.2f" y="%d" class="bar-value">%s</text>`,
			float64(labelW)+w+8, y+rowH/2+4, template.HTMLEscapeString(b.Text))
	}
	sb.WriteString(`</svg>`)
	return template.HTML(sb.String())
}

// HasLegend reports whether a legend is required (two or more series).
func (c *barChart) HasLegend() bool { return len(c.Series) >= 2 }

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// formatNumber renders a value the way a reader expects: thousands separated,
// and decimals kept only where they carry information.
func formatNumber(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return withThousands(strconv.FormatInt(int64(v), 10))
	}
	if math.Abs(v) >= 100 {
		return withThousands(strconv.FormatFloat(v, 'f', 0, 64))
	}
	return strconv.FormatFloat(v, 'f', 2, 64)
}

func withThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}
