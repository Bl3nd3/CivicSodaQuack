// Copyright (c) 2026 Neomantra Corp

package investigate

// civicPublishing tests whether a city's routine public record is thinning
// out — fewer offences, service requests and permits reaching the published
// data than in the years before.
//
// This is the general-purpose counterpart to police-transparency, and it exists
// partly to prove the shape generalises: it borrows a different mode, a
// different set of concepts and a different city binding, and every step from
// Plan to Explain is unchanged. An investigation that only worked for one
// question would be a report with extra steps.
//
// The claim is about publishing, not about the city. Fewer reported offences is
// a fact about the record; whether it means less crime, less reporting, or less
// publishing is exactly what the probes cannot settle, and the caveats say so
// rather than letting the verdict imply an answer.
var civicPublishing = &Investigation{
	Name:  "civic-publishing",
	Title: "Civic record publishing",
	Claim: "The city is publishing fewer routine public records than it used to.",
	About: "Measures the volume of the city's routine published record — reported " +
		"offences, 311 service requests, and building permits — year over year, " +
		"and how much of it is published in a completed state. It sees the " +
		"published record only: a change here is a change in what was released, " +
		"which may or may not be a change in what happened.",
	Mode: "ranking",
	// These name the claim, not the subject matter. Routing weights a term by
	// how few investigations claim it (see discover.go), so a bare topic word
	// like "potholes" or "crime" — unique to this list only by accident —
	// would score as though it were diagnostic, and send "how many potholes
	// will there be next year?" to an investigation that answers a different
	// question about the same noun. Subject words earn their place only when
	// they cannot be read as a question this investigation does not answer.
	Match: []string{
		"transparent", "transparency",
		"publish", "publishes", "publishing", "published",
		"release", "releases", "releasing",
		"disclose", "discloses", "disclosing", "disclosure",
		"open data", "foia", "records", "recordkeeping",
	},

	Probes: []Probe{
		{
			Name:         "offence-records",
			Asks:         "Are fewer reported offences being published each year?",
			Concept:      "crimes",
			PeriodColumn: "date",
			Unit:         "published offence records",
			Supports:     Down,
			RiseMeans:    "more offence records are being published",
			FallMeans:    "fewer offence records are being published",
			SQL: `
SELECT CAST(date_part('year', date) AS INTEGER) AS period,
       COUNT(*)                                 AS value
FROM {{c:crimes}}
WHERE date IS NOT NULL
  AND date >= DATE '1990-01-01' AND date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
			Caveats: []string{
				"Reported offences are not offences. The count moves with what " +
					"residents report and what the department records, and both change " +
					"for reasons unrelated to publishing policy.",
			},
		},
		{
			Name:         "service-request-records",
			Asks:         "Are fewer 311 service requests being published each year?",
			Concept:      "service_requests",
			PeriodColumn: "created_date",
			Unit:         "published service requests",
			Supports:     Down,
			RiseMeans:    "more service requests are being published",
			FallMeans:    "fewer service requests are being published",
			SQL: `
SELECT CAST(date_part('year', created_date) AS INTEGER) AS period,
       COUNT(*)                                         AS value
FROM {{c:service_requests}}
WHERE created_date IS NOT NULL
  AND created_date >= DATE '1990-01-01' AND created_date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
			Caveats: []string{
				"311 volume tracks how many ways a city gives residents to file and " +
					"how well those channels work. A drop can be a quieter city or a " +
					"harder-to-reach one.",
			},
		},
		{
			Name:         "permit-records",
			Asks:         "Are fewer building permits being published each year?",
			Concept:      "building_permits",
			PeriodColumn: "issue_date",
			Unit:         "published permit records",
			Supports:     Down,
			RiseMeans:    "more permit records are being published",
			FallMeans:    "fewer permit records are being published",
			SQL: `
SELECT CAST(date_part('year', issue_date) AS INTEGER) AS period,
       COUNT(*)                                       AS value
FROM {{c:building_permits}}
WHERE issue_date IS NOT NULL
  AND issue_date >= DATE '1990-01-01' AND issue_date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
			Caveats: []string{
				"Permit volume is construction activity as much as publishing " +
					"practice, and construction is cyclical.",
			},
		},
		{
			Name:              "request-closure-disclosure",
			Asks:              "Is a smaller share of service requests published with a closure date?",
			Concept:           "service_requests",
			PeriodColumn:      "created_date",
			Unit:              "requests with a closure date",
			Per:               "100 requests",
			Scale:             100,
			MeasuresAbsenceOf: []string{"closed_date"},
			// An open request has no closure date. A year of 311 requests is
			// largely worked through within one further year; measuring inside
			// that would report the backlog as a disclosure failure.
			SettlesAfter: 1,
			Supports:     Down,
			RiseMeans:    "a larger share of requests are published with their resolution",
			FallMeans:    "a smaller share of requests are published with their resolution",
			SQL: `
SELECT CAST(date_part('year', created_date) AS INTEGER)              AS period,
       COUNT(*) FILTER (WHERE closed_date IS NOT NULL)               AS value,
       COUNT(*)                                                      AS denominator
FROM {{c:service_requests}}
WHERE created_date IS NOT NULL
  AND created_date >= DATE '1990-01-01' AND created_date < DATE '2035-01-01'
GROUP BY period
ORDER BY period`,
			Caveats: []string{
				"An open request has no closure date, so the most recent periods " +
					"always look under-disclosed. Read this on years old enough for " +
					"their requests to have been worked through.",
			},
		},
	},

	Caveats: []string{
		"This measures the published record, not the city. A fall in published " +
			"records is consistent with less activity, less reporting by residents, " +
			"and less publishing by the city — the data here cannot tell them apart.",
		"Each of these datasets is published by a different department on its own " +
			"schedule. They move together only by coincidence, and a divergence " +
			"between them is not evidence of anything on its own.",
	},
}
