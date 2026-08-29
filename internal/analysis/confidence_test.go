// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
)

// corruptionDB builds a Chicago database holding the corruption mode's
// contracts dataset, so the assessor runs against real DuckDB rather than a
// stub. The SQL this package generates is most of its risk; a fake Queryer
// would test the arithmetic and none of the queries.
type fixture struct {
	rows int // contract rows to write
	// nullDepartments many of those rows get a NULL department.
	nullDepartments int
	// futureDates many get an award date centuries from now.
	futureDates int
	// syncStatus of the recorded run: "ok", "error", or "running".
	syncStatus string
	// rowsWritten recorded by the sync run; 0 uses rows.
	rowsWritten int
	// upstreamUpdated is the portal's data_updated_at.
	upstreamUpdated time.Time
	// syncedAt is when the run started.
	syncedAt time.Time
}

func newCorruptionDB(t *testing.T, f fixture) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "data.cityofchicago.org.duckdb")
	w, err := duckdb.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer w.Close()

	if _, err := w.DB.Exec(`CREATE TABLE contracts (
		socrata_id VARCHAR, vendor_name VARCHAR, department VARCHAR,
		award_amount DOUBLE, procurement_type VARCHAR, start_date TIMESTAMP)`); err != nil {
		t.Fatalf("create contracts: %v", err)
	}

	// Rows are generated rather than listed so a fixture can ask for a
	// thousand of them without a thousand lines of test.
	if f.rows > 0 {
		if _, err := w.DB.Exec(fmt.Sprintf(`
			INSERT INTO contracts
			SELECT 'row' || i,
			       'Vendor ' || (i %% 5),
			       CASE WHEN i < %d THEN NULL ELSE 'Dept ' || (i %% 3) END,
			       1000.0 + i,
			       'Sole Source',
			       CASE WHEN i < %d THEN TIMESTAMP '3999-01-01'
			            ELSE TIMESTAMP '2024-06-01' END
			FROM range(%d) t(i)`,
			f.nullDepartments, f.futureDates, f.rows)); err != nil {
			t.Fatalf("insert contracts: %v", err)
		}
	}

	status := f.syncStatus
	if status == "" {
		status = "ok"
	}
	written := f.rowsWritten
	if written == 0 {
		written = f.rows
	}
	syncedAt := f.syncedAt
	if syncedAt.IsZero() {
		syncedAt = time.Now().Add(-24 * time.Hour)
	}
	var finished any = syncedAt.Add(time.Minute)
	if status == "running" {
		finished = nil
	}
	if _, err := w.DB.Exec(`INSERT INTO _csq.sync_runs
		(run_id, dataset_id, table_name, started_at, finished_at, status, rows_written, duration_ms)
		VALUES ('run1', 'rsxa-ify5', 'contracts', $1, $2, $3, $4, 60000)`,
		syncedAt, finished, status, written); err != nil {
		t.Fatalf("insert sync_run: %v", err)
	}

	upstream := f.upstreamUpdated
	if upstream.IsZero() {
		upstream = time.Now().Add(-48 * time.Hour)
	}
	if _, err := w.DB.Exec(`INSERT INTO _csq.catalog
		(id, name, description, category, tags, row_count, updated_at, fetched_at, raw)
		VALUES ('rsxa-ify5', 'Contracts', NULL, NULL, '[]', NULL, $1, $2, 'null')`,
		upstream, time.Now()); err != nil {
		t.Fatalf("insert catalog: %v", err)
	}
	return path
}

func assess(t *testing.T, path, query string) *confidence.Report {
	t.Helper()
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	rep, err := s.Confidence(context.Background(), "corruption", query)
	if err != nil {
		t.Fatalf("confidence: %v", err)
	}
	return rep
}

