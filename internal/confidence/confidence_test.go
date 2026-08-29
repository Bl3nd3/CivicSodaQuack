// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"testing"
	"time"
)

// sig is a terse constructor for scoring tests: a check with retention r that
// can cost at most (1 - floor).
func sig(name string, level Level, r, floor float64) Signal {
	return Signal{Name: name, Level: level, Score: r, Floor: floor}
}

func TestScore_IsAProductOfRetentions(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 0.9, 0),
		sig("b", Warn, 0.5, 0),
	}}}}
	r.finalize()

	// 0.9 × 0.5 = 0.45
	if r.Score != 45 {
		t.Errorf("Score = %d, want 45", r.Score)
	}
}

// The property that makes two scores comparable: a check that finds nothing
// wrong leaves the index exactly where it was. R therefore depends only on the
// defects found, never on how many ways they were looked for.
func TestScore_APassingCheckDoesNotMoveTheIndex(t *testing.T) {
	alone := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Warn, 0.9, 0),
	}}}}
	alone.finalize()

	withMore := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Warn, 0.9, 0),
		sig("b", Pass, 1.0, 0),
		sig("c", Pass, 1.0, 0.5),
	}}}}
	withMore.finalize()

	if alone.Score != withMore.Score {
		t.Errorf("passing checks moved the score: %d vs %d", alone.Score, withMore.Score)
	}
	if alone.Score != 90 {
		t.Errorf("Score = %d, want 90", alone.Score)
	}
}

// A defect must never be diluted by the checks that passed around it. This is
// the case the averaged model got wrong: New York holding 54%% of its rows
// averaged to 90%%.
func TestScore_DefectsAreNotDilutedByPasses(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig(SignalSync, Pass, 1, 0),
		sig(SignalCompleteness, Fail, 0.54, 0),
		sig("c", Pass, 1, 0.3), sig("d", Pass, 1, 0.3),
		sig("e", Pass, 1, 0.4), sig("f", Pass, 1, 0.6),
	}}}}
	r.finalize()

	if r.Score != 54 {
		t.Errorf("Score = %d, want 54 — the passing checks must not lift it", r.Score)
	}
}

// Continuity: no threshold may move the score in a jump. The averaged model put
// a 37-point cliff across a 0.2%% change in completeness.
func TestScore_IsContinuousAcrossThresholds(t *testing.T) {
	at := func(ratio float64) int {
		rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{
			sig(SignalSync, Pass, 1, 0),
			completenessSignal(int64(ratio*1000), 1000, bookRecord{}, "t"),
		}}}}
		rep.finalize()
		return rep.Score
	}
	above, below := at(0.901), at(0.899)
	if diff := above - below; diff > 2 {
		t.Errorf("a %d-point jump across the warn/fail boundary (%d vs %d)",
			diff, above, below)
	}
}

// Monotonicity: more of a defect always costs more, never less.
func TestScore_IsMonotoneInTheDefect(t *testing.T) {
	prev := 101
	for _, ratio := range []float64{1.0, 0.95, 0.9, 0.75, 0.5, 0.25, 0.1} {
		rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{
			completenessSignal(int64(ratio*10000), 10000, bookRecord{}, "t"),
		}}}}
		rep.finalize()
		if rep.Score > prev {
			t.Errorf("score rose to %d at ratio %v after %d", rep.Score, ratio, prev)
		}
		prev = rep.Score
	}
}

// An unmeasurable property is not evidence in either direction, so it leaves
// the product alone — and is reported through Coverage instead.
func TestScore_UnknownSignalsAreExcludedAndLowerCoverage(t *testing.T) {
	with := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 0),
		sig("b", Unknown, 0, 0),
	}}}}
	with.finalize()

	if with.Score != 100 {
		t.Errorf("Score = %d, want 100 — an Unknown must not score", with.Score)
	}
	if with.Coverage != 50 {
		t.Errorf("Coverage = %d, want 50", with.Coverage)
	}
}

func TestScore_FullCoverageWhenEverythingWasMeasured(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 0), sig("b", Warn, 0.8, 0),
	}}}}
	r.finalize()
	if r.Coverage != 100 {
		t.Errorf("Coverage = %d, want 100", r.Coverage)
	}
}

