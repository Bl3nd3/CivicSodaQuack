// Copyright (c) 2026 Neomantra Corp

package patterns

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/personal"
)

// Asking a question in English without a model is a much smaller problem than
// it looks, provided the answer space stays small.
//
// csq is not translating a question into arbitrary SQL — that is the brittle
// version, and the one where a plausible-looking wrong query is the failure
// mode. It is choosing among six reviewed shapes and the columns of one table
// the user already holds. That is a ranking problem over a few dozen
// candidates, which keyword scoring handles honestly.
//
// Three rules keep it from becoming the brittle version:
//
//   - It shows its reasoning. Every choice comes back with the word that
//     produced it, so a wrong guess is visible before anything is saved rather
//     than discovered in a result.
//   - It refuses rather than guesses. A question that matches no pattern, or a
//     role with no plausible column, is reported as such and the user is handed
//     the explicit `csq modes add` command instead. A cache that misses is
//     fine; a router that invents is not.
//   - It never writes SQL. The most it can do is pick the wrong template, and
//     the template it picks was still written and reviewed by a person.

// Suggestion is what the router proposes for a question.
type Suggestion struct {
	Pattern *Pattern
	Table   personal.Table
	Columns map[Role]string

	// Reasons records why each choice was made, keyed by "pattern", "table",
	// or a role name. Shown to the user before anything is written.
	Reasons map[string]string
	// Warnings are things the user should notice about the match — most often
	// that the question asks for something the chosen shape does not do.
	Warnings []string
	// DateFormat is set when the chosen date column holds text and the question
	// gave no way to read it; it stays empty and the caller must ask.
	NeedsDateFormat bool

	// Runners-up, for "did you mean".
	Alternatives []string
}

// Suggest picks a pattern, a table, and a column for each role.
//
// An error means csq could not choose honestly — the message says what was
// missing and how to say it explicitly.
func Suggest(question string, inv *personal.Portal, preferTable string) (*Suggestion, error) {
	terms := tokenise(question)
	if len(terms) == 0 {
		return nil, fmt.Errorf("the question has no words csq can match on")
	}
	if inv == nil || len(inv.Tables) == 0 {
		return nil, fmt.Errorf("this database holds no tables to ask about")
	}

	p, patternWhy, alts, err := choosePattern(terms)
	if err != nil {
		return nil, err
	}

	table, tableWhy, err := chooseTable(terms, inv, preferTable, p)
	if err != nil {
		return nil, err
	}

	s := &Suggestion{
		Pattern: p, Table: table,
		Columns:      map[Role]string{},
		Reasons:      map[string]string{"pattern": patternWhy, "table": tableWhy},
		Alternatives: alts,
	}

	// Assign columns role by role, never reusing one: a query grouping a column
	// by itself is always a mistake, and it is an easy one for a scorer to make
	// when one column happens to look good for two roles.
	used := map[string]bool{}
	for _, param := range p.Params {
		col, why, ok := chooseColumn(terms, table, param, used)
		if !ok {
			if param.Required {
				return nil, fmt.Errorf(
					"csq matched your question to the %s pattern, but could not tell which "+
						"column in %q is the %s.\n  Say it explicitly:\n    csq modes add %s "+
						"--table %s %s <column> ...\n  'csq modes tables' lists the columns.",
					p.Name, table.Name, param.Role, p.Name, table.Name, param.Flag)
			}
			continue
		}
		s.Columns[param.Role] = col
		s.Reasons[string(param.Role)] = why
		used[strings.ToLower(col)] = true

		if param.Temporal {
			if t, _ := columnType(table, col); !isTemporalType(t) {
				s.NeedsDateFormat = true
			}
		}
	}

	s.Warnings = append(s.Warnings, warningsFor(terms, p)...)
	return s, nil
}

