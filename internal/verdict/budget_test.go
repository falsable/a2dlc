package verdict

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The budgets of the engines add up, and the cut functions of all of them are kept.
func TestMergeDiffBudgetAddsEnginesAndKeepsCuts(t *testing.T) {
	if MergeDiffBudget(nil, nil) != nil {
		t.Fatal("no engine budget must leave the verdict's budget absent")
	}
	acc := MergeDiffBudget(nil, &contract.DiffBudget{TotalS: 30, UsedS: 12, Cut: []contract.DiffCut{{Target: "a.ts::f", Reason: "over"}}})
	acc = MergeDiffBudget(acc, &contract.DiffBudget{TotalS: 30, UsedS: 30, Cut: []contract.DiffCut{{Target: "b.py::g", Reason: "over"}}})
	if acc.TotalS != 60 || acc.UsedS != 42 || len(acc.Cut) != 2 || acc.Cut[1].Target != "b.py::g" {
		t.Fatalf("merged: %+v", acc)
	}
	if !HasCut(acc) || HasCut(&contract.DiffBudget{TotalS: 1, UsedS: 0}) || HasCut(nil) {
		t.Fatal("HasCut must say whether some function was cut")
	}
}

// A cut function the engine answered becomes INCONCLUSIVE with the cut as reason; a FAIL is still a finding, and a
// target that was not cut is left alone.
func TestMarkCutMakesAnsweredTargetsInconclusiveButKeepsFail(t *testing.T) {
	targets := []contract.TargetResult{
		{Target: "a.ts::f", Verdict: contract.PASS},
		{Target: "a.ts::g", Verdict: contract.FAIL, Reason: "counterexample"},
		{Target: "a.ts::h", Verdict: contract.PASS},
	}
	MarkCut(targets, []contract.DiffCut{{Target: "a.ts::f", Reason: "budget"}, {Target: "a.ts::g", Reason: "budget"}})
	if targets[0].Verdict != contract.INCONCLUSIVE || targets[0].Reason != "cut by the per-diff budget: budget" {
		t.Fatalf("cut PASS target: %+v", targets[0])
	}
	if targets[1].Verdict != contract.FAIL || targets[1].Reason != "counterexample" {
		t.Fatalf("FAIL changed: %+v", targets[1])
	}
	if targets[2].Verdict != contract.PASS {
		t.Fatalf("an uncut target changed: %+v", targets[2])
	}
}
