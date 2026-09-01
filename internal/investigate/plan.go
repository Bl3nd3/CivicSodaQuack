// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"fmt"
	"sort"
	"strings"

	"github.com/neomantra/CivicSodaQuack/internal/confidence"
	"github.com/neomantra/CivicSodaQuack/internal/modes"
)

// PlannedProbe is one indicator and what was decided about it before any data
// was read.
//
// Supports is carried here, on the plan, rather than being decided when the
// numbers come back. That is the whole point of having a plan: the direction
// that would count as evidence is fixed in advance, so a fall cannot be
// reported as damning and a rise as reassuring by the same investigation.
type PlannedProbe struct {
	Name string `json:"name"`
	Asks string `json:"asks"`
	// Supports is the movement that would support the claim.
	Supports Direction `json:"supports"`
	Unit     string    `json:"unit"`
	Per      string    `json:"per,omitempty"`

	// SQL is the expanded statement, exactly what will run.
	SQL string `json:"sql"`
	// Tables are the local tables this probe reads.
	Tables []string `json:"tables"`
	// Concept and PeriodColumn locate the series in time for Validate.
	Concept      string `json:"concept"`
	PeriodColumn string `json:"period_column"`

	// Skipped and Reason record an indicator that will not run, and why. A
	// skipped probe is kept on the plan rather than dropped: an investigation
	// that quietly answers three of five questions and reports a verdict is
	// making a claim it did not test.
	Skipped bool   `json:"skipped"`
	Reason  string `json:"reason,omitempty"`
	// Fixable marks a skip that a sync would resolve, as opposed to one the
	// portal will never satisfy.
	Fixable bool `json:"fixable"`

	probe   Probe
	targets []confidence.Target
}

// Plan is step two: the indicators this investigation will actually run
// against this city, settled before a number is seen.
type Plan struct {
	Investigation string `json:"investigation"`
	Claim         string `json:"claim"`
	Mode          string `json:"mode"`
	City          string `json:"city"`
	Portal        string `json:"portal"`

	Probes []PlannedProbe `json:"probes"`

	// Runnable and Total drive the coverage half of the confidence figure.
	Runnable int `json:"runnable"`
	Total    int `json:"total"`
}

// Runnable and Total describe what the plan could attempt, and are what the
// readiness report is built from. They are deliberately not the coverage term
// in the confidence figure: a probe can be runnable and still settle nothing,
// and only the report knows which ones did. See Report.answered.

// Targets collects the confidence targets for every probe that will run, so
// the datasets behind the answer are profiled and the ones behind a skipped
// question are not.
func (p *Plan) Targets() []confidence.Target {
	seen := map[string]bool{}
	var out []confidence.Target
	for _, pp := range p.Probes {
		if pp.Skipped {
			continue
		}
		for _, t := range pp.targets {
			key := t.Portal + "." + t.Table + "|" + strings.Join(t.Columns, ",")
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, t)
		}
	}
	return out
}

// TableAvailable reports whether a local table exists and holds rows. It is
// the modes package's own type, re-exported here so callers wire one function.
type TableAvailable = modes.TableAvailable

// MakePlan resolves an investigation against one city's binding.
//
// Every runnability decision is delegated to the mode rather than reimplemented:
// whether a concept is bound, whether the columns a probe reads exist on this
// portal, whether the tables have been synced. An investigation that decided
// those for itself would eventually disagree with `csq modes run` about what a
// city can answer, and the disagreement would surface as a confident verdict
// built on a table the mode knows is missing.
func MakePlan(inv *Investigation, alias string, b *modes.Binding, have TableAvailable) (*Plan, error) {
	m, err := modes.Lookup(inv.Mode)
	if err != nil {
		return nil, fmt.Errorf("investigation %q needs mode %q: %w", inv.Name, inv.Mode, err)
	}
	if b == nil {
		return nil, fmt.Errorf("investigation %q has no binding for this portal", inv.Name)
	}

	plan := &Plan{
		Investigation: inv.Name, Claim: inv.Claim, Mode: m.Name,
		City: b.City, Portal: b.Portal, Total: len(inv.Probes),
	}

	for _, probe := range inv.Probes {
		q := modes.Query{Name: probe.Name, Desc: probe.Asks, SQL: probe.SQL}
		pp := PlannedProbe{
			Name: probe.Name, Asks: probe.Asks, Supports: probe.Supports,
			Unit: probe.Unit, Per: probe.Per,
			Concept: probe.Concept, PeriodColumn: probe.PeriodColumn,
			probe: probe,
		}

		// Two separate questions with two different remedies. "This portal does
		// not publish it" is a fact about the city and will not change; "you
		// have not synced it" is one command away. Reporting either as the
		// other sends a reader in circles.
		if ok, why := m.Comparable(q, b); !ok {
			pp.Skipped, pp.Reason = true, b.Portal+" "+why
			plan.Probes = append(plan.Probes, pp)
			continue
		}
		if have != nil {
			var missing []string
			for _, name := range modes.ConceptRefs(probe.SQL) {
				bd, bound := b.Concepts[name]
				if bound && !have(bd.Table) {
					missing = append(missing, bd.Table)
				}
			}
			if len(missing) > 0 {
				sort.Strings(missing)
				pp.Skipped, pp.Fixable = true, true
				pp.Reason = "not synced yet: " + strings.Join(missing, ", ")
				plan.Probes = append(plan.Probes, pp)
				continue
			}
		}

		sqlText, err := modes.ExpandConceptsFor(m, probe.SQL, alias, b)
		if err != nil {
			pp.Skipped, pp.Reason = true, err.Error()
			plan.Probes = append(plan.Probes, pp)
			continue
		}
		pp.SQL = sqlText
		pp.Tables = tablesFor(probe, b)
		pp.targets = profileTargets(m, q, alias, b, probe)
		plan.Runnable++
		plan.Probes = append(plan.Probes, pp)
	}
	return plan, nil
}

