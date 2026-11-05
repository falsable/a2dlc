package run

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

var updateDoc = flag.Bool("update", false, "rewrite docs/STATE_MACHINE.md from fsm.go")

var modelOutcomes = []string{OutDone, OutSkipped, OutSkippedArt, OutEscalated, OutFailed}

// normalize renumbers the invocations by rank (only their order matters to the rules and the invariants) and resets the
// counter above them, so the state space is finite and the exploration reaches a fixpoint.
func normalize(s ModelState) ModelState {
	n := s
	n.Stages = append([]ModelStage(nil), s.Stages...)
	var vals []int
	for _, st := range s.Stages {
		if st.FinishedIn > 0 {
			vals = append(vals, st.FinishedIn)
		}
	}
	sort.Ints(vals)
	rank, r := map[int]int{}, 0
	for _, v := range vals {
		if _, ok := rank[v]; !ok {
			r++
			rank[v] = r
		}
	}
	for i := range n.Stages {
		if n.Stages[i].FinishedIn > 0 {
			n.Stages[i].FinishedIn = rank[n.Stages[i].FinishedIn]
		}
	}
	n.Invocation = r
	return n
}

func modelKey(s ModelState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s|", s.Outcome)
	for _, st := range s.Stages {
		fmt.Fprintf(&b, "%s,%t,%t,%t,%d;", st.Status, st.Artifact, st.Intact, st.StaleSkip, st.FinishedIn)
	}
	return b.String()
}

// worldEvents are what can change between invocations (R7): nothing, an artifact altered, or a skip made stale.
func worldEvents(s ModelState) []ModelState {
	out := []ModelState{s}
	for i, st := range s.Stages {
		if st.Artifact && st.Intact {
			n := s
			n.Stages = append([]ModelStage(nil), s.Stages...)
			n.Stages[i].Intact = false
			out = append(out, n)
		}
		if st.Status == contract.StageSkipped && !st.Artifact && !st.StaleSkip {
			n := s
			n.Stages = append([]ModelStage(nil), s.Stages...)
			n.Stages[i].StaleSkip = true
			out = append(out, n)
		}
	}
	return out
}

// invocations enumerates every invocation of s: every outcome of every stage that actually runs (lazily: a stage that does
// not run is never branched on), and every point from which the budget breaker trips, or none.
func invocations(s ModelState, visit func(next ModelState, step ModelStep, over func(int) bool)) {
	k := len(s.Stages)
	for trip := -1; trip < k; trip++ {
		trip := trip
		over := func(i int) bool { return trip >= 0 && i >= trip }
		var rec func(prefix []string)
		rec = func(prefix []string) {
			calls, more := 0, false
			next, step := ModelInvoke(s, func(int) string {
				if calls < len(prefix) {
					calls++
					return prefix[calls-1]
				}
				more = true
				return OutEscalated // stop here; the branch below tries every outcome of this stage
			}, over)
			if !more {
				visit(next, step, over)
				return
			}
			for _, o := range modelOutcomes {
				rec(append(append([]string(nil), prefix...), o))
			}
		}
		rec(nil)
	}
}

// TestModelCheckRunMachine explores every state the model can reach from a new run, through any number of invocations
// with every world event between them, until no new state appears, and checks I1 to I6 on every transition. With the
// invocations renumbered by rank the state space is finite, so the search is exhaustive for this number of stages.
func TestModelCheckRunMachine(t *testing.T) {
	stagesN := modelStages()
	start := NewModel(stagesN)
	frontier := []ModelState{start}
	seen := map[string]bool{modelKey(start): true}
	transitions, violations, rounds := 0, 0, 0
	for len(frontier) > 0 {
		rounds++
		var next []ModelState
		for _, s := range frontier {
			for _, w := range worldEvents(s) {
				invocations(w, func(n ModelState, step ModelStep, over func(int) bool) {
					transitions++
					bad := append(CheckInvariants(n, step), CheckStep(w, n, step, over)...)
					if len(bad) > 0 {
						violations++
						if violations <= 5 {
							t.Errorf("from %s\nby %+v\nto %s:\n  %s", modelKey(w), step, modelKey(n), strings.Join(bad, "\n  "))
						}
					}
					nn := normalize(n)
					if k := modelKey(nn); !seen[k] {
						seen[k] = true
						next = append(next, nn)
					}
				})
			}
		}
		frontier = next
	}
	t.Logf("model: %d stages: %d states, %d transitions checked in %d rounds to a fixpoint, %d violations", stagesN, len(seen), transitions, rounds, violations)
	if len(seen) < 100 {
		t.Fatalf("the exploration reached only %d states: the enumeration is broken", len(seen))
	}
}

// The model checker must be able to fail: a machine without redo (R3) leaves a later stage with an older result than an
// earlier one, and I1 catches it.
func TestModelCheckerCatchesAMachineWithoutRedo(t *testing.T) {
	s := NewModel(2)
	s, _ = ModelInvoke(s, func(int) string { return OutDone }, func(int) bool { return false })
	s.Stages[0].Intact = false
	// a broken invocation that re-runs stage 0 but keeps stage 1
	broken := s
	broken.Invocation++
	broken.Stages = append([]ModelStage(nil), s.Stages...)
	broken.Stages[0] = ModelStage{Status: contract.StageDone, Artifact: true, Intact: true, FinishedIn: broken.Invocation}
	if bad := CheckInvariants(broken, ModelStep{Ran: []int{0}, BudgetStop: -1}); len(bad) == 0 || !strings.HasPrefix(bad[0], "I1") {
		t.Fatalf("I1 did not catch a kept stage after a re-run one: %v", bad)
	}
	good, step := ModelInvoke(s, func(int) string { return OutDone }, func(int) bool { return false })
	if len(step.Ran) != 2 || len(CheckInvariants(good, step)) != 0 {
		t.Fatalf("the real rule did not re-run stage 1: %+v %v", step, CheckInvariants(good, step))
	}
}

func TestStageTransitionsCoverEveryStatus(t *testing.T) {
	for _, st := range []string{contract.StagePending, contract.StageRunning, contract.StageDone, contract.StageSkipped, contract.StageEscalated, contract.StageFailed} {
		if _, ok := StageTransitions[st]; !ok {
			t.Errorf("status %s has no transitions", st)
		}
	}
	if AllowedTransition(contract.StageDone, contract.StageFailed) {
		t.Error("a finished stage cannot fail without running")
	}
}

// docs/STATE_MACHINE.md is rendered from fsm.go; -update rewrites it.
func TestStateMachineDoc(t *testing.T) {
	path := filepath.Join("..", "..", "docs", "STATE_MACHINE.md")
	want := StateMachineDoc()
	if *updateDoc {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s is not what fsm.go renders: run go test ./internal/run -run TestStateMachineDoc -update", path)
	}
}

// modelStages is how many stages the model checker explores: 3 by default (under a second; every rule of Run relates at
// most an earlier stage, the current one and a later one), 4 with A2DLC_MODEL_STAGES=4 (86841 states, about two minutes).
func modelStages() int {
	if os.Getenv("A2DLC_MODEL_STAGES") == "4" {
		return 4
	}
	return 3
}
