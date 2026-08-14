// Copyright (c) 2026 Neomantra Corp

package modes

// rankingMode compares cities on published indicators, normalised per resident.
//
// Two design constraints are deliberate and load-bearing, because the audience
// for this mode includes people deciding where to live and they are the least
// equipped to catch a bad number:
//
// No composite score. The mode emits several separately-caveated indicators and
// never a single "city score". A composite invites a screenshot, and any
// weighting that produces one is an editorial judgement disguised as
// arithmetic.
//
// Absent data is never a good result. A city that does not publish crime data
// must not appear safe. Every query is gated on the city binding the concept
// *and* declaring a population; cities failing either are excluded by name with
// the reason, rather than dropped silently or shown with a blank.
//
// Raw counts are never compared across cities — only per-capita rates — because
// a raw count ranking is a population ranking wearing a disguise.
var rankingMode = &Mode{
	Name:        "ranking",
	Title:       "City ranking — comparable indicators, normalised per resident",
	Summary:     "Compare cities on crime, 311 responsiveness, and permit activity",
	MultiPortal: true,
	About: "Compares every attached city on the indicators both publish, " +
		"normalised by population. Each query runs once per city and the results " +
		"are stacked for comparison. Cities that do not publish an indicator, or " +
		"that have no population recorded, are excluded from that comparison by " +
		"name — never shown as zero or blank. There is deliberately no overall " +
		"score: these are separate measures of separate things, and combining " +
		"them would be an editorial choice pretending to be a calculation.",

	Concepts: []Concept{
		{
			Name:     "crimes",
			Purpose:  "Reported offences, for per-capita rates and arrest share.",
			Required: []string{"date", "primary_type"},
			Optional: []string{"arrest"},
		},
		{
			Name:     "service_requests",
			Purpose:  "311-style requests, for responsiveness and service load.",
			Required: []string{"created_date", "status"},
			Optional: []string{"closed_date", "sr_type"},
		},
		{
			Name:     "building_permits",
			Purpose:  "Permits issued, as a proxy for construction activity.",
			Required: []string{"issue_date"},
			Optional: []string{"processing_time"},
		},
	},

	Queries: []Query{
		{
			Name: "crime-rate",
			Desc: "Reported offences per 1,000 residents per year. Reported crime is not crime: it reflects what residents report and what the department records, both of which vary by city.",
			SQL: `
SELECT date_part('year', date)                              AS year,
       COUNT(*)                                             AS reported_offences,
       ROUND(COUNT(*) * 1000.0 / {{POP}}, 2)                AS per_1000_residents
FROM {{c:crimes}}
WHERE date IS NOT NULL
  AND date >= DATE '2015-01-01' AND date < DATE '2035-01-01'
GROUP BY year
ORDER BY year DESC
LIMIT 10`,
		},
		{
			Name: "crime-mix",
			Desc: "Share of reported offences by type — composition, not rate. Comparable only where both cities use similar offence categories, which is often not the case.",
			SQL: `
SELECT primary_type,
       COUNT(*)                                           AS offences,
       ROUND(100.0 * COUNT(*) / SUM(COUNT(*)) OVER (), 2) AS pct_of_reported
FROM {{c:crimes}}
WHERE date >= DATE '2020-01-01' AND date < DATE '2035-01-01'
GROUP BY primary_type
ORDER BY offences DESC
LIMIT 12`,
		},
		{
			Name: "arrest-share",
			Desc: "Share of reported offences recording an arrest. A low share is not a failure measure — clearance practice and recording rules differ.",
			SQL: `
SELECT date_part('year', date)                                        AS year,
       COUNT(*)                                                       AS reported_offences,
       ROUND(100.0 * SUM(CASE WHEN arrest THEN 1 ELSE 0 END)
             / COUNT(*), 2)                                           AS pct_with_arrest
FROM {{c:crimes}}
WHERE date >= DATE '2018-01-01' AND date < DATE '2035-01-01'
GROUP BY year
ORDER BY year DESC
LIMIT 8`,
		},
		{
			Name: "311-responsiveness",
			Desc: "Median days to close a service request. The most genuinely comparable indicator here — but only across request types a city actually handles.",
			SQL: `
SELECT date_part('year', created_date)                                AS year,
       COUNT(*)                                                       AS requests,
       ROUND(MEDIAN(date_diff('day', created_date, closed_date)), 1)  AS median_days_to_close,
       ROUND(100.0 * SUM(CASE WHEN closed_date IS NOT NULL THEN 1 ELSE 0 END)
             / COUNT(*), 1)                                           AS pct_closed
FROM {{c:service_requests}}
WHERE created_date IS NOT NULL
  AND created_date >= DATE '2019-01-01' AND created_date < DATE '2035-01-01'
GROUP BY year
ORDER BY year DESC
LIMIT 8`,
		},
		{
			Name: "311-load",
			Desc: "Service requests per 1,000 residents. Higher can mean worse conditions or better reporting channels — it does not distinguish them.",
			SQL: `
SELECT date_part('year', created_date)                AS year,
       COUNT(*)                                       AS requests,
       ROUND(COUNT(*) * 1000.0 / {{POP}}, 2)          AS per_1000_residents
FROM {{c:service_requests}}
WHERE created_date IS NOT NULL
  AND created_date >= DATE '2019-01-01' AND created_date < DATE '2035-01-01'
GROUP BY year
ORDER BY year DESC
LIMIT 8`,
		},
		{
			Name: "permit-activity",
			Desc: "Building permits issued per 10,000 residents — a rough proxy for construction activity. Permit regimes differ, so treat cross-city gaps as a question.",
			SQL: `
SELECT date_part('year', issue_date)                   AS year,
       COUNT(*)                                        AS permits_issued,
       ROUND(COUNT(*) * 10000.0 / {{POP}}, 2)          AS per_10000_residents
FROM {{c:building_permits}}
WHERE issue_date IS NOT NULL
  AND issue_date >= DATE '2015-01-01' AND issue_date < DATE '2035-01-01'
GROUP BY year
ORDER BY year DESC
LIMIT 10`,
		},
	},

	Caveats: []string{
		"These are separate indicators, not a ranking. There is deliberately no " +
			"overall score: combining crime, service responsiveness, and construction " +
			"into one number requires a weighting that is an editorial judgement, and " +
			"presenting it as arithmetic would be dishonest.",
		"A city missing from a comparison is missing because it does not publish that " +
			"indicator or has no recorded population — never because it scored zero. " +
			"Read the exclusion list before drawing any conclusion from who is present.",
		"Reported crime is not crime. It measures what residents report and what a " +
			"department records. A city with higher trust in police, or better reporting " +
			"channels, will show more reported offences for identical underlying " +
			"conditions. Never read these rates as relative danger.",
		"Offence categories, 311 request types, and permit rules are set by each city " +
			"independently. Two cities' 'theft' or 'pothole' may not cover the same " +
			"ground, so composition comparisons are indicative at best.",
		"Population is a single declared figure, not a time series. Rates for earlier " +
			"years are computed against today's population and will be wrong in " +
			"proportion to how much the city has grown or shrunk.",
		"City boundaries are not metro areas. A city whose suburbs sit outside its " +
			"boundary reports different rates from one that annexed them, with no " +
			"difference on the ground.",
		"This cannot tell you where to live. It compares a handful of things two " +
			"bureaucracies happen to publish, which is not the same as what a place is " +
			"like to live in — and says nothing about cost, schools, transit, or " +
			"anything at neighbourhood scale, where variation within a city usually " +
			"exceeds the variation between cities.",
	},
}
