// Copyright (c) 2026 Neomantra Corp

package modes

import (
	"encoding/json"
	"fmt"
)

// The schema below is the single description of what a mode file may contain.
// It is used three ways, and that is the point of keeping one copy:
//
//   - `csq modes schema` prints it, so someone writing a file by hand can see
//     the exact shape rather than reverse-engineering it from an example.
//   - `csq modes ask` and `csq modes add` write against it, so a generated mode
//     is held to the same grammar a person writes by hand.
//   - The loader validates against the same field set, so a file that satisfies
//     the schema and a file that satisfies the loader cannot drift apart.
//
// A second, hand-maintained copy of this shape would be the obvious way to do
// it and the obvious way for the three to disagree — at which point csq
// generates documents its own loader rejects, and the error lands on the user.
//
// Every object sets additionalProperties:false, matching the loader's
// KnownFields/DisallowUnknownFields behaviour: a typo is an error at the point
// it was made, not a key silently dropped on the floor.

// ModeSchema returns the JSON Schema for a `kind: mode` document.
func ModeSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "An analysis profile: the concepts it needs, the SQL that answers a question, and the limits of that answer.",
		"properties": map[string]any{
			"kind": map[string]any{
				"const":       "mode",
				"description": "Always the string \"mode\".",
			},
			"name": map[string]any{
				"type":        "string",
				"pattern":     "^[a-z0-9][a-z0-9-]*$",
				"description": "Slug used on the command line. Lowercase, no spaces.",
			},
			"title":   strField("One-line title shown as the report heading."),
			"summary": strField("One sentence, shown in `csq modes` listings."),
			"about":   strField("A paragraph describing what the mode assembles and what it is for."),
			"concepts": map[string]any{
				"type": "array",
				"description": "The logical tables this mode needs, described by what they must contain " +
					"rather than by dataset id. A query refers to one as {{c:name}}. Omit or leave empty " +
					"only for a mode that reads csq's own _csq bookkeeping schema.",
				"items": conceptSchema(),
			},
			"queries": map[string]any{
				"type":        "array",
				"minItems":    1,
				"description": "The canned analyses this mode can run. At least one is required.",
				"items":       querySchema(),
			},
			"caveats": map[string]any{
				"type":     "array",
				"minItems": 1,
				"items":    map[string]any{"type": "string"},
				"description": "Interpretation limits, printed above every result. Required: state what these " +
					"numbers cannot show, which populations are missing, and which patterns have innocent " +
					"explanations. A number without its caveat is how civic data gets misread.",
			},
		},
		"required":             []any{"kind", "name", "title", "summary", "about", "queries", "caveats"},
		"additionalProperties": false,
	}
}

// BindingSchema returns the JSON Schema for a `kind: binding` document.
func BindingSchema() map[string]any {
	return map[string]any{
		"type":        "object",
		"description": "One portal's answer to a mode's concepts: which local table fulfils each concept, and what that portal calls each column.",
		"properties": map[string]any{
			"kind":   map[string]any{"const": "binding", "description": "Always the string \"binding\"."},
			"mode":   strField("Name of the mode this binding satisfies."),
			"portal": strField("Socrata host, e.g. data.cityofchicago.org."),
			"city":   strField("Human label for the jurisdiction, e.g. \"Chicago, IL\"."),
			"population": map[string]any{
				"type": "integer",
				"description": "Resident count, used only to turn counts into per-capita rates. " +
					"Omit unless you know it — a guessed denominator produces a confidently wrong rate.",
			},
			"population_source": strField(
				"Where the population figure came from, e.g. \"2020 Decennial Census, table P1\". " +
					"Required whenever population is set."),
			"datasets": map[string]any{
				"type": "object",
				"description": "Concept name → the local table fulfilling it. Every key must be a concept " +
					"the mode declares.",
				"additionalProperties": datasetSchema(),
			},
			"notes": map[string]any{
				"type":        "array",
				"items":       map[string]any{"type": "string"},
				"description": "Portal-wide caveats a reader must know before comparing this city with another.",
			},
		},
		"required":             []any{"kind", "mode", "portal", "city", "datasets"},
		"additionalProperties": false,
	}
}

func conceptSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    strField("Referenced in SQL as {{c:name}}. Lowercase with underscores."),
			"purpose": strField("Why the mode needs this table, in one sentence."),
			"required": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Canonical column names the queries read. A portal that cannot supply one " +
					"of these cannot bind the concept at all.",
			},
			"optional": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
				"description": "Columns some queries use when the portal has them. A query needing a missing " +
					"optional column excludes that city by name rather than reading NULL as a value.",
			},
		},
		"required":             []any{"name", "purpose"},
		"additionalProperties": false,
	}
}

func querySchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"pattern":     "^[a-z0-9][a-z0-9-]*$",
				"description": "Slug, unique within the mode.",
			},
			"desc": strField("What the result shows, in one sentence."),
			"sql": strField(
				"A single read-only SELECT (a leading WITH is fine). Refer to tables only as {{c:name}}, " +
					"never by their real table names, so the query works on any city that binds the mode. " +
					"Refer to columns by the concept's canonical names."),
			"entity": strField(
				"Optional. The result column naming what each row is about (e.g. vendor_name). " +
					"Set it together with measure to get a concentration reading."),
			"measure": strField(
				"Optional. The result column holding the number a reader would quote (e.g. total_awarded). " +
					"Leave both entity and measure unset rather than guessing — a share computed over the " +
					"wrong column is a confidently wrong percentage."),
		},
		"required":             []any{"name", "desc", "sql"},
		"additionalProperties": false,
	}
}

func datasetSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":    strField("Socrata 4x4 dataset id on this portal, e.g. rsxa-ify5."),
			"table": strField("Local DuckDB table name holding it."),
			"name":  strField("Upstream dataset title."),
			"rows":  map[string]any{"type": "integer", "description": "Approximate upstream row count. Informational."},
			"notes": strField("Definitional caveats specific to this portal's version of the dataset."),
			"columns": map[string]any{
				"type":                 "object",
				"additionalProperties": map[string]any{"type": "string"},
				"description": "Concept column → this portal's column, or any SQL expression over it. " +
					"When present this map is authoritative: a concept column absent from it is treated as " +
					"unavailable here, and queries needing it exclude this city with a reason. Use an " +
					"expression when the portal publishes the right value in the wrong type, e.g. " +
					"try_strptime(issuance_date, '%m/%d/%Y') for a date held as text.",
			},
		},
		"required":             []any{"id", "table", "name"},
		"additionalProperties": false,
	}
}

func strField(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// DocumentSchema returns the schema for a single mode-or-binding file: either
// shape is valid, discriminated by "kind".
func DocumentSchema() map[string]any {
	return map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"title":       "csq mode or binding document",
		"description": "One csq modes file. A mode declares an analysis; a binding maps one portal's datasets onto it.",
		"oneOf":       []any{ModeSchema(), BindingSchema()},
	}
}

// SchemaJSON renders the document schema as indented JSON.
func SchemaJSON() (string, error) {
	b, err := json.MarshalIndent(DocumentSchema(), "", "  ")
	if err != nil {
		return "", fmt.Errorf("render schema: %w", err)
	}
	return string(b) + "\n", nil
}
