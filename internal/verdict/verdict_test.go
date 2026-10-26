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
		got, out, pend := Aggregate(c.targets, c.waived, c.approved, c.engineErrs, nil)
		if got != c.want || !reflect.DeepEqual(pend, c.pending) || len(out) != len(c.targets) {
			t.Errorf("%s: got %s %v, want %s %v", c.name, got, pend, c.want, c.pending)
		}
	}
}

func divergence(target string) contract.TargetResult {
	return contract.TargetResult{Target: target, Verdict: contract.FAIL, Counterexample: &contract.Counterexample{Args: "[1]", Old: "1", New: "2"}}
}

func TestAggregateNotesADivergenceUnlessCritical(t *testing.T) {
	cases := []struct {
		name     string
		target   contract.TargetResult
		critical []string
		waived   map[string]bool
		want     string
		note     string
		pending  []string
	}{
		{"nothing is critical without the field", divergence("a/v.py::f"), nil, nil, contract.PASS, contract.NoteBehaviorDivergence, []string{}},
		{"a glob over the file makes it FAIL", divergence("a/v.py::f"), []string{"a/v.py"}, nil, contract.FAIL, "", []string{}},
		{"a glob over file::symbol makes it FAIL", divergence("a/v.py::f"), []string{"a/*.py::f"}, nil, contract.FAIL, "", []string{}},
		{"another symbol of the file stays a note", divergence("a/v.py::g"), []string{"a/v.py::f"}, nil, contract.PASS, contract.NoteBehaviorDivergence, []string{}},
		{"critical with a waiver is as before: pending", divergence("a/v.py::f"), []string{"a/v.py"}, map[string]bool{"a/v.py::f": true}, contract.PASS, "", []string{"a/v.py::f"}},
		{"non-critical with a waiver is waived, not a note", divergence("a/v.py::f"), nil, map[string]bool{"a/v.py::f": true}, contract.PASS, "", []string{"a/v.py::f"}},
		{"a FAIL without a counterexample is never a note", tr("a/v.py::f", "FAIL"), nil, nil, contract.FAIL, "", []string{}},
		{"INCONCLUSIVE does not change", tr("a/v.py::f", "INCONCLUSIVE"), nil, nil, contract.INCONCLUSIVE, "", []string{}},
		{"INCONCLUSIVE stays INCONCLUSIVE on critical code", tr("a/v.py::f", "INCONCLUSIVE"), []string{"a/v.py"}, nil, contract.INCONCLUSIVE, "", []string{}},
	}
	for _, c := range cases {
		got, out, pend := Aggregate([]contract.TargetResult{c.target}, c.waived, nil, 0, c.critical)
		if got != c.want || out[0].Note != c.note || !reflect.DeepEqual(pend, c.pending) {
			t.Errorf("%s: got %s note=%q pending=%v, want %s note=%q pending=%v", c.name, got, out[0].Note, pend, c.want, c.note, c.pending)
		}
	}
}

func TestIsCritical(t *testing.T) {
	cases := []struct {
		globs  []string
		target string
		want   bool
	}{
		{nil, "a.py::f", false},
		{[]string{"a.py"}, "a.py::f", true},
		{[]string{"app/domain/**"}, "app/domain/valor.py::construir_informe", true},
		{[]string{"app/domain/**"}, "app/other/valor.py::f", false},
		{[]string{"a.py::f"}, "a.py::f", true},
		{[]string{"a.py::f"}, "a.py::g", false},
		{[]string{"a.py::Cls.*"}, "a.py::Cls.method", true},
		{[]string{"a.py::*"}, "a.py::f", true},
		{[]string{"**/billing*.py::*"}, "svc/billing_x.py::charge", true},
		{[]string{"a.py::f"}, "a.py", false},
	}
	for _, c := range cases {
		if got := IsCritical(c.globs, c.target); got != c.want {
			t.Errorf("IsCritical(%v, %q) = %v", c.globs, c.target, got)
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
