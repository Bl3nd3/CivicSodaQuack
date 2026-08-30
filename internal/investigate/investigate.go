// Copyright (c) 2026 Neomantra Corp

// Package investigate answers a civic question end to end, and shows its work.
//
// A mode hands you a table. An investigation takes a question — "is Chicago
// becoming less transparent about policing?" — and returns a verdict, a
// confidence, the findings behind it, and the reasons the whole thing might be
// wrong. That is a much stronger claim to make, so the machinery here exists
// mostly to stop it being made carelessly.
//
// # The seven steps
//
//	Discover  which investigation the question is asking for, and about where
//	Plan      which indicators bear on it, declared before any data is read
//	Sync      whether the datasets those indicators need are actually held
//	Validate  how far each dataset can be trusted, and where it stops
//	Analyze   run the indicators and measure what moved
//	Challenge try to explain each movement away; withdraw what does not survive
//	Explain   state the verdict, the confidence, and what would change it
//
// The order is load-bearing rather than decorative. Plan runs before Analyze so
// that which indicators count, and which direction of each supports the claim,
// are fixed before anyone has seen a number — an investigation that chooses its
// indicators after seeing the results is not an investigation, it is an
// argument. Validate runs before Analyze so that a series is never measured
// past the point where its data stops. Challenge runs after Analyze because a
// finding has to exist before it can be attacked, and the attack is the only
// thing standing between "records fell 12%" and "the year is not over yet".
//
// # What an investigation is not allowed to do
//
// It does not write SQL from the question. Probes are declared here, reviewed
// like any other code, and expanded through the same concept bindings the modes
// use — so an investigation is portable across cities for the same reason a
// mode is, and so what runs can be read before it runs.
//
// It does not decide the verdict by weighing findings against each other with
// invented coefficients. Findings survive Challenge or they are withdrawn, and
// the verdict is a count of what survived. There is no scoring rubric to tune.
//
// It does not produce a confidence number of its own invention. Confidence is
// the confidence package's R — the share of the records the investigation
// meant to read that were present and usable — multiplied by the share of the
// planned indicators that could actually be answered. Both are counts over
// counts, so the product keeps a plain reading and introduces no constant.
package investigate

import (
	"fmt"
	"sort"
	"strings"
)

// Direction is the way an indicator moved, or the way it would have to move to
// support a claim.
type Direction string

const (
	// Up means the indicator rose.
	Up Direction = "up"
	// Down means the indicator fell.
	Down Direction = "down"
	// Flat means the indicator did not move enough to read as either.
	Flat Direction = "flat"
)

// Arrow renders a direction for a terminal.
func (d Direction) Arrow() string {
	switch d {
	case Up:
		return "↑"
	case Down:
		return "↓"
	}
	return "→"
}

// MaterialChange is the movement below which an indicator is called flat.
//
// This is a presentation cutoff, not a term in any score: it decides whether a
// finding is worth a reader's attention, never how much a finding is worth.
// Civic counts wander by a few percent between years for reasons no analysis
// here can see — a reporting-system migration, a leap year, a bad week of data
// entry — and calling a 1% drift a finding would bury the movements that matter
// under the ones that do not.
const MaterialChange = 0.05

