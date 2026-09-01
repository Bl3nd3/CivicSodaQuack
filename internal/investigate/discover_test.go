// Copyright (c) 2026 Neomantra Corp

package investigate

import (
	"errors"
	"testing"
)

func TestDiscover_RoutesTheWorkedExample(t *testing.T) {
	d, err := Discover("Is Chicago becoming less transparent about policing?")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "police-transparency" {
		t.Errorf("routed to %q, want police-transparency", d.Name)
	}
	if d.Place != "Chicago" {
		t.Errorf("place = %q, want Chicago", d.Place)
	}
	// "transparent" is claimed by both investigations and must not be what
	// decided this; "policing" is claimed by one and must be.
	if len(d.Matched) == 0 || d.Matched[0] != "policing" {
		t.Errorf("matched = %v, want the discriminating term first", d.Matched)
	}
}

// A word shared by every investigation cannot tell them apart, and the weights
// have to say so rather than being hand-set. This pins the property that makes
// routing survive someone adding a third investigation.
func TestDiscover_SharedTermsWeighLessThanUniqueOnes(t *testing.T) {
	w := termWeights()
	shared, unique := w["transparency"], w["policing"]
	if shared <= 0 || unique <= 0 {
		t.Fatalf("missing weights: transparency=%v policing=%v", shared, unique)
	}
	if shared >= unique {
		t.Errorf("shared term weighs %v, unique weighs %v — shared must weigh less",
			shared, unique)
	}
}

func TestDiscover_RoutesPublishingQuestions(t *testing.T) {
	d, err := Discover("Is Chicago publishing fewer records than it used to?")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "civic-publishing" {
		t.Errorf("routed to %q, want civic-publishing", d.Name)
	}
}

// Substring matching would send any question about policy to the policing
// investigation, and any mention of "permitting" to the permits probe.
func TestDiscover_MatchesWholeWordsOnly(t *testing.T) {
	if containsTerm(normalise("What is the city's data policy?"), "police") {
		t.Error("\"policy\" matched the term \"police\"")
	}
	if !containsTerm(normalise("questions about policing?"), "policing") {
		t.Error("\"policing?\" did not match the term \"policing\"")
	}
}

func TestDiscover_UnknownQuestionIsRefusedNotGuessed(t *testing.T) {
	_, err := Discover("How many potholes will there be next year?")
	var nomatch *NoMatchError
	if !errors.As(err, &nomatch) {
		t.Fatalf("err = %v, want NoMatchError — csq must not invent an analysis", err)
	}
}

func TestDiscover_PlaceIsReadFromTheBindings(t *testing.T) {
	d, err := Discover("Is New York publishing fewer records?")
	if err != nil {
		t.Fatal(err)
	}
	if d.Place != "New York" {
		t.Errorf("place = %q, want New York", d.Place)
	}
}

func TestDiscover_NoPlaceIsNotAnError(t *testing.T) {
	d, err := Discover("Is policing becoming less transparent?")
	if err != nil {
		t.Fatal(err)
	}
	if d.Place != "" {
		t.Errorf("place = %q, want empty when the question names none", d.Place)
	}
}

func TestDiscoverNamed_SkipsRoutingButKeepsThePlace(t *testing.T) {
	d, err := DiscoverNamed("civic-publishing", "what about Chicago?")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name != "civic-publishing" {
		t.Errorf("name = %q", d.Name)
	}
	if d.Place != "Chicago" {
		t.Errorf("place = %q, want Chicago", d.Place)
	}
}

func TestLookup_RejectsUnknownNames(t *testing.T) {
	if _, err := Lookup("no-such-investigation"); err == nil {
		t.Fatal("expected an error for an unknown investigation")
	}
}
