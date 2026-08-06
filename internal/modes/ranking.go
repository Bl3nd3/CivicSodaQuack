// Copyright (c) 2026 Neomantra Corp

package modes

// rankingMode compares the portals you have attached against each other.
//
// It ranks open-data transparency — breadth, subject coverage, freshness — and
// not livability, safety, or governance quality. That restraint is deliberate.
// Ranking cities on outcomes would need population denominators and a mapping
// between incompatible per-city schemas, neither of which csq has; a tool that
// produced such a number anyway would be inventing it. What every csq database
// does share is the _csq bookkeeping schema, so this mode reads only that and
// works against any portal without per-city special-casing.
var rankingMode = &Mode{
	Name:        "ranking",
	Title:       "City ranking — comparing portals on open-data transparency",
	Summary:     "Rank attached portals by breadth, subject coverage, and freshness",
	MultiPortal: true,
	About: "Compares every attached portal on what it publishes and how current " +
		"it is: dataset counts, subject-area coverage, how much you have actually " +
		"synced locally, and how recently. Reads only the _csq schema that every " +
		"csq database carries, so any portal can be added without new code. Pass " +
		"several --db flags to rank them side by side. This measures data " +
		"transparency, not how good a city is to live in — see the caveats.",

	// No Datasets: this mode reads the _csq bookkeeping schema of databases you
	// have already built, rather than syncing anything of its own.

	Queries: []Query{
		{
			Name: "breadth",
			Desc: "Datasets published per portal, and how many carry a description.",
			SQL: `
SELECT portal,
       COUNT(*)                                            AS datasets_published,
       COUNT(DISTINCT category)                            AS distinct_categories,
       SUM(CASE WHEN COALESCE(TRIM(description), '') <> '' THEN 1 ELSE 0 END)
                                                           AS with_description,
       ROUND(100.0 * SUM(CASE WHEN COALESCE(TRIM(description), '') <> '' THEN 1 ELSE 0 END)
             / COUNT(*), 1)                                AS pct_documented
FROM {{CATALOG}}
GROUP BY portal
ORDER BY datasets_published DESC`,
		},
		{
			Name: "category-coverage",
			Desc: "Subject-area coverage per portal. Blanks are real gaps in what a city publishes.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(category), ''), '(uncategorised)') AS category,
       portal,
       COUNT(*)                                                AS datasets
FROM {{CATALOG}}
GROUP BY category, portal
ORDER BY category, datasets DESC`,
		},
		{
			Name: "sync-coverage",
			Desc: "How much of each portal you hold locally — synced datasets against published ones.",
			SQL: `
WITH published AS (
  SELECT portal, COUNT(*) AS datasets_published
  FROM {{CATALOG}}
  GROUP BY portal
),
synced AS (
  SELECT portal,
         COUNT(DISTINCT dataset_id)                                     AS datasets_synced,
         SUM(CASE WHEN status = 'ok' THEN rows_written ELSE 0 END)      AS rows_held
  FROM {{SYNCRUNS}}
  WHERE status = 'ok'
  GROUP BY portal
)
SELECT p.portal,
       p.datasets_published,
       COALESCE(s.datasets_synced, 0)                                   AS datasets_synced,
       ROUND(100.0 * COALESCE(s.datasets_synced, 0)
             / NULLIF(p.datasets_published, 0), 2)                      AS pct_synced,
       COALESCE(s.rows_held, 0)                                         AS rows_held
FROM published p
LEFT JOIN synced s USING (portal)
ORDER BY rows_held DESC`,
		},
		{
			Name: "freshness",
			Desc: "How recently each portal was synced, and how long the last run took.",
			SQL: `
SELECT portal,
       COUNT(*)                                    AS sync_runs,
       SUM(CASE WHEN status = 'ok' THEN 1 ELSE 0 END)     AS ok_runs,
       SUM(CASE WHEN status <> 'ok' THEN 1 ELSE 0 END)    AS failed_runs,
       MAX(started_at)                             AS last_sync_started,
       ROUND(MAX(duration_ms) / 1000.0, 1)         AS slowest_run_seconds
FROM {{SYNCRUNS}}
GROUP BY portal
ORDER BY last_sync_started DESC NULLS LAST`,
		},
		{
			Name: "largest-datasets",
			Desc: "The biggest tables you hold, across every attached portal.",
			SQL: `
SELECT portal, dataset_id, table_name, rows_written, started_at
FROM {{SYNCRUNS}}
WHERE status = 'ok' AND rows_written > 0
ORDER BY rows_written DESC
LIMIT 30`,
		},
		{
			Name: "shared-categories",
			Desc: "Categories present on every attached portal — the only fair basis for a like-for-like comparison.",
			SQL: `
WITH portals AS (SELECT COUNT(DISTINCT portal) AS n FROM {{CATALOG}}),
cats AS (
  SELECT COALESCE(NULLIF(TRIM(category), ''), '(uncategorised)') AS category,
         COUNT(DISTINCT portal) AS portals_with,
         COUNT(*)               AS total_datasets
  FROM {{CATALOG}}
  GROUP BY category
)
SELECT c.category, c.portals_with, c.total_datasets
FROM cats c, portals p
WHERE c.portals_with = p.n
ORDER BY c.total_datasets DESC`,
		},
	},

	Caveats: []string{
		"This ranks open-data transparency, not quality of governance or life. A city " +
			"that publishes more is easier to scrutinise; that is the only claim these " +
			"numbers support.",
		"Dataset counts reward volume, not usefulness. A portal can inflate its total " +
			"with dozens of near-duplicate yearly extracts and deprecated boundary " +
			"files — both are common in practice.",
		"Categories are each portal's own labels, not a shared taxonomy. Chicago's " +
			"'Public Safety' and another city's nearest equivalent may not cover the " +
			"same ground, so cross-portal category comparisons are indicative at best.",
		"Sync coverage measures what you chose to pull, not what the portal offers. A " +
			"low percentage usually reflects your config, not the city.",
		"Comparisons are only as fair as the portals attached. Ranking two cities on " +
			"categories only one of them uses will produce a confident, meaningless " +
			"ordering — start from the shared-categories query.",
	},
}
