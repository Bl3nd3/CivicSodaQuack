// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"testing"
	"time"
)

// sig is a terse constructor for scoring tests.
func sig(name string, level Level, score, weight float64) Signal {
	return Signal{Name: name, Level: level, Score: score, Weight: weight}
}

func TestScore_IsAWeightedMean(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 3),
		sig("b", Warn, 0.5, 1),
	}}}}
	r.finalize()

	// (1*3 + 0.5*1) / 4 = 0.875
	if r.Score != 88 {
		t.Errorf("Score = %d, want 88", r.Score)
	}
	if r.Band != BandHigh {
		t.Errorf("Band = %q, want %q", r.Band, BandHigh)
	}
}

// An unmeasurable property is not evidence in either direction. Scoring it as
// zero would punish a portal for publishing less metadata; scoring it as one
// would let a gap in the evidence read as a clean bill of health.
func TestScore_UnknownSignalsAreExcludedNotScored(t *testing.T) {
	with := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 3),
		sig("b", Unknown, 0, 3),
	}}}}
	with.finalize()

	without := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 3),
	}}}}
	without.finalize()

	if with.Score != without.Score {
		t.Errorf("an Unknown signal changed the score: %d vs %d", with.Score, without.Score)
	}
	if with.Score != 100 {
		t.Errorf("Score = %d, want 100", with.Score)
	}
}

// Advisory signals are shown to the reader but never scored: a dominant vendor
// is a fact about procurement, not a defect in the data.
func TestScore_AdvisorySignalsDoNotCount(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 3),
		sig(SignalConcentration, Warn, 0, 0),
	}}}}
	r.finalize()

	if r.Score != 100 {
		t.Errorf("Score = %d, want 100 — an advisory signal must not score", r.Score)
	}
	if len(r.Problems()) != 1 {
		t.Errorf("advisory signal should still be reported as a problem to read")
	}
}

// A weighted mean is too forgiving of a dataset that never arrived: seven clean
// checks on an empty table would average out to "moderate".
func TestScore_FailedSyncCapsTheScore(t *testing.T) {
	failed := sig(SignalSync, Fail, 0, weightSync)
	failed.Cap = CapFailedSync
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		failed,
		sig("b", Pass, 1, 1), sig("c", Pass, 1, 1), sig("d", Pass, 1, 1),
		sig("e", Pass, 1, 1), sig("f", Pass, 1, 1), sig("g", Pass, 1, 1),
	}}}}
	r.finalize()

	if r.Score > CapFailedSync {
		t.Errorf("Score = %d, want <= %d", r.Score, CapFailedSync)
	}
	if r.Band != BandInsufficient {
		t.Errorf("Band = %q, want %q", r.Band, BandInsufficient)
	}
}

func TestScore_IncompleteCopyCapsTheScore(t *testing.T) {
	short := sig(SignalCompleteness, Fail, 0.5, weightCompleteness)
	short.Cap = CapIncomplete
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig(SignalSync, Pass, 1, weightSync),
		short,
		sig("c", Pass, 1, 1), sig("d", Pass, 1, 1),
	}}}}
	r.finalize()

	if r.Score > CapIncomplete {
		t.Errorf("Score = %d, want <= %d", r.Score, CapIncomplete)
	}
}

// Nothing measurable must read as "assessed and terrible" rather than
// "not assessed" — they call for opposite responses from the reader.
func TestScore_NothingMeasurableIsNotAssessed(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Unknown, 0, 3),
	}}}}
	r.finalize()

	if r.Assessed {
		t.Error("Assessed = true with no measurable signal")
	}
	if r.Band != BandInsufficient {
		t.Errorf("Band = %q, want %q", r.Band, BandInsufficient)
	}
}

func TestReport_EmptyTargetsIsNotAssessed(t *testing.T) {
	r := &Report{}
	r.finalize()
	if r.Assessed {
		t.Error("a report over no datasets must not claim to be assessed")
	}
}

// Every report carries the limits on reading it. A fitness score quoted as an
// accuracy score is the specific misuse this package designs against.
func TestReport_AlwaysCarriesItsLimits(t *testing.T) {
	r := &Report{}
	r.finalize()
	if len(r.Limits) == 0 {
		t.Fatal("report has no limits")
	}
}

