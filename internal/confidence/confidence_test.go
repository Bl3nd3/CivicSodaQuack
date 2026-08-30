// Copyright (c) 2026 Neomantra Corp

package confidence

import (
	"testing"
	"time"
)

// scored is a terse constructor for a check that enters R with retention r.
func scored(name string, r float64) Signal {
	return Signal{Name: name, Kind: Scored, Level: LevelFor(r), Score: r}
}

// diag is a check that is reported but never scored.
func diag(name string, level Level) Signal {
	return Signal{Name: name, Kind: Diagnostic, Level: level}
}

func TestScore_IsTheProductOfRetentions(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		scored("a", 0.9),
		scored("b", 0.5),
	}}}}
	r.finalize()

	if r.Score != 45 { // 0.9 × 0.5
		t.Errorf("Score = %d, want 45", r.Score)
	}
}

// The property that makes two scores comparable: a check that costs nothing
// leaves R exactly where it was. R therefore depends only on the evidence
// actually lost, never on how many ways it was looked for.
func TestScore_ACostlessCheckDoesNotMoveTheIndex(t *testing.T) {
	alone := &Report{Datasets: []DatasetReport{{Signals: []Signal{scored("a", 0.9)}}}}
	alone.finalize()

	withMore := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		scored("a", 0.9), scored("b", 1.0), scored("c", 1.0),
	}}}}
	withMore.finalize()

	if alone.Score != withMore.Score {
		t.Errorf("costless checks moved the score: %d vs %d", alone.Score, withMore.Score)
	}
	if alone.Score != 90 {
		t.Errorf("Score = %d, want 90", alone.Score)
	}
}

// A loss must never be diluted by the checks that found nothing.
func TestScore_LossesAreNotDilutedByPasses(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		scored(SignalCompleteness, 0.54),
		scored("c", 1), scored("d", 1), scored("e", 1), scored("f", 1),
	}}}}
	r.finalize()

	if r.Score != 54 {
		t.Errorf("Score = %d, want 54", r.Score)
	}
}

// Continuity and monotonicity: every input moves the score smoothly, in one
// direction, with no threshold anywhere in the arithmetic to jump across.
func TestScore_IsContinuousAndMonotone(t *testing.T) {
	at := func(ratio float64) int {
		rep := &Report{Datasets: []DatasetReport{{Signals: []Signal{
			completenessSignal(int64(ratio*100000), 100000, nil, "t"),
		}}}}
		rep.finalize()
		return rep.Score
	}
	prev := 101
	for _, ratio := range []float64{1.0, 0.951, 0.95, 0.949, 0.9, 0.899, 0.75, 0.5, 0.25, 0.1} {
		got := at(ratio)
		if got > prev {
			t.Errorf("score rose to %d at ratio %v after %d", got, ratio, prev)
		}
		prev = got
	}
	// Either side of the presentation cutoffs, the number moves by the input.
	for _, boundary := range []float64{FailBelow, WarnBelow} {
		above, below := at(boundary+0.0005), at(boundary-0.0005)
		if above-below > 1 {
			t.Errorf("a %d-point jump across %v", above-below, boundary)
		}
	}
}

// Diagnostics are reported but can never move R.
func TestScore_DiagnosticsCannotMoveTheIndex(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		scored("a", 1.0),
		diag(SignalFreshness, Warn),
		diag(SignalSync, Fail),
		diag(SignalConcentration, Warn),
	}}}}
	r.finalize()

	if r.Score != 100 {
		t.Errorf("Score = %d, want 100 — diagnostics must not score", r.Score)
	}
	if len(r.Problems()) != 3 {
		t.Errorf("got %d problems, want 3 — diagnostics must still be reported",
			len(r.Problems()))
	}
	if r.Coverage != 100 {
		t.Errorf("Coverage = %d, want 100 — a diagnostic is not an unrun check", r.Coverage)
	}
}

// A sync failure explains a shortfall that completeness already counted.
// Scoring it too would charge the same missing rows twice, and would let a
// sync that failed at 90% read as having delivered nothing.
func TestScore_AFailedSyncIsNotChargedTwice(t *testing.T) {
	now := time.Now()
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		syncSignal(bookRecord{found: true, lastStatus: "error",
			lastStarted: &now, lastFinished: &now}, "t"),
		completenessSignal(900, 1000, nil, "t"),
		scored(SignalUsability, 1),
	}}}}
	r.finalize()

	if r.Score != 90 {
		t.Errorf("Score = %d, want 90 — nine tenths of the rows are here", r.Score)
	}
}