// Advisory checks floor at 1, so they cannot move the index. No special case in
// the arithmetic is needed for that — it falls out of the floor.
func TestScore_AdvisorySignalsCannotMoveTheIndex(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Pass, 1.0, 0),
		sig(SignalConcentration, Warn, 1, FloorAdvisory),
	}}}}
	r.finalize()

	if r.Score != 100 {
		t.Errorf("Score = %d, want 100 — an advisory signal must not score", r.Score)
	}
	if len(r.Problems()) != 1 {
		t.Errorf("advisory signal should still be reported as a problem to read")
	}
	if r.Coverage != 100 {
		t.Errorf("Coverage = %d, want 100 — an advisory is not an unmeasured check", r.Coverage)
	}
}

// A fatal defect takes the whole index, with no cap needed to force it.
func TestScore_FatalDefectsZeroTheIndex(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig(SignalSync, Fail, 0, FloorFatal),
		sig("b", Pass, 1, 0.3), sig("c", Pass, 1, 0.3), sig("d", Pass, 1, 0.3),
		sig("e", Pass, 1, 0.3), sig("f", Pass, 1, 0.3), sig("g", Pass, 1, 0.3),
	}}}}
	r.finalize()

	if r.Score != 0 {
		t.Errorf("Score = %d, want 0", r.Score)
	}
	if r.Band != BandInsufficient {
		t.Errorf("Band = %q, want %q", r.Band, BandInsufficient)
	}
}

// A floor bounds how much one defect can cost, so a survivable fault cannot
// zero an otherwise sound answer.
func TestScore_FloorsBoundWhatOneDefectCosts(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig(SignalFreshness, Fail, 0, FloorFreshness),
	}}}}
	r.finalize()

	if r.Score != 50 {
		t.Errorf("Score = %d, want 50 — the stalest data still retains half", r.Score)
	}
}

// A retention factor below its declared floor is clamped: a transfer function
// cannot cost more than the severity the check declared.
func TestRetention_IsClampedToTheFloor(t *testing.T) {
	s := sig("x", Fail, -0.5, 0.4)
	if got := s.retention(); got != 0.4 {
		t.Errorf("retention = %v, want 0.4", got)
	}
	if got := (sig("y", Pass, 2, 0)).retention(); got != 1 {
		t.Errorf("retention = %v, want 1", got)
	}
}

// A non-zero product must not round down to zero: "0%%" is reserved for
// "nothing behind this answer at all".
func TestScore_TinyButNonZeroDoesNotReadAsFatal(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Fail, 0.001, 0),
	}}}}
	r.finalize()
	if r.Score != 1 {
		t.Errorf("Score = %d, want 1", r.Score)
	}
}

// Nothing measurable must read as// Nothing measurable must read as "assessed and terrible" rather than
// "not assessed" — they call for opposite responses from the reader.
func TestScore_NothingMeasurableIsNotAssessed(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		sig("a", Unknown, 0, 0),
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
		sig("fail", Fail, 0, 0),
		sig("warn", Warn, 0.5, 0),
		sig("advisory", Warn, 1, FloorAdvisory),
		sig("pass", Pass, 1, 0),
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

func TestStaleness_RisesMonotonicallyAndIsBounded(t *testing.T) {
	prev := -1.0
	for _, days := range []int{0, 30, 60, 90, 200, 365, 700, 1095, 2000} {
		got := stalenessOf(days)
		if got < prev {
			t.Errorf("stalenessOf(%d) = %v fell below the previous %v", days, got, prev)
		}
		if got < 0 || got > 1 {
			t.Errorf("stalenessOf(%d) = %v out of [0,1]", days, got)
		}
		prev = got
	}
}

// The transfer function is the model's one shared curve, so it is worth
// pinning: full retention at no defect, exactly the floor at saturation, and
// linear between.
func TestRetain_TransferFunction(t *testing.T) {
	cases := []struct {
		floor, severity, want float64
	}{
		{0.5, 0, 1}, {0.5, 1, 0.5}, {0.5, 0.5, 0.75},
		{0, 1, 0}, {0.3, 2, 0.3}, {0.3, -1, 1},
	}
	for _, c := range cases {
		if got := retain(c.floor, c.severity); got != c.want {
			t.Errorf("retain(%v, %v) = %v, want %v", c.floor, c.severity, got, c.want)
		}
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
