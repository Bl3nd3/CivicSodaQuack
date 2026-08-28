// Copyright (c) 2026 Neomantra Corp

package modes

// Bindings for data.cityofnewyork.us.
//
// This is the binding that proved the concept layer was still Chicago-shaped.
// Chicago's crime table calls an offence date `date` and its category
// `primary_type`; NYPD calls them `cmplnt_fr_dt` and `ofns_desc`. Nothing about
// those names is more correct than the other, so the concept keeps canonical
// names and each binding declares its own mapping.
//
// It also proved the value of treating a missing column as an exclusion rather
// than a NULL: NYPD's complaint extract records no arrest flag at all, so New
// York is excluded from arrest-share by name instead of appearing to have an
// arrest rate of zero.
//
// Every id, column, and row count below was verified against the live API.
func init() {
	registerBinding(nycRanking)
}

var nycRanking = &Binding{
	Mode:             "ranking",
	Portal:           "data.cityofnewyork.us",
	City:             "New York, NY",
	Population:       8804190,
	PopulationSource: "2020 Decennial Census, table P1",
	Concepts: map[string]BoundDataset{
		"crimes": {
			ID: "qgea-i56i", Table: "nypd_complaints", Name: "NYPD Complaint Data Historic",
			Rows: 10071507,
			Columns: map[string]string{
				"date":         "cmplnt_fr_dt",
				"primary_type": "ofns_desc",
				// No arrest flag: this extract records reported complaints, and
				// arrests are published separately with no join key. Declared by
				// omission, which excludes NYC from arrest-share by name.
			},
			Notes: "Complaints reported to NYPD, not arrests or convictions. Offence " +
				"categories (ofns_desc) follow NY penal law and do not line up with " +
				"Chicago's IUCR primary_type.",
		},
		"service_requests": {
			ID: "erm2-nwe9", Table: "requests_311", Name: "311 Service Requests from 2020 to Present",
			Rows: 22123771,
			// created_date, closed_date and status already match the concept.
			Notes: "Covers 2020 onward only; earlier requests are in a separate " +
				"dataset, so a series crossing 2020 is not continuous.",
		},
		"building_permits": {
			ID: "ipu4-2q9a", Table: "dob_permits", Name: "DOB Permit Issuance", Rows: 3989937,
			Columns: map[string]string{
				// DOB publishes issuance_date as *text* holding MM/DD/YYYY, not a
				// date, so this maps to a parse expression rather than a column.
				// Without it permit-activity fails outright -- DuckDB will not
				// compare VARCHAR '06/17/2020' against a DATE -- and a SoQL range
				// filter on the raw column compares lexicographically, matching
				// almost nothing while looking like a valid result. try_strptime
				// rather than strptime because the column carries unparseable
				// values that would otherwise abort the whole query.
				"issue_date": `try_strptime(issuance_date, '%m/%d/%Y')`,
				// No processing_time equivalent is published.
			},
			Notes: "One row per permit issuance including renewals and subtypes, so " +
				"counts run higher per project than a city recording one permit per job.",
		},
	},
	Notes: []string{
		"NYPD complaint data and Chicago's crime data are both 'reported offences' " +
			"but are built on different legal categories and recording rules. Compare " +
			"trends within a city with more confidence than levels between cities.",
		"New York's 311 is among the most heavily used in the country, which raises " +
			"request volume for reasons unrelated to underlying conditions.",
		"DOB permit issuance counts renewals and subtypes as separate rows, so the " +
			"per-capita permit rate is not comparable to a city that files once per job " +
			"without normalising first.",
	},
}
