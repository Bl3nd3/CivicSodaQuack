# CivicSodaQuack

Turn any Socrata Open Data API portal into a fast, local, queryable DuckDB + MCP
surface for AI agents. See [AGENTS.md](./AGENTS.md) for the full project brief.

## Status

**Phase 4** — snapshot publishing. After syncing one or more portals into per-portal DuckDB files, run `csq snapshot` to package one as a `.tar.zst` for distribution; consume with `csq fetch --from <url>`.

Prefer not to use a terminal? `csq web --db <portal.duckdb> --open` serves the same analyses as a local web page.

## Quickstart

```bash
go build -o csq ./cmd/csq

# Discover what's on a portal
./csq catalog --portal data.cityofchicago.org --category "Public Safety"

# Generate a starter YAML
./csq catalog --portal data.cityofchicago.org --category "Public Safety" \
  --output data.cityofchicago.org.yaml

# Sync the datasets enumerated in the YAML
./csq sync --config data.cityofchicago.org.yaml --dry-run    # preview
./csq sync --config data.cityofchicago.org.yaml              # execute
```

Set `SOCRATA_APP_TOKEN` (referenced in the YAML as `${SOCRATA_APP_TOKEN}`) to lift anonymous rate limits.

### Use it in a browser

Not everyone wants a terminal. `csq web` serves the same analyses as a local web
page, from the same binary — no install step, no toolchain, no files next to the
executable.

```bash
./csq web --db data.cityofchicago.org.duckdb --open
```

Three views: **Analyses** (the modes, with a readiness dot so an unsynced
analysis is never mistaken for one that found nothing), **Explore data** (every
dataset the portal publishes, and whether you hold a copy), and **Data health**
(the `research` mode in plain language — what failed, what is missing, which
columns carry impossible dates).

Two properties are structural rather than incidental:

- **The browser cannot send SQL.** Every endpoint takes a mode name and a query
  name, and runs the SQL the mode declares. There is no ad-hoc query box, so
  there is no injection surface, and what runs in the page is exactly what
  `csq modes run` runs — caveats included.
- **Caveats and exclusions ship with the rows.** They are fields on the same
  response, and the page has no code path that renders a table without them. A
  city excluded from a comparison is named above the numbers, never in a
  footnote below them.

Pair `--config` with `--db` to let the page download data too:

```bash
./csq web --db data.cityofchicago.org.duckdb \
          --config data.cityofchicago.org.yaml --open
```

An analysis with no data then offers a **Download this data** button that syncs
just the datasets that analysis needs, with live per-dataset progress. Downloads
take the same advisory lock `csq sync` takes, and one runs at a time. Without
`--config` the page is read-only and prints the command instead. If the database
file does not exist yet, `csq web --db new.duckdb --config portal.yaml` creates
it — starting from nothing is the case the browser UI exists to serve.

Every database is opened `READ_ONLY` for reading, so this composes with a
running `csq mcp` or `csq sync` on the same file.

#### Share the results

Any mode renders to a standalone HTML report — inline SVG charts, inline CSS,
no scripts and no external requests — so it can be emailed, dropped in a shared
folder, or opened on a machine with no network:

```
http://127.0.0.1:8080/report/corruption.html?download=1
```

The report leads with the mode's caveats and prints the full table under every
chart. Charts are drawn only where the result shape supports one honestly: a
single label column and a measure. Results with several numeric columns of
different units stay tables, because the chart that would fit them is a
dual-axis chart.

The default listen address is loopback. csq has no login, so `--addr` on a wider
interface publishes every synced dataset to anyone who can reach the port; the
server warns when you do it.

### Modes

Modes are curated analysis profiles: for a given civic question, the datasets worth syncing, the SQL that turns them into an answer, and the caveats that keep the answer honest. Running `csq` with no arguments lists them.

```bash
./csq modes                    # list
./csq modes show corruption    # datasets, queries, and caveats
```

| Mode | Scope | What it covers |
| --- | --- | --- |
| `corruption` | single portal | Contract concentration by vendor and department, procurement-type mix, lobbyist compensation, contribution recipients, and the overlap between the two |
| `ranking` | cross-portal | Comparison of portal breadth, category coverage, how much you hold locally, and freshness |
| `police` | single portal | Civilian oversight — COPA/BIA complaint volume, finding and category outcomes, complainant demographics, officer tenure, shootings, beat distribution |
| `research` | cross-portal | Provenance for citation, failed-run and coverage gaps, schema inventory, candidate join keys, and generated data-quality checks |

For a mode with datasets, generate a config and sync it, then run the queries:

```bash
./csq modes init police --output police.yaml
./csq sync --config police.yaml
./csq modes run police --db data.cityofchicago.org.duckdb
./csq modes run police --db data.cityofchicago.org.duckdb --query finding-outcomes
```

