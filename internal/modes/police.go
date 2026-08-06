// Copyright (c) 2026 Neomantra Corp

package modes

// policeMode supports civilian oversight of a police department: what the
// public complains about, what the oversight bodies do with those complaints,
// and who is on each side of them.
//
// The direction matters. This mode reads accountability records about the
// department — complaints, findings, oversight caseloads. It deliberately
// builds nothing that profiles members of the public, and the arrest data it
// includes is present as a denominator for complaint rates, not as a lookup.
var policeMode = &Mode{
	Name:    "police",
	Title:   "Police monitoring — civilian oversight of the department",
	Summary: "Complaints against police, oversight findings, and how they resolve",
	Portal:  "data.cityofchicago.org",
	About: "Civilian-side oversight. Chicago publishes its police accountability " +
		"caseload through COPA (the Civilian Office of Police Accountability) and " +
		"BIA (the department's internal Bureau of Internal Affairs). This mode " +
		"syncs those case records and asks what the public complains about, how " +
		"often complaints are sustained, how long they take, and how outcomes " +
		"vary by category and demographic. Arrest volume is included only as a " +
		"denominator for normalising complaint rates.",

	Datasets: []Dataset{
		{ID: "mft5-nfa8", Table: "copa_cases", Name: "COPA Cases - Summary", Rows: 189405,
			Why: "One row per case: category, finding, status, beat, both parties' demographics."},
		{ID: "ufxy-tgry", Table: "copa_by_officer", Name: "COPA Cases - By Involved Officer", Rows: 214782,
			Why: "Case rows exploded per involved officer, with officer demographics and tenure."},
		{ID: "vnz2-rmie", Table: "copa_by_complainant", Name: "COPA Cases - By Complainant or Subject", Rows: 204678,
			Why: "Case rows exploded per complainant, with complainant demographics."},
		{ID: "t7km-zpxd", Table: "bia_by_officer", Name: "BIA Cases - By Involved Officer", Rows: 185012,
			Why: "The department's internal-affairs caseload, for comparison against COPA's."},
		{ID: "9pd8-s9t4", Table: "police_board_foia", Name: "FOIA Request Log - Chicago Police Board", Rows: 473,
			Why: "What the public is formally asking the Police Board for."},
		{ID: "28me-84fj", Table: "police_sentiment", Name: "Police Sentiment Scores", Rows: 7557,
			Why: "Survey-based trust and safety scores by district, as community context."},
		{ID: "dpt3-jri9", Table: "arrests", Name: "Arrests", Rows: 741043,
			Why: "Denominator only — complaint counts mean little without enforcement volume."},
	},

	Queries: []Query{
		{
			Name: "complaint-volume",
			Desc: "COPA complaints per year, with the share still open.",
			SQL: `
SELECT date_part('year', complaint_date)                         AS year,
       COUNT(*)                                                  AS complaints,
       COUNT(DISTINCT log_no)                                    AS distinct_cases,
       SUM(CASE WHEN current_status ILIKE '%open%' THEN 1 ELSE 0 END) AS still_open
FROM {{P}}.main.copa_cases
WHERE complaint_date IS NOT NULL
  AND complaint_date >= DATE '2000-01-01'
  AND complaint_date < DATE '2030-01-01'
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
FROM {{P}}.main.copa_cases
GROUP BY finding_code
ORDER BY cases DESC`,
		},
		{
			Name: "category-outcomes",
			Desc: "Complaint categories ranked by volume, with the share carrying a recorded finding.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(current_category), ''), '(uncategorised)') AS category,
       COUNT(*)                                                        AS cases,
       SUM(CASE WHEN TRIM(COALESCE(finding_code, '')) <> '' THEN 1 ELSE 0 END) AS with_finding,
       ROUND(100.0 * SUM(CASE WHEN TRIM(COALESCE(finding_code, '')) <> '' THEN 1 ELSE 0 END)
             / COUNT(*), 1)                                            AS pct_with_finding
FROM {{P}}.main.copa_cases
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
FROM {{P}}.main.copa_by_complainant
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
FROM {{P}}.main.copa_by_officer
GROUP BY years_on_force
ORDER BY complaint_rows DESC
LIMIT 30`,
		},
		{
			Name: "shootings",
			Desc: "Cases flagged as police shootings, by year and current status.",
			SQL: `
SELECT date_part('year', complaint_date) AS year,
       COALESCE(NULLIF(TRIM(current_status), ''), '(not recorded)') AS current_status,
       COUNT(*) AS cases
FROM {{P}}.main.copa_cases
WHERE TRIM(COALESCE(police_shooting, '')) NOT IN ('', 'No', 'NO', 'no')
  AND complaint_date IS NOT NULL
  AND complaint_date >= DATE '2000-01-01'
  AND complaint_date < DATE '2030-01-01'
GROUP BY year, current_status
ORDER BY year DESC, cases DESC`,
		},
		{
			Name: "beat-hotspots",
			Desc: "Police beats generating the most complaints. Pair with arrests before ranking — volume tracks enforcement activity.",
			SQL: `
SELECT COALESCE(NULLIF(TRIM(beat), ''), '(not recorded)') AS beat,
       COUNT(*)                                           AS complaints,
       COUNT(DISTINCT date_part('year', complaint_date))  AS years_present
FROM {{P}}.main.copa_cases
GROUP BY beat
ORDER BY complaints DESC
LIMIT 30`,
		},
		{
			Name: "oversight-comparison",
			Desc: "COPA versus BIA caseload per year — the two bodies handle different complaint classes.",
			SQL: `
SELECT year, SUM(copa) AS copa_cases, SUM(bia) AS bia_cases
FROM (
  SELECT date_part('year', complaint_date) AS year, 1 AS copa, 0 AS bia
  FROM {{P}}.main.copa_cases      WHERE complaint_date IS NOT NULL
  UNION ALL
  SELECT date_part('year', complaint_date) AS year, 0 AS copa, 1 AS bia
  FROM {{P}}.main.bia_by_officer  WHERE complaint_date IS NOT NULL
)
WHERE year BETWEEN 2000 AND 2030
GROUP BY year
ORDER BY year DESC`,
		},
	},

	Caveats: []string{
		"Chicago's published COPA and BIA extracts carry no officer identifier — no " +
			"name, badge, or UID. You can profile complaints by demographics, tenure, " +
			"and assignment, but you cannot track an individual officer or count " +
			"repeat subjects. That is a deliberate upstream choice, not a sync gap.",
		"A complaint is an allegation. It records that someone came forward, not that " +
			"anything was substantiated.",
		"An unsustained or unfounded finding does not mean the complaint was false. It " +
			"means the evidence available to the investigator did not meet their " +
			"standard of proof, which is a different claim.",
		"Finding codes and category labels change over time and are not consistently " +
			"backfilled. Check the portal's current code list before mapping a code to " +
			"'sustained', and be wary of trends that cross a schema change.",
		"Complaint counts are not misconduct rates. A beat with more complaints may " +
			"have more enforcement activity, more residents willing to file, or both — " +
			"normalise against arrests before ranking anything.",
		"The BIA and COPA datasets cover different complaint classes and overlap " +
			"imperfectly. Do not add them together as if they were one caseload.",
	},
}
