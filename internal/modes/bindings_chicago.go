// Copyright (c) 2026 Neomantra Corp

package modes

// Bindings for data.cityofchicago.org — the reference implementation.
//
// This file is the template for adding a city: map each concept a mode declares
// onto the dataset that portal publishes for it, record what makes that city's
// data unusual, and leave a concept unbound where the portal has nothing honest
// to offer. No mode code changes.
//
// Every dataset id and row count here was verified against the live Socrata API.
func init() {
	registerBinding(chicagoCorruption)
	registerBinding(chicagoPolice)
	registerBinding(chicagoRanking)
}

var chicagoCorruption = &Binding{
	Mode:   "corruption",
	Portal: "data.cityofchicago.org",
	City:   "Chicago, IL",
	Concepts: map[string]BoundDataset{
		"contracts": {
			ID: "rsxa-ify5", Table: "contracts", Name: "Contracts", Rows: 185699,
			Notes: "award_amount is the awarded value, not spend. Vendor names are free text.",
		},
		"lobbyist_compensation": {
			ID: "dw2f-w78u", Table: "lobbyist_compensation",
			Name: "Lobbyist Data - Compensation", Rows: 40961,
		},
		"lobbyist_contributions": {
			ID: "p9p7-vfqc", Table: "lobbyist_contributions",
			Name: "Lobbyist Data - Contributions", Rows: 9053,
			Notes: "Contributions by registered lobbyists only — not a full campaign-finance record.",
		},
	},
	Notes: []string{
		"Chicago's lobbying disclosure is comparatively strict, so lobbying record " +
			"counts here are higher than cities with lighter regimes. That reflects " +
			"disclosure rules, not more lobbying.",
		"Contracts and lobbying records share no join key. Linking a vendor to a " +
			"lobbying client is a name match requiring manual verification.",
	},
}

var chicagoPolice = &Binding{
	Mode:   "police",
	Portal: "data.cityofchicago.org",
	City:   "Chicago, IL",
	Concepts: map[string]BoundDataset{
		"complaints": {
			ID: "mft5-nfa8", Table: "copa_cases", Name: "COPA Cases - Summary", Rows: 189405,
			Notes: "COPA's caseload. BIA (internal affairs) handles a different complaint " +
				"class and is published separately; the two overlap imperfectly and must " +
				"not be summed.",
		},
		"complaints_by_officer": {
			ID: "ufxy-tgry", Table: "copa_by_officer",
			Name: "COPA Cases - By Involved Officer", Rows: 214782,
			Notes: "Carries officer demographics and tenure but NO officer identifier — " +
				"no name, badge, or UID.",
		},
		"complaints_by_complainant": {
			ID: "vnz2-rmie", Table: "copa_by_complainant",
			Name: "COPA Cases - By Complainant or Subject", Rows: 204678,
		},
		"arrests": {
			ID: "dpt3-jri9", Table: "arrests", Name: "Arrests", Rows: 741043,
			Notes: "Denominator only.",
		},
	},
	Notes: []string{
		"Chicago's published COPA and BIA extracts carry no officer identifier, so " +
			"repeat-officer analysis is impossible with them. That is a deliberate " +
			"upstream choice, not a sync gap — a city that does publish identifiers " +
			"will support analyses Chicago cannot.",
		"Chicago publishes no use-of-force or tactical-response dataset on this portal, " +
			"so force is only visible through the police_shooting flag on complaints.",
	},
}

var chicagoRanking = &Binding{
	Mode:   "ranking",
	Portal: "data.cityofchicago.org",
	City:   "Chicago, IL",
	// A denominator without a citation is not usable in a comparison someone
	// might act on, so the source is recorded alongside the figure.
	Population:       2746388,
	PopulationSource: "2020 Decennial Census, table P1",
	Concepts: map[string]BoundDataset{
		"crimes": {
			ID: "ijzp-q8t2", Table: "crimes", Name: "Crimes - 2001 to Present", Rows: 8614139,
			Notes: "Reported offences as recorded by CPD. Excludes murders where victim " +
				"data would identify a minor, and the most recent days are incomplete.",
		},
		"service_requests": {
			ID: "v6vf-nfxy", Table: "requests_311", Name: "311 Service Requests", Rows: 14453929,
			Notes: "Unified 311 system from Dec 2018. Earlier requests live in per-type " +
				"legacy datasets, so a series crossing 2018 is not continuous.",
		},
		"building_permits": {
			ID: "ydr8-5enu", Table: "building_permits", Name: "Building Permits", Rows: 844259,
		},
	},
	Notes: []string{
		"Chicago's 311 system was consolidated in December 2018; request volumes " +
			"before and after are not comparable, and the queries here start in 2019 " +
			"for that reason.",
		"Chicago publishes unusually granular crime data. Comparing its reported " +
			"offence rate against a city publishing less is a comparison of publishing " +
			"practice as much as of conditions.",
	},
}