// A Probe is one indicator bearing on an investigation's claim.
//
// The output contract is fixed and narrow: a probe returns one row per period,
// with a period, a value, and optionally a denominator. Everything downstream —
// the change measurement, the partial-period guard, the challenge that asks
// whether a rate moved only because its denominator did — reads that shape and
// nothing else. A probe free to return any shape would need an interpreter, and
// an interpreter is where an investigation starts inventing findings.
type Probe struct {
	// Name is a stable slug, used in output and to address the probe.
	Name string
	// Asks is the sub-question this indicator answers, in plain words.
	Asks string

	// Concept is the dataset the period column belongs to. Validate profiles
	// it to find where its coverage actually stops, which is what keeps a
	// half-finished year from reading as a collapse.
	Concept string
	// PeriodColumn is the concept's canonical date column. Together with
	// Concept it locates the series in time without parsing the SQL.
	PeriodColumn string

	// SQL must yield exactly: period (a year), value, and optionally
	// denominator. It is expanded through the mode's concept bindings, so it
	// refers to datasets as {{c:name}} and to columns by their canonical
	// names, whatever the portal calls them.
	SQL string

	// Unit names what value counts, for the finding sentence.
	Unit string
	// Per is the rate denominator's unit, set only when the SQL returns one.
	// "1,000 arrests" reads as "complaints per 1,000 arrests".
	Per string
	// Scale multiplies a rate so it lands in readable units. Zero means 1.
	Scale float64

	// MeasuresAbsenceOf names the columns whose emptiness is this probe's
	// observation rather than a defect in its evidence.
	//
	// The confidence score counts a row as lost when a column the query reads
	// is null, which is right for almost every query and exactly wrong for a
	// disclosure probe: "what share of cases carry an outcome" reads
	// finding_code precisely in order to count the rows that lack it. Left
	// alone, Chicago's 73% of open cases would be scored as 73% of the
	// evidence missing, and an indicator that measured every record it needed
	// would report 7% confidence.
	//
	// So the columns named here are excluded from the evidence profile. The
	// period column never is: a row with no date cannot be placed in time, and
	// that is a genuine loss whatever the probe is counting.
	MeasuresAbsenceOf []string

	// SettlesAfter is how many periods a record needs before the field this
	// probe reads stops changing. Zero means it is settled on arrival.
	//
	// This closes a false-positive generator that coverage alone cannot see.
	// "What share of cases carry an outcome" is structurally low for recent
	// years because those cases are still open — nothing has been withheld,
	// the investigation simply has not finished. A probe reading a field that
	// fills in later declares how long that takes, and the periods too recent
	// to have settled are held back from the measurement in the same way a
	// part-finished period is: shown, and not measured.
	SettlesAfter int

	// Supports is the direction this indicator must move to support the
	// investigation's claim. Declared here, in the registry, so that it is
	// fixed before any data is read — see the package doc.
	Supports Direction
	// RiseMeans and FallMeans read the movement back in plain language.
	RiseMeans string
	FallMeans string

	// Caveats are limits specific to this indicator, added to the
	// investigation's own.
	Caveats []string
}

// rate reports whether this probe measures a ratio rather than a count.
func (p Probe) rate() bool { return p.Per != "" }

// scale returns the multiplier for a rate, defaulting to 1.
func (p Probe) scale() float64 {
	if p.Scale <= 0 {
		return 1
	}
	return p.Scale
}

// An Investigation is a question worth asking of a city, and the indicators
// that bear on it.
//
// Claim is deliberately a proposition rather than a topic. "Policing
// transparency" is not something evidence can support or contradict; "the city
// is publishing less about policing than it used to" is. The verdict is a
// statement about the claim, so the claim has to be falsifiable before any of
// this means anything.
type Investigation struct {
	Name  string
	Title string
	// Claim is the proposition the evidence is tested against, phrased so that
	// it could turn out to be false.
	Claim string
	// About explains what the investigation looks at and what it cannot see.
	About string

	// Mode names the mode whose concepts and portal bindings this borrows.
	// Investigations add interpretation on top of an existing dataset mapping
	// rather than a second one, so a city bound for the mode is bound for the
	// investigation.
	Mode string

	// Match are the terms that make a question route here. Discover scores
	// them against the question; see discover.go for how ties are broken.
	Match []string

	Probes []Probe

	// Caveats are limits on the investigation as a whole, printed with every
	// verdict it produces.
	Caveats []string
}

// Probe returns the named probe.
func (inv *Investigation) Probe(name string) (Probe, bool) {
	for _, p := range inv.Probes {
		if p.Name == name {
			return p, true
		}
	}
	return Probe{}, false
}

// registry is the ordered set of built-in investigations.
var registry = []*Investigation{
	policeTransparency,
	civicPublishing,
}

// All returns every registered investigation, in display order.
func All() []*Investigation {
	out := make([]*Investigation, len(registry))
	copy(out, registry)
	return out
}

// Lookup returns the investigation with this name.
func Lookup(name string) (*Investigation, error) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, inv := range registry {
		if inv.Name == want {
			return inv, nil
		}
	}
	return nil, fmt.Errorf("unknown investigation %q (have: %s)",
		name, strings.Join(Names(), ", "))
}

// Names lists every investigation name.
func Names() []string {
	out := make([]string, 0, len(registry))
	for _, inv := range registry {
		out = append(out, inv.Name)
	}
	sort.Strings(out)
	return out
}
