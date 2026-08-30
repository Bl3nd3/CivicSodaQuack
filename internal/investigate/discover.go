// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// Discovery is step one: which investigation a question is asking for, about
// which place, and on what evidence that was decided.
//
// The reasoning is returned rather than logged because routing is the one step
// where a wrong answer is invisible in the output. Every later step reports its
// own uncertainty; a question routed to the wrong investigation produces a
// confident, well-caveated verdict about something the reader did not ask.
type Discovery struct {
	Investigation *Investigation `json:"-"`
	Name          string         `json:"investigation"`
	Title         string         `json:"title"`
	// Matched are the question's terms that routed it here, strongest first.
	Matched []string `json:"matched"`
	Score   float64  `json:"score"`
	// Place is the city named in the question, empty when it named none.
	Place string `json:"place,omitempty"`
	// Alternatives are the investigations that also matched, for a reader who
	// wants to know what else this could have been.
	Alternatives []Candidate `json:"alternatives,omitempty"`
}

// Candidate is one investigation the question could have meant.
type Candidate struct {
	Name    string   `json:"name"`
	Score   float64  `json:"score"`
	Matched []string `json:"matched,omitempty"`
}

// AmbiguousError means two investigations matched a question equally well.
//
// Guessing between them would be the worst available option: both would produce
// a confident verdict, and the reader has no way to see that the question was
// read two ways. Asking costs one flag.
type AmbiguousError struct {
	Question   string
	Candidates []Candidate
}

func (e *AmbiguousError) Error() string {
	names := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		names = append(names, c.Name)
	}
	return fmt.Sprintf(
		"that question matches %s equally well; pick one with --investigation",
		strings.Join(names, " and "))
}

// NoMatchError means nothing in the registry covers the question.
type NoMatchError struct{ Question string }

func (e *NoMatchError) Error() string {
	return fmt.Sprintf(
		"no investigation covers that question (have: %s)\n"+
			"  investigations are curated, not generated — csq will not invent an "+
			"analysis it cannot caveat",
		strings.Join(Names(), ", "))
}

// Discover routes a question to an investigation.
//
// A term's weight is how few investigations claim it: a word declared by one
// investigation discriminates between them, and a word declared by all of them
// discriminates between none. That is measured over the registry rather than
// chosen, so adding an investigation re-weights the vocabulary automatically
// and there is no table of importances to keep honest.
func Discover(question string) (*Discovery, error) {
	q := normalise(question)
	weights := termWeights()

	var scored []Candidate
	byName := map[string]*Investigation{}
	for _, inv := range registry {
		byName[inv.Name] = inv
		var matched []string
		var score float64
		for _, term := range inv.Match {
			if !containsTerm(q, term) {
				continue
			}
			matched = append(matched, term)
			score += weights[strings.ToLower(term)]
		}
		if score <= 0 {
			continue
		}
		// Strongest term first, so the reported reasoning leads with the word
		// that actually decided it.
		sort.SliceStable(matched, func(i, j int) bool {
			return weights[strings.ToLower(matched[i])] > weights[strings.ToLower(matched[j])]
		})
		scored = append(scored, Candidate{Name: inv.Name, Score: round2(score), Matched: matched})
	}

	if len(scored) == 0 {
		return nil, &NoMatchError{Question: question}
	}
	sort.SliceStable(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > 1 && scored[0].Score == scored[1].Score {
		tied := []Candidate{scored[0], scored[1]}
		return nil, &AmbiguousError{Question: question, Candidates: tied}
	}

	top := scored[0]
	inv := byName[top.Name]
	return &Discovery{
		Investigation: inv,
		Name:          inv.Name,
		Title:         inv.Title,
		Matched:       top.Matched,
		Score:         top.Score,
		Place:         placeIn(q),
		Alternatives:  scored[1:],
	}, nil
}

// DiscoverNamed skips routing for a caller that already knows which
// investigation it wants, while still reporting the place the question names.
func DiscoverNamed(name, question string) (*Discovery, error) {
	inv, err := Lookup(name)
	if err != nil {
		return nil, err
	}
	return &Discovery{
		Investigation: inv,
		Name:          inv.Name,
		Title:         inv.Title,
		Matched:       []string{"(named explicitly)"},
		Place:         placeIn(normalise(question)),
	}, nil
}

// termWeights measures how discriminating each match term is, as the reciprocal
// of the number of investigations declaring it.
func termWeights() map[string]float64 {
	count := map[string]int{}
	for _, inv := range registry {
		seen := map[string]bool{}
		for _, t := range inv.Match {
			t = strings.ToLower(t)
			if seen[t] {
				continue
			}
			seen[t] = true
			count[t]++
		}
	}
	out := make(map[string]float64, len(count))
	for t, n := range count {
		out[t] = 1.0 / float64(n)
	}
	return out
}

// placeIn finds a city named in the question, using the labels the bindings
// already carry. csq knows the cities it can answer for; asking the question
// text to spell a portal hostname would be absurd.
func placeIn(q string) string {
	best := ""
	for _, label := range knownPlaces() {
		if !containsTerm(q, strings.ToLower(label)) {
			continue
		}
		// Longest wins, so "New York" is not shadowed by a shorter label that
		// happens to be a substring of the same phrase.
		if len(label) > len(best) {
			best = label
		}
	}
	return best
}

// knownPlaces collects every place label the registry could investigate: the
// city names from every binding of every investigated mode, plus the bare first
// word of each, since nobody types "Chicago, IL".
func knownPlaces() []string {
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[strings.ToLower(s)] {
			return
		}
		seen[strings.ToLower(s)] = true
		out = append(out, s)
	}
	for _, inv := range registry {
		for _, b := range modes.BindingsFor(inv.Mode) {
			add(b.City)
			if city, _, ok := strings.Cut(b.City, ","); ok {
				add(city)
			}
		}
	}
	return out
}

// normalise lowercases and collapses punctuation to spaces, so term matching
// sees "policing?" and "policing" alike.
func normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	b.WriteByte(' ')
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte(' ')
		}
	}
	b.WriteByte(' ')
	return b.String()
}

// containsTerm reports whether a normalised question contains a term as whole
// words. Substring matching would route any question mentioning "permitting" to
// the permits probe and any mention of "policy" to policing.
func containsTerm(normalisedQuestion, term string) bool {
	t := strings.TrimSpace(normalise(term))
	if t == "" {
		return false
	}
	return strings.Contains(normalisedQuestion, " "+t+" ")
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
