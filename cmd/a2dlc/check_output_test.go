package main

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestTestLinesShowsOnlyFailingTests(t *testing.T) {
	v := contract.Verdict{Tests: []contract.TestResult{
		{Target: "t.py::test_x", Gate: "nop", Verdict: contract.FAIL, Reason: "passes on the base"},
		{Target: "t.py::test_y", Gate: "nop", Verdict: contract.PASS},
		{Target: "t.py::test_z", Gate: "nop", Verdict: contract.FAIL, Waived: true},
	}}
	got := testLines(v)
	if len(got) != 1 || got[0] != "  FAIL test t.py::test_x (nop): passes on the base" {
		t.Fatalf("test lines: %q", got)
	}
}
