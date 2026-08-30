// Copyright (c) 2026 Neomantra Corp

package main

import (
	"math"
	"strconv"
	"strings"
	"testing"
)

// A money column must never reach the user in scientific notation. The value
// here is a real one: SUM(award_amount) for Chicago contracts with no recorded
// procurement type, which printed as 6.81247059363e+10 before this.
func TestRenderCell_LargeFloatsAreDecimal(t *testing.T) {
	cases := []struct {
		name string
		in   any
		want string
	}{
		{"contract total", float64(68124705936.3), "68124705936.3"},
		{"whole dollars", float64(14200000), "14200000"},
		{"cents kept", float64(1234.56), "1234.56"},
		{"small share", float64(0.0000015), "0.0000015"},
		{"negative", float64(-42624199.5), "-42624199.5"},
		{"zero", float64(0), "0"},
		{"float32", float32(1234.5), "1234.5"},
		{"null", nil, "-"},
		{"bytes", []byte("BID"), "BID"},
		{"int stays put", int64(185826), "185826"},
		{"string stays put", "SOLE SOURCE", "SOLE SOURCE"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := renderCell(c.in)
			if got != c.want {
				t.Errorf("renderCell(%v) = %q, want %q", c.in, got, c.want)
			}
			if strings.ContainsAny(got, "eE") && c.name != "bytes" && c.name != "string stays put" {
				t.Errorf("renderCell(%v) = %q, which carries an exponent", c.in, got)
			}
		})
	}
}

// Precision -1 is chosen so the rendering is exact: every float must survive a
// round trip through the cell. Readability must not cost accuracy.
func TestRenderCell_FloatsRoundTrip(t *testing.T) {
	vals := []float64{
		68124705936.3, 42696077885.2, 0.1, 1.0 / 3.0,
		math.MaxInt64, 1e-9, -273.15, 12486464619.2,
	}
	for _, v := range vals {
		got := renderCell(v)
		back, err := strconv.ParseFloat(got, 64)
		if err != nil {
			t.Errorf("renderCell(%v) = %q, which does not parse: %v", v, got, err)
			continue
		}
		if back != v {
			t.Errorf("renderCell(%v) = %q, parses back as %v", v, got, back)
		}
	}
}

// The same function feeds --format csv, so a cell must stay a bare number: no
// thousands separators, nothing a CSV consumer would import as text.
func TestRenderCell_StaysCSVSafe(t *testing.T) {
	for _, v := range []float64{68124705936.3, 14200000, -42624199.5} {
		got := renderCell(v)
		if strings.ContainsAny(got, ",\" ") {
			t.Errorf("renderCell(%v) = %q, which is not a bare number", v, got)
		}
	}
}

// Non-finite values must render as something, not panic or print an exponent.
func TestRenderCell_NonFinite(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{math.NaN(), "NaN"},
		{math.Inf(1), "+Inf"},
		{math.Inf(-1), "-Inf"},
	}
	for _, c := range cases {
		if got := renderCell(c.in); got != c.want {
			t.Errorf("renderCell(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
