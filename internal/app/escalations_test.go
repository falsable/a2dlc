package app

import (
	"os"
	"path/filepath"
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

// What the check writes to .a2dlc/escalations.json is the document of escalation.schema.json: SaveVerdict's output over a
// verdict that makes every source of escalations of check speak decodes strictly (unknown fields, the required fields of the
// schema) and names the tree of the verdict.
func TestSavedEscalationsDecodeStrictly(t *testing.T) {
	a, _, dir := newRepo(t)
	const tree = "git-tree:4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	div := &contract.Counterexample{Args: "(1,)", Old: "1", New: "2"}
	v := contract.Verdict{SchemaVersion: contract.SchemaVersion, Verdict: contract.FAIL, TreeHash: tree, CreatedAt: "2026-11-07T10:00:00Z",
		Targets: []contract.VerdictTarget{
			{TargetResult: contract.TargetResult{Target: "a.py::f", Verdict: contract.FAIL, Counterexample: div}},
			{TargetResult: contract.TargetResult{Target: "a.py::g", Verdict: contract.INCONCLUSIVE, Reason: "timeout"}},
			{TargetResult: contract.TargetResult{Target: "a.py::h", Verdict: contract.FAIL, Counterexample: div}, Waived: true},
		},
		PendingWaivers: []string{"a.py::h"},
		EngineErrors:   []contract.EngineError{{Language: "python", Files: []string{"a.py"}, Error: "container down"}},
		Tests: []contract.TestResult{
			{Target: "t.py::t1", Gate: contract.GateTestsDiff, Verdict: contract.FAIL, Reason: "red only in candidate", Base: contract.TestPassed, Candidate: "failed", Trees: map[string]string{"base": "passed", "candidate": "failed"}},
			{Target: "t.py::t2", Gate: contract.GateBaseline, Verdict: contract.FAIL, Base: "failed", Candidate: "failed", Flaky: true, Reruns: &contract.Reruns{Base: []string{"passed", "failed"}}},
		},
		TestGates: &contract.TestGates{NOPOracle: contract.Gate{Status: contract.GateError, Reason: "image missing"}},
		Coverage:  &contract.Coverage{Status: contract.CoverageRan, Files: []contract.CoverageFile{{File: "b.py", Uncovered: []int{3, 4, 9}}}},
	}
	if err := a.SaveVerdict(v); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, pathEscalations))
	if err != nil {
		t.Fatal(err)
	}
	var es contract.Escalations
	if err := contract.DecodeStrictBytes(b, &es); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	kinds := map[string]bool{}
	for _, it := range es.Items {
		kinds[it.Kind] = true
	}
	for _, k := range []string{"behavior_divergence", "tooling", "signature_pending", "flaky_test", "uncovered_change"} {
		if !kinds[k] {
			t.Errorf("the verdict should have produced a %s escalation, got %v", k, kinds)
		}
	}
	if es.TreeHash != tree || es.SchemaVersion != contract.SchemaVersion {
		t.Fatalf("escalations.json names %q (schema_version %d), want the verdict's tree", es.TreeHash, es.SchemaVersion)
	}
}
