// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/investigate"
)

// expectedPermits is the reference row count Chicago's ranking binding records
// for the building permits dataset.
//
// A fixture holding fewer rows than this is genuinely an incomplete local copy,
// and the shortfall challenge will withdraw any fall measured on it — which is
// correct, and makes it impossible to test a surviving finding without writing
// a realistic number of rows. So the fixture writes them.
const expectedPermits = 844259

// permitFixture describes the shape of a permits series to build.
type permitFixture struct {
	// perYear maps a year to how many permits were issued in it.
	perYear map[int]int
	// partialYear, when set, gets rows only up to 30 June — a year the record
	// enters and does not finish.
	partialYear int
	partialRows int
}

// newPermitsDB builds a Chicago database holding only the building permits
// dataset, with bookkeeping rows so the evidence behind an answer can be
// profiled the way it is in production.
//
// Only one of the ranking mode's three concepts is present on purpose: it
// exercises the plan's skip path in the same run as its measurement path, and
// those two must not be tested apart — an investigation that measures what it
// has while quietly forgetting what it lacks is the failure this design exists
// to prevent.
func newPermitsDB(t *testing.T, f permitFixture) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.cityofchicago.org.duckdb")
	w, err := duckdb.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if _, err := w.DB.Exec(
		`CREATE TABLE building_permits (
			socrata_id VARCHAR, issue_date TIMESTAMP, processing_time DOUBLE)`); err != nil {
		t.Fatalf("create building_permits: %v", err)
	}

	total := 0
	for year, n := range f.perYear {
		// Spread over the year's real length, so a full year reaches 31
		// December. Hard-coding 365 leaves a leap year one day short, and the
		// coverage step correctly reads that as a year the record does not
		// finish — which is the right behaviour and the wrong fixture.
		if _, err := w.DB.Exec(fmt.Sprintf(`
			INSERT INTO building_permits
			SELECT 'p%[1]d-' || i,
			       make_date(%[1]d, 1, 1)::TIMESTAMP
			         + INTERVAL (i %% date_diff('day', make_date(%[1]d, 1, 1),
			                                    make_date(%[1]d + 1, 1, 1))) DAY,
			       1.0
			FROM range(%[2]d) t(i)`, year, n)); err != nil {
			t.Fatalf("insert %d permits: %v", year, err)
		}
		total += n
	}
	if f.partialYear != 0 {
		if _, err := w.DB.Exec(fmt.Sprintf(`
			INSERT INTO building_permits
			SELECT 'p%d-' || i,
			       TIMESTAMP '%d-01-01' + INTERVAL (i %% 180) DAY,
			       1.0
			FROM range(%d) t(i)`,
			f.partialYear, f.partialYear, f.partialRows)); err != nil {
			t.Fatalf("insert partial permits: %v", err)
		}
		total += f.partialRows
	}

	syncedAt := time.Now().Add(-24 * time.Hour)
	if _, err := w.DB.Exec(`INSERT INTO _csq.sync_runs
		(run_id, dataset_id, table_name, started_at, finished_at, status, rows_written, duration_ms)
		VALUES ('run1', 'ydr8-5enu', 'building_permits', $1, $2, 'ok', $3, 60000)`,
		syncedAt, syncedAt.Add(time.Minute), total); err != nil {
		t.Fatalf("insert sync_run: %v", err)
	}
	if _, err := w.DB.Exec(`INSERT INTO _csq.catalog
		(id, name, description, category, tags, row_count, updated_at, fetched_at, raw)
		VALUES ('ydr8-5enu', 'Building Permits', NULL, NULL, '[]', NULL, $1, $2, 'null')`,
		time.Now().Add(-48*time.Hour), time.Now()); err != nil {
		t.Fatalf("insert catalog: %v", err)
	}
	return path
}

func investigateFixture(t *testing.T, path, question string) *investigate.Report {
	t.Helper()
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	rep, err := s.Investigate(context.Background(), question, investigate.Options{})
	if err != nil {
		t.Fatalf("investigate: %v", err)
	}
	return rep
}

