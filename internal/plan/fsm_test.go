package plan

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

const modelSlices = 4

// allGraphs are every dependency graph on n slices where a slice depends only on lower indices: every DAG up to a
// relabeling that keeps the index order topological. For 4 slices, 2^6 = 64 shapes.
func allGraphs(n int) [][][]int {
	var pairs [][2]int
	for i := 0; i < n; i++ {
		for j := 0; j < i; j++ {
			pairs = append(pairs, [2]int{i, j})
		}
	}
	var out [][][]int
	for mask := 0; mask < 1<<len(pairs); mask++ {
		deps := make([][]int, n)
		for k, p := range pairs {
			if mask&(1<<k) != 0 {
				deps[p[0]] = append(deps[p[0]], p[1])
			}
		}
		out = append(out, deps)
	}
	return out
}

func identity(n int) []int {
	o := make([]int, n)
	for i := range o {
		o[i] = i
	}
	return o
}

// normalize renumbers FinishedIn by rank so two states that differ only in how many invocations came before are one.
func normalize(s ModelState) ModelState {
	vals := map[int]bool{0: true}
	for _, st := range s.Slices {
		vals[st.FinishedIn] = true
	}
	var sorted []int
	for v := range vals {
		sorted = append(sorted, v)
	}
	sort.Ints(sorted)
	rank := map[int]int{}
	for r, v := range sorted {
		rank[v] = r
	}
	n := s
	n.Slices = append([]ModelSlice(nil), s.Slices...)
	for i := range n.Slices {
		n.Slices[i].FinishedIn = rank[n.Slices[i].FinishedIn]
	}
	n.Invocation = len(sorted) - 1
	return n
}

func key(s ModelState) string {
	var b strings.Builder
	for _, st := range s.Slices {
		fmt.Fprintf(&b, "%s/%v/%d,", st.Status, st.Verifies, st.FinishedIn)
	}
	b.WriteString(s.Outcome)
	return b.String()
}

// invocations returns every invocation from s: each sequence of outcomes for the slices that actually run, in the order
// they run, with the final check passing or not. Choices are made lazily, so a slice that does not run adds no branch.
func invocations(s ModelState, visit func(n ModelState, step ModelStep, finalOK bool)) {
	all := []string{OutPassed, OutEscalated, OutFailed}
	var rec func(prefix []int)
	rec = func(prefix []int) {
		calls := 0
		extended := false
		run := func(finalOK bool) (ModelState, ModelStep) {
			calls = 0
			return ModelInvoke(s, func(int) string {
				c := 0
				if calls < len(prefix) {
					c = prefix[calls]
				} else {
					extended = true
				}
				calls++
				return all[c]
			}, finalOK)
		}
		n, step := run(true)
		if extended { // a slice ran past the prefix: branch on its outcome
			for c := range all {
				rec(append(append([]int{}, prefix...), c))
			}
			return
		}
		visit(n, step, true)
		n, step = run(false)
		visit(n, step, false)
	}
	rec(nil)
}

// TestModelCheckPlanMachine explores, for every graph, every state reachable through invocations with any slice
// outcome and any final check, and through a passed slice that stops verifying between invocations. Every state must
// keep the invariants, and from every state an invocation where everything passes must reach PASS (liveness).
func TestModelCheckPlanMachine(t *testing.T) {
	graphs := allGraphs(modelSlices)
	if len(graphs) != 64 {
		t.Fatalf("%d graphs, want 64", len(graphs))
	}
	totalStates, totalTransitions := 0, 0
	for gi, deps := range graphs {
		start := NewModelState(deps, identity(modelSlices))
		seen := map[string]bool{key(start): true}
		queue := []ModelState{start}
		for len(queue) > 0 {
			s := queue[0]
			queue = queue[1:]
			totalStates++
			// Liveness: everything passes and the final check passes → PASS, in one invocation.
			if n, _ := ModelInvoke(s, func(int) string { return OutPassed }, true); n.Outcome != contract.PASS {
				t.Fatalf("graph %d: from %s an all-pass invocation ends %s", gi, key(s), n.Outcome)
			}
			var succ []ModelState
			invocations(s, func(n ModelState, step ModelStep, finalOK bool) {
				totalTransitions++
				if bad := CheckModel(s, n, step, finalOK); len(bad) > 0 {
					t.Fatalf("graph %d deps %v: from %s, ran %v, final=%v, to %s: %v", gi, deps, key(s), step.Ran, finalOK, key(n), bad)
				}
				succ = append(succ, n)
			})
			// Between invocations, a passed slice may stop verifying (its tree or a frozen input changed).
			for i, st := range s.Slices {
				if st.Status == contract.PlanPassed && st.Verifies {
					n := s
					n.Slices = append([]ModelSlice(nil), s.Slices...)
					n.Slices[i].Verifies = false
					succ = append(succ, n)
					totalTransitions++
				}
			}
			for _, n := range succ {
				n = normalize(n)
				if k := key(n); !seen[k] {
					seen[k] = true
					queue = append(queue, n)
				}
			}
		}
	}
	t.Logf("%d graphs, %d states, %d transitions, 0 violations", len(graphs), totalStates, totalTransitions)
}

// The checker catches what it is for: a machine that runs a slice whose dependency escalated breaks J2.
func TestModelCheckerCatchesAMutant(t *testing.T) {
	deps := [][]int{{}, {0}}
	s := NewModelState(deps, identity(2))
	n, step := ModelInvoke(s, func(i int) string {
		if i == 0 {
			return OutEscalated
		}
		return OutPassed
	}, true)
	n.Slices[1] = ModelSlice{Status: contract.PlanPassed, Verifies: true, FinishedIn: 1} // the mutant: no block
	step.Ran = append(step.Ran, 1)
	bad := strings.Join(CheckModel(s, n, step, true), " | ")
	if !strings.Contains(bad, "J2") {
		t.Fatalf("the checker missed a slice passed over an escalated dependency: %q", bad)
	}
}

// A slice that stops verifying re-runs, and so do its dependents, but not its siblings (P4).
func TestRedoReachesDependentsOnly(t *testing.T) {
	deps := [][]int{{}, {0}, {}} // 1 depends on 0; 2 is independent
	s, _ := ModelInvoke(NewModelState(deps, identity(3)), func(int) string { return OutPassed }, true)
	s.Slices[0].Verifies = false
	_, step := ModelInvoke(s, func(int) string { return OutPassed }, true)
	if fmt.Sprint(step.Ran) != "[0 1]" {
		t.Fatalf("ran %v, want [0 1]", step.Ran)
	}
}

var updateDoc = flag.Bool("update", false, "rewrite docs/PLAN_MACHINE.md from fsm.go")

// docs/PLAN_MACHINE.md is rendered from fsm.go; -update rewrites it.
func TestPlanMachineDoc(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "PLAN_MACHINE.md")
	want := MachineDoc()
	if *updateDoc {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s is not what fsm.go renders: run go test ./internal/plan -run TestPlanMachineDoc -update", path)
	}
}
