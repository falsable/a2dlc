package plan

import (
	"fmt"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// The plan machine (RFC-0008 section 5), written like the slice machine (internal/run/fsm.go): as data and a pure
// function, which the model checker explores (fsm_test.go) and the plan runner implements (runner.go, held to this model
// by its conformance test).

// MachineRules are the rules of one invocation of `a2dlc plan run`.
var MachineRules = []Rule{
	{"P1", "Slices are visited in topological order, ties broken by slice id; each invocation visits each slice at most once."},
	{"P2", "A slice runs only when every slice it depends on has passed, on a base that is the feature's base with those candidates merged in topological order."},
	{"P3", "A slice whose dependency did not pass is blocked, and so, transitively, is every slice that depends on it; nothing else is."},
	{"P4", "A passed slice whose run still verifies is kept; one that no longer verifies runs again, and so does every slice that depends on it (the plan-level redo)."},
	{"P5", "An escalated, failed or blocked slice is visited again on the next invocation (resume)."},
	{"P6", "The outcome is PASS when every slice passed and the merge of every candidate passes the final check; escalated when some slice escalated or the final merge or check did not pass; INCONCLUSIVE when some slice failed and none escalated."},
}

// MachineInvariants are checked on every state the model reaches and on every state the runner leaves on disk.
var MachineInvariants = []Rule{
	{"J1", "No stale base: a passed slice finished no earlier than every slice it depends on."},
	{"J2", "A passed slice has every slice it depends on passed; a blocked slice has one that did not pass."},
	{"J3", "A failure blocks exactly its dependents: a slice that is not passed, escalated or failed is blocked if and only if some slice it depends on did not pass."},
	{"J4", "The outcome is PASS if and only if every slice passed and the final check of their merge passed."},
	{"J5", "An invocation over a plan whose slices all passed and still verify runs nothing."},
	{"J6", "An invocation runs each slice at most once, so every invocation terminates."},
}

// Model outcomes of a slice run.
const (
	OutPassed    = "passed"
	OutEscalated = "escalated"
	OutFailed    = "failed"
)

// ModelSlice is what the model knows of one slice.
type ModelSlice struct {
	Status     string
	Verifies   bool // a passed slice whose run still verifies
	FinishedIn int
}

// ModelState is a plan run in the model. Deps[i] are the slices i depends on, by index; Order is the topological visit
// order (P1).
type ModelState struct {
	Deps       [][]int
	Order      []int
	Slices     []ModelSlice
	Outcome    string
	Invocation int
}

// ModelStep is what one invocation ran, in order.
type ModelStep struct {
	Ran []int
}

// NewModelState is a plan run that has not started.
func NewModelState(deps [][]int, order []int) ModelState {
	s := ModelState{Deps: deps, Order: order, Slices: make([]ModelSlice, len(deps))}
	for i := range s.Slices {
		s.Slices[i].Status = contract.PlanPending
	}
	return s
}

// ModelInvoke is one invocation of the plan runner. outcome says what the i-th slice ends in when it runs; finalOK says
// whether the merge of every candidate passes the final check.
func ModelInvoke(s ModelState, outcome func(i int) string, finalOK bool) (ModelState, ModelStep) {
	n := s
	n.Slices = append([]ModelSlice(nil), s.Slices...)
	n.Invocation = s.Invocation + 1
	var step ModelStep
	rerun := make([]bool, len(n.Slices)) // ran in this invocation
	for _, i := range n.Order {
		st := &n.Slices[i]
		depsPassed, depRerun := true, false
		for _, d := range n.Deps[i] {
			if n.Slices[d].Status != contract.PlanPassed {
				depsPassed = false
			}
			if rerun[d] {
				depRerun = true
			}
		}
		if !depsPassed { // P3
			st.Status = contract.PlanBlocked
			continue
		}
		if st.Status == contract.PlanPassed && st.Verifies && !depRerun { // P4
			continue
		}
		step.Ran = append(step.Ran, i) // P2, P4, P5
		rerun[i] = true
		st.FinishedIn = n.Invocation
		switch outcome(i) {
		case OutPassed:
			st.Status, st.Verifies = contract.PlanPassed, true
		case OutEscalated:
			st.Status, st.Verifies = contract.PlanEscalated, false
		case OutFailed:
			st.Status, st.Verifies = contract.PlanFailed, false
		default:
			panic("unknown plan model outcome")
		}
	}
	n.Outcome = modelOutcome(n, finalOK)
	return n, step
}

func modelOutcome(s ModelState, finalOK bool) string {
	all, escalated, failed := true, false, false
	for _, st := range s.Slices {
		switch st.Status {
		case contract.PlanPassed:
		case contract.PlanEscalated:
			all, escalated = false, true
		case contract.PlanFailed:
			all, failed = false, true
		default:
			all = false
		}
	}
	switch {
	case all && finalOK:
		return contract.PASS
	case all || escalated:
		return "escalated" // P6: a final merge or check that did not pass escalates
	case failed:
		return contract.INCONCLUSIVE
	}
	return "escalated"
}

// CheckModel returns the invariants s breaks after step (J1 to J4, J6); prev is the state before, for J5.
func CheckModel(prev, s ModelState, step ModelStep, finalOK bool) []string {
	var bad []string
	all := true
	for i, st := range s.Slices {
		notPassedDep := false
		for _, d := range s.Deps[i] {
			if s.Slices[d].Status != contract.PlanPassed {
				notPassedDep = true
			}
			if st.Status == contract.PlanPassed && s.Slices[d].FinishedIn > st.FinishedIn {
				bad = append(bad, fmt.Sprintf("J1: slice %d passed in invocation %d before its dependency %d (%d)", i, st.FinishedIn, d, s.Slices[d].FinishedIn))
			}
		}
		if st.Status == contract.PlanPassed && notPassedDep {
			bad = append(bad, fmt.Sprintf("J2: slice %d passed with a dependency that did not pass", i))
		}
		if st.Status == contract.PlanBlocked && !notPassedDep {
			bad = append(bad, fmt.Sprintf("J3: slice %d is blocked with every dependency passed", i))
		}
		if (st.Status == contract.PlanPending) && notPassedDep && s.Invocation > 0 {
			bad = append(bad, fmt.Sprintf("J3: slice %d was left pending with a dependency that did not pass", i))
		}
		if st.Status != contract.PlanPassed {
			all = false
		}
	}
	if (s.Outcome == contract.PASS) != (all && finalOK) {
		bad = append(bad, fmt.Sprintf("J4: outcome %s with every slice passed=%v and final check ok=%v", s.Outcome, all, finalOK))
	}
	seen := map[int]bool{}
	for _, i := range step.Ran {
		if seen[i] {
			bad = append(bad, fmt.Sprintf("J6: slice %d ran twice in one invocation", i))
		}
		seen[i] = true
	}
	settled := true
	for _, st := range prev.Slices {
		if st.Status != contract.PlanPassed || !st.Verifies {
			settled = false
		}
	}
	if settled && len(step.Ran) > 0 {
		bad = append(bad, "J5: a settled plan ran slices again")
	}
	return bad
}

// MachineDoc renders the plan machine for docs/PLAN_MACHINE.md.
func MachineDoc() string {
	var b strings.Builder
	b.WriteString("# The `a2dlc plan run` machine\n\n")
	b.WriteString("Above the slice machine ([STATE_MACHINE.md](STATE_MACHINE.md)), a feature plan (RFC-0008) runs its slices in dependency order: `a2dlc plan run`, one invocation at a time, state in `.a2dlc/run/_plan/<feature>/state.json` (`contracts/plan-state.schema.json`). Generated from `internal/plan/fsm.go`; the model checker is `internal/plan/fsm_test.go` and the conformance test of the runner is `internal/plan/runner_test.go`.\n\n")
	b.WriteString("Slice statuses in a plan: `pending`, `passed`, `escalated`, `failed`, `blocked`.\n\n")
	b.WriteString("```mermaid\nstateDiagram-v2\n    [*] --> pending\n    pending --> passed: its run passes\n    pending --> escalated\n    pending --> failed\n    pending --> blocked: a dependency did not pass\n")
	b.WriteString("    blocked --> passed: resume, dependencies passed\n    escalated --> passed: resume\n    failed --> passed: resume\n    passed --> passed: kept while its run verifies\n    passed --> escalated: runs again (it or a dependency stopped verifying)\n    passed --> failed: runs again\n    passed --> blocked: a dependency ran again and did not pass\n```\n\n")
	b.WriteString("## Rules of one invocation\n\n")
	for _, r := range MachineRules {
		b.WriteString("- **" + r.ID + ".** " + r.Title + "\n")
	}
	b.WriteString("\n## Invariants\n\n")
	for _, r := range MachineInvariants {
		b.WriteString("- **" + r.ID + ".** " + r.Title + "\n")
	}
	b.WriteString("\n## What one invocation does on disk\n\n")
	b.WriteString("- Each slice's intent is derived from the plan (`intents/<slice>.json`), with `boundary.allowed_paths` set to the slice's `owns`: a Builder change outside them escalates in Stage 3 (CB-04).\n")
	b.WriteString("- A slice's base is the feature's base with the candidates of the slices it depends on merged in visit order (`git merge-tree`, no working tree touched). A merge has a fixed author and date, so the same inputs give the same commit and a resumed slice finds its run on the same base; a single dependency is a fast-forward. Bases and the final merge are kept under `refs/a2dlc/plan/<feature>/` so `git gc` does not drop them.\n")
	b.WriteString("- A slice whose run is on another base (a dependency ran again) has its old run moved to `superseded/` and starts a new one: a run never changes base.\n")
	b.WriteString("- The final check is `a2dlc check` against the feature's base on the merged commit; its receipt is `final.json`, never `.a2dlc/verdict.json`. The plan's folder is under `.a2dlc/run`, so like a slice's run it stays out of the `tree_hash` (`gitx.Excluded`).\n")
	b.WriteString("- A changed plan file or another base is refused: the person moves the old state aside to start over.\n")
	b.WriteString("\n## Outside the model\n\n")
	b.WriteString("- Replanning on CB-02b (RFC-0008 P5) and slices of one level running at the same time: not implemented yet; slices run one after another.\n")
	b.WriteString("- What a slice's run ends in: that is the slice machine's judgment.\n")
	b.WriteString("\n## How it is checked\n\n")
	b.WriteString("- **Model checker** (`TestModelCheckPlanMachine`): every dependency graph on 4 slices (64 shapes, from four independent slices to a full chain), every outcome of every slice that runs, the final check passing or not, and a passed slice that stops verifying between invocations; breadth-first to a fixpoint, with invocations renumbered by rank.\n")
	b.WriteString("- **Conformance** (`TestRunnerConformsToTheModel`): the real runner over the same graphs with scripted slice runs and real git merges; after each invocation, the slices it ran, their order, their statuses and the outcome must be what the model predicts.\n")
	return b.String()
}