func TestSignalOrder_PassesThenAdvisoriesThenProblems(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("fail", Fail, 0, 1),
		sig("warn", Warn, 0.5, 1),
		sig("advisory", Warn, 0, 0),
		sig("pass", Pass, 1, 1),
	}}}}
	r.finalize()

	want := []string{"pass", "advisory", "warn", "fail"}
	for i, w := range want {
		if r.Signals[i].Name != w {
			t.Errorf("signal %d = %q, want %q", i, r.Signals[i].Name, w)
		}
	}
}

func TestGrade(t *testing.T) {
	cases := []struct {
		score int
		band  string
	}{
		{100, BandHigh}, {85, BandHigh},
		{84, BandModerate}, {65, BandModerate},
		{64, BandLow}, {40, BandLow},
		{39, BandInsufficient}, {0, BandInsufficient},
	}
	for _, c := range cases {
		if got := Grade(c.score); got != c.band {
			t.Errorf("Grade(%d) = %q, want %q", c.score, got, c.band)
		}
	}
}

// --- individual signals ---

func TestSyncSignal(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	ok := now.Add(-24 * time.Hour)

	t.Run("never synced fails", func(t *testing.T) {
		s := syncSignal(bookRecord{}, "t")
		if s.Level != Fail || s.Score != 0 {
			t.Errorf("got %v/%v, want Fail/0", s.Level, s.Score)
		}
	})

	t.Run("interrupted run fails", func(t *testing.T) {
		s := syncSignal(bookRecord{found: true, lastStatus: "running", lastStarted: &ok}, "t")
		if s.Level != Fail {
			t.Errorf("Level = %v, want Fail", s.Level)
		}
		if s.Detail == "" {
			t.Error("an interrupted sync must say what to do about it")
		}
	})

	t.Run("failed run reports the error", func(t *testing.T) {
		s := syncSignal(bookRecord{
			found: true, lastStatus: "error", lastStarted: &ok, lastFinished: &ok,
			lastError: "429 from portal",
		}, "t")
		if s.Level != Fail {
			t.Errorf("Level = %v, want Fail", s.Level)
		}
		if s.Detail != "429 from portal" {
			t.Errorf("Detail = %q, want the upstream error", s.Detail)
		}
	})

	t.Run("clean run passes", func(t *testing.T) {
		s := syncSignal(bookRecord{
			found: true, lastStatus: "ok", lastStarted: &ok, lastFinished: &ok, okAt: &ok,
		}, "t")
		if s.Level != Pass || s.Score != 1 {
			t.Errorf("got %v/%v, want Pass/1", s.Level, s.Score)
		}
	})
}

func TestCompletenessSignal(t *testing.T) {
	cases := []struct {
		name           string
		held, expected int64
		want           Level
	}{
		{"complete", 1000, 1000, Pass},
		{"slightly short", 997, 1000, Pass},
		{"grown upstream", 1200, 1000, Pass},
		{"noticeably short", 940, 1000, Warn},
		{"truncated", 540, 1000, Fail},
		{"empty", 0, 1000, Fail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := completenessSignal(c.held, c.expected, bookRecord{}, "t")
			if s.Level != c.want {
				t.Errorf("Level = %v, want %v (label %q)", s.Level, c.want, s.Label)
			}
		})
	}

	t.Run("no reference count is unknown, not a failure", func(t *testing.T) {
		s := completenessSignal(1000, 0, bookRecord{}, "t")
		if s.Level != Unknown {
			t.Errorf("Level = %v, want Unknown", s.Level)
		}
	})

	t.Run("falls back to the portal catalog count", func(t *testing.T) {
		n := int64(2000)
		s := completenessSignal(1000, 0, bookRecord{catalogRows: &n}, "t")
		if s.Level != Fail {
			t.Errorf("Level = %v, want Fail against the catalog count", s.Level)
		}
	})
}

