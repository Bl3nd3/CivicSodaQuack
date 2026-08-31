# CivicSodaQuack — notes for agents working on this repo

**csq** turns any Socrata Open Data portal into a local DuckDB file, then puts a
CLI, an MCP server, and a browser UI over it. Go, one binary, no service to run.

This file is orientation for someone editing the code. `README.md` is the user
documentation and is the more complete reference for behaviour; when the two
disagree, the README and the code win.

## Layout

```
cmd/csq/           every subcommand, one file each (sync.go, modes.go, web.go, ...)
internal/
  socrata/         SODA client: catalog, paginated fetch, app tokens, 429 backoff
  sync/            incremental materialisation into DuckDB, high-water marks
  config/          per-portal YAML (dataset list, overrides, cleaning rules)
  duckdb/          connection helpers and the `_csq` bookkeeping schema
  portallock/      advisory <dbpath>.lock (flock) shared by every subcommand
  modes/           curated analysis profiles: concepts, bindings, queries, caveats
  patterns/        builds a mode from reviewed SQL shapes — no model, no network
  personal/        schema inventory, read-only SQL guard, mode documents, save/verify
  analysis/        runs mode queries headlessly, returns structured results
  confidence/      scores how far the data behind an answer can be trusted
  web/             browser UI, CSV/JSON export, standalone HTML reports
  mcpserver/       MCP tools over the attached portals
  snapshot/        publish and fetch prebuilt .duckdb tarballs
```

The CLI is the complete interface and stays that way. Anything the web UI or
MCP server can do is reachable from a subcommand.

## The two indirections worth understanding first

**Concepts and bindings** (`internal/modes/concepts.go`). A mode names the
tables it needs by *what they must contain*, not by dataset ID: "contracts with
a vendor and an award amount" is a `Concept`, and `rsxa-ify5` is one portal's
answer to it. A `Binding` maps a city's datasets and column names onto those
concepts, so queries are written once against canonical names and referenced in
SQL as `{{c:contracts}}`.

Two consequences that catch people out:

- A binding's `Columns` value may be **any SQL expression**, not just a column
  name — `CanonicalView` emits it verbatim as `<expr> AS <canonical>`. NYC
  publishes permit dates as `MM/DD/YYYY` text, and the binding maps them through
  `try_strptime` so no query has to know that.
- When `Columns` is non-empty it is **authoritative**. A concept column missing
  from it is unavailable on that portal, and a query needing it excludes the
  city with a stated reason. Treating a missing column as merely unmapped would
  let a NULL read as a real value, which for a rate is indistinguishable from a
  good answer.

Adding a city means adding a binding. Adding a mode means appending to the
registry in `internal/modes/` (`var registry` in `modes.go`). Neither touches
the CLI.

**External modes are the same objects.** `loader.go` reads YAML *and* JSON from
`~/.csq/modes/` into exactly the `Mode` and `Binding` structs above, through one
validator with two decoders — both strict about unknown keys, because a silently
ignored `caveats` or `columns` produces a mode that runs and answers wrongly.
`schema.go` holds the JSON Schema for those documents, and it is deliberately
the only copy: `csq modes schema` prints it, and `csq modes ask` / `add` write
against it. A second hand-maintained copy is the obvious way for the two to
drift, at which point csq generates documents its own loader rejects and the
error lands on the user.

An external mode **replaces a built-in of the same name**. That is not a
convenience — it is the mechanism the `personal` mode is built on.

**Confidence** (`internal/confidence/`). Every mode query carries a score: the
share of the records the query meant to consult that were actually there and
usable. For each dataset it reads, three counts — `E` rows the portal holds,
`H` rows held locally, `U` rows where every column the query reads is usable:

```
completeness = min(1, H/E)     usability = U/H     r = completeness × usability     R = ∏ r
```