`ranking` and `research` have no datasets of their own — they read the `_csq` bookkeeping schema that every csq database carries, so point them at ones you already built:

```bash
./csq modes run ranking  --db chicago.duckdb --db cookcounty.duckdb
./csq modes run research --db chicago.duckdb --db cookcounty.duckdb
```

`research` is the due-diligence pass that belongs *before* an analysis. It records provenance for citation (which datasets, when, which config hash), surfaces failed runs and the gap between what a portal publishes and what you pulled, and profiles the corpus — schema inventory, candidate join keys, and every date column that needs range-checking. Two of its queries emit SQL rather than guessing at numbers they cannot compute in a single pass:

```bash
./csq modes run research --db cookcounty.duckdb --query date-range-sql --quiet
```

Running the statements that produces against the Cook County corpus reports a `sentence_date` reaching the year **2921** and an `arrest_date` starting in **1915** — the kind of upstream defect that silently ruins a time series. Read generated SQL before running it, as you would any generated code.

`modes run` attaches every portal `READ_ONLY`, so it composes with a running `csq mcp` or `csq sync` holding the same file instead of contending for the write lock.

Each mode carries interpretation caveats, printed by `modes show`, printed above `modes run` output, and embedded as comments in the YAML that `modes init` writes. They are a structural requirement — a test fails the build if a mode declares none. Contract concentration is frequently legitimate (specialised work may have one qualified bidder), and an unsustained complaint is not a false one; a tool reporting on procurement and policing should say so where the numbers are. Three scope limits worth stating up front:

- **`ranking` compares open-data transparency, not livability or governance.** Outcome-based city ranking would need population denominators and a mapping between incompatible per-city schemas, neither of which csq has.
- **`police` is civilian-side oversight only**, reading accountability records about the department. Chicago's published COPA and BIA extracts carry no officer identifier, so repeat-officer analysis is not possible with them; arrest volume is included solely as a denominator for complaint rates.
- **A recent timestamp does not mean recent data.** `research --query provenance` reports when *csq* last pulled a dataset, which says nothing about the city. `_csq.catalog.updated_at` is better — it carries the portal's `data_updated_at`, ignoring metadata edits so that rewriting a description no longer makes a stale dataset look current — but it still moves when a portal republishes unchanged rows. The five Cook County State's Attorney datasets in `datacatalog.cookcountyil.gov.yaml` are the live example: abandoned by the SAO on 2024-12-30 with coverage ending 2024-11-30, yet `updated_at` reads 2026-04-02. Read the dataset's own description before describing anything as current; no timestamp on either side is sufficient.

Adding a mode means appending to the registry in `internal/modes/`, not touching the CLI.

### Serve via MCP

```bash
# Stdio (default; for local agent integrations)
./csq mcp --db data.cityofchicago.org.duckdb

# Multi-portal with explicit alias
./csq mcp --db chicago=data.cityofchicago.org.duckdb \
          --db nyc=data.cityofnewyork.us.duckdb

# HTTP (for remote agents; bind to loopback by default)
./csq mcp --db data.cityofchicago.org.duckdb --http 127.0.0.1:8080
```

The MCP server exposes four read tools: `list_datasets`, `describe_dataset`, `search_datasets`, and `query_sql`. The `query_sql` tool runs read-only DuckDB SQL across every attached portal; cross-portal queries use `<alias>.<schema>.<table>`, e.g. `SELECT * FROM chicago._csq.catalog UNION ALL SELECT * FROM nyc._csq.catalog`. Results are capped at 1000 rows / 1MB / 30s.

Pair `--db` with `--config` (positionally) to enable two write tools:

```bash
./csq mcp --db data.cityofchicago.org.duckdb \
          --config data.cityofchicago.org.yaml
```

- `sync_dataset(portal, dataset_id, full_refresh?)` — runs the same `sync.Run` that `csq sync` uses, for one dataset. Blocks until done.
- `refresh_catalog(portal?)` — refetches `/api/catalog/v1` and upserts `_csq.catalog`. Per-portal failures don't abort the batch.

Without `--config` for a portal, only the read tools are exposed. The MCP server's portal lock (Phase 5) covers the write tools — a separate `csq sync` against the same DB will still see the lock.

The build version (used in `Implementation.Version` and Phase 4 snapshot manifests) is injected at build time from `git describe --tags --always --dirty`. Plain `go build` falls back to the package default `0.6.0-dev`.

### Distribute via snapshot

Package an existing synced DuckDB into a portable tarball:

```bash
./csq snapshot --db data.cityofchicago.org.duckdb \
               --output chicago-2026-04-23.tar.zst
```