// choosePattern scores the six shapes against the question's words.
func choosePattern(terms []string) (*Pattern, string, []string, error) {
	type scored struct {
		p      *Pattern
		score  float64
		reason string
	}
	var ranked []scored

	for _, p := range All() {
		score, hit := scoreKeywords(terms, patternKeywords[p.Name])
		if score <= 0 {
			continue
		}
		ranked = append(ranked, scored{p, score, fmt.Sprintf("matched %q", hit)})
	}
	if len(ranked) == 0 {
		return nil, "", nil, fmt.Errorf(
			"csq could not tell which kind of analysis this question asks for.\n"+
				"  It matches questions to these shapes by keyword:\n%s\n"+
				"  Rephrase using one of those words, or pick a shape yourself:\n"+
				"    csq modes patterns\n"+
				"    csq modes add <pattern> --db <file> --table <name> ...",
			patternHintBlock())
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	// A tie is a genuine ambiguity, and picking either would be a coin flip
	// presented as an answer.
	if len(ranked) > 1 && ranked[0].score == ranked[1].score {
		return nil, "", nil, fmt.Errorf(
			"this question matches %s and %s equally well, so csq will not choose "+
				"between them.\n  Say which you want:\n    csq modes add %s --db <file> "+
				"--table <name> ...\n  'csq modes patterns show <name>' describes each.",
			ranked[0].p.Name, ranked[1].p.Name, ranked[0].p.Name)
	}

	var alts []string
	for _, r := range ranked[1:] {
		alts = append(alts, r.p.Name)
	}
	return ranked[0].p, ranked[0].reason, alts, nil
}

// chooseTable prefers a table the question names, then one whose columns look
// like what the question is about.
//
// Column evidence matters more than the table's own name and is why this is not
// just string matching on table names: "which vendors got the most money" names
// neither `contracts` nor anything in its description, but the table holding
// `vendor_name` and `award_amount` is unmistakably the right one.
func chooseTable(terms []string, inv *personal.Portal, prefer string, p *Pattern) (personal.Table, string, error) {
	if prefer != "" {
		t, ok := inv.Table(prefer)
		if !ok {
			return personal.Table{}, "", fmt.Errorf("no table %q here; this database holds: %s",
				prefer, strings.Join(inv.TableNames(), ", "))
		}
		return t, "you named it with --table", nil
	}
	if len(inv.Tables) == 1 {
		return inv.Tables[0], "the only table in this database", nil
	}

	type scored struct {
		t      personal.Table
		score  float64
		reason string
	}
	var ranked []scored

	for _, t := range inv.Tables {
		var score float64
		var reason string

		if s, hit := scoreKeywords(terms, tokenise(t.Name+" "+t.DatasetName)); s > 0 {
			score += s * 2
			reason = fmt.Sprintf("its name matches %q", hit)
		}
		if s, hit := scoreKeywords(terms, tokenise(t.Description)); s > 0 {
			score += s
			if reason == "" {
				reason = fmt.Sprintf("its description matches %q", hit)
			}
		}
		// Column evidence: how well this table can fill the pattern's roles.
		var colScore float64
		var colHit string
		for _, c := range t.Columns {
			s, hit := scoreKeywords(terms, tokeniseIdent(c.Name))
			if s > colScore {
				colScore, colHit = s, hit
			}
		}
		if colScore > 0 {
			score += colScore * 3
			if reason == "" || colScore*3 > 2 {
				reason = fmt.Sprintf("it has a column matching %q", colHit)
			}
		}
		// A table that cannot supply a required role is not a candidate at all.
		if !canFillRequired(t, p) {
			continue
		}
		if score <= 0 {
			continue
		}
		ranked = append(ranked, scored{t, score, reason})
	}

	if len(ranked) == 0 {
		return personal.Table{}, "", fmt.Errorf(
			"csq could not tell which table this question is about.\n"+
				"  Name it with --table. This database holds: %s",
			strings.Join(inv.TableNames(), ", "))
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	if len(ranked) > 1 && ranked[0].score == ranked[1].score {
		return personal.Table{}, "", fmt.Errorf(
			"this question fits %q and %q equally well.\n  Name one with --table.",
			ranked[0].t.Name, ranked[1].t.Name)
	}
	return ranked[0].t, ranked[0].reason, nil
}

// canFillRequired reports whether a table has at least one plausible column for
// every required role, so a table that cannot answer is never proposed.
func canFillRequired(t personal.Table, p *Pattern) bool {
	used := map[string]bool{}
	for _, param := range p.Params {
		if !param.Required {
			continue
		}
		col, _, ok := chooseColumn(nil, t, param, used)
		if !ok {
			return false
		}
		used[strings.ToLower(col)] = true
	}
	return true
}

// chooseColumn picks the column that best fills one role.
//
// Two kinds of evidence are combined: the column's declared type, which is hard
// evidence a scorer should weigh heavily, and its name, which is soft. A
// numeric column is a far better measure candidate than a VARCHAR one whatever
// either is called, and a scorer that ignored types would happily sum a
// telephone number.
func chooseColumn(terms []string, t personal.Table, param Param, used map[string]bool) (string, string, bool) {
	type scored struct {
		name   string
		score  float64
		reason string
	}
	var ranked []scored

	for _, c := range t.Columns {
		if used[strings.ToLower(c.Name)] {
			continue
		}
		score, reason := scoreColumnForRole(terms, c, param)
		if score <= 0 {
			continue
		}
		ranked = append(ranked, scored{c.Name, score, reason})
	}
	if len(ranked) == 0 {
		return "", "", false
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })
	return ranked[0].name, ranked[0].reason, true
}

func scoreColumnForRole(terms []string, c personal.Column, param Param) (float64, string) {
	name := tokeniseIdent(c.Name)
	var score float64
	var reason string

	// Type evidence first: it either qualifies the column or rules it out.
	switch {
	case param.Numeric:
		if isNumericType(c.Type) {
			score += 3
			reason = "the only numeric-typed candidate"
		} else if isTextType(c.Type) {
			// Text holding money is the civic-data norm, so this stays a
			// candidate — but a weak one that needs name evidence to win.
			score += 0.5
		} else {
			return 0, ""
		}
	case param.Temporal:
		if isTemporalType(c.Type) {
			score += 3
			reason = "a date-typed column"
		} else if isTextType(c.Type) {
			score += 0.5
		} else {
			return 0, ""
		}
	default:
		// Entity, category and group are all text-shaped.
		if isTextType(c.Type) {
			score += 1
		} else if isNumericType(c.Type) || isTemporalType(c.Type) {
			// A number can name a thing (a badge, a ward), but rarely.
			score += 0.1
		}
	}

	// Name evidence against the role's own vocabulary.
	if s, hit := scoreKeywords(name, roleKeywords[param.Role]); s > 0 {
		score += s * 3
		reason = fmt.Sprintf("its name looks like a %s (%q)", param.Role, hit)
	}

	// Name evidence against the user's own words, expanded through the civic
	// synonym table. This is what makes "money" land on award_amount.
	if len(terms) > 0 {
		if s, hit := scoreKeywords(expand(terms), name); s > 0 {
			score += s * 2.5
			reason = fmt.Sprintf("matched %q in your question", hit)
		}
	}
	if score <= 0 {
		return 0, ""
	}
	if reason == "" {
		reason = fmt.Sprintf("best remaining %s candidate", param.Role)
	}
	return score, reason
}

// warningsFor flags a question asking for something the chosen shape does not
// do. The commonest is asking for the smallest when every ranking pattern
// orders largest-first.
func warningsFor(terms []string, p *Pattern) []string {
	var out []string
	if p.Name != topN.Name && p.Name != concentration.Name {
		return nil
	}
	for _, t := range terms {
		switch t {
		case "least", "lowest", "smallest", "fewest", "bottom", "minimum":
			out = append(out, fmt.Sprintf(
				"your question says %q, but the %s pattern ranks largest-first. "+
					"The rows you want are at the *end* of the full result, not in this "+
					"top slice — edit the saved query's ORDER BY to ASC if you need them.",
				t, p.Name))
			return out
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// Scoring primitives.

// scoreKeywords returns how strongly a term list matches a vocabulary, and the
// term that matched best.
//
// A longer match scores higher, so "over time" beats a bare "time": a
// two-word phrase is far more likely to be deliberate than a single common word.
func scoreKeywords(terms []string, vocab []string) (float64, string) {
	if len(terms) == 0 || len(vocab) == 0 {
		return 0, ""
	}
	set := map[string]bool{}
	for _, t := range terms {
		set[t] = true
	}
	// Bigrams let a vocabulary contain phrases.
	for i := 0; i+1 < len(terms); i++ {
		set[terms[i]+" "+terms[i+1]] = true
	}

	var best float64
	var bestTerm string
	var total float64
	for _, v := range vocab {
		if !set[v] {
			continue
		}
		w := 1.0
		if strings.Contains(v, " ") {
			w = 2.5 // a phrase is stronger evidence than a word
		} else if len(v) <= 3 {
			w = 0.5 // short tokens collide too easily to trust
		}
		total += w
		if w > best {
			best, bestTerm = w, v
		}
	}
	return total, bestTerm
}

var stopwords = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "in": true, "on": true,
	"for": true, "to": true, "and": true, "or": true, "is": true, "are": true,
	"was": true, "were": true, "what": true, "which": true, "who": true,
	"whom": true, "show": true, "me": true, "my": true, "list": true,
	"give": true, "get": true, "got": true, "find": true, "i": true,
	"do": true, "does": true, "did": true, "it": true, "its": true,
	"that": true, "this": true, "there": true, "here": true, "with": true,
	"by": true, "from": true, "at": true, "as": true, "be": true, "been": true,
	"has": true, "have": true, "had": true, "can": true, "could": true,
	"would": true, "should": true, "any": true, "all": true, "each": true,
}

// tokenise lowercases, splits on anything that is not a letter or digit, drops
// stopwords, and lightly singularises.
func tokenise(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9')
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if stopwords[f] || f == "" {
			continue
		}
		out = append(out, singular(f))
	}
	return out
}

// tokeniseIdent splits a column or table name into words, handling both
// snake_case and camelCase, since portals use each about equally.
func tokeniseIdent(s string) []string {
	var spaced strings.Builder
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' &&
			(runes[i-1] < 'A' || runes[i-1] > 'Z') {
			spaced.WriteRune(' ')
		}
		spaced.WriteRune(r)
	}
	return tokenise(spaced.String())
}

// singular strips a trailing plural 's', which is enough for the vocabulary
// here and avoids pulling in a stemmer that would also mangle real column names.
func singular(s string) string {
	switch {
	case strings.HasSuffix(s, "ies") && len(s) > 4:
		return s[:len(s)-3] + "y"
	case strings.HasSuffix(s, "ses") && len(s) > 4:
		return s[:len(s)-2]
	case strings.HasSuffix(s, "s") && !strings.HasSuffix(s, "ss") && len(s) > 3:
		return s[:len(s)-1]
	}
	return s
}

// expand adds civic-data synonyms for the user's words, so a question asked in
// plain English can match a column named in agency shorthand.
func expand(terms []string) []string {
	out := append([]string(nil), terms...)
	for _, t := range terms {
		out = append(out, synonyms[t]...)
	}
	return out
}

// synonyms maps how people ask onto how portals name things. It is deliberately
// small and hand-written: every entry is a claim that two words mean the same
// thing in civic data, and a wrong one produces a confidently wrong column.
var synonyms = map[string][]string{
	"money":    {"amount", "amt", "total", "cost", "value", "paid", "payment", "dollar", "price", "spend", "fee", "budget", "award"},
	"paid":     {"amount", "amt", "total", "payment", "paid", "cost"},
	"pay":      {"amount", "amt", "total", "payment", "salary", "compensation"},
	"spend":    {"amount", "amt", "total", "cost", "expenditure", "spend"},
	"spending": {"amount", "amt", "total", "cost", "expenditure"},
	"cost":     {"amount", "amt", "cost", "total", "price", "value"},
	"salary":   {"salary", "compensation", "pay", "rate", "wage"},
	"value":    {"amount", "amt", "value", "total"},
	"worth":    {"amount", "amt", "value", "total"},
	"award":    {"award", "amount", "amt", "total"},

	"vendor":     {"vendor", "supplier", "contractor", "company", "firm", "payee", "business"},
	"supplier":   {"vendor", "supplier", "contractor", "company"},
	"contractor": {"contractor", "vendor", "company", "firm"},
	"company":    {"company", "vendor", "firm", "business", "corporation"},
	"firm":       {"firm", "company", "vendor"},
	"business":   {"business", "company", "vendor"},
	"lobbyist":   {"lobbyist", "lobby", "registrant"},
	"recipient":  {"recipient", "payee"},
	"employee":   {"employee", "title", "position"},
	"officer":    {"officer", "badge", "star", "member"},

	"department":   {"department", "dept", "agency", "division", "bureau", "office"},
	"agency":       {"agency", "department", "dept", "bureau", "office"},
	"division":     {"division", "department", "dept", "unit"},
	"ward":         {"ward", "district", "precinct", "beat", "area"},
	"district":     {"district", "ward", "precinct", "area", "borough"},
	"neighborhood": {"neighborhood", "community", "area", "district", "ward"},
	"community":    {"community", "neighborhood", "area", "district"},
	"borough":      {"borough", "district", "area"},
	"zip":          {"zip", "postal", "zipcode"},

	"date":  {"date", "time", "day", "issued", "awarded", "filed", "created", "start"},
	"when":  {"date", "time", "issued", "awarded", "filed", "created"},
	"year":  {"year", "date", "annual"},
	"month": {"month", "date", "monthly"},
	"time":  {"date", "time", "timestamp"},

	"type":     {"type", "kind", "category", "class", "code"},
	"kind":     {"kind", "type", "category", "class"},
	"category": {"category", "type", "kind", "class", "code"},
	"status":   {"status", "state", "disposition", "outcome", "result"},
	"outcome":  {"outcome", "status", "disposition", "finding", "result"},

	"permit":    {"permit", "license", "application"},
	"complaint": {"complaint", "case", "allegation", "incident"},
	"crime":     {"crime", "offense", "offence", "incident", "arrest"},
	"contract":  {"contract", "award", "procurement", "purchase"},
	"request":   {"request", "service", "ticket", "case"},
}

// patternKeywords is the vocabulary that routes a question to a shape. Phrases
// are included because they are far stronger evidence than their words.
var patternKeywords = map[string][]string{
	"top-n": {
		"most", "top", "biggest", "largest", "highest", "leading", "rank", "ranking",
		"ranked", "best", "worst", "least", "lowest", "smallest", "fewest",
		"how much", "who received", "who got", "total by", "sum by", "per vendor",
	},
	"concentration": {
		"share", "concentration", "concentrated", "dominate", "dominant", "monopoly",
		"outsized", "percentage", "percent", "proportion", "bulk", "captured",
		"share of", "most of", "how concentrated", "single vendor", "one vendor",
	},
	"trend": {
		"trend", "trending", "over time", "by month", "by year", "monthly", "yearly",
		"annual", "growing", "growth", "rising", "rise", "falling", "fall", "decline",
		"increase", "increasing", "decrease", "decreasing", "history", "historical",
		"seasonal", "each month", "per month", "per year", "changed over",
	},
	"breakdown": {
		"breakdown", "break down", "distribution", "split", "composition",
		"by type", "by category", "by status", "by kind", "how many of",
		"what kind", "what type", "categories", "proportion of",
	},
	"coverage": {
		"missing", "null", "empty", "blank", "incomplete", "complete", "completeness",
		"coverage", "populated", "quality", "data quality", "how good", "how complete",
		"gaps", "gap", "reliable",
	},
	"name-variants": {
		"duplicate", "duplicated", "duplication", "spelling", "spellings", "variant",
		"variants", "variation", "misspell", "misspelled", "same name", "dedupe",
		"deduplicate", "spelled", "inconsistent",
	},
}

// roleKeywords is the vocabulary for what a column *is*, independent of the
// question. It is what lets csq propose a sensible column even when the user's
// wording gives no clue.
var roleKeywords = map[Role][]string{
	RoleEntity: {
		"name", "vendor", "supplier", "contractor", "company", "firm", "business",
		"payee", "recipient", "applicant", "owner", "lobbyist", "officer", "employee",
		"title", "organization", "organisation", "agency", "person",
	},
	RoleMeasure: {
		"amount", "amt", "total", "cost", "value", "price", "paid", "payment",
		"sum", "fee", "dollar", "budget", "spend", "salary", "compensation",
		"award", "revenue", "expenditure", "balance", "count", "quantity",
	},
	RoleDate: {
		"date", "time", "timestamp", "day", "issued", "awarded", "filed", "created",
		"received", "reported", "start", "end", "opened", "closed", "completion",
	},
	RoleCategory: {
		"type", "kind", "category", "class", "status", "code", "disposition",
		"outcome", "result", "classification", "group", "level", "stage",
	},
	RoleGroup: {
		"department", "dept", "agency", "division", "bureau", "office", "unit",
		"district", "ward", "borough", "precinct", "beat", "area", "region",
		"community", "neighborhood", "category", "type",
	},
}

func patternHintBlock() string {
	var b strings.Builder
	for _, p := range All() {
		kws := patternKeywords[p.Name]
		show := kws
		if len(show) > 6 {
			show = show[:6]
		}
		fmt.Fprintf(&b, "    %-14s %s\n", p.Name, strings.Join(show, ", "))
	}
	return b.String()
}

// CommandLine renders the explicit `csq modes add` invocation equivalent to
// this suggestion, so a user who wants to adjust one column can copy it rather
// than reconstruct it.
func (s *Suggestion) CommandLine(dbPath, modeName string) string {
	parts := []string{"csq modes add", s.Pattern.Name,
		"--db", dbPath, "--table", s.Table.Name}
	for _, param := range s.Pattern.Params {
		if col, ok := s.Columns[param.Role]; ok && col != "" {
			parts = append(parts, param.Flag, col)
		}
	}
	if s.NeedsDateFormat {
		parts = append(parts, "--date-format", "'%m/%d/%Y'")
	}
	if modeName != "" && modeName != "personal" {
		parts = append(parts, "--as", modeName)
	}
	return strings.Join(parts, " ")
}
