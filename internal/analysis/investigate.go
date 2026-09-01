// Copyright (c) 2026 Neomantra Corp

package analysis

import (
	"context"
	"fmt"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/investigate"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// Investigate answers a civic question against the attached portal.
//
// The session owns three things the investigate package deliberately does not:
// the read-only host connection, the portal-to-binding resolution, and the
// knowledge of which tables actually hold rows. Wiring them here — the same
// place Confidence is wired — keeps investigate free of session concerns and
// testable against a bare database, and keeps this package the single answer
// to "what can the attached data support".
func (s *Session) Investigate(ctx context.Context, question string,
	opts investigate.Options) (*investigate.Report, error) {

	ports := s.snapshot()
	switch len(ports) {
	case 0:
		return nil, fmt.Errorf("no data is loaded yet")
	case 1:
	default:
		// An investigation reaches a verdict about one city. Attaching several
		// and answering for the first would be indefensible, and answering for
		// all of them is a different feature with a different verdict shape.
		return nil, fmt.Errorf(
			"an investigation covers one city; %d portals are attached", len(ports))
	}
	p := ports[0]

	// Routing has to happen before the binding can be looked up, because which
	// mode's binding applies depends on which investigation the question
	// reached. The result is handed to Run so the report carries the reasoning
	// that actually chose it.
	disc, err := route(question, opts)
	if err != nil {
		return nil, err
	}
	opts.Discovery = disc

	b, err := modes.LookupBinding(disc.Investigation.Mode, p.Portal)
	if err != nil {
		return nil, fmt.Errorf(
			"%q cannot be investigated for %s yet — %w", disc.Name, p.Portal, err)
	}
	if opts.Reproduce == "" {
		opts.Reproduce = fmt.Sprintf("csq investigate %q --db %s", question, p.Path)
	}

	return investigate.Run(ctx, s.host, question, p.Alias, b, s.tablePresence(p.Alias), opts)
}

// route resolves the question to an investigation, honouring an explicit
// choice when the caller made one.
func route(question string, opts investigate.Options) (*investigate.Discovery, error) {
	if opts.Discovery != nil {
		return opts.Discovery, nil
	}
	if opts.Investigation != "" {
		return investigate.DiscoverNamed(opts.Investigation, question)
	}
	return investigate.Discover(question)
}

// InvestigationStatus reports one investigation against the attached portal,
// without running it.
//
// The setup question — "could this question be answered here at all, and what
// would it take" — is worth answering separately from the question itself. For
// a large corpus the investigation is expensive and the answer is often "sync
// two datasets first", which nobody should have to wait for a full run to
// learn.
type InvestigationStatus struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Claim string `json:"claim"`
	About string `json:"about"`
	Mode  string `json:"mode"`
	City  string `json:"city,omitempty"`

	// Applicable reports that this portal is bound to the investigation's mode
	// at all — a different problem from an unsynced one, with a different fix.
	Applicable bool `json:"applicable"`
	// Ready reports that every indicator can run right now.
	Ready      bool                  `json:"ready"`
	Runnable   int                   `json:"runnable"`
	Total      int                   `json:"total"`
	Reason     string                `json:"reason,omitempty"`
	Readiness  investigate.Readiness `json:"readiness"`
	Indicators []IndicatorStatus     `json:"indicators"`
}

// IndicatorStatus is one probe and whether it can run.
type IndicatorStatus struct {
	Name    string `json:"name"`
	Asks    string `json:"asks"`
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`
	Fixable bool   `json:"fixable"`
}

// InvestigationStatuses reports every registered investigation against the
// attached portal.
func (s *Session) InvestigationStatuses(ctx context.Context) ([]InvestigationStatus, error) {
	ports := s.snapshot()
	if len(ports) != 1 {
		return nil, fmt.Errorf("an investigation covers one city; %d portals are attached",
			len(ports))
	}
	p := ports[0]
	have := s.tablePresence(p.Alias)

	all := investigate.All()
	out := make([]InvestigationStatus, 0, len(all))
	for _, inv := range all {
		st := InvestigationStatus{
			Name: inv.Name, Title: inv.Title, Claim: inv.Claim,
			About: inv.About, Mode: inv.Mode, Total: len(inv.Probes),
			Indicators: []IndicatorStatus{},
		}
		b, err := modes.LookupBinding(inv.Mode, p.Portal)
		if err != nil {
			st.Reason = fmt.Sprintf(
				"not available for %s — no dataset mapping exists for this portal yet",
				p.Portal)
			out = append(out, st)
			continue
		}
		st.Applicable, st.City = true, b.City

		plan, err := investigate.MakePlan(inv, p.Alias, b, have)
		if err != nil {
			st.Reason = err.Error()
			out = append(out, st)
			continue
		}
		st.Runnable = plan.Runnable
		st.Readiness = plan.Assess(inv, b)
		st.Ready = st.Readiness.Ready
		for _, pp := range plan.Probes {
			st.Indicators = append(st.Indicators, IndicatorStatus{
				Name: pp.Name, Asks: pp.Asks, Skipped: pp.Skipped,
				Reason: pp.Reason, Fixable: pp.Fixable,
			})
		}
		if !st.Ready {
			if st.Runnable == 0 {
				st.Reason = "no indicator can run against the data held"
			} else {
				st.Reason = fmt.Sprintf("%d of %d indicators can run",
					st.Runnable, st.Total)
			}
			if len(st.Readiness.Missing) > 0 {
				st.Reason += "; missing " + strings.Join(st.Readiness.Missing, ", ")
			}
		}
		out = append(out, st)
	}
	return out, nil
}
