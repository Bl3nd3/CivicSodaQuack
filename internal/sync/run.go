// Copyright (c) 2026 Neomantra Corp

package sync

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync/atomic"
	"time"

	"github.com/oklog/ulid/v2"
	"golang.org/x/sync/errgroup"

	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
)

// Deps are the collaborators the orchestrator needs. All fields are required
// except Resolver (defaults to DefaultSelectorResolver), Strategy (defaults to
// FullReplaceStrategy), and Reporter (defaults to StderrReporter to os.Stderr).
type Deps struct {
	DB             *duckdb.Writer
	Client         *socrata.Client
	Scheme         string // "http" for tests, "https" otherwise
	Resolver       SelectorResolver
	Strategy       WriteStrategy
	Reporter       ProgressReporter
	Only           []string // --only IDs
	DryRun         bool
	RefreshCatalog bool
	// FullRefreshIDs forces matching dataset IDs to bootstrap (Mode=full_replace)
	// for this run. Mutually exclusive with FullRefreshAll.
	FullRefreshIDs []string
	// FullRefreshAll forces every resolved dataset to bootstrap for this run.
	FullRefreshAll bool
}

// Summary is the end-of-run tally.
type Summary struct {
	RunID   string
	Planned int // number resolved
	OK      int
	Failed  int
	Aborted int
	Wall    time.Duration
	// Swept records what a previously killed run left behind and this one
	// cleared. Surfaced rather than silent: finding orphaned rows means a
	// previous sync died without reporting it, which is worth knowing.
	Swept duckdb.SweepResult
}

// Run executes the sync described by cfg. Returns a non-nil error iff any
// dataset failed (even under on_error=continue), so callers can map to exit 1.
func Run(ctx context.Context, cfg *config.Config, d Deps) (Summary, error) {
	started := time.Now()
	runID := newRunID()
	sum := Summary{RunID: runID}

	if d.Resolver == nil {
		d.Resolver = &DefaultSelectorResolver{}
	}
	scheme := d.Scheme
	if scheme == "" {
		scheme = "https"
	}
	if d.Strategy == nil {
		d.Strategy = &IncrementalStrategy{
			Portal: cfg.Portal, Scheme: scheme, RunID: runID,
		}
	}

	// Clear what a previously killed run left behind, before this one writes
	// anything. Safe here specifically: every caller holds the portal's write
	// lock, so nothing in _csq_staging can belong to a live sync, and this run
	// has not created its own staging tables yet. A dry run changes nothing and
	// so sweeps nothing.
	if !d.DryRun {
		swept, err := d.DB.SweepAbandoned(time.Now())
		if err != nil {
			return sum, fmt.Errorf("clear abandoned state from a previous run: %w", err)
		}
		sum.Swept = swept
	}

	// Catalog: refresh if asked or cache empty.
	catalog, err := d.DB.ReadCatalog()
	if err != nil {
		return sum, fmt.Errorf("read cached catalog: %w", err)
	}
	if d.RefreshCatalog || len(catalog) == 0 {
		if scheme == "http" {
			catalog, err = d.Client.FetchCatalogScheme(cfg.Portal, scheme)
		} else {
			catalog, err = d.Client.FetchCatalog(cfg.Portal)
		}
		if err != nil {
			return sum, fmt.Errorf("fetch catalog: %w", err)
		}
		if err := d.DB.UpsertCatalog(catalog, time.Now().UTC()); err != nil {
			return sum, fmt.Errorf("upsert catalog: %w", err)
		}
	}

	targets, err := d.Resolver.Resolve(ctx, cfg, catalog, d.Only)
	if err != nil {
		return sum, err
	}
	sum.Planned = len(targets)

	if d.FullRefreshAll && len(d.FullRefreshIDs) > 0 {
		return sum, errors.New("FullRefreshAll and FullRefreshIDs are mutually exclusive")
	}
	if len(d.FullRefreshIDs) > 0 {
		resolved := make(map[string]struct{}, len(targets))
		for _, t := range targets {
			resolved[t.ID] = struct{}{}
		}
		refreshSet := make(map[string]struct{}, len(d.FullRefreshIDs))
		for _, id := range d.FullRefreshIDs {
			if _, ok := resolved[id]; !ok {
				return sum, fmt.Errorf("--full-refresh %s: not in resolved selector set", id)
			}
			refreshSet[id] = struct{}{}
		}
		for i := range targets {
			if _, ok := refreshSet[targets[i].ID]; ok {
				targets[i].Effective.Mode = "full_replace"
			}
		}
	}
	if d.FullRefreshAll {
		for i := range targets {
			targets[i].Effective.Mode = "full_replace"
		}
	}

	if d.DryRun {
		sum.Wall = time.Since(started)
		return sum, nil
	}

	if d.Reporter == nil {
		d.Reporter = &StderrReporter{Out: stderrWriter{}}
	}

	total := len(targets)
	var ok, failed, aborted int64

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(cfg.Concurrency)

	for i, t := range targets {
		i, t := i, t
		g.Go(func() error {
			if gctx.Err() != nil && cfg.OnError == "abort" {
				recordAborted(d.DB, runID, t, time.Now().UTC(), gctx.Err())
				atomic.AddInt64(&aborted, 1)
				return nil
			}
			res, _ := runOne(gctx, d, runID, t, i+1, total)
			switch res.Status {
			case "ok":
				atomic.AddInt64(&ok, 1)
			case "aborted":
				atomic.AddInt64(&aborted, 1)
			default:
				atomic.AddInt64(&failed, 1)
				if cfg.OnError == "abort" {
					return fmt.Errorf("abort on first failure: %w", res.Err)
				}
			}
			return nil
		})
	}
	_ = g.Wait()

	sum.OK = int(ok)
	sum.Failed = int(failed)
	sum.Aborted = int(aborted)
	sum.Wall = time.Since(started)

	if sum.Failed > 0 || sum.Aborted > 0 {
		return sum, errors.New("one or more datasets failed")
	}
	return sum, nil
}