func findingNamed(t *testing.T, rep *investigate.Report, probe string) investigate.Finding {
	t.Helper()
	for _, f := range rep.Analysis.Findings {
		if f.Probe == probe {
			return f
		}
	}
	t.Fatalf("no finding for probe %q", probe)
	return investigate.Finding{}
}

// The whole pipeline on a complete local copy: a real fall is measured, it
// survives every challenge, and the part-finished year is excluded from the
// measurement while still being reported.
func TestInvestigate_MeasuresARealFallAndReportsThePartialYear(t *testing.T) {
	path := newPermitsDB(t, permitFixture{
		perYear: map[int]int{
			2021: 200000, 2022: 200000, 2023: 200000, 2024: 140000,
		},
		partialYear: 2025, partialRows: 110000,
	})
	rep := investigateFixture(t, path, "Is Chicago publishing fewer records?")

	if rep.Investigation != "civic-publishing" {
		t.Fatalf("routed to %q", rep.Investigation)
	}

	f := findingNamed(t, rep, "permit-records")
	if f.Withdrawn {
		t.Fatalf("a real fall on a complete copy was withdrawn by %q", f.WithdrawnBy)
	}
	if f.LatestPeriod != 2024 {
		t.Errorf("measured %d — the part-finished 2025 must not be the latest period",
			f.LatestPeriod)
	}
	if f.Direction != investigate.Down {
		t.Errorf("direction = %q, want down", f.Direction)
	}
	if f.Change > -0.29 || f.Change < -0.31 {
		t.Errorf("change = %v, want about -0.30 (140k against a 200k baseline)", f.Change)
	}
	if !f.Supports {
		t.Error("a fall in published permits supports the claim the plan declared")
	}

	// The partial year is excluded from the measurement and still visible.
	var saw2025 bool
	for _, p := range f.Series {
		if p.Period == 2025 {
			saw2025 = true
			if p.Complete {
				t.Error("2025 is a part-year and must not be marked complete")
			}
		}
	}
	if !saw2025 {
		t.Error("2025 is missing from the series — it is excluded from the measurement, not deleted")
	}

	joined := strings.Join(rep.Caveats, "\n")
	if !strings.Contains(joined, "2025 is incomplete") {
		t.Errorf("the partial year is not in the caveats:\n%s", joined)
	}
}

// The other half of the same run: two of the four indicators have no data, and
// the report says which, why, and what would fix it.
func TestInvestigate_UnsyncedIndicatorsAreReportedNotIgnored(t *testing.T) {
	path := newPermitsDB(t, permitFixture{
		perYear: map[int]int{2021: 200000, 2022: 200000, 2023: 200000, 2024: 140000},
	})
	rep := investigateFixture(t, path, "Is Chicago publishing fewer records?")

	if rep.Plan.Runnable != 1 {
		t.Errorf("runnable = %d, want 1 — only permits are synced", rep.Plan.Runnable)
	}
	if rep.Plan.Total != 4 {
		t.Errorf("total = %d, want 4", rep.Plan.Total)
	}
	if rep.Readiness.Ready {
		t.Error("readiness must not be ready with three indicators unsynced")
	}
	if rep.Readiness.FixCommand == "" {
		t.Error("an unready investigation must say what to run")
	}
	// The fix command names the datasets that are actually missing.
	for _, want := range []string{"ijzp-q8t2", "v6vf-nfxy"} {
		if !strings.Contains(rep.Readiness.FixCommand, want) {
			t.Errorf("fix command %q does not name missing dataset %s",
				rep.Readiness.FixCommand, want)
		}
	}

	joined := strings.Join(rep.Caveats, "\n")
	if !strings.Contains(joined, "not synced yet") {
		t.Errorf("skipped indicators are not in the caveats:\n%s", joined)
	}

	// Coverage is one of four, and the confidence carries that.
	if rep.Coverage != 25 {
		t.Errorf("coverage = %d%%, want 25%%", rep.Coverage)
	}
	if !rep.Assessed {
		t.Fatal("the evidence behind the answered indicator should have been profiled")
	}
	if rep.Confidence > rep.Retention {
		t.Errorf("confidence %d exceeds retention %d — coverage must only reduce it",
			rep.Confidence, rep.Retention)
	}
}

