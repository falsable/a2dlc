package verdict

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// A budget that cut nothing still writes "cut":[] (the schema requires an array), and an absent budget stays absent.
func TestDiffBudgetCutIsAnArrayWhenWritten(t *testing.T) {
	v := contract.Verdict{Verdict: contract.PASS, DiffBudget: &contract.DiffBudget{TotalS: 60, UsedS: 3}}
	CompleteDiffBudget(&v)
	b, err := json.Marshal(v)
	if err != nil || !strings.Contains(string(b), `"cut":[]`) {
		t.Fatalf("cut is not an array: %s %v", b, err)
	}
	none := contract.Verdict{Verdict: contract.PASS}
	CompleteDiffBudget(&none)
	if b, _ = json.Marshal(none); strings.Contains(string(b), "diff_budget") {
		t.Fatalf("an absent budget must stay absent: %s", b)
	}
}

func tr(target, v string) contract.TargetResult {
	return contract.TargetResult{Target: target, Verdict: v}
}

func TestAggregate(t *testing.T) {
	cases := []struct {
		name       string
		targets    []contract.TargetResult
		waived     map[string]bool
		approved   map[string]bool
		engineErrs int
		want       string
		pending    []string
	}{
		{"no targets is PASS", nil, nil, nil, 0, contract.PASS, []string{}},
		{"a FAIL without a waiver is FAIL", []contract.TargetResult{tr("a.ts::f", "FAIL"), tr("a.ts::g", "INCONCLUSIVE")}, nil, nil, 0, contract.FAIL, []string{}},
		{"FAIL with an unsigned waiver passes and stays pending", []contract.TargetResult{tr("a.ts::f", "FAIL")}, map[string]bool{"a.ts::f": true}, nil, 0, contract.PASS, []string{"a.ts::f"}},
		{"FAIL with a signed waiver passes with no pending", []contract.TargetResult{tr("a.ts::f", "FAIL")}, map[string]bool{"a.ts::f": true}, map[string]bool{"a.ts::f": true}, 0, contract.PASS, []string{}},
		{"the waiver does not cover an INCONCLUSIVE", []contract.TargetResult{tr("a.ts::f", "INCONCLUSIVE")}, map[string]bool{"a.ts::f": true}, nil, 0, contract.INCONCLUSIVE, []string{}},
		{"waiver of a target that passes does not stay pending", []contract.TargetResult{tr("a.ts::f", "PASS")}, map[string]bool{"a.ts::f": true}, nil, 0, contract.PASS, []string{}},
		{"engine down is INCONCLUSIVE", []contract.TargetResult{tr("a.ts::f", "PASS")}, nil, nil, 1, contract.INCONCLUSIVE, []string{}},
		{"FAIL beats engine down", []contract.TargetResult{tr("a.ts::f", "FAIL")}, nil, nil, 1, contract.FAIL, []string{}},
	}
	for _, c := range cases {
		got, out, pend := Aggregate(c.targets, c.waived, c.approved, c.engineErrs)
		if got != c.want || !reflect.DeepEqual(pend, c.pending) || len(out) != len(c.targets) {
			t.Errorf("%s: got %s %v, want %s %v", c.name, got, pend, c.want, c.pending)
		}
	}
}

func TestMatch(t *testing.T) {
	cases := []struct {
		glob, path string
		want       bool
	}{
		{"src/lib/**", "src/lib/routes.ts", true},
		{"src/lib/**", "src/lib/a/b.ts", true},
		{"src/lib/**", "src/library.ts", false},
		{"**/*.py", "a.py", true},
		{"**/*.py", "pkg/sub/a.py", true},
		{"**/*.py", "pkg/a.pyc", false},
		{"src/*.ts", "src/a/b.ts", false},
		{"src/?.ts", "src/a.ts", true},
		{"src/a.b.ts", "src/aXb.ts", false},
	}
	for _, c := range cases {
		if got := Match(c.glob, c.path); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.glob, c.path, got)
		}
	}
}

func TestAssignFirstEngineWins(t *testing.T) {
	engines := []contract.EngineConfig{{Paths: []string{"src/lib/**"}}, {Paths: []string{"src/**"}}}
	byEngine, without := Assign(engines, []string{"src/lib/a.ts", "src/b.ts", "README.md"})
	if !reflect.DeepEqual(byEngine[0], []string{"src/lib/a.ts"}) || !reflect.DeepEqual(byEngine[1], []string{"src/b.ts"}) || !reflect.DeepEqual(without, []string{"README.md"}) {
		t.Fatalf("by=%v without=%v", byEngine, without)
	}
}
