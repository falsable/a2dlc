package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func vtarget(name, verdict, note string, waived bool, c *contract.Counterexample, reason string) contract.VerdictTarget {
	return contract.VerdictTarget{TargetResult: contract.TargetResult{Target: name, Verdict: verdict, Reason: reason, Counterexample: c}, Waived: waived, Note: note}
}

func causeTexts(v contract.Verdict) []string {
	var out []string
	for _, c := range blockingCauses(v, contract.Config{}) {
		out = append(out, c.Text)
	}
	return out
}

// Every cause that keeps a verdict from PASS and is not a target or a test gets one line that names it.
func TestBlockingLinesNameEachNonTargetCause(t *testing.T) {
	v := contract.Verdict{Verdict: contract.INCONCLUSIVE,
		Coverage: &contract.Coverage{Status: contract.CoverageRan,
			Files: []contract.CoverageFile{{File: "src/a.py", Uncovered: []int{3, 4, 5, 9}}}},
		OutOfScope:     []contract.OutOfScope{{File: "src/x.go", Reason: "no_engine"}, {File: "README.md", Reason: "no_engine"}},
		PendingWaivers: []string{"src/lib/f.ts::f"}}
	got := causeTexts(v)
	if len(got) != 3 {
		t.Fatalf("want 3 lines (uncovered, no_engine, pending), got %d: %q", len(got), got)
	}
	if !strings.Contains(got[0], "src/a.py") || !strings.Contains(got[0], "3-5, 9") {
		t.Errorf("uncovered line must name the file and its ranges: %q", got[0])
	}
	if !strings.Contains(got[1], "src/x.go") || !strings.Contains(got[1], "out_of_scope_paths") {
		t.Errorf("no_engine line must name the file and the key that declares it out of scope: %q", got[1])
	}
	if !strings.Contains(got[2], "src/lib/f.ts::f") || !strings.Contains(got[2], "approve --pending") {
		t.Errorf("pending line must name the target and the command: %q", got[2])
	}
	for _, l := range got {
		if strings.Contains(l, "README.md") {
			t.Errorf("a doc with reason no_engine is not a cause: %q", l)
		}
	}
}

// The no_engine line comes from a real check: the same file the escalation names, and nothing for the other files.
func TestBlockingLinesNameNoEngineSourceFromRealCheck(t *testing.T) {
	a, dir := noEngineRepo(t, "")
	write(t, dir, "cmd/main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "README.md", "changed\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("v=%s err=%v", v.Verdict, err)
	}
	got := a.BlockingLines(v)
	if len(got) != 1 || !strings.Contains(got[0], "cmd/main.go") || !strings.Contains(got[0], "out_of_scope_paths") {
		t.Fatalf("want one line naming cmd/main.go and out_of_scope_paths, got %q", got)
	}
}

// The per-target text check printed before this ticket, moved here unchanged.
func TestBlockingLinesTargets(t *testing.T) {
	long := strings.Repeat("x", clipAt+50)
	v := contract.Verdict{Targets: []contract.VerdictTarget{
		vtarget("a.py::fail", contract.FAIL, "", false, &contract.Counterexample{Args: "(1,)", Old: "0", New: long}, ""),
		vtarget("a.py::inc", contract.INCONCLUSIVE, "", false, nil, "type not derivable"),
		vtarget("a.py::ok", contract.PASS, "", false, nil, ""),
		vtarget("a.py::waived", contract.FAIL, "", true, &contract.Counterexample{}, ""),
		vtarget("a.py::noted", contract.FAIL, contract.NoteBehaviorDivergence, false, &contract.Counterexample{}, ""),
	}}
	got := causeTexts(v)
	if len(got) != 2 {
		t.Fatalf("want 2 lines (fail, inconclusive), got %d: %q", len(got), got)
	}
	if !strings.Contains(got[0], "FAIL a.py::fail: args=(1,) old=0 new=") || !strings.HasSuffix(got[0], "…") || len(got[0]) > clipAt+80 {
		t.Errorf("FAIL line must carry the counterexample, clipped: %q", got[0])
	}
	if got[1] != "INCONCLUSIVE a.py::inc (type not derivable)" {
		t.Errorf("INCONCLUSIVE line: %q", got[1])
	}
}

// The Stop hook hands the agent the same text check prints for targets and uncovered lines, and still keeps back what only a
// person can resolve.
func TestHookAndCheckShareTargetLines(t *testing.T) {
	v := contract.Verdict{Verdict: contract.FAIL,
		Targets: []contract.VerdictTarget{
			vtarget("src/lib/f.ts::f", contract.FAIL, "", false, &contract.Counterexample{Args: "[]", Old: "1", New: "2"}, ""),
			vtarget("src/lib/g.ts::g", contract.INCONCLUSIVE, "", false, nil, "type not derivable"),
		},
		Coverage: &contract.Coverage{Status: contract.CoverageRan,
			Files: []contract.CoverageFile{{File: "src/a.py", Uncovered: []int{3, 4, 5}}}},
		OutOfScope:     []contract.OutOfScope{{File: "src/x.go", Reason: "no_engine"}},
		PendingWaivers: []string{"src/lib/h.ts::h"}}
	shared := map[string]bool{}
	for _, l := range causeTexts(v) {
		shared[l] = true
	}
	items := feedbackItems(v)
	if len(items) != 3 {
		t.Fatalf("want fail, inconclusive and uncovered, got %q", items)
	}
	for _, it := range items {
		if !shared[it] {
			t.Errorf("hook item is not a line check prints: %q", it)
		}
		if strings.Contains(it, "src/x.go") || strings.Contains(it, "src/lib/h.ts") {
			t.Errorf("hook handed the agent a cause only a person resolves: %q", it)
		}
	}
}