// An incomplete local copy cannot support a claim that records are falling,
// because the missing rows are indistinguishable from the fall. This is the
// behaviour the real Chicago database exhibits.
func TestInvestigate_ShortLocalCopyWithdrawsTheFall(t *testing.T) {
	// A tenth of the reference count, with the same downward shape.
	path := newPermitsDB(t, permitFixture{
		perYear: map[int]int{2021: 20000, 2022: 20000, 2023: 20000, 2024: 14000},
	})
	rep := investigateFixture(t, path, "Is Chicago publishing fewer records?")

	f := findingNamed(t, rep, "permit-records")
	if !f.Withdrawn {
		t.Fatal("a fall measured on a copy 98% short of the portal must be withdrawn")
	}
	if f.WithdrawnBy != "local-copy-shortfall" {
		t.Errorf("withdrawn by %q, want local-copy-shortfall", f.WithdrawnBy)
	}
	if rep.Verdict != investigate.VerdictInsufficient {
		t.Errorf("verdict = %q, want %q when nothing survives",
			rep.Verdict, investigate.VerdictInsufficient)
	}
	if len(rep.Surviving()) != 0 {
		t.Error("no finding should survive")
	}
	// The reader is still told the investigation looked.
	if !strings.Contains(strings.Join(rep.Caveats, "\n"), "withdrawn") {
		t.Error("the withdrawal is missing from the caveats")
	}
}

// Answering about the wrong city is the one failure that looks like a success,
// so it has to be refused rather than caveated.
func TestInvestigate_RefusesAQuestionAboutAnotherCity(t *testing.T) {
	path := newPermitsDB(t, permitFixture{perYear: map[int]int{2023: 10, 2024: 10}})
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	_, err = s.Investigate(context.Background(),
		"Is New York publishing fewer records?", investigate.Options{})
	if err == nil {
		t.Fatal("expected a refusal: the question names a city this database is not")
	}
	if !strings.Contains(err.Error(), "New York") {
		t.Errorf("error %q should name the city that was asked about", err)
	}
}

// The reproduction key identifies the corpus, not the moment of asking.
func TestInvestigate_SnapshotNamesTheCorpus(t *testing.T) {
	path := newPermitsDB(t, permitFixture{
		perYear: map[int]int{2021: 200000, 2022: 200000, 2023: 200000, 2024: 140000},
	})
	rep := investigateFixture(t, path, "Is Chicago publishing fewer records?")

	if !strings.HasPrefix(rep.Snapshot, "chicago-") {
		t.Errorf("snapshot = %q, want a chicago- prefix", rep.Snapshot)
	}
	// It carries the last successful sync date, which the fixture set to
	// yesterday — not today.
	wantDate := time.Now().Add(-24 * time.Hour).Format("2006-01-02")
	if !strings.Contains(rep.Snapshot, wantDate) {
		t.Errorf("snapshot = %q, want the sync date %s", rep.Snapshot, wantDate)
	}
	if rep.Reproduce == "" {
		t.Error("a report must carry the command that reproduces it")
	}
}

func TestInvestigationStatuses_ReportsWhatCanRun(t *testing.T) {
	path := newPermitsDB(t, permitFixture{perYear: map[int]int{2023: 10, 2024: 10}})
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	sts, err := s.InvestigationStatuses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]InvestigationStatus{}
	for _, st := range sts {
		byName[st.Name] = st
	}

	pub, ok := byName["civic-publishing"]
	if !ok {
		t.Fatal("civic-publishing is missing from the statuses")
	}
	if !pub.Applicable {
		t.Error("Chicago is bound to the ranking mode, so this is applicable")
	}
	if pub.Ready {
		t.Error("three of four indicators are unsynced; this is not ready")
	}
	if pub.Runnable != 1 {
		t.Errorf("runnable = %d, want 1", pub.Runnable)
	}

	// The police investigation has no synced data at all, and its reason must
	// distinguish that from being unavailable for the city.
	pol, ok := byName["police-transparency"]
	if !ok {
		t.Fatal("police-transparency is missing from the statuses")
	}
	if !pol.Applicable {
		t.Error("Chicago is bound to the police mode, so this is applicable")
	}
	if pol.Runnable != 0 {
		t.Errorf("runnable = %d, want 0", pol.Runnable)
	}
}