// An unrunnable check leaves the product alone and is reported through
// coverage, so a gap in the evidence cannot read as a clean result.
func TestScore_UnknownChecksLowerCoverageNotTheScore(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		scored("a", 1.0),
		{Name: "b", Kind: Scored, Level: Unknown},
	}}}}
	r.finalize()

	if r.Score != 100 {
		t.Errorf("Score = %d, want 100", r.Score)
	}
	if r.Coverage != 50 {
		t.Errorf("Coverage = %d, want 50", r.Coverage)
	}
}

func TestScore_NothingMeasurableIsNotAssessed(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		{Name: "a", Kind: Scored, Level: Unknown},
	}}}}
	r.finalize()

	if r.Assessed {
		t.Error("Assessed = true with nothing measured")
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

func TestReport_AlwaysCarriesItsLimits(t *testing.T) {
	r := &Report{}
	r.finalize()
	if len(r.Limits) == 0 {
		t.Fatal("report has no limits")
	}
}

func TestSignalOrder_PassesThenDiagnosticsThenProblems(t *testing.T) {
	r := &Report{Datasets: []DatasetReport{{Signals: []Signal{
		scored("fail", 0.1),
		scored("warn", 0.95),
		diag("advisory", Warn),
		scored("pass", 1),
	}}}}
	r.finalize()

	want := []string{"pass", "advisory", "warn", "fail"}
	for i, w := range want {
		if r.Signals[i].Name != w {
			t.Errorf("signal %d = %q, want %q", i, r.Signals[i].Name, w)
		}
	}
}

// One rule derives every check's presentation, so a warning means the same
// loss whichever check raised it.
func TestLevelFor(t *testing.T) {
	cases := []struct {
		r    float64
		want Level
	}{
		{1.0, Pass}, {0.9995, Pass}, {0.99, Warn}, {0.90, Warn}, {0.89, Fail}, {0, Fail},
	}
	for _, c := range cases {
		if got := LevelFor(c.r); got != c.want {
			t.Errorf("LevelFor(%v) = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestGrade(t *testing.T) {
	cases := []struct {
		score int
		band  string
	}{
		{100, BandHigh}, {95, BandHigh},
		{94, BandModerate}, {80, BandModerate},
		{79, BandLow}, {50, BandLow},
		{49, BandInsufficient}, {0, BandInsufficient},
	}
	for _, c := range cases {
		if got := Grade(c.score); got != c.band {
			t.Errorf("Grade(%d) = %q, want %q", c.score, got, c.band)
		}
	}
}

// --- the two scored checks ---

func TestCompletenessSignal_RetentionIsTheRatio(t *testing.T) {
	cases := []struct {
		name           string
		held, expected int64
		wantScore      float64
		wantLevel      Level
	}{
		{"complete", 1000, 1000, 1, Pass},
		{"one short", 999, 1000, 0.999, Pass},
		{"noticeably short", 940, 1000, 0.94, Warn},
		{"truncated", 540, 1000, 0.54, Fail},
		{"empty", 0, 1000, 0, Fail},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := completenessSignal(c.held, c.expected, nil, "t")
			if s.Score != c.wantScore {
				t.Errorf("Score = %v, want %v", s.Score, c.wantScore)
			}
			if s.Level != c.wantLevel {
				t.Errorf("Level = %v, want %v (%s)", s.Level, c.wantLevel, s.Label)
			}
		})
	}
}

// Growth must not pay for a defect elsewhere in the product.
func TestCompletenessSignal_GrowthClampsAtOne(t *testing.T) {
	s := completenessSignal(2000, 1000, nil, "t")
	if s.Score != 1 {
		t.Errorf("Score = %v, want 1", s.Score)
	}
	if s.Level != Pass {
		t.Errorf("Level = %v, want Pass", s.Level)
	}
}

func TestCompletenessSignal_NoReferenceIsUnknown(t *testing.T) {
	s := completenessSignal(1000, 0, nil, "t")
	if s.Level != Unknown {
		t.Errorf("Level = %v, want Unknown", s.Level)
	}

	n := int64(2000)
	s = completenessSignal(1000, 0, &n, "t")
	if s.Level != Fail || s.Score != 0.5 {
		t.Errorf("got %v/%v, want Fail/0.5 against the catalog count", s.Level, s.Score)
	}
}

// U is the joint count, not a combination of per-column rates: nulls in civic
// data cluster, and assuming independence would overstate the loss.
func TestUsabilitySignal_UsesTheJointCount(t *testing.T) {
	// Two columns each 10% null, but the same rows are null in both, so 90% of
	// rows survive — not 0.9 × 0.9 = 81%.
	p := tableProfile{rows: 1000, usable: 900, cols: []colProfile{
		{name: "a", nulls: 100},
		{name: "b", nulls: 100},
	}}
	s := usabilitySignal(p, "t")

	if s.Score != 0.9 {
		t.Errorf("Score = %v, want 0.9 — the joint count, not the product of rates", s.Score)
	}
	if s.Lost != 100 {
		t.Errorf("Lost = %d, want 100", s.Lost)
	}
}

func TestUsabilitySignal_NamesTheWorstColumn(t *testing.T) {
	p := tableProfile{rows: 1000, usable: 963, cols: []colProfile{
		{name: "vendor_name", nulls: 0},
		{name: "department", nulls: 32},
		{name: "award_amount", nulls: 5},
	}}
	s := usabilitySignal(p, "contracts")

	if s.Level != Warn {
		t.Errorf("Level = %v, want Warn", s.Level)
	}
	if s.Label != "3.2% of records lack department" {
		t.Errorf("Label = %q", s.Label)
	}
}

func TestUsabilitySignal_ImpossibleDatesCountAsUnusable(t *testing.T) {
	p := tableProfile{rows: 1000, usable: 950, cols: []colProfile{
		{name: "start_date", role: roleDate, past: 30, future: 20},
	}}
	s := usabilitySignal(p, "t")

	if s.Score != 0.95 {
		t.Errorf("Score = %v, want 0.95", s.Score)
	}
	if s.Label != "5% of records carry an impossible date in start_date" {
		t.Errorf("Label = %q", s.Label)
	}
}

func TestUsabilitySignal_EmptyTableScoresZero(t *testing.T) {
	s := usabilitySignal(tableProfile{rows: 0}, "t")
	if s.Level != Fail || s.Score != 0 {
		t.Errorf("got %v/%v, want Fail/0 — an empty table profiles beautifully otherwise",
			s.Level, s.Score)
	}
}

func TestUsabilitySignal_CleanTablePasses(t *testing.T) {
	p := tableProfile{rows: 1000, usable: 1000, cols: []colProfile{
		{name: "a"}, {name: "b"}, {name: "c"},
	}}
	s := usabilitySignal(p, "t")
	if s.Level != Pass || s.Score != 1 {
		t.Errorf("got %v/%v, want Pass/1", s.Level, s.Score)
	}
}

// --- diagnostics ---

func TestSyncSignal_IsAlwaysDiagnostic(t *testing.T) {
	now := time.Now()
	for _, b := range []bookRecord{
		{},
		{found: true, lastStatus: "running", lastStarted: &now},
		{found: true, lastStatus: "error", lastStarted: &now, lastFinished: &now},
		{found: true, lastStatus: "ok", lastStarted: &now, lastFinished: &now, okAt: &now},
	} {
		s := syncSignal(b, "t")
		if s.Kind != Diagnostic {
			t.Errorf("sync signal %q is Scored; it must never enter R", s.Label)
		}
	}
}

// Upstream freshness and sync recency answer opposite questions.
func TestFreshnessSignal_MeasuresUpstreamNotSync(t *testing.T) {
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	syncedToday, frozen := now, now.AddDate(-3, 0, 0)

	s := freshnessSignal(bookRecord{upstreamUpdated: &frozen, okAt: &syncedToday}, "t", now)
	if s.Level != Warn {
		t.Errorf("Level = %v, want Warn — three-year-old data synced today is stale", s.Level)
	}
	if s.Kind != Diagnostic {
		t.Error("freshness must be diagnostic: staleness removes no rows")
	}
}

func TestLagSignal_DetectsACopyBehindThePortal(t *testing.T) {
	synced := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	changed := synced.AddDate(0, 6, 0)

	s := lagSignal(bookRecord{okAt: &synced, upstreamUpdated: &changed}, "t")
	if s.Level != Warn || s.Kind != Diagnostic {
		t.Errorf("got %v/%v, want Warn/Diagnostic", s.Level, s.Kind)
	}

	current := lagSignal(bookRecord{okAt: &changed, upstreamUpdated: &synced}, "t")
	if current.Level != Pass {
		t.Errorf("Level = %v, want Pass", current.Level)
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
		{"lobbyist_id", "VARCHAR", roleValue},
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
	rows := [][]any{{"Acme", 610.0}, {"Beta", 200.0}, {"Gamma", 190.0}}
	AddConcentration(r, "vendor_name", "total_awarded", cols, rows)

	if len(r.Signals) != 1 {
		t.Fatalf("got %d signals, want 1", len(r.Signals))
	}
	s := r.Signals[0]
	if !s.Advisory() {
		t.Error("concentration must be diagnostic — dominance is not a data defect")
	}
	if s.Label != "Acme accounts for 61% of the total shown" {
		t.Errorf("Label = %q", s.Label)
	}
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

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
