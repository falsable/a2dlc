package app

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func withCounterexample(e contract.Escalation, args string) contract.Escalation {
	e.Evidence.Counterexample = &contract.Counterexample{Args: args}
	return e
}

func TestDedupeByIDKeepsTheShortestCounterexample(t *testing.T) {
	long := withCounterexample(escalation("behavior_divergence", "src/f.py::f", "primary", contract.Evidence{}), "x=1000")
	short := withCounterexample(escalation("behavior_divergence", "src/f.py::f", "orthogonal", contract.Evidence{}), "x=2")
	short.Builder = "builder_orthogonal"
	got := DedupeByID([]contract.Escalation{long, short})
	if len(got) != 1 || got[0].Evidence.Counterexample.Args != "x=2" || got[0].Builder != "builder_orthogonal" {
		t.Fatalf("one escalation with the shortest input expected: %+v", got)
	}
	// the same two in the other order give the same escalation
	got = DedupeByID([]contract.Escalation{short, long})
	if len(got) != 1 || got[0].Evidence.Counterexample.Args != "x=2" {
		t.Fatalf("order must not matter: %+v", got)
	}
}

func TestDedupeByIDKeepsTheFirstWithoutCounterexamples(t *testing.T) {
	first := escalation("tooling", "src/f.py::*", "first", contract.Evidence{})
	first.Builder = "builder_primary"
	second := escalation("tooling", "src/f.py::*", "second", contract.Evidence{})
	second.Builder = "builder_orthogonal"
	got := DedupeByID([]contract.Escalation{first, second})
	if len(got) != 1 || got[0].Summary != "first" || got[0].Builder != "builder_primary" {
		t.Fatalf("the first one stays: %+v", got)
	}
}

func TestDedupeByIDKeepsDistinctIDs(t *testing.T) {
	a := escalation("behavior_divergence", "src/a.py::f", "a", contract.Evidence{})
	b := escalation("behavior_divergence", "src/b.py::f", "b", contract.Evidence{})
	if got := DedupeByID([]contract.Escalation{a, b}); len(got) != 2 {
		t.Fatalf("different targets are different escalations: %+v", got)
	}
}

func TestToolingEscalationIsToolingWithTheTarget(t *testing.T) {
	e := ToolingEscalation("src/f.py::*", "fuzzing did not run for python: container down")
	if e.Kind != "tooling" || e.Target != "src/f.py::*" || e.ID == "" || e.ClusterID == "" || len(e.ResolveWith) != 0 {
		t.Fatalf("%+v", e)
	}
}
