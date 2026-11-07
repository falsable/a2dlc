package main

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestBlockingLinesShowsOnlyWhatKeepsTheVerdictFromPass(t *testing.T) {
	long := strings.Repeat("x", clipAt+50)
	target := func(name, verdict, note string, waived bool, c *contract.Counterexample, reason string) contract.VerdictTarget {
		return contract.VerdictTarget{TargetResult: contract.TargetResult{Target: name, Verdict: verdict, Reason: reason, Counterexample: c}, Waived: waived, Note: note}
	}
	v := contract.Verdict{
		Targets: []contract.VerdictTarget{
			target("a.py::fail", contract.FAIL, "", false, &contract.Counterexample{Args: "(1,)", Old: "0", New: long}, ""),
			target("a.py::inc", contract.INCONCLUSIVE, "", false, nil, "type not derivable"),
			target("a.py::ok", contract.PASS, "", false, nil, ""),
			target("a.py::waived", contract.FAIL, "", true, &contract.Counterexample{}, ""),
			target("a.py::noted", contract.FAIL, contract.NoteBehaviorDivergence, false, &contract.Counterexample{}, ""),
		},
		Tests: []contract.TestResult{
			{Target: "t.py::test_x", Gate: "nop", Verdict: contract.FAIL, Reason: "passes on the base"},
			{Target: "t.py::test_y", Gate: "nop", Verdict: contract.PASS},
		},
	}
	got := blockingLines(v)
	if len(got) != 3 {
		t.Fatalf("want 3 lines (fail, inconclusive, test), got %d: %q", len(got), got)
	}
	if !strings.Contains(got[0], "FAIL a.py::fail: args=(1,) old=0 new=") || !strings.HasSuffix(got[0], "…") || len(got[0]) > clipAt+80 {
		t.Errorf("FAIL line must carry the counterexample, clipped: %q", got[0])
	}
	if got[1] != "  INCONCLUSIVE a.py::inc (type not derivable)" {
		t.Errorf("INCONCLUSIVE line: %q", got[1])
	}
	if got[2] != "  FAIL test t.py::test_x (nop): passes on the base" {
		t.Errorf("test line: %q", got[2])
	}
}
