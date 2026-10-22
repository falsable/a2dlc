package app

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The cluster key is kind plus the normalized message, never the target (decision 3): one root cause that shows up on
// several targets is one cluster, and a different kind is a different cluster even with the same message.
func TestClusterKeyIsKindAndNormalizedMessageNotTarget(t *testing.T) {
	a := escalation("behavior_divergence", "src/a.py::f", "summary", contract.Evidence{Message: "2 values differ on line 40"})
	b := escalation("behavior_divergence", "src/b.py::g", "summary", contract.Evidence{Message: "2 values differ on line 41"})
	c := escalation("tooling", "src/a.py::f", "summary", contract.Evidence{Message: "2 values differ on line 40"})
	if a.ClusterID != b.ClusterID {
		t.Fatalf("same kind and normalized message must share a cluster: %s %s", a.ClusterID, b.ClusterID)
	}
	if a.ClusterID == c.ClusterID {
		t.Fatalf("a different kind must not share a cluster: %s", a.ClusterID)
	}
	if a.ID == b.ID {
		t.Fatalf("the id is kind plus target: different targets, different ids")
	}
}

// Without a message the cluster falls back to the summary.
func TestClusterFallsBackToSummaryWithoutMessage(t *testing.T) {
	a := escalation("behavior_divergence", "x::f", "The behavior changed relative to the base.", contract.Evidence{})
	want := contract.ClusterID("behavior_divergence", "The behavior changed relative to the base.")
	if a.ClusterID != want {
		t.Fatalf("cluster %s, want %s", a.ClusterID, want)
	}
}

// The equivalence_s gate that could not run is a tooling escalation, as the other test gates are.
func TestEquivalenceGateErrorIsToolingEscalation(t *testing.T) {
	v := contract.Verdict{TestGates: &contract.TestGates{
		NOPOracle:    contract.Gate{Status: contract.GateNotConfigured},
		Baseline:     contract.Gate{Status: contract.GateNotConfigured},
		EquivalenceS: &contract.Gate{Status: contract.GateError, Reason: "engine errors: python: container down"},
	}}
	var found bool
	for _, e := range escalations(v) {
		if e.Kind == "tooling" && e.Target == "equivalence_s" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no tooling escalation for an equivalence_s error: %+v", escalations(v))
	}
	v.TestGates.EquivalenceS.Status = contract.GateRan
	for _, e := range escalations(v) {
		if e.Target == "equivalence_s" {
			t.Fatalf("a ran gate escalates nothing: %+v", e)
		}
	}
}
