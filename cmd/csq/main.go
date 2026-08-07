// Copyright (c) 2026 Neomantra Corp
//
// csq — CivicSodaQuack CLI.
//
// Phase 0: extract a single Socrata dataset from a single portal into a
// per-portal DuckDB file, using /api/views/{id}.json metadata as the schema
// authority.

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/neomantra/CivicSodaQuack/internal/duckdb"
	"github.com/neomantra/CivicSodaQuack/internal/portallock"
	"github.com/neomantra/CivicSodaQuack/internal/socrata"
)

const usage = `csq — CivicSodaQuack

Usage:
  csq extract  --portal <host> --dataset <4x4> [options]
  csq catalog  --portal <host> [--refresh] [--json] [--output FILE]
  csq sync     --config <portal.yaml> [--dry-run] [--only IDs] [--full-refresh ID ...] [--full-refresh-all]
  csq mcp      --db <portal.duckdb> [--db ...] [--config <yaml> ...] [--http <addr>]
  csq snapshot --db <portal.duckdb> --output <snap.tar.zst> [--portal NAME] [--force]
  csq fetch    (--from <url> | --index <url> [--snapshot <id>]) [--output <path>] [--no-verify] [--force]
  csq snapshot-index update --index <path> --add <tarball> --url <url> [--max-keep N]
  csq snapshot-index validate --index <path>
  csq modes    [show|init|run] <mode> [options]
  csq query    --db <portal.duckdb> [--db ...] [--format csv|json|parquet] <SQL>

Modes — curated analysis profiles, ready to sync and query:
  corruption   Contract concentration, lobbying spend, and political contributions
  ranking      Rank attached portals by breadth, subject coverage, and freshness
  police       Complaints against police, oversight findings, and how they resolve
  research     Audit what you hold, where it came from, and which columns to distrust

  Start with 'csq modes' to list them, or 'csq modes show <mode>' for the
  datasets, queries, and interpretation caveats each one carries.

All subcommands except 'fetch' acquire <dbpath>.lock (advisory flock).
Pass --no-lock to bypass or --lock-wait <duration> to retry.

Examples:
  csq extract  --portal data.cityofchicago.org --dataset 6zsd-86xi --limit 10000
  csq catalog  --portal data.cityofchicago.org --category "Public Safety"
  csq sync     --config data.cityofchicago.org.yaml --full-refresh 6zsd-86xi
  csq sync     --config data.cityofchicago.org.yaml --full-refresh-all --lock-wait 30s
  csq mcp      --db data.cityofchicago.org.duckdb --config data.cityofchicago.org.yaml
  csq snapshot --db data.cityofchicago.org.duckdb --output chicago-2026-04-28.tar.zst
  csq fetch    --from https://example.com/snapshots/chicago-2026-04-28.tar.zst
  csq modes    show police
  csq modes    init police --output police.yaml
  csq modes    run  police --db data.cityofchicago.org.duckdb --query finding-outcomes
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "extract":
		if err := runExtract(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq extract: %v\n", err)
			os.Exit(1)
		}
	case "catalog":
		if err := runCatalog(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq catalog: %v\n", err)
			os.Exit(1)
		}
	case "sync":
		if err := runSync(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq sync: %v\n", err)
			os.Exit(1)
		}
	case "mcp":
		if err := runMCP(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq mcp: %v\n", err)
			os.Exit(1)
		}
	case "snapshot":
		if err := runSnapshot(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq snapshot: %v\n", err)
			os.Exit(1)
		}
	case "fetch":
		if err := runFetch(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq fetch: %v\n", err)
			os.Exit(1)
		}
	case "snapshot-index":
		if err := runSnapshotIndex(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq snapshot-index: %v\n", err)
			os.Exit(1)
		}
	case "modes", "mode":
		if err := runModes(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq modes: %v\n", err)
			os.Exit(1)
		}
	case "query":
		if err := runQuery(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "csq query: %v\n", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
}

func runExtract(args []string) error {
	fs := flag.NewFlagSet("extract", flag.ContinueOnError)
	var (
		portal    string
		dataset   string
		dbPath    string
		table     string
		appToken  string
		orderBy   string
		where     string
		batchSize int
		limit     int
		replace   bool
		verbose   bool
		noLock    bool
		lockWait  time.Duration
	)
	fs.StringVar(&portal, "portal", "", "Socrata portal host (e.g. data.cityofchicago.org)")
	fs.StringVar(&dataset, "dataset", "", "Socrata dataset 4x4 id (e.g. 6zsd-86xi)")
	fs.StringVar(&dbPath, "db", "", "DuckDB file path (default: <portal>.duckdb)")
	fs.StringVar(&table, "table", "", "Target table name (default: dataset id with - → _)")
	fs.StringVarP(&appToken, "token", "t", os.Getenv("SOCRATA_APP_TOKEN"), "Socrata app token (env: SOCRATA_APP_TOKEN)")
	fs.StringVar(&orderBy, "order-by", ":id", "Socrata $order expression (required for stable pagination)")
	fs.StringVar(&where, "where", "", "Optional SoQL $where clause")
	fs.IntVar(&batchSize, "batch", 5000, "Rows per page")
	fs.IntVar(&limit, "limit", 0, "Max rows to fetch (0 = all)")
	fs.BoolVar(&replace, "replace", true, "Drop and recreate the table before inserting")
	fs.BoolVarP(&verbose, "verbose", "v", false, "Verbose progress output")
	fs.BoolVar(&noLock, "no-lock", false, "Skip portal lock acquisition")
	fs.DurationVar(&lockWait, "lock-wait", 0,
		"Retry lock acquisition for up to this duration before giving up")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if portal == "" || dataset == "" {
		fs.PrintDefaults()
		return fmt.Errorf("--portal and --dataset are required")
	}

	if dbPath == "" {
		dbPath = filepath.Clean(portal) + ".duckdb"
	}
	if table == "" {
		table = strings.ReplaceAll(dataset, "-", "_")
	}

	lock, err := portallock.Acquire(dbPath, portallock.Options{NoLock: noLock, LockWait: lockWait})
	if err != nil {
		return err
	}
	defer lock.Release()

	client := &socrata.Client{
		AppToken:  appToken,
		BatchSize: batchSize,
	}

	if verbose {
		fmt.Fprintf(os.Stderr, "[csq] fetching metadata %s/%s\n", portal, dataset)
	}
	meta, err := client.FetchMetadata(portal, dataset)
	if err != nil {
		return fmt.Errorf("fetch metadata: %w", err)
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "[csq] %q — %d columns\n", meta.Name, len(meta.Columns))
	}

	schema := duckdb.BuildSchema(table, meta.Columns)

	if verbose {
		fmt.Fprintf(os.Stderr, "[csq] opening duckdb %s\n", dbPath)
	}
	w, err := duckdb.Open(dbPath)
	if err != nil {
		return err
	}
	defer w.Close()

	if replace {
		if err := w.ReplaceTable(schema); err != nil {
			return err
		}
	} else {
		if err := w.EnsureTable(schema); err != nil {
			return err
		}
	}

	start := time.Now()
	total := 0
	err = client.StreamRows(portal, dataset, orderBy, where, limit, func(page []socrata.Row) error {
		if err := w.InsertRows(schema, page); err != nil {
			return err
		}
		total += len(page)
		if verbose {
			fmt.Fprintf(os.Stderr, "[csq] inserted %d rows (total %d, elapsed %s)\n",
				len(page), total, time.Since(start).Round(time.Millisecond))
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("stream rows: %w", err)
	}

	fmt.Printf("extracted %d rows into %s.%s in %s\n",
		total, dbPath, table, time.Since(start).Round(time.Millisecond))
	return nil
}
