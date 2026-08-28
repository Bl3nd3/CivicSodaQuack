// Copyright (c) 2026 Neomantra Corp

package web

import (
	"strings"
	"testing"
)

// A chart is only honest for a result whose shape it can represent. These cases
// pin the rule, because the failure mode is silent: a chart drawn from the
// wrong columns still looks like a chart.
func TestPickChart(t *testing.T) {
	cases := []struct {
		name string
		cols []string
		rows [][]any
		want bool
	}{
		{
			name: "one label and one measure charts",
			cols: []string{"vendor", "total"},
			rows: [][]any{{"Acme", 41200000.0}, {"Globex", 26800000.0}},
			want: true,
		},
		{
			name: "two text columns is a table, not a chart",
			cols: []string{"portal", "category", "n"},
			rows: [][]any{{"chicago", "Finance", int64(4)}, {"chicago", "Health", int64(2)}},
			want: false,
		},
		{
			name: "no measure is a table",
			cols: []string{"column", "type"},
			rows: [][]any{{"date", "TIMESTAMP"}, {"id", "VARCHAR"}},
			want: false,
		},
		{
			name: "city column is a series, not the label",
			cols: []string{"city", "year", "rate"},
			rows: [][]any{{"Chicago", "2024", 31.2}, {"New York", "2024", 22.8}},
			want: true,
		},
		{
			name: "empty result never charts",
			cols: []string{"vendor", "total"},
			rows: [][]any{},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pickChart(tc.cols, tc.rows) != nil
			if got != tc.want {
				t.Fatalf("pickChart = %v, want %v", got, tc.want)
			}
		})
	}
}

// A single bar is a stat tile wearing a chart's clothes; the form heuristic
// says render the number, not the axis.
func TestBuildChart_RejectsSingleBar(t *testing.T) {
	cols := []string{"vendor", "total"}
	rows := [][]any{{"Acme", 100.0}}
	spec := pickChart(cols, rows)
	if spec == nil {
		t.Fatal("expected a spec for one label + one measure")
	}
	if c := buildChart(cols, rows, spec, 20); c != nil {
		t.Fatal("a one-row result must not produce a bar chart")
	}
}

// Past two cities the categorical slots stop being safe under colour-vision
// deficiency, and generating a third hue is the documented wrong answer. The
// chart is dropped so the table carries the result instead.
func TestBuildChart_CapsSeriesAtTwoCities(t *testing.T) {
	cols := []string{"city", "year", "rate"}
	rows := [][]any{
		{"Chicago", "2024", 31.2},
		{"New York", "2024", 22.8},
		{"Boston", "2024", 18.4},
	}
	spec := pickChart(cols, rows)
	if spec == nil {
		t.Fatal("expected a spec")
	}
	if c := buildChart(cols, rows, spec, 20); c != nil {
		t.Fatal("three cities must not chart; expected a fallback to the table")
	}
}

func TestBarChartSVG_LabelsEveryBarDirectly(t *testing.T) {
	cols := []string{"vendor", "total"}
	rows := [][]any{{"Acme", 41200000.0}, {"Globex", 26800000.0}}
	c := buildChart(cols, rows, pickChart(cols, rows), 20)
	if c == nil {
		t.Fatal("expected a chart")
	}
	svg := string(c.SVG())
	for _, want := range []string{"Acme", "Globex", "41,200,000", "26,800,000", "<title>"} {
		if !strings.Contains(svg, want) {
			t.Errorf("SVG missing %q", want)
		}
	}
	// A value must never render as a bar of zero width.
	if strings.Contains(svg, `width="0.00"`) {
		t.Error("a nonzero value rendered as a zero-width bar")
	}
}

func TestFormatNumber(t *testing.T) {
	cases := map[float64]string{
		0:        "0",
		1234:     "1,234",
		41200000: "41,200,000",
		-9876:    "-9,876",
		31.234:   "31.23",
		1234.567: "1,235",
	}
	for in, want := range cases {
		if got := formatNumber(in); got != want {
			t.Errorf("formatNumber(%v) = %q, want %q", in, got, want)
		}
	}
}

// A mode query orders by the measure it is about. Charting a different numeric
// column draws bars in an order the table contradicts.
func TestPickChart_ChartsTheSortedMeasure(t *testing.T) {
	// Shape of corruption/top-vendors: ordered by total_awarded, not by count.
	cols := []string{"vendor_name", "contracts", "total_awarded", "avg_award"}
	rows := [][]any{
		{"BLUE CROSS & BLUE SHIELD", int64(37), 12486464619.2, 337472016.74},
		{"CAREMARK INC", int64(24), 2886189117.0, 120257879.88},
		{"AECOM HUNT", int64(10), 2311221436.0, 231122143.6},
		{"AOR TRANSIT", int64(30), 1902435065.3, 63414502.18},
	}
	spec := pickChart(cols, rows)
	if spec == nil {
		t.Fatal("expected a chart spec")
	}
	if got := cols[spec.valueCol]; got != "total_awarded" {
		t.Fatalf("charting %q; want total_awarded, the column the query sorted by", got)
	}
}

// With nothing sorted, the first numeric column is still a reasonable measure.
func TestPickChart_FallsBackToFirstNumeric(t *testing.T) {
	cols := []string{"category", "a", "b"}
	rows := [][]any{
		{"x", int64(5), int64(9)},
		{"y", int64(9), int64(2)},
		{"z", int64(1), int64(7)},
	}
	spec := pickChart(cols, rows)
	if spec == nil {
		t.Fatal("expected a chart spec")
	}
	if cols[spec.valueCol] != "a" {
		t.Fatalf("charting %q; want the first numeric column", cols[spec.valueCol])
	}
}

func TestIsMonotonic(t *testing.T) {
	desc := [][]any{{10.0}, {5.0}, {5.0}, {1.0}}
	if !isMonotonic(desc, 0) {
		t.Error("a descending column with a tie is monotonic")
	}
	if isMonotonic([][]any{{1.0}, {9.0}, {4.0}}, 0) {
		t.Error("a column that changes direction is not monotonic")
	}
	if isMonotonic([][]any{{7.0}, {7.0}}, 0) {
		t.Error("a constant column is not evidence of a sort")
	}
}
