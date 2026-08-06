// Copyright (c) 2026 Neomantra Corp

package modes

// researchMode is the odd one out, and deliberately so.
//
// The other modes answer a civic question. This one asks whether the data can
// bear the weight of an answer at all: what you actually hold, where it came
// from, how complete it is, and which columns will embarrass you if you trust
// them. It is the step that belongs before a finding, not after.
//
// The queries here are the ones worth running on any corpus, so the mode reads
// only the _csq bookkeeping schema and DuckDB's own catalog functions. Nothing
// is portal-specific, and nothing needs syncing first beyond whatever you were
// already going to analyse.
var researchMode = &Mode{
	Name:        "research",
	Title:       "Research — provenance, coverage, and data-quality profiling",
	Summary:     "Audit what you hold, where it came from, and which columns to distrust",
	MultiPortal: true,
	About: "The due-diligence pass that belongs before an analysis, not after " +
		"it. Records provenance for citation and reproduction — which datasets " +
		"were synced, when, from which run, and whether anything failed — then " +
		"profiles the corpus itself: schema inventory, candidate join keys, " +
		"date columns that need range-checking, and the coverage gaps between " +
		"what a portal publishes and what you actually pulled. Reads only the " +
		"_csq schema and DuckDB's catalog, so it works on any portal.",

	// No Datasets: this mode audits whatever you have already synced.

	Queries: []Query{
		{
			Name: "provenance",
			Desc: "The citation record: every successful sync, when it ran, how long it took, and the config hash that produced it.",
			SQL: `
SELECT portal,
       dataset_id,
       table_name,
       rows_written,
       started_at,
       finished_at,
       ROUND(duration_ms / 1000.0, 1) AS duration_seconds,
       config_hash
FROM {{SYNCRUNS}}
WHERE status = 'ok'
ORDER BY portal, started_at DESC`,
		},
		{
			Name: "failed-runs",
			Desc: "Syncs that did not succeed, with the error. A silent gap in a corpus is the fastest route to a wrong conclusion.",
			SQL: `
SELECT portal, dataset_id, table_name, status, started_at,
       COALESCE(NULLIF(TRIM(error), ''), '(no message recorded)') AS error
FROM {{SYNCRUNS}}
WHERE status <> 'ok'
ORDER BY started_at DESC
LIMIT 50`,
		},
		{
			Name: "coverage-gaps",
			Desc: "Datasets the portal publishes that you have never successfully synced. Your corpus is a sample; this is what it excludes.",
			SQL: `
WITH published AS (
  SELECT portal, id AS dataset_id, name,
         COALESCE(NULLIF(TRIM(category), ''), '(uncategorised)') AS category
  FROM {{CATALOG}}
),
synced AS (
  SELECT DISTINCT portal, dataset_id
  FROM {{SYNCRUNS}}
  WHERE status = 'ok'
)
SELECT p.portal, p.category, COUNT(*) AS never_synced
FROM published p
LEFT JOIN synced s ON s.portal = p.portal AND s.dataset_id = p.dataset_id
WHERE s.dataset_id IS NULL
GROUP BY p.portal, p.category
ORDER BY never_synced DESC`,
		},
		{
			Name: "schema-inventory",
			Desc: "Every column you hold, grouped by table — the map of the corpus.",
			SQL: `
SELECT database_name AS portal,
       table_name,
       COUNT(*)                                                        AS columns,
       COUNT(*) FILTER (WHERE data_type IN ('DATE', 'TIMESTAMP'))      AS date_columns,
       COUNT(*) FILTER (WHERE data_type IN ('DOUBLE', 'BIGINT', 'INTEGER', 'DECIMAL')) AS numeric_columns,
       COUNT(*) FILTER (WHERE data_type = 'VARCHAR')                   AS text_columns
FROM duckdb_columns()
WHERE database_name IN ({{ALIASES}})
  AND schema_name = 'main'
GROUP BY portal, table_name
ORDER BY portal, table_name`,
		},
		{
			Name: "join-candidates",
			Desc: "Column names shared by two or more tables — where the corpus can be joined. Shared name is a hint, not a guarantee of shared meaning.",
			SQL: `
SELECT column_name,
       COUNT(*)                                  AS appears_in_tables,
       COUNT(DISTINCT data_type)                 AS distinct_types,
       STRING_AGG(DISTINCT data_type, ', ')      AS types,
       STRING_AGG(database_name || '.' || table_name, ', ') AS tables
FROM duckdb_columns()
WHERE database_name IN ({{ALIASES}})
  AND schema_name = 'main'
GROUP BY column_name
HAVING COUNT(*) > 1
ORDER BY appears_in_tables DESC, column_name
LIMIT 40`,
		},
		{
			Name: "date-columns",
			Desc: "Every date and timestamp column you hold. Range-check each one before trusting a time series — civic data routinely carries impossible dates.",
			SQL: `
SELECT database_name AS portal, table_name, column_name, data_type
FROM duckdb_columns()
WHERE database_name IN ({{ALIASES}})
  AND schema_name = 'main'
  AND data_type IN ('DATE', 'TIMESTAMP', 'TIMESTAMP WITH TIME ZONE')
ORDER BY portal, table_name, column_name`,
		},
		{
			Name: "profile-sql",
			Desc: "Generates a per-table SUMMARIZE statement. Copy the sql column and run it — this emits SQL rather than guessing at numbers it cannot compute in one pass.",
			SQL: `
SELECT database_name AS portal,
       table_name,
       'SUMMARIZE SELECT * FROM ' || database_name || '.' || schema_name
         || '.' || table_name || ';' AS sql
FROM duckdb_columns()
WHERE database_name IN ({{ALIASES}})
  AND schema_name = 'main'
GROUP BY database_name, schema_name, table_name
ORDER BY portal, table_name`,
		},
		{
			Name: "date-range-sql",
			Desc: "Generates a MIN/MAX range check for every date column at once. Run the output to find impossible dates before they reach a chart.",
			SQL: `
SELECT 'SELECT ''' || database_name || '.' || table_name || '.' || column_name
         || ''' AS column, MIN(' || column_name || ') AS min_value, MAX('
         || column_name || ') AS max_value FROM ' || database_name || '.'
         || schema_name || '.' || table_name AS sql
FROM duckdb_columns()
WHERE database_name IN ({{ALIASES}})
  AND schema_name = 'main'
  AND data_type IN ('DATE', 'TIMESTAMP', 'TIMESTAMP WITH TIME ZONE')
ORDER BY database_name, table_name, column_name`,
		},
	},

	Caveats: []string{
		"A clean profile means the data is well-formed, not that it is true. " +
			"Completeness and plausibility are the floor for an analysis, never the " +
			"evidence for one.",
		"Your corpus is a sample of the portal, shaped by your config rather than by " +
			"the city. Read coverage-gaps before describing anything here as 'the " +
			"data', and state the selection in any write-up.",
		"Upstream freshness is not recorded. csq stores when it last synced, which is " +
			"not when the city last updated the data — a dataset can be frozen for " +
			"years and still show a sync from this morning. Check the portal's own " +
			"metadata before calling anything current.",
		"Shared column names are a hint about joins, not a contract. The same name " +
			"can carry different meaning, grain, or encoding across datasets, and " +
			"identifiers are frequently reused between unrelated systems.",
		"Date columns in civic data routinely contain impossible values — typos put " +
			"records centuries away, and nulls are often encoded as sentinel dates. " +
			"Always bound a time series explicitly rather than trusting MIN and MAX.",
		"Free-text categorical columns are rarely normalised upstream. The same " +
			"category can appear under several spellings and casings, which silently " +
			"splits a group and understates every count derived from it.",
		"The generator queries emit SQL for you to run; they do not execute it. " +
			"Read what they produce before running it, as you would any generated code.",
	},
}
