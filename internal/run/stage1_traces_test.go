package run

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestApplyTraces(t *testing.T) {
	zero, one, bc := 0, 1, 0.8
	harnessErr := "runner crashed"
	trace := []contract.CaptureTrace{{Function: "calc.py::mean", Input: "[1]", Output: "1.0", BranchIDs: []string{"b1"}}}
	cases := []struct {
		name     string
		resp     contract.TracesResponse
		funcs    bool
		wantWhy  string
		wantBC   *float64
		wantTrac int
	}{
		{"green with coverage", contract.TracesResponse{TestExit: &zero, Traces: trace, BranchCoverage: &bc}, true, "", &bc, 1},
		{"harness error", contract.TracesResponse{TestExit: &zero, Error: &harnessErr}, true, "trace harness failed: runner crashed", nil, 0},
		{"base tests fail", contract.TracesResponse{TestExit: &one, Traces: trace, BranchCoverage: &bc}, true, "did not exit 0", &bc, 1},
		{"no runner ran", contract.TracesResponse{}, false, "did not exit 0", nil, 0},
		{"no branch coverage for changed functions", contract.TracesResponse{TestExit: &zero, Traces: trace}, true, "no branch coverage", nil, 1},
		{"no branch coverage, no changed functions", contract.TracesResponse{TestExit: &zero}, false, "", nil, 0},
	}
	for _, c := range cases {
		var capt contract.Capture
		why := applyTraces(&capt, c.resp, c.funcs)
		if (c.wantWhy == "") != (why == "") || (c.wantWhy != "" && !strings.Contains(why, c.wantWhy)) {
			t.Errorf("%s: reason %q, want %q", c.name, why, c.wantWhy)
		}
		if (capt.BranchCoverage == nil) != (c.wantBC == nil) || (c.wantBC != nil && *capt.BranchCoverage != *c.wantBC) {
			t.Errorf("%s: branch_coverage %v, want %v (written only when present)", c.name, capt.BranchCoverage, c.wantBC)
		}
		if len(capt.Traces) != c.wantTrac {
			t.Errorf("%s: %d traces, want %d", c.name, len(capt.Traces), c.wantTrac)
		}
	}
}
