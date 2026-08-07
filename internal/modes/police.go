// Copyright (c) 2026 Neomantra Corp

package modes

// policeMode supports civilian oversight of a police department: what the
// public complains about, what the oversight bodies do with those complaints,
// and who is on each side of them.
//
// The direction matters. This mode reads accountability records about the
// department. It deliberately builds nothing that profiles members of the
// public, and where arrest data is bound it is present only as a denominator
// for complaint rates, never as a lookup.
//
// Defined in terms of concepts so any city publishing an oversight caseload can
// bind to it. Cities differ enormously here — some publish per-officer records
// with identifiers, some publish only aggregates — so concepts are declared
// narrowly and queries needing an unbound concept are skipped rather than
// approximated.
var policeMode = &Mode{
	Name:    "police",
	Title:   "Police monitoring — civilian oversight of the department",
	Summary: "Complaints against police, oversight findings, and how they resolve",
	About: "Civilian-side oversight. Reads the published accountability caseload " +
		"— complaints against officers, the categories they fall into, and how " +
		"they resolve — and asks what the public complains about, how often " +
		"complaints are sustained, and how outcomes vary. Arrest volume is used " +
		"only as a denominator for normalising complaint rates.",

	Concepts: []Concept{
		{
			Name:     "complaints",
			Purpose:  "One row per complaint case: category, finding, status, date.",
			Required: []string{"complaint_date", "current_category", "finding_code"},
			Optional: []string{"current_status", "beat", "police_shooting", "log_no"},
		},
		{
			Name:     "complaints_by_officer",
			Purpose:  "Complaint rows exploded per involved officer, with officer attributes.",
			Required: []string{"log_no"},
			Optional: []string{"race_of_involved_officer", "years_on_force_of_involved_officer"},
		},
		{
			Name:     "complaints_by_complainant",
			Purpose:  "Complaint rows exploded per complainant, for who comes forward.",
			Required: []string{"log_no"},
			Optional: []string{"race_of_complainant", "sex_of_complainant"},
		},
		{
			Name:     "arrests",
			Purpose:  "Denominator only — complaint counts mean little without enforcement volume.",
			Required: []string{},
			Optional: []string{"arrest_date"},
		},
	},

	Queries: []Query{
		{
			Name: "complaint-volume",
			Desc: "Complaints per year.",
			SQL: `
SELECT date_part('year', complaint_date) AS year,
       COUNT(*)                          AS complaints
FROM {{c:complaints}}
WHERE complaint_date IS NOT NULL
  AND complaint_date >= DATE '1990-01-01' AND complaint_date < DATE '2035-01-01'
GROUP BY year
ORDER BY year DESC`,
		},
		{
			Name: "finding-outcomes",
			Desc: "Distribution of finding codes. Check your portal's code list before reading these as sustained/unfounded.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(finding_code), ''), '(none recorded)') AS finding_code,
       COUNT(*)                                                    AS cases,
       ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2)          AS pct
FROM {{c:complaints}}
GROUP BY finding_code
ORDER BY cases DESC`,
		},
		{
			Name: "category-outcomes",
			Desc: "Complaint categories by volume, with the share carrying a recorded finding.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(current_category), ''), '(uncategorised)') AS category,
       COUNT(*)                                                        AS cases,
       SUM(CASE WHEN TRIM(COALESCE(finding_code, '')) <> '' THEN 1 ELSE 0 END) AS with_finding,
       ROUND(100.0 * SUM(CASE WHEN TRIM(COALESCE(finding_code, '')) <> '' THEN 1 ELSE 0 END)
             / COUNT(*), 1)                                            AS pct_with_finding
FROM {{c:complaints}}
GROUP BY category
HAVING COUNT(*) >= 25
ORDER BY cases DESC
LIMIT 30`,
		},
		{
			Name: "complainant-demographics",
			Desc: "Who files complaints, by recorded race and sex.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(race_of_complainant), ''), '(not recorded)') AS race,
       COALESCE(NULLIF(TRIM(sex_of_complainant), ''), '(not recorded)')  AS sex,
       COUNT(*)                                                          AS complainants,
       ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2)                AS pct
FROM {{c:complaints_by_complainant}}
GROUP BY race, sex
ORDER BY complainants DESC
LIMIT 40`,
		},
		{
			Name: "officer-tenure",
			Desc: "Complaints by the involved officer's years on the force — where in a career complaints cluster.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(years_on_force_of_involved_officer), ''), '(not recorded)') AS years_on_force,
       COUNT(*)                                           AS complaint_rows,
       COUNT(DISTINCT log_no)                             AS distinct_cases,
       ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) AS pct
FROM {{c:complaints_by_officer}}
GROUP BY years_on_force
ORDER BY complaint_rows DESC
LIMIT 30`,
		},
		{
			Name: "shootings",
			Desc: "Cases flagged as police shootings, by year and status.",
			SQL: `
SELECT date_part('year', complaint_date) AS year,
       COALESCE(NULLIF(TRIM(current_status), ''), '(not recorded)') AS current_status,
       COUNT(*) AS cases
FROM {{c:complaints}}
WHERE TRIM(COALESCE(police_shooting, '')) NOT IN ('', 'No', 'NO', 'no')
  AND complaint_date IS NOT NULL
  AND complaint_date >= DATE '1990-01-01' AND complaint_date < DATE '2035-01-01'
GROUP BY year, current_status
ORDER BY year DESC, cases DESC`,
		},
		{
			Name: "beat-hotspots",
			Desc: "Areas generating the most complaints. Pair with arrests before ranking — volume tracks enforcement activity.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(beat), ''), '(not recorded)') AS beat,
       COUNT(*)                                           AS complaints
FROM {{c:complaints}}
GROUP BY beat
ORDER BY complaints DESC
LIMIT 30`,
		},
	},

	Caveats: []string{
		"A complaint is an allegation. It records that someone came forward, not that " +
			"anything was substantiated.",
		"An unsustained or unfounded finding does not mean the complaint was false. It " +
			"means the evidence available did not meet the investigator's standard of " +
			"proof, which is a different claim.",
		"Finding codes and category labels are portal-specific, change over time, and " +
			"are rarely backfilled. Check the current code list before mapping a code to " +
			"'sustained', and be wary of trends crossing a schema change.",
		"Complaint counts are not misconduct rates. An area with more complaints may " +
			"have more enforcement activity, more residents willing to file, or both — " +
			"normalise against arrests before ranking anything.",
		"Complaint volume is not comparable between cities. It reflects how easy a city " +
			"makes filing, how much it publishes, and which bodies it routes cases " +
			"through — never rank departments on these counts.",
		"Where a portal de-identifies officers, repeat-subject analysis is impossible " +
			"by construction. Check the binding's notes before assuming otherwise.",
	},
}
