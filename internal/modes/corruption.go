// Copyright (c) 2026 Neomantra Corp

package modes

// corruptionMode bundles the public-record trails that procurement and
// influence reporting normally start from: who a city pays, who is paid to
// lobby it, and who those lobbyists give money to.
//
// It is defined in terms of concepts, not datasets. Any portal publishing
// contracts with a vendor and an award amount can bind to it; see
// bindings_chicago.go for the reference binding.
//
// Every query asks "where is the concentration unusual?" — never "who is
// corrupt." Concentration is routine in specialised procurement (one firm may
// be the only qualified bidder), so an outlier is a question for a press
// officer, not a finding.
var corruptionMode = &Mode{
	Name:    "corruption",
	Title:   "Corruption detection — procurement and influence trails",
	Summary: "Contract concentration, lobbying spend, and political contributions",
	About: "Assembles the paper trail that procurement and influence reporting " +
		"starts from: contracts and award amounts, the lobbyists registered to " +
		"influence the city, what their clients pay them, and the political " +
		"contributions those lobbyists make. The queries surface concentration " +
		"and overlap — a vendor taking most of a department's spend, a lobbyist " +
		"paid by a client while giving to officials. These are leads, not " +
		"findings.",

	Concepts: []Concept{
		{
			Name:     "contracts",
			Purpose:  "Award amounts by vendor and department — the spine of the mode.",
			Required: []string{"vendor_name", "department", "award_amount"},
			Optional: []string{"procurement_type", "start_date"},
		},
		{
			Name:     "lobbyist_compensation",
			Purpose:  "What each client pays each registered lobbyist.",
			Required: []string{"client_name", "lobbyist_id", "compensation_amount"},
			Optional: []string{"lobbyist_first_name", "lobbyist_last_name", "period_start", "period_end"},
		},
		{
			Name:     "lobbyist_contributions",
			Purpose:  "Political contributions made by lobbyists, with recipient.",
			Required: []string{"recipient", "amount", "lobbyist_id"},
			Optional: []string{"contribution_date"},
		},
	},

	Queries: []Query{
		{
			Name:    "top-vendors",
			Desc:    "Vendors by total contract value awarded. The starting picture.",
			Entity:  "vendor_name",
			Measure: "total_awarded",
			SQL: `
SELECT vendor_name,
       COUNT(*)                     AS contracts,
       ROUND(SUM(award_amount), 2)  AS total_awarded,
       ROUND(AVG(award_amount), 2)  AS avg_award
FROM {{c:contracts}}
WHERE award_amount IS NOT NULL AND vendor_name IS NOT NULL
GROUP BY vendor_name
ORDER BY total_awarded DESC
LIMIT 25`,
		},
		{
			Name: "department-concentration",
			Desc: "Vendors taking an outsized share of one department's spend. Concentration is a question, not an answer — some work has one qualified bidder.",
			SQL: `
WITH by_vendor AS (
  SELECT department, vendor_name, SUM(award_amount) AS amt
  FROM {{c:contracts}}
  WHERE award_amount IS NOT NULL AND department IS NOT NULL AND vendor_name IS NOT NULL
  GROUP BY department, vendor_name
),
shares AS (
  SELECT department, vendor_name, amt,
         SUM(amt) OVER (PARTITION BY department) AS dept_total
  FROM by_vendor
)
SELECT department, vendor_name,
       ROUND(amt, 2)                      AS vendor_amount,
       ROUND(dept_total, 2)               AS department_total,
       ROUND(100.0 * amt / dept_total, 1) AS pct_of_department
FROM shares
WHERE dept_total > 0 AND amt > 1000000 AND 100.0 * amt / dept_total > 40
ORDER BY pct_of_department DESC, vendor_amount DESC
LIMIT 40`,
		},
		{
			Name:    "procurement-type",
			Desc:    "Award value split by procurement type — how much spend bypasses open competition.",
			Entity:  "procurement_type",
			Measure: "total_awarded",
			SQL: `
SELECT COALESCE(procurement_type, '(unspecified)') AS procurement_type,
       COUNT(*)                                    AS contracts,
       ROUND(SUM(award_amount), 2)                 AS total_awarded,
       ROUND(100.0 * SUM(award_amount)
             / SUM(SUM(award_amount)) OVER (), 1)  AS pct_of_spend
FROM {{c:contracts}}
WHERE award_amount IS NOT NULL
GROUP BY procurement_type
ORDER BY total_awarded DESC`,
		},
		{
			Name:    "lobbyist-compensation",
			Desc:    "Highest-paying lobbying relationships, client to lobbyist.",
			Entity:  "client_name",
			Measure: "total_compensation",
			SQL: `
SELECT client_name,
       COUNT(*)                           AS filings,
       ROUND(SUM(compensation_amount), 2) AS total_compensation,
       COUNT(DISTINCT lobbyist_id)        AS distinct_lobbyists
FROM {{c:lobbyist_compensation}}
WHERE compensation_amount IS NOT NULL
GROUP BY client_name
HAVING SUM(compensation_amount) > 0
ORDER BY total_compensation DESC
LIMIT 30`,
		},
		{
			Name:    "contribution-recipients",
			Desc:    "Who receives money from registered lobbyists, ranked by total.",
			Entity:  "recipient",
			Measure: "total_amount",
			SQL: `
SELECT recipient,
       COUNT(*)                    AS contributions,
       COUNT(DISTINCT lobbyist_id) AS distinct_lobbyists,
       ROUND(SUM(amount), 2)       AS total_amount
FROM {{c:lobbyist_contributions}}
WHERE amount IS NOT NULL AND recipient IS NOT NULL
GROUP BY recipient
ORDER BY total_amount DESC
LIMIT 30`,
		},
		{
			Name: "paid-and-giving",
			Desc: "Lobbyists who were compensated and also contributed politically. Legal and routine — the point is the overlap, and its size.",
			SQL: `
WITH comp AS (
  SELECT lobbyist_id, SUM(compensation_amount) AS total_compensation
  FROM {{c:lobbyist_compensation}}
  WHERE compensation_amount IS NOT NULL
  GROUP BY lobbyist_id
),
give AS (
  SELECT lobbyist_id, SUM(amount) AS total_contributed,
         COUNT(DISTINCT recipient) AS distinct_recipients
  FROM {{c:lobbyist_contributions}}
  WHERE amount IS NOT NULL
  GROUP BY lobbyist_id
)
SELECT c.lobbyist_id,
       ROUND(c.total_compensation, 2) AS total_compensation,
       ROUND(g.total_contributed, 2)  AS total_contributed,
       g.distinct_recipients,
       ROUND(100.0 * g.total_contributed
             / NULLIF(c.total_compensation, 0), 2) AS contributed_pct_of_pay
FROM comp c
JOIN give g USING (lobbyist_id)
ORDER BY total_contributed DESC
LIMIT 30`,
		},
	},

	Caveats: []string{
		"Concentration is not evidence of wrongdoing. Sole-source and low-bidder-count " +
			"procurement is legitimate and common for specialised work. Treat every " +
			"outlier as a question for the department, not a conclusion.",
		"Lobbying and political contributions by registered lobbyists are lawful and " +
			"disclosed. Their presence in this data is the system working as designed; " +
			"only the pattern and scale are worth reporting on.",
		"Vendor names are free text and are rarely deduplicated upstream. The same firm " +
			"may appear under several spellings, which understates concentration. " +
			"Normalise before quoting a total.",
		"Contracts records the award amount, not what was actually paid. Modifications, " +
			"cancellations, and underspend are not reflected.",
		"There is usually no join key between contracts and lobbying records. Any link " +
			"between a vendor and a lobbying client is a name match you must verify.",
		"Disclosure regimes differ by city. A portal with more lobbying records may have " +
			"stricter reporting rules, not more lobbying — never rank cities on these counts.",
	},
}