Every term is a count over a count. There are no weights, severity
coefficients, or saturation points anywhere in the arithmetic, and that is
deliberate — an earlier version had about twenty tuned constants, and six of its
eight checks turned out to be the same measurement (rows that do not survive).
**If you find yourself adding a constant to this package, that is the signal to
stop and re-derive.** The only permitted constants are presentational (the band
names) or definitional (DuckDB's minimum timestamp).

Freshness and sync status are *reported beside* R, never folded into it:
staleness removes no rows, so it has no reading as evidence loss. `U` is
measured with one joint SQL filter, not per-column rates multiplied — nulls in
civic data cluster hard, and assuming independence roughly halves the apparent
survival on real Chicago data.

**Patterns** (`internal/patterns/`) are how the `personal` mode is filled in.
A pattern is a reviewed SQL template written against canonical role names
(`entity`, `measure`, `event_date`, ...) that the generated binding maps onto
the portal's real columns — the same indirection every other mode uses. The
output goes through the same loader, guard, and `EXPLAIN` as a hand-written
mode, and `patterns_test.go` asserts that rather than trusting it.

csq makes no network call to build one. There is no model anywhere in this
path; `internal/llm` and `internal/cache` existed for an earlier drafted
version and were removed with it.

`ask.go` is the keyword router that puts an English question in front of the
patterns. It is not NL-to-SQL: it ranks six shapes and the columns of one
table, shows the word behind every choice, and refuses when it cannot choose.
The worst it can do is pick the wrong reviewed template — never invent one.
Watch the role-assignment order in `Suggest`: roles are filled greedily in
`Params` order with no column reused, and a scorer that lets `vendor_name` win
the *group* role produces a query that runs and answers a different question.

Three rules hold here:

- **A role is filled on evidence, never on type alone.** `csq modes add` takes
  the columns from the user outright. `ask` may propose one, but only when the
  column matches that role's own vocabulary — Chicago supplies `crimes.id`,
  `crimes.ward` and `requests_311.community_area`, all numeric and all
  meaningless to sum, and each was picked at some point before that rule.
- **Casts come from the declared type.** A `VARCHAR` measure is wrapped in
  `TRY_CAST` (one bad row becomes a NULL the confidence score counts, rather
  than a failed query); a text date is *refused* without `--date-format`,
  because MM/DD versus DD/MM mislabels a third of the year without erroring.
- **A pattern's caveats are its reason to exist.** The limits that matter most
  are properties of the shape, not the data — a top-N always hides its tail, a
  free-text entity column always understates concentration. Written once and
  reviewed, they beat regenerated ones. `patterns_test.go` fails a pattern
  that ships without them.

## Conventions

- **Interpretation caveats are structural.** Every mode must declare
  `Caveats`; `modes_test.go` fails the build otherwise. Contract concentration
  is often legitimate and an unsustained complaint is not a false one — a tool
  reporting on procurement and policing says so where the numbers are.
- **A score never travels alone.** Renderers emit the score, the counts that
  produced it, and the limits together. There is deliberately no code path that
  emits a bare percentage.
- **"Could not measure" and "measured zero" are different** and must stay
  different everywhere. An unassessed report renders as "not assessed", never
  as 0%.
- Errors say what to do next. Assessment failures never retract an answer the
  user already has.

## Working on it

```
task build        # or: go build ./cmd/csq
task test         # go test ./...
task vet
gofmt -l .
```

CI is `.github/workflows/ci.yml` with `.golangci.yml`. Run `go test ./...` and
`gofmt -l .` before pushing; `go test -race ./internal/analysis/` is worth it if
you touch the session cache, which is shared across concurrent HTTP handlers.

## Gotchas

- **DuckDB's file lock is cross-process and mutually exclusive.** A `READ_ONLY`
  attach does *not* let you read a database another process is syncing, and a
  sync cannot open a file the web UI is holding. Read-only attach buys
  coexistence with other *readers* only. Within one process both are fine,
  which is why `csq web`'s own download works without detaching.
- **`_csq.catalog.row_count` is effectively always NULL.** Use
  `BoundDataset.Rows` as the reference count.
- **A `READ_ONLY` attach is a snapshot** — DuckDB binds the catalog at attach
  time, so data written afterwards is invisible until you `DETACH` and
  re-attach (`analysis.Session.Refresh`).
- **A recent timestamp does not mean recent data.** `updated_at` carries the
  portal's `data_updated_at`, which still moves when a portal republishes
  unchanged rows. The Cook County State's Attorney datasets are the live case:
  abandoned in 2024, still reporting a 2026 `updated_at`.
- Advisory locking is per **dbpath**, not per portal.