// Upstream freshness and sync recency answer opposite questions. A dataset
// synced this morning is still stale if the city stopped publishing in 2023,
// and that is the case worth catching.
func TestFreshnessSignal_MeasuresUpstreamNotSync(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	syncedToday := now
	frozen := now.AddDate(-3, 0, 0)

	s := freshnessSignal(bookRecord{upstreamUpdated: &frozen, okAt: &syncedToday}, "t", now)
	if s.Level != Fail {
		t.Errorf("Level = %v, want Fail — three-year-old data synced today is stale", s.Level)
	}
}

func TestFreshnessScore_DecaysMonotonically(t *testing.T) {
	prev := 2.0
	for _, days := range []int{0, 30, 60, 90, 200, 365, 700, 1095, 2000} {
		got := freshnessScore(days)
		if got > prev {
			t.Errorf("freshnessScore(%d) = %v rose above the previous %v", days, got, prev)
		}
		if got < 0 || got > 1 {
			t.Errorf("freshnessScore(%d) = %v out of [0,1]", days, got)
		}
		prev = got
	}
}

func TestLagSignal_DetectsACopyBehindThePortal(t *testing.T) {
	synced := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	changed := synced.AddDate(0, 6, 0)

	s := lagSignal(bookRecord{okAt: &synced, upstreamUpdated: &changed}, "t")
	if s.Level != Fail {
		t.Errorf("Level = %v, want Fail — six months behind", s.Level)
	}

	current := lagSignal(bookRecord{okAt: &changed, upstreamUpdated: &synced}, "t")
	if current.Level != Pass {
		t.Errorf("Level = %v, want Pass", current.Level)
	}
}

func TestNullSignal_NamesTheWorstColumn(t *testing.T) {
	p := tableProfile{rows: 1000, cols: []colProfile{
		{name: "vendor_name", nulls: 0},
		{name: "department", nulls: 32},
		{name: "award_amount", nulls: 5},
	}}
	s := nullSignal(p, "contracts")

	if s.Level != Warn {
		t.Errorf("Level = %v, want Warn", s.Level)
	}
	if s.Label != "3.2% of records lack department" {
		t.Errorf("Label = %q", s.Label)
	}
}

func TestNullSignal_CleanColumnsCollapseToOneLine(t *testing.T) {
	p := tableProfile{rows: 1000, cols: []colProfile{
		{name: "a"}, {name: "b"}, {name: "c"},
	}}
	s := nullSignal(p, "t")
	if s.Level != Pass {
		t.Errorf("Level = %v, want Pass", s.Level)
	}
	if s.Label != "all 3 columns this query reads are populated" {
		t.Errorf("Label = %q", s.Label)
	}
}

func TestNullSignal_EmptyTableFails(t *testing.T) {
	s := nullSignal(tableProfile{rows: 0}, "t")
	if s.Level != Fail || s.Score != 0 {
		t.Errorf("got %v/%v, want Fail/0 — an empty table profiles beautifully otherwise",
			s.Level, s.Score)
	}
}

func TestDateSignal(t *testing.T) {
	clean := tableProfile{rows: 1000, cols: []colProfile{
		{name: "d", role: roleDate},
	}}
	s, ok := dateSignal(clean, "t")
	if !ok || s.Level != Pass {
		t.Errorf("got ok=%v level=%v, want true/Pass", ok, s.Level)
	}

	bad := tableProfile{rows: 1000, cols: []colProfile{
		{name: "d", role: roleDate, past: 30, future: 20},
	}}
	s, ok = dateSignal(bad, "t")
	if !ok || s.Level != Fail {
		t.Errorf("got ok=%v level=%v, want true/Fail", ok, s.Level)
	}

	if _, ok := dateSignal(tableProfile{rows: 10}, "t"); ok {
		t.Error("a table with no date columns should produce no date signal")
	}
}

func TestKeySignal_NullKeysDropOutOfJoins(t *testing.T) {
	p := tableProfile{rows: 1000, cols: []colProfile{
		{name: "lobbyist_id", role: roleKey, nulls: 80, distinct: 400},
	}}
	s, ok := keySignal(p, "t")
	if !ok || s.Level != Fail {
		t.Errorf("got ok=%v level=%v, want true/Fail", ok, s.Level)
	}

	clean := tableProfile{rows: 1000, cols: []colProfile{
		{name: "case_id", role: roleKey, distinct: 900},
	}}
	s, _ = keySignal(clean, "t")
	if s.Level != Pass {
		t.Errorf("Level = %v, want Pass", s.Level)
	}
	if s.Detail == "" {
		t.Error("a passing key signal should still report cardinality")
	}
}