// profileTargets resolves the datasets a probe reads, minus the columns whose
// emptiness the probe exists to count.
//
// See Probe.MeasuresAbsenceOf. Removing them is not a way of flattering the
// score: those columns are still read, still measured, and the thing being
// measured is reported as a finding. What changes is that their nulls stop
// being counted as evidence the investigation failed to obtain, which they are
// not — the investigation obtained every one of those rows and the emptiness is
// what it found.
func profileTargets(m *modes.Mode, q modes.Query, alias string,
	b *modes.Binding, probe Probe) []confidence.Target {

	targets := confidence.TargetsFor(m, q, alias, b)
	if len(probe.MeasuresAbsenceOf) == 0 {
		return targets
	}
	for i := range targets {
		kept := targets[i].Columns[:0]
		for _, col := range targets[i].Columns {
			if col == probe.PeriodColumn || !contains(probe.MeasuresAbsenceOf, col) {
				kept = append(kept, col)
			}
		}
		targets[i].Columns = kept
	}
	return targets
}

// tablesFor lists the local tables a probe reads, for display and for the
// readiness report.
func tablesFor(p Probe, b *modes.Binding) []string {
	var out []string
	for _, name := range modes.ConceptRefs(p.SQL) {
		if bd, ok := b.Concepts[name]; ok {
			out = append(out, bd.Table)
		}
	}
	sort.Strings(out)
	return out
}

// Readiness is step three: whether the data the plan needs is actually held,
// and what to run if it is not.
//
// Sync is a step in the investigation rather than a precondition of it because
// the honest answer to "is this city becoming less transparent" is sometimes
// "csq cannot tell you, and here is the one command that would let it". A tool
// that fails with a binder error at this point has answered a different
// question badly.
type Readiness struct {
	Ready bool `json:"ready"`
	// Missing are the tables the plan needs and does not have.
	Missing []string `json:"missing,omitempty"`
	// Blocked are probes this portal will never answer, whatever is synced.
	Blocked []string `json:"blocked,omitempty"`
	// FixCommand syncs exactly what is missing, and nothing else.
	FixCommand string `json:"fix_command,omitempty"`
	// DatasetIDs are the Socrata 4x4s behind Missing, for a caller that would
	// rather run the sync itself than print a command.
	DatasetIDs []string `json:"dataset_ids,omitempty"`
}

// Assess reports what the plan still needs before it can run.
func (p *Plan) Assess(inv *Investigation, b *modes.Binding) Readiness {
	r := Readiness{Ready: true}
	missing := map[string]bool{}
	ids := map[string]bool{}

	for _, pp := range p.Probes {
		if !pp.Skipped {
			continue
		}
		if !pp.Fixable {
			r.Blocked = append(r.Blocked, pp.Name+" — "+pp.Reason)
			continue
		}
		r.Ready = false
		probe, ok := inv.Probe(pp.Name)
		if !ok {
			continue
		}
		for _, name := range modes.ConceptRefs(probe.SQL) {
			bd, bound := b.Concepts[name]
			if !bound {
				continue
			}
			missing[bd.Table] = true
			ids[bd.ID] = true
		}
	}

	for t := range missing {
		r.Missing = append(r.Missing, t)
	}
	for id := range ids {
		r.DatasetIDs = append(r.DatasetIDs, id)
	}
	sort.Strings(r.Missing)
	sort.Strings(r.DatasetIDs)

	if !r.Ready {
		r.FixCommand = fmt.Sprintf(
			"csq modes init %s --portal %s --output %s.yaml && csq sync --config %s.yaml --only %s",
			p.Mode, p.Portal, p.Mode, p.Mode, strings.Join(r.DatasetIDs, ","))
	}
	// A plan with nothing runnable is not ready even when nothing is fixable:
	// there is no answer to be had, and saying "ready" would be a lie a caller
	// would act on.
	if p.Runnable == 0 {
		r.Ready = false
	}
	return r
}
