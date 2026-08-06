// Copyright (c) 2026 Neomantra Corp

package modes

// corruptionMode bundles the public-record trails that procurement and
// influence reporting normally start from: who the city pays, who is paid to
// lobby it, and who those lobbyists give money to.
//
// Every query here answers "where is the concentration unusual?" — never "who
// is corrupt." Concentration is a routine feature of specialised procurement
// (one firm may be the only bidder qualified to service a bridge), so an
// outlier is a question to ask a press officer, not a finding.
var corruptionMode = &Mode{
	Name:    "corruption",
	Title:   "Corruption detection — procurement and influence trails",
	Summary: "Contract concentration, lobbying spend, and political contributions",
	Portal:  "data.cityofchicago.org",
	About: "Assembles the paper trail that procurement and influence reporting " +
		"starts from: city contracts and award amounts, the lobbyists registered " +
		"to influence the city, what their clients pay them, and the political " +
		"contributions those lobbyists make. The queries surface concentration " +
		"and overlap — a vendor taking most of a department's spend, a lobbyist " +
		"paid by a client while giving to officials. These are leads, not " +
		"findings.",

	Datasets: []Dataset{
		{ID: "rsxa-ify5", Table: "contracts", Name: "Contracts", Rows: 185699,
			Why: "Award amounts, vendors, departments, procurement type — the spine of the mode."},
		{ID: "dw2f-w78u", Table: "lobbyist_compensation", Name: "Lobbyist Data - Compensation", Rows: 40961,
			Why: "What each client pays each registered lobbyist, by period."},
		{ID: "p9p7-vfqc", Table: "lobbyist_contributions", Name: "Lobbyist Data - Contributions", Rows: 9053,
			Why: "Political contributions made by lobbyists, with recipient."},
		{ID: "g8p5-y4m5", Table: "lobbyist_clients", Name: "Lobbyist Data - Clients", Rows: 22286,
			Why: "Client roster, for joining compensation back to interests."},
		{ID: "pahz-egmi", Table: "lobbying_activity", Name: "Lobbyist Data - Lobbying Activity", Rows: 119550,
			Why: "Which departments and subjects were lobbied."},
		{ID: "5d79-9xqr", Table: "lobbyist_gifts", Name: "Lobbyist Data - Gifts", Rows: 780,
			Why: "Gifts from lobbyists to city officials. Small but high-signal."},
		{ID: "xzkq-xp2w", Table: "employee_salaries", Name: "Current Employee Names, Salaries, and Position Titles", Rows: 31788,
			Why: "Department headcount and payroll, as a denominator for spend."},
		{ID: "iekz-rtng", Table: "tif_projects", Name: "Financial Incentive Projects - (TIF-Funded) Economic Development", Rows: 40,
			Why: "TIF subsidy recipients — a recurring subject of oversight reporting."},
	},

	Queries: []Query{
		{
			Name: "top-vendors",
			Desc: "Vendors by total contract value awarded. The starting picture.",
			SQL: `
SELECT vendor_name,
       COUNT(*)                     AS contracts,
       ROUND(SUM(award_amount), 2)  AS total_awarded,
       ROUND(AVG(award_amount), 2)  AS avg_award,
       MIN(start_date)              AS first_award,
       MAX(start_date)              AS last_award
FROM {{P}}.main.contracts
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
  FROM {{P}}.main.contracts
  WHERE award_amount IS NOT NULL AND department IS NOT NULL AND vendor_name IS NOT NULL
  GROUP BY department, vendor_name
),
shares AS (
  SELECT department, vendor_name, amt,
         SUM(amt) OVER (PARTITION BY department) AS dept_total
  FROM by_vendor
)
SELECT department, vendor_name,
       ROUND(amt, 2)                            AS vendor_amount,
       ROUND(dept_total, 2)                     AS department_total,
       ROUND(100.0 * amt / dept_total, 1)       AS pct_of_department
FROM shares
WHERE dept_total > 0
  AND amt > 1000000
  AND 100.0 * amt / dept_total > 40
ORDER BY pct_of_department DESC, vendor_amount DESC
LIMIT 40`,
		},
		{
			Name: "procurement-type",
			Desc: "Award value split by procurement type — how much spend bypasses open competition.",
			SQL: `
SELECT COALESCE(procurement_type, '(unspecified)') AS procurement_type,
       COUNT(*)                                    AS contracts,
       ROUND(SUM(award_amount), 2)                 AS total_awarded,
       ROUND(100.0 * SUM(award_amount)
             / SUM(SUM(award_amount)) OVER (), 1)  AS pct_of_spend
FROM {{P}}.main.contracts
WHERE award_amount IS NOT NULL
GROUP BY procurement_type
ORDER BY total_awarded DESC`,
		},
		{
			Name: "lobbyist-compensation",
			Desc: "Highest-paying lobbying relationships, client to lobbyist.",
			SQL: `
SELECT client_name,
       lobbyist_last_name || ', ' || lobbyist_first_name AS lobbyist,
       COUNT(*)                                          AS filings,
       ROUND(SUM(compensation_amount), 2)                AS total_compensation,
       MIN(period_start)                                 AS first_period,
       MAX(period_end)                                   AS last_period
FROM {{P}}.main.lobbyist_compensation
WHERE compensation_amount IS NOT NULL
GROUP BY client_name, lobbyist
HAVING SUM(compensation_amount) > 0
ORDER BY total_compensation DESC
LIMIT 30`,
		},
		{
			Name: "contribution-recipients",
			Desc: "Who receives money from registered lobbyists, ranked by total.",
			SQL: `
SELECT recipient,
       COUNT(*)                             AS contributions,
       COUNT(DISTINCT lobbyist_id)          AS distinct_lobbyists,
       ROUND(SUM(amount), 2)                AS total_amount,
       MIN(contribution_date)               AS first_contribution,
       MAX(contribution_date)               AS last_contribution
FROM {{P}}.main.lobbyist_contributions
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
  SELECT lobbyist_id,
         MAX(lobbyist_last_name || ', ' || lobbyist_first_name) AS lobbyist,
         SUM(compensation_amount)                               AS total_compensation
  FROM {{P}}.main.lobbyist_compensation
  WHERE compensation_amount IS NOT NULL
  GROUP BY lobbyist_id
),
give AS (
  SELECT lobbyist_id,
         SUM(amount)                 AS total_contributed,
         COUNT(DISTINCT recipient)   AS distinct_recipients
  FROM {{P}}.main.lobbyist_contributions
  WHERE amount IS NOT NULL
  GROUP BY lobbyist_id
)
SELECT c.lobbyist,
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
		"Vendor names are free text and are not deduplicated upstream. The same firm " +
			"may appear under several spellings, which understates concentration. " +
			"Normalise before quoting a total.",
		"Contracts records the award amount, not what was actually paid. Modifications, " +
			"cancellations, and underspend are not reflected here.",
		"There is no join key between contracts and lobbying records. Any link between " +
			"a vendor and a lobbying client is a name match you must verify by hand.",
	},
}