func TestRoleFor(t *testing.T) {
	cases := []struct {
		name, dtype string
		want        role
	}{
		{"issue_date", "TIMESTAMP", roleDate},
		{"date", "DATE", roleDate},
		{"created", "TIMESTAMP WITH TIME ZONE", roleDate},
		{"lobbyist_id", "VARCHAR", roleKey},
		{"socrata_id", "VARCHAR", roleKey},
		{"vendor_name", "VARCHAR", roleValue},
		{"award_amount", "DOUBLE", roleValue},
	}
	for _, c := range cases {
		if got := roleFor(c.name, c.dtype); got != c.want {
			t.Errorf("roleFor(%q, %q) = %v, want %v", c.name, c.dtype, got, c.want)
		}
	}
}

// --- concentration ---

func TestAddConcentration(t *testing.T) {
	r := &Report{}
	cols := []string{"vendor_name", "total_awarded"}
	rows := [][]any{
		{"Acme", 610.0},
		{"Beta", 200.0},
		{"Gamma", 190.0},
	}
	AddConcentration(r, "vendor_name", "total_awarded", cols, rows)

	if len(r.Signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(r.Signals))
	}
	s := r.Signals[0]
	if !s.Advisory() {
		t.Error("concentration must be advisory — dominance is not a data defect")
	}
	if s.Label != "Acme accounts for 61% of the total shown" {
		t.Errorf("Label = %q", s.Label)
	}
	// The qualifier is what keeps a top-N share from reading as a share of all.
	if !contains(s.Detail, "not the whole dataset") {
		t.Errorf("Detail must scope the share to the rows shown, got %q", s.Detail)
	}
}

func TestAddConcentration_QuietWhenEvenlySpread(t *testing.T) {
	r := &Report{}
	rows := [][]any{{"a", 100.0}, {"b", 100.0}, {"c", 100.0}}
	AddConcentration(r, "e", "m", []string{"e", "m"}, rows)
	if len(r.Signals) != 0 {
		t.Errorf("got %d signals, want 0 — 33%% is not concentration", len(r.Signals))
	}
}

// A measure that changes sign is not a share of anything, and a percentage of a
// mixed total is worse than no percentage.
func TestAddConcentration_AbandonsMixedSigns(t *testing.T) {
	r := &Report{}
	rows := [][]any{{"a", 100.0}, {"b", -400.0}}
	AddConcentration(r, "e", "m", []string{"e", "m"}, rows)
	if len(r.Signals) != 0 {
		t.Errorf("got %d signals, want 0", len(r.Signals))
	}
}

func TestAddConcentration_NeedsADeclaredMeasure(t *testing.T) {
	r := &Report{}
	rows := [][]any{{"a", 900.0}, {"b", 100.0}}
	AddConcentration(r, "e", "", []string{"e", "m"}, rows)
	if len(r.Signals) != 0 {
		t.Error("without a declared measure there is nothing to compute a share over")
	}
}

func TestAddConcentration_ParsesDecimalsReturnedAsText(t *testing.T) {
	r := &Report{}
	rows := [][]any{{"a", "610.00"}, {"b", "390.00"}}
	AddConcentration(r, "e", "m", []string{"e", "m"}, rows)
	if len(r.Signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(r.Signals))
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

func TestPct(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "100%"}, {0.997, "99.7%"}, {0.032, "3.2%"}, {0.5, "50%"},
	}
	for _, c := range cases {
		if got := pct(c.in); got != c.want {
			t.Errorf("pct(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestCommas(t *testing.T) {
	cases := []struct {
		in   int64
		want string
	}{
		{0, "0"}, {999, "999"}, {1000, "1,000"}, {4631164, "4,631,164"},
	}
	for _, c := range cases {
		if got := commas(c.in); got != c.want {
			t.Errorf("commas(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}