func signalNamed(t *testing.T, rep *confidence.Report, name string) confidence.Signal {
	t.Helper()
	for _, s := range rep.Signals {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("no %q signal in report (have %d signals)", name, len(rep.Signals))
	return confidence.Signal{}
}

// The end-to-end case: a healthy dataset profiles cleanly through real SQL.
func TestConfidence_HealthyDataset(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000})
	rep := assess(t, path, "top-vendors")

	if !rep.Assessed {
		t.Fatal("Assessed = false on a healthy dataset")
	}
	if len(rep.Datasets) != 1 {
		t.Fatalf("got %d datasets, want 1", len(rep.Datasets))
	}
	if rep.Datasets[0].Rows != 1000 {
		t.Errorf("Rows = %d, want 1000", rep.Datasets[0].Rows)
	}
	if s := signalNamed(t, rep, confidence.SignalSync); s.Level != confidence.Pass {
		t.Errorf("sync = %v, want Pass", s.Level)
	}
	if s := signalNamed(t, rep, confidence.SignalNullDensity); s.Level != confidence.Pass {
		t.Errorf("null_density = %v (%s), want Pass", s.Level, s.Label)
	}
}

// Only the datasets a query reads should be profiled. top-vendors touches
// contracts alone, even though the mode binds three datasets.
func TestConfidence_ProfilesOnlyWhatTheQueryReads(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 100})
	rep := assess(t, path, "top-vendors")

	if len(rep.Datasets) != 1 {
		t.Fatalf("got %d datasets, want 1 — the query reads only contracts", len(rep.Datasets))
	}
	if rep.Datasets[0].Table != "contracts" {
		t.Errorf("Table = %q, want contracts", rep.Datasets[0].Table)
	}
}

func TestConfidence_NullDensityIsMeasuredThroughRealSQL(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000, nullDepartments: 32})
	rep := assess(t, path, "department-concentration")

	s := signalNamed(t, rep, confidence.SignalNullDensity)
	if s.Level != confidence.Warn {
		t.Errorf("null_density = %v, want Warn", s.Level)
	}
	if s.Label != "3.2% of records lack department" {
		t.Errorf("Label = %q", s.Label)
	}
}

func TestConfidence_ImpossibleDatesAreCaught(t *testing.T) {
	// start_date is an optional concept column, so a query has to mention it
	// for it to be profiled. procurement-type does not, department-
	// concentration does not either — use the query that reads it.
	path := newCorruptionDB(t, fixture{rows: 1000, futureDates: 40})
	rep := assess(t, path, "top-vendors")

	// top-vendors does not read start_date, so no date signal should appear:
	// profiling a column the query ignores would depress a score for an answer
	// that cannot reach it.
	for _, s := range rep.Signals {
		if s.Name == confidence.SignalDateRange {
			t.Errorf("date_range signal on a query that reads no date column: %q", s.Label)
		}
	}
}

func TestConfidence_FailedSyncCapsTheScore(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000, syncStatus: "error"})
	rep := assess(t, path, "top-vendors")

	if s := signalNamed(t, rep, confidence.SignalSync); s.Level != confidence.Fail {
		t.Errorf("sync = %v, want Fail", s.Level)
	}
	if rep.Score > confidence.CapFailedSync {
		t.Errorf("Score = %d, want <= %d", rep.Score, confidence.CapFailedSync)
	}
}

// A run that started and never finished is the signature a killed sync leaves.
// The table may hold a partial load, which profiles perfectly well.
func TestConfidence_InterruptedSyncIsCaught(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000, syncStatus: "running"})
	rep := assess(t, path, "top-vendors")

	s := signalNamed(t, rep, confidence.SignalSync)
	if s.Level != confidence.Fail {
		t.Errorf("sync = %v, want Fail", s.Level)
	}
}

// Rows present against rows the sync says it wrote: the fault a completeness
// check misses whenever no reference count is available.
func TestConfidence_RowShortfallAgainstTheSyncIsCaught(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 500, rowsWritten: 1000})
	rep := assess(t, path, "top-vendors")

	s := signalNamed(t, rep, confidence.SignalRowIntegrity)
	if s.Level != confidence.Fail {
		t.Errorf("row_integrity = %v (%s), want Fail", s.Level, s.Label)
	}
}