func runOne(ctx context.Context, d Deps, runID string, t DatasetTarget, idx, total int) (DatasetResult, error) {
	started := time.Now().UTC()
	rowid, err := d.DB.StartSyncRun(runID, t.ID, t.Effective.Table, t.Effective.Hash(), started)
	if err != nil {
		return DatasetResult{Target: t, Status: "failed", Err: err, StartedAt: started, FinishedAt: time.Now().UTC()}, err
	}

	res, _ := d.Strategy.Sync(ctx, t, d.Client, d.DB, d.Reporter, idx, total)
	errStr := ""
	if res.Err != nil {
		errStr = res.Err.Error()
	}
	if ferr := d.DB.FinishSyncRun(rowid, res.Status, res.RowsWritten, errStr, time.Now().UTC()); ferr != nil {
		// Best-effort: log-shaped failure in the result err but don't overwrite primary error
		if res.Err == nil {
			res.Err = ferr
		}
	}
	d.Reporter.DatasetDone(idx, total, t, res)
	return res, nil
}

func recordAborted(db *duckdb.Writer, runID string, t DatasetTarget, now time.Time, cause error) {
	rowid, err := db.StartSyncRun(runID, t.ID, t.Effective.Table, t.Effective.Hash(), now)
	if err != nil {
		return
	}
	msg := "aborted"
	if cause != nil {
		msg = cause.Error()
	}
	_ = db.FinishSyncRun(rowid, "aborted", 0, msg, now)
}

// newRunID returns a fresh ULID as a string. ULIDs are time-ordered, so
// run_ids sort chronologically without also joining on started_at.
func newRunID() string {
	entropy := ulid.Monotonic(rand.New(rand.NewSource(time.Now().UnixNano())), 0)
	return ulid.MustNew(ulid.Timestamp(time.Now()), entropy).String()
}

// stderrWriter lazy-imports os.Stderr, keeping this file import-light for tests.
type stderrWriter struct{}

func (stderrWriter) Write(p []byte) (int, error) {
	return writeStderr(p)
}
