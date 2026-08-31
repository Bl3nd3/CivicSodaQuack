// Copyright (c) 2026 Neomantra Corp

package modes

// personalMode is the mode a user writes for themselves, and the only built-in
// that expects to be replaced.
//
// The other four modes ship with their questions already chosen. This one ships
// empty, because the question it answers is whichever one the user has. Running
// `csq modes ask "<question>"` matches that question against csq's analysis
// patterns and the columns of the tables the user holds, builds the concepts,
// SQL, and caveats from a reviewed template, saves the result as JSON in the
// modes directory, and from then on that file *is* this mode — the loader
// replaces a built-in with an external mode of the same name, so nothing
// downstream can tell the difference.
//
// That replacement is the whole design. A generated mode is not a special kind
// of object with its own runner and its own trust rules; it is an ordinary mode
// file, held to the same validation, expanded through the same bindings, scored
// by the same confidence arithmetic, and editable in a text editor when a
// column was matched wrongly.
//
// Until then, the queries below are the empty state, and they are the ones
// worth running before writing any mode: what is actually here, how big it is,
// and how stale. Deliberately concept-free — a mode declaring concepts with no
// binding cannot run, and shipping one would mean shipping something broken.
var personalMode = &Mode{
	Name:        "personal",
	Title:       "Personal — your own mode, built from your own question",
	Summary:     "Ask a question in English; csq writes the mode and keeps it as JSON you own",
	MultiPortal: true,
	About: "The mode you write for yourself. Run `csq modes ask \"<question>\" " +
		"--db <file>` and csq matches your question against its analysis patterns " +
		"and the columns of the tables you hold, shows you which pattern and which " +
		"columns it picked and why, and builds the mode once you confirm. No API " +
		"key, no network, no model: the SQL comes from a reviewed template and the " +
		"caveats come with it. What lands is a JSON file in your modes directory — " +
		"read it, edit it, delete a query you dislike, and ask again to add more. " +
		"`csq modes patterns` lists the shapes available and `csq modes add` drives " +
		"one directly when you already know what you want. Until you have asked " +
		"something, this mode reports what you hold and how stale it is — the " +
		"inventory worth reading before choosing a question.",

	Queries: []Query{
		{
			Name: "what-i-hold",
			Desc: "Every dataset synced locally, largest first: the raw material a personal mode can be written against.",
			SQL: `
SELECT portal,
       id                AS dataset_id,
       name              AS dataset_name,
       COALESCE(category, '(uncategorised)') AS category,
       updated_at,
       fetched_at
FROM {{CATALOG}}
ORDER BY portal, fetched_at DESC
LIMIT 100`,
		},
		{
			Name: "how-stale",
			Desc: "Days since each dataset was last fetched. A recent portal timestamp does not mean recent data, so this measures your copy, not theirs.",
			SQL: `
SELECT portal,
       id   AS dataset_id,
       name AS dataset_name,
       fetched_at,
       DATE_DIFF('day', fetched_at, now())  AS days_since_fetched,
       updated_at,
       DATE_DIFF('day', updated_at, now())  AS days_since_portal_update
FROM {{CATALOG}}
WHERE fetched_at IS NOT NULL
ORDER BY days_since_fetched DESC
LIMIT 50`,
		},
		{
			Name: "sync-health",
			Desc: "Whether the last sync of each table succeeded. A mode written over a half-synced table answers with a gap in it.",
			SQL: `
WITH latest AS (
  SELECT portal, dataset_id, table_name, status, rows_written, started_at, error,
         ROW_NUMBER() OVER (PARTITION BY portal, dataset_id ORDER BY started_at DESC) AS rn
  FROM {{SYNCRUNS}}
)
SELECT portal, dataset_id, table_name, status,
       rows_written, started_at,
       COALESCE(NULLIF(TRIM(error), ''), '') AS error
FROM latest
WHERE rn = 1
ORDER BY (status <> 'ok') DESC, started_at DESC
LIMIT 100`,
		},
	},

	Caveats: []string{
		"This is the empty state of the personal mode. It reports what you have synced, " +
			"not anything about a city — run `csq modes ask \"<question>\" --db <file>` " +
			"to replace it with queries that answer something.",
		"Once built, the SQL in this mode comes from a reviewed pattern pointed at " +
			"columns matched to your question. csq checks that it is read-only, that it " +
			"validates, and that DuckDB can plan it — none of which is a check that the " +
			"right columns were matched. Read the SQL under `csq modes show personal` " +
			"before quoting a result.",
		"A drafted mode is a file you own, at the path `csq modes where` reports. Editing " +
			"it is expected: correct a column mapping, drop a query, sharpen a caveat. " +
			"Asking another question adds to that file and never rewrites what is in it.",
		"A recent `updated_at` does not mean recent data. It carries the portal's own " +
			"`data_updated_at`, which moves when a portal republishes unchanged rows — so a " +
			"dataset abandoned years ago can still report a timestamp from this week.",
	},
}