The tarball contains a `manifest.json` (portal, snapshot id, dataset/row counts, SHA-256 of the DuckDB) and the DuckDB file itself, all zstd-compressed.

Upload the tarball anywhere your agents can reach (S3, GitHub Releases, an internal CDN, a local file). To restore on another host:

```bash
./csq fetch --from https://example.com/snapshots/chicago-2026-04-23.tar.zst
# or
./csq fetch --from file:///path/to/chicago-2026-04-23.tar.zst
```

`csq fetch` verifies the SHA-256 against the manifest before declaring success. Pass `--no-verify` to skip (not recommended).

A publisher who maintains a per-portal `index.json` lets consumers fetch the latest snapshot without knowing the ID:

```bash
# Latest in the index
./csq fetch --index https://snapshots.example.com/chicago/index.json
# Pinned by snapshot_id
./csq fetch --index https://snapshots.example.com/chicago/index.json --snapshot 01HZ...
```

The publisher updates the index after each snapshot:

```bash
./csq snapshot-index update \
  --index snapshots/chicago/index.json \
  --add chicago-2026-04-28.tar.zst \
  --url https://snapshots.example.com/chicago/chicago-2026-04-28.tar.zst \
  --max-keep 30
```

A reusable GitHub Actions workflow (`.github/workflows/snapshot.yml`) is provided for downstream repos to run nightly. See [docs/snapshot-publishing.md](./docs/snapshot-publishing.md).

### Full-refresh and locking

Force one or more datasets to re-bootstrap on the next sync without editing YAML:

```bash
./csq sync --config data.cityofchicago.org.yaml --full-refresh 6zsd-86xi
./csq sync --config data.cityofchicago.org.yaml --full-refresh-all
```

All subcommands that open a per-portal DuckDB acquire `<dbpath>.lock` (advisory `flock`). If another `csq` process is holding the lock, the second errors with a message naming the lock file. Pass `--no-lock` to bypass or `--lock-wait 30s` to retry briefly. `csq fetch` does not lock (it writes a fresh file).

For very long catch-up runs on large datasets, opt into mid-stream HWM persistence in YAML:

```yaml
overrides:
  6zsd-86xi:
    checkpoint_every_n_pages: 100   # 0 = disabled (Phase 2 default)
```

A failure on page 1500 of a 2000-page catch-up then resumes from the most recent checkpoint instead of from the original HWM.

### Config shape

```yaml
portal: data.cityofchicago.org
app_token: ${SOCRATA_APP_TOKEN}
concurrency: 4
on_error: continue

defaults:
  batch_size: 5000
  order_by: ":id"

include:
  - category: "Public Safety"
  - tag: "311*"
exclude:
  - id: 85ca-t3if     # giant, skip

overrides:
  6zsd-86xi:
    table: crimes
    where: "date >= '2015-01-01'"
    batch_size: 10000
    columns:
      skip: [location_description_raw]
    # Phase 2 fields (both optional):
    mode: full_replace        # force full-replace on every run; default is incremental
    hwm_column: ":updated_at" # override the high-water-mark column
```

Catalog and per-dataset sync history live in the `_csq` schema inside the portal's DuckDB:

```sql
SELECT id, name, category FROM _csq.catalog LIMIT 10;
SELECT dataset_id, status, rows_written, duration_ms
  FROM _csq.sync_runs ORDER BY started_at DESC LIMIT 10;

-- Per-dataset incremental-sync state (Phase 2)
SELECT dataset_id, hwm_updated_at, last_full_replace_at, last_run_id
  FROM _csq.dataset_state ORDER BY hwm_updated_at DESC;
```

## Layout

```
cmd/csq/              # CLI entrypoint
internal/socrata/     # SODA2 client: metadata + paginated row streaming
internal/duckdb/      # DuckDB writer + Socrata→DuckDB schema mapping
internal/config/      # YAML loader + per-dataset effective config
internal/sync/        # Sync orchestrator + strategies (FullReplace, Incremental)
internal/mcpserver/   # MCP server: pools, ATTACH, tools, transports
internal/snapshot/    # Snapshot publishing: tar+zst format, Pack producer, Fetch consumer
internal/modes/       # Curated analysis profiles: datasets, queries, caveats
internal/analysis/    # Headless mode execution: planning, exclusions, readiness
internal/web/         # Browser UI: JSON API, embedded assets, HTML reports
```

## License

Released under the [MIT License](https://en.wikipedia.org/wiki/MIT_License), see [LICENSE.txt](./LICENSE.txt).

Copyright (c) 2026 [Neomantra Corp](https://www.neomantra.com).   

----
Made with :heart: and :fire: by the team behind [Nimble.Markets](https://nimble.markets).
