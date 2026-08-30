// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// Options tunes a run.
type Options struct {
	// Investigation names one explicitly, skipping the routing step.
	Investigation string
	// Discovery supplies an already-routed question, for a caller that had to
	// route it first in order to find the right binding. Passing the result
	// back keeps the reasoning that actually chose the investigation in the
	// report, rather than a second routing pass that only confirms the name.
	Discovery *Discovery
	// Now overrides the clock, for tests and for reproducing an old run.
	Now time.Time
	// Reproduce is the command a reader can run to get this report again.
	Reproduce string
}

func (o Options) now() time.Time {
	if o.Now.IsZero() {
		return time.Now()
	}
	return o.Now
}

// WrongCityError means the question named a place the attached database cannot
// answer for.
//
// This is an error rather than a warning because the failure it prevents is
// silent and total: every step downstream would run perfectly and produce a
// fully caveated verdict about the wrong city, and nothing in the output would
// look wrong.
type WrongCityError struct {
	Asked    string
	Attached string
	Portal   string
}

func (e *WrongCityError) Error() string {
	return fmt.Sprintf(
		"the question asks about %s but the attached database is %s (%s)\n"+
			"  attach that city's portal instead, or drop the place name to "+
			"investigate what is attached",
		e.Asked, e.Attached, e.Portal)
}

// Run carries a question through all seven steps and returns the report.
//
// The steps are sequenced here and nowhere else, so the order that makes the
// output trustworthy — indicators fixed before data is read, coverage measured
// before a series is compared, findings attacked before they are counted —
// is one function long and can be checked by reading it.
func Run(ctx context.Context, q confidence.Queryer, question, alias string,
	b *modes.Binding, have TableAvailable, opts Options) (*Report, error) {

	now := opts.now()

	// 1 — Discover: what is being asked, and about where.
	var disc *Discovery
	var err error
	switch {
	case opts.Discovery != nil:
		disc = opts.Discovery
	case opts.Investigation != "":
		disc, err = DiscoverNamed(opts.Investigation, question)
	default:
		disc, err = Discover(question)
	}
	if err != nil {
		return nil, err
	}
	if disc == nil || disc.Investigation == nil {
		return nil, fmt.Errorf("no investigation was resolved for that question")
	}
	inv := disc.Investigation

	if b == nil {
		return nil, fmt.Errorf("investigation %q has no dataset mapping for this portal;"+
			" a binding maps its concepts onto one portal's datasets", inv.Name)
	}
	if disc.Place != "" && !sameCity(disc.Place, b.City) {
		return nil, &WrongCityError{Asked: disc.Place, Attached: b.City, Portal: b.Portal}
	}

	// 2 — Plan: which indicators, and which direction of each would count.
	plan, err := MakePlan(inv, alias, b, have)
	if err != nil {
		return nil, err
	}

	// 3 — Sync: is the evidence the plan needs actually held?
	readiness := plan.Assess(inv, b)

	// 4 — Validate: how far can each dataset be trusted, and where does it stop?
	validation := Validate(ctx, q, inv, plan, alias, b, now)

	// 5 — Analyze: run the indicators and measure what moved.
	analysis := Analyze(ctx, q, inv, plan, validation)

	// 6 — Challenge: try to explain each movement away.
	Challenge(inv, plan, validation, analysis)

	// 7 — Explain: the verdict, the confidence, and everything needed to
	// disagree with both.
	r := &Report{
		Question: question, Asked: now, Discovery: disc,
		Investigation: inv.Name, Title: inv.Title, Claim: inv.Claim,
		City: b.City, Portal: b.Portal,
		Plan: plan, Readiness: readiness, Validation: validation, Analysis: analysis,
		Snapshot:  snapshotName(ctx, q, alias, b.City, now),
		Reproduce: opts.Reproduce,
	}
	r.finalize()
	r.AddCaveats(inv.Caveats...)
	r.AddCaveats(b.Notes...)
	return r, nil
}

// sameCity compares a place from a question with a binding's label, so
// "Chicago" matches "Chicago, IL" without matching "Chicago Heights".
func sameCity(asked, bound string) bool {
	a := strings.ToLower(strings.TrimSpace(asked))
	bl := strings.ToLower(strings.TrimSpace(bound))
	if a == bl {
		return true
	}
	if city, _, ok := strings.Cut(bl, ","); ok {
		return a == strings.TrimSpace(city)
	}
	return false
}

// snapshotName identifies the corpus this verdict was drawn from: the city and
// the date its data was last successfully synced.
//
// It is the reproduction key. Re-running this investigation against a corpus
// carrying the same name should produce the same verdict, and against a
// different one it legitimately may not — civic portals revise history, and a
// report that could not say which version of the data it read would be
// unreproducible in a way nobody would notice.
func snapshotName(ctx context.Context, q confidence.Queryer, alias, city string, now time.Time) string {
	slug := citySlug(city)
	stamp := now.Format("2006-01-02")

	rows, err := q.QueryContext(ctx, fmt.Sprintf(
		`SELECT MAX(finished_at) FROM %s._csq.sync_runs WHERE status = 'ok'`, alias))
	if err == nil {
		defer rows.Close()
		if rows.Next() {
			var v any
			if rows.Scan(&v) == nil {
				if t, ok := asTime(v); ok {
					stamp = t.Format("2006-01-02")
				}
			}
		}
	}
	return slug + "-" + stamp
}

// citySlug reduces a binding label to a filename-safe name.
func citySlug(city string) string {
	s := city
	if head, _, ok := strings.Cut(city, ","); ok {
		s = head
	}
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "portal"
	}
	return out
}