// A dataset synced this morning is still stale if the city stopped publishing.
func TestConfidence_StaleUpstreamDespiteFreshSync(t *testing.T) {
	path := newCorruptionDB(t, fixture{
		rows:            1000,
		syncedAt:        time.Now().Add(-time.Hour),
		upstreamUpdated: time.Now().AddDate(-3, 0, 0),
	})
	rep := assess(t, path, "top-vendors")

	s := signalNamed(t, rep, confidence.SignalFreshness)
	if s.Level != confidence.Fail {
		t.Errorf("freshness = %v (%s), want Fail", s.Level, s.Label)
	}
	if rep.FreshnessDays == nil || *rep.FreshnessDays < 1000 {
		t.Errorf("FreshnessDays = %v, want ~1095", rep.FreshnessDays)
	}
}

// The portal changed the data after csq last pulled it: a provable gap between
// the local copy and the source, distinct from the data merely being old.
func TestConfidence_CopyBehindThePortalIsCaught(t *testing.T) {
	path := newCorruptionDB(t, fixture{
		rows:            1000,
		syncedAt:        time.Now().AddDate(0, -6, 0),
		upstreamUpdated: time.Now().Add(-24 * time.Hour),
	})
	rep := assess(t, path, "top-vendors")

	s := signalNamed(t, rep, confidence.SignalLag)
	if s.Level != confidence.Fail {
		t.Errorf("local_lag = %v (%s), want Fail", s.Level, s.Label)
	}
}

// An empty table passes every column check ever devised. The sync and
// completeness signals are what stop it from scoring well.
func TestConfidence_EmptyTableDoesNotScoreWell(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 0})
	rep := assess(t, path, "top-vendors")

	if rep.Score >= 50 {
		t.Errorf("Score = %d on an empty table, want well under 50", rep.Score)
	}
}

// A query over csq's own bookkeeping has no synced dataset behind it, and must
// say "not assessed" rather than render a zero.
func TestConfidence_BookkeepingModeIsNotAssessed(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 100})
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	rep, err := s.Confidence(context.Background(), "research", "provenance")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Assessed {
		t.Error("Assessed = true for a mode that reads only _csq")
	}
	if rep.Score != 0 || rep.Band != confidence.BandInsufficient {
		t.Errorf("got %d/%s, want 0/insufficient", rep.Score, rep.Band)
	}
}

// Run must attach the report to its result, or none of this reaches a reader.
func TestRun_AttachesConfidenceToTheResult(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000})
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "corruption", "top-vendors", 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.Confidence == nil {
		t.Fatal("Result.Confidence is nil")
	}
	if !res.Confidence.Assessed {
		t.Error("confidence was not assessed")
	}
	if len(res.Confidence.Limits) == 0 {
		t.Error("the report reached a result without its limits")
	}
}

// Concentration reads off the rows the user is shown. Five vendors with
// linearly increasing awards put no single one over the threshold, so the
// signal should stay quiet rather than manufacture a caution.
func TestRun_ConcentrationUsesTheReturnedRows(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000})
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	res, err := s.Run(context.Background(), "corruption", "top-vendors", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, sig := range res.Confidence.Signals {
		if sig.Name == confidence.SignalConcentration && !sig.Advisory() {
			t.Error("concentration must never be scored")
		}
	}
}

// Repeat assessments of the same datasets must not re-scan them.
func TestConfidence_IsCachedAcrossQueries(t *testing.T) {
	path := newCorruptionDB(t, fixture{rows: 1000})
	s, err := Open([]DBSpec{{Path: path}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	first, err := s.Confidence(ctx, "corruption", "top-vendors")
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Confidence(ctx, "corruption", "top-vendors")
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Error("a repeated assessment re-profiled instead of using the cache")
	}
}

// A database with no _csq bookkeeping must degrade to Unknown signals rather
// than take the answer down with it.
func TestConfidence_MissingBookkeepingDegradesGracefully(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data.cityofchicago.org.duckdb")
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE contracts (
		vendor_name VARCHAR, department VARCHAR, award_amount DOUBLE)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO contracts VALUES ('A', 'D', 10.0), ('B', 'D', 20.0)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	rep := assess(t, path, "top-vendors")
	// It must not panic, and must not claim the sync succeeded.
	if s := signalNamed(t, rep, confidence.SignalSync); s.Level != confidence.Fail {
		t.Errorf("sync = %v, want Fail — there is no sync record at all", s.Level)
	}
}
