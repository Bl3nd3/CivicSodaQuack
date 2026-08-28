// Copyright (c) 2026 Neomantra Corp

package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/neomantra/CivicSodaQuack/internal/config"
	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/portallock"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
	syncpkg "github.com/neomantra/CivicSodaQuack/internal/sync"
)

func runSync(args []string) error {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	var (
		configPath     string
		dryRun         bool
		refreshCatalog bool
		concurrency    int
		only           string
		verbose        bool
		fullRefresh    []string
		fullRefreshAll bool
		noLock         bool
		lockWait       time.Duration
	)
	fs.StringVar(&configPath, "config", "", "Portal YAML config (required)")
	fs.BoolVar(&dryRun, "dry-run", false, "Resolve selectors and print, don't write")
	fs.BoolVar(&refreshCatalog, "refresh-catalog", false, "Force refetch catalog before resolution")
	fs.IntVar(&concurrency, "concurrency", 0, "Override YAML concurrency (0 = use YAML)")
	fs.StringVar(&only, "only", "", "Comma-separated 4x4 ids to intersect with the resolved set")
	fs.BoolVarP(&verbose, "verbose", "v", false, "Verbose progress")
	fs.StringArrayVar(&fullRefresh, "full-refresh", nil,
		"Force named dataset(s) to bootstrap this run (repeatable)")
	fs.BoolVar(&fullRefreshAll, "full-refresh-all", false,
		"Force every resolved dataset to bootstrap this run")
	fs.BoolVar(&noLock, "no-lock", false, "Skip portal lock acquisition")
	fs.DurationVar(&lockWait, "lock-wait", 0,
		"Retry lock acquisition for up to this duration before giving up")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if configPath == "" {
		return fmt.Errorf("--config is required")
	}
	if fullRefreshAll && len(fullRefresh) > 0 {
		return fmt.Errorf("--full-refresh and --full-refresh-all are mutually exclusive")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if concurrency > 0 {
		cfg.Concurrency = concurrency
	}

	lock, err := portallock.Acquire(cfg.DB, portallock.Options{
		NoLock:   noLock,
		LockWait: lockWait,
	})
	if err != nil {
		return err
	}
	defer lock.Release()

	w, err := duckdb.Open(cfg.DB)
	if err != nil {
		return err
	}
	defer w.Close()

	// Report unfinished runs before the sweep resolves them: after it, the
	// count is zero and the fact that a previous sync died would vanish.
	if n, err := w.IncompleteSyncRunCount(); err == nil && n > 0 {
		fmt.Fprintf(os.Stderr, "[csq] %d prior run(s) never finished; clearing what they left behind\n", n)
	}

	client := &socrata.Client{AppToken: cfg.AppToken}

	var onlyIDs []string
	if only != "" {
		for _, id := range strings.Split(only, ",") {
			id = strings.TrimSpace(id)
			if id != "" {
				onlyIDs = append(onlyIDs, id)
			}
		}
	}

	scheme := "https"
	if s := os.Getenv("CSQ_SCHEME"); s != "" {
		scheme = s
	}
	summary, err := syncpkg.Run(context.Background(), cfg, syncpkg.Deps{
		DB:             w,
		Client:         client,
		Scheme:         scheme,
		Reporter:       &syncpkg.StderrReporter{Out: os.Stderr},
		Only:           onlyIDs,
		DryRun:         dryRun,
		RefreshCatalog: refreshCatalog,
		FullRefreshIDs: fullRefresh,
		FullRefreshAll: fullRefreshAll,
	})
	if dryRun {
		fmt.Fprintf(os.Stderr, "[csq] dry-run: would sync %d datasets\n", summary.Planned)
		return nil
	}
	if !summary.Swept.Empty() {
		fmt.Fprintf(os.Stderr, "[csq] reclaimed %s\n", summary.Swept)
	}
	fmt.Fprintf(os.Stderr, "[csq] summary: %d ok, %d failed, %d aborted, wall %s\n",
		summary.OK, summary.Failed, summary.Aborted, summary.Wall)
	if err != nil {
		return err
	}
	return nil
}
