// Copyright (c) 2026 Neomantra Corp

package investigate

// policeTransparency tests whether a city is disclosing less about the conduct
// of its police department than it used to.
//
// Transparency is not directly measurable, so the claim is deliberately narrow:
// it is about the published record, not about the department. Every probe here
// measures something about what reaches the public — how many accountability
// records appear, how many carry an outcome, how many carry a category — and
// none of them measures whether misconduct rose or fell. A city could publish
// beautifully while behaving badly, and this investigation would say so in
// glowing terms. That limit is stated in the caveats rather than hidden behind
// the word "transparency".
//
// The probes borrow the police mode's concepts, so any city bound for police
// monitoring can be investigated without touching this file.
var policeTransparency = &Investigation{
	Name:  "police-transparency",
	Title: "Police transparency",
	Claim: "The city is disclosing less about police accountability than it used to.",
	About: "Measures the published accountability record — how many complaint " +
		"cases appear, how many carry a recorded outcome, how many carry a " +
		"category, and how complaint volume compares with enforcement volume. " +
		"It reads only what the city chose to publish, so it can see disclosure " +
		"practice and cannot see conduct.",
	Mode: "police",
	Match: []string{
		"police", "policing", "cop", "cops", "officer", "officers",
		"misconduct", "complaint", "complaints", "oversight", "accountability",
		"transparent", "transparency", "disclose", "disclosure", "copa",
		"brutality", "shooting", "shootings",
	},

	Probes: []Probe{
		{
			Name:         "case-publication",
			Asks:         "Are fewer complaint cases reaching the public record each year?",
			Concept:      "complaints",
			PeriodColumn: "complaint_date",
			Unit:         "published complaint cases",
			Supports:     Down,
			RiseMeans:    "more accountability records are reaching the public record",
			FallMeans:    "fewer accountability records are reaching the public record",
			SQL: `
SELECT CAST(date_part('year', complaint_date) AS INTEGER) AS period,
       COUNT(*)                                           AS value
FROM {{c:complaints}}
WHERE complaint_date IS NOT NULL
  AND complaint_date >= DATE '1990-01-01' AND complaint_date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
			Caveats: []string{
				"A fall in published cases can mean fewer complaints were filed, or " +
					"that fewer filed complaints are being published. This indicator " +
					"cannot separate the two, and they point in opposite directions.",
			},
		},
		{
			Name:         "outcome-disclosure",
			Asks:         "Is a smaller share of cases published with a recorded outcome?",
			Concept:      "complaints",
			PeriodColumn: "complaint_date",
			Unit:         "cases with a recorded finding",
			Per:          "100 cases",
			Scale:        100,
			// The empty finding_code is the observation, not missing evidence.
			MeasuresAbsenceOf: []string{"finding_code"},
			// An open case has no finding yet. Two years is the span after
			// which a year's caseload is mostly resolved, and measuring inside
			// it would report the age of the cases as a disclosure failure.
			SettlesAfter: 2,
			Supports:     Down,
			RiseMeans:    "a larger share of cases are published with their outcome",
			FallMeans:    "a smaller share of cases are published with their outcome",
			SQL: `
SELECT CAST(date_part('year', complaint_date) AS INTEGER)                    AS period,
       SUM(CASE WHEN TRIM(COALESCE(finding_code, '')) <> '' THEN 1 ELSE 0 END) AS value,
       COUNT(*)                                                              AS denominator
FROM {{c:complaints}}
WHERE complaint_date IS NOT NULL
  AND complaint_date >= DATE '1990-01-01' AND complaint_date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
			Caveats: []string{
				"Recent years always look under-disclosed here: an open case has no " +
					"finding yet, so the most recent periods carry cases that simply " +
					"have not closed. Read this indicator on years old enough to have " +
					"resolved.",
			},
		},
		{
			Name:              "category-disclosure",
			Asks:              "Is a smaller share of cases published with a category?",
			Concept:           "complaints",
			PeriodColumn:      "complaint_date",
			Unit:              "cases with a recorded category",
			Per:               "100 cases",
			Scale:             100,
			MeasuresAbsenceOf: []string{"current_category"},
			Supports:          Down,
			RiseMeans:         "a larger share of cases say what they were about",
			FallMeans:         "a smaller share of cases say what they were about",
			SQL: `
SELECT CAST(date_part('year', complaint_date) AS INTEGER)                          AS period,
       SUM(CASE WHEN TRIM(COALESCE(current_category, '')) <> '' THEN 1 ELSE 0 END) AS value,
       COUNT(*)                                                                    AS denominator
FROM {{c:complaints}}
WHERE complaint_date IS NOT NULL
  AND complaint_date >= DATE '1990-01-01' AND complaint_date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
		},
		{
			Name:         "complaint-rate",
			Asks:         "Are complaints falling relative to enforcement volume?",
			Concept:      "complaints",
			PeriodColumn: "complaint_date",
			Unit:         "complaints",
			Per:          "1,000 arrests",
			Scale:        1000,
			Supports:     Down,
			RiseMeans:    "more complaints are filed per unit of enforcement activity",
			FallMeans:    "fewer complaints are filed per unit of enforcement activity",
			SQL: `
WITH c AS (
  SELECT CAST(date_part('year', complaint_date) AS INTEGER) AS period, COUNT(*) AS n
  FROM {{c:complaints}}
  WHERE complaint_date IS NOT NULL
    AND complaint_date >= DATE '1990-01-01' AND complaint_date < DATE '2035-01-01'
  GROUP BY period
),
a AS (
  SELECT CAST(date_part('year', arrest_date) AS INTEGER) AS period, COUNT(*) AS n
  FROM {{c:arrests}}
  WHERE arrest_date IS NOT NULL
    AND arrest_date >= DATE '1990-01-01' AND arrest_date < DATE '2035-01-01'
  GROUP BY period
)
SELECT c.period      AS period,
       c.n           AS value,
       a.n           AS denominator
FROM c JOIN a ON a.period = c.period
ORDER BY period`,
			Caveats: []string{
				"A falling complaint rate is genuinely ambiguous. It is consistent " +
					"with better conduct, and equally consistent with a complaint " +
					"process that has become harder to reach. Nothing in this data " +
					"distinguishes them, so this indicator should never be quoted alone.",
				"Arrests are a denominator of convenience, not a measure of police " +
					"contact. Most encounters never produce an arrest, and the share " +
					"that do changes with enforcement policy.",
			},
		},
	},

	Caveats: []string{
		"This measures the published record, not the department. A city that " +
			"publishes less may be behaving identically and disclosing less, or " +
			"disclosing identically and doing less. The evidence here distinguishes " +
			"neither from a genuine change in conduct.",
		"Oversight bodies publish on their own schedule, and a case appears in the " +
			"record when it is entered rather than when the incident happened. " +
			"Recent periods are therefore thin for reasons that have nothing to do " +
			"with disclosure policy.",
		"A complaint is an allegation. None of these counts records that anything " +
			"was substantiated.",
	},
}
