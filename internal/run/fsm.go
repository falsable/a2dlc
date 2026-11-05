package run

import (
	"fmt"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// The formal state machine of `a2dlc run` (protocol §5). It is written as data and as a pure function so that three things
// read the same definition:
//
//   - people, through docs/STATE_MACHINE.md, which StateMachineDoc renders from it (a test fails when the file is stale);
//   - the model checker (fsm_test.go), which explores every reachable state of the model for a small number of stages and
//     checks the invariants below on each one;
//   - the conformance test (fsm_conformance_test.go), which drives the real Run with scripted stages and checks that the
//     implementation lands, invocation after invocation, in the state the model predicts.
//
// The model covers the orchestration Run performs over the stages: order, resume, re-running, the budget breaker and the
// stop on escalation. What happens inside a stage (the Tier 0 loop, the Saboteur rounds) is each stage's own bounded loop
// and is outside it.

// StageTransitions are the status changes a stage may make. A stage starts pending; Run puts it in running before calling
// it; the stage ends in one of four statuses; any finished stage can be run again on a later invocation (resume or redo).
var StageTransitions = map[string][]string{
	contract.StagePending:   {contract.StageRunning, contract.StageEscalated},
	contract.StageRunning:   {contract.StageDone, contract.StageSkipped, contract.StageEscalated, contract.StageFailed},
	contract.StageDone:      {contract.StageRunning, contract.StageEscalated},
	contract.StageSkipped:   {contract.StageRunning, contract.StageEscalated},
	contract.StageEscalated: {contract.StageRunning, contract.StageEscalated},
	contract.StageFailed:    {contract.StageRunning, contract.StageEscalated},
}

// AllowedTransition says whether a stage may go from one status to another (StageTransitions). Staying in the same status
// is always allowed: a stage Run does not visit keeps what it had.
func AllowedTransition(from, to string) bool {
	if from == to {
		return true
	}
	for _, x := range StageTransitions[from] {
		if x == to {
			return true
		}
	}
	return false
}

// RunRule is one rule of an invocation of Run, in the order Run applies them to each stage.
type RunRule struct {
	ID, Text string
}

// RunRules are the rules ModelInvoke implements, and Run with it.
var RunRules = []RunRule{
	{"R1", "Stages are visited in the fixed order of contract.Stages, one invocation at a time; each invocation visits each stage at most once."},
	{"R2", "Until some stage of this invocation has run, a stage that is done with its artifact intact is kept, and so is a stage that is skipped without an artifact whose reason the config has not made stale."},
	{"R3", "Any other stage runs. Once one stage of an invocation has run, every later stage of that invocation runs too (redo): a new result never sits on top of later results computed from an older one."},
	{"R4", "Before a stage runs, the budget breaker (CB-03) is checked: over the limit, the stage ends escalated with the budget reason, it is not called, and the invocation stops."},
	{"R5", "A stage that ends escalated sets the outcome to escalated; one that ends failed sets it to INCONCLUSIVE; in both cases the invocation stops and no later stage is visited."},
	{"R6", "A stage that ends done or skipped lets the invocation go on to the next stage; the outcome is set by the stages themselves (Stages 4 to 7 write PASS), not by Run."},
	{"R7", "Between invocations only the world changes: an artifact can be altered, a skip can become stale, the budget can be raised. The state on disk is the only memory."},
	{"R8", "Stage 0's artifact is the intent, and opening a run binds it: an intent changed since Stage 0 recorded it makes Run refuse to resume (delete the run's folder to start over), instead of re-running Stage 0 under the stages already judged against the old one."},
}

// Invariants the model checker verifies on every reachable state, and the conformance test on every state the real Run
// leaves on disk.
var Invariants = []RunRule{
	{"I1", "No stale result: along the stages, ignoring pending ones, the invocation that last finished each stage never decreases up to the first stage that is not done or skipped."},
	{"I2", "At most one stage is escalated or failed among those the last invocation visited, and it is the last one that invocation visited."},
	{"I3", "Every status change of a stage is in StageTransitions."},
	{"I4", "A stage never runs while the budget is exceeded."},
	{"I5", "An invocation visits at most as many stages as there are, so every invocation terminates."},
	{"I6", "An invocation over a state where every stage is done with an intact artifact, or skipped and not stale, runs nothing (resume is idempotent)."},
	{"I7", "A stage that is done has an artifact: Run refuses a stage that ends done without one, because resume keeps a done stage by its artifact (R2) and verify-run requires it."},
}

// Stage outcomes a model stage can produce when it runs.
const (
	OutDone       = "done"
	OutSkipped    = "skipped"     // skipped without an artifact
	OutSkippedArt = "skipped_art" // skipped leaving an artifact (Run never keeps it: it runs again)
	OutEscalated  = "escalated"
	OutFailed     = "failed"
)

// ModelStage is what the model knows of one stage.
type ModelStage struct {
	Status     string
	Artifact   bool // the stage left an artifact
	Intact     bool // the artifact is as it was hashed
	StaleSkip  bool // a skip whose reason the config no longer supports
	FinishedIn int  // the invocation that last finished it (0: never)
}

// ModelState is the model's whole state: the stages, the outcome and the invocation counter.
type ModelState struct {
	Stages     []ModelStage
	Outcome    string // "" (none), "escalated", "INCONCLUSIVE", or what a stage wrote
	Invocation int
}

// ModelStep is what one invocation did: the stages it ran, in order, and whether the budget stopped it.
type ModelStep struct {
	Ran        []int
	BudgetStop int // index of the stage the budget breaker escalated, -1 if none
}

// ModelInvoke is one invocation of Run in the model. outcome says what the i-th stage ends in when it runs; overBudget
// says whether CB-03 trips before the i-th stage would run. It returns the next state and what the invocation did.
func ModelInvoke(s ModelState, outcome func(i int) string, overBudget func(i int) bool) (ModelState, ModelStep) {
	n := ModelState{Stages: append([]ModelStage(nil), s.Stages...), Outcome: s.Outcome, Invocation: s.Invocation + 1}
	step := ModelStep{BudgetStop: -1}
	redo := false
	for i := range n.Stages {
		st := &n.Stages[i]
		if !redo && st.Status == contract.StageDone && st.Artifact && st.Intact { // R2
			continue
		}
		if !redo && st.Status == contract.StageSkipped && !st.Artifact && !st.StaleSkip { // R2
			continue
		}
		if overBudget(i) { // R4
			st.Status = contract.StageEscalated
			n.Outcome = "escalated"
			step.BudgetStop = i
			return n, step
		}
		redo = true // R3
		step.Ran = append(step.Ran, i)
		o := outcome(i)
		st.FinishedIn, st.StaleSkip = n.Invocation, false
		switch o {
		case OutDone:
			st.Status, st.Artifact, st.Intact = contract.StageDone, true, true
		case OutSkipped:
			st.Status, st.Artifact, st.Intact = contract.StageSkipped, false, false
		case OutSkippedArt:
			st.Status, st.Artifact, st.Intact = contract.StageSkipped, true, true
		case OutEscalated:
			st.Status, st.Artifact, st.Intact = contract.StageEscalated, false, false
			n.Outcome = "escalated" // R5
			return n, step
		case OutFailed:
			st.Status, st.Artifact, st.Intact = contract.StageFailed, false, false
			n.Outcome = contract.INCONCLUSIVE // R5
			return n, step
		default:
			panic("unknown model outcome " + o)
		}
	}
	return n, step
}

// NewModel is the state of a run that has not started: every stage pending.
func NewModel(stages int) ModelState {
	m := ModelState{Stages: make([]ModelStage, stages)}
	for i := range m.Stages {
		m.Stages[i].Status = contract.StagePending
	}
	return m
}

// CheckInvariants returns the invariants s breaks after step (I1, I2, I5); I3, I4 and I6 need the previous state and
// are checked by CheckStep.
func CheckInvariants(s ModelState, step ModelStep) []string {
	var bad []string
	last := 0
	for i, st := range s.Stages {
		if st.Status != contract.StageDone && st.Status != contract.StageSkipped {
			break
		}
		if st.FinishedIn < last {
			bad = append(bad, fmt.Sprintf("I1: stage %d finished in invocation %d, before stage %d (%d)", i, st.FinishedIn, i-1, last))
		}
		last = st.FinishedIn
	}
	visitedEnd := -1
	if len(step.Ran) > 0 {
		visitedEnd = step.Ran[len(step.Ran)-1]
	}
	if step.BudgetStop >= 0 {
		visitedEnd = step.BudgetStop
	}
	for i, st := range s.Stages {
		if st.Status == contract.StageDone && !st.Artifact {
			bad = append(bad, fmt.Sprintf("I7: stage %d is done without an artifact", i))
		}
	}
	stopped := 0
	for i, st := range s.Stages {
		if st.FinishedIn == s.Invocation || i == step.BudgetStop {
			if st.Status == contract.StageEscalated || st.Status == contract.StageFailed {
				stopped++
				if i != visitedEnd {
					bad = append(bad, fmt.Sprintf("I2: stage %d stopped the invocation but stage %d was visited after it", i, visitedEnd))
				}
			}
		}
	}
	if stopped > 1 {
		bad = append(bad, fmt.Sprintf("I2: %d stages stopped one invocation", stopped))
	}
	if len(step.Ran) > len(s.Stages) {
		bad = append(bad, "I5: an invocation ran more stages than there are")
	}
	return bad
}

// CheckStep returns the invariants the change from prev to next breaks (I3, I4, I6), given what the invocation did and
// which stages were over budget.
func CheckStep(prev, next ModelState, step ModelStep, overBudget func(i int) bool) []string {
	var bad []string
	ran := map[int]bool{}
	for _, i := range step.Ran {
		ran[i] = true
		if overBudget(i) {
			bad = append(bad, fmt.Sprintf("I4: stage %d ran over budget", i))
		}
	}
	for i := range next.Stages {
		from, to := prev.Stages[i].Status, next.Stages[i].Status
		if ran[i] {
			from = contract.StageRunning // R3: Run marks it running before calling it
			if !AllowedTransition(prev.Stages[i].Status, contract.StageRunning) {
				bad = append(bad, fmt.Sprintf("I3: stage %d went %s -> running", i, prev.Stages[i].Status))
			}
		}
		if !AllowedTransition(from, to) {
			bad = append(bad, fmt.Sprintf("I3: stage %d went %s -> %s", i, from, to))
		}
	}
	settled := true
	for _, st := range prev.Stages {
		keep := (st.Status == contract.StageDone && st.Artifact && st.Intact) ||
			(st.Status == contract.StageSkipped && !st.Artifact && !st.StaleSkip)
		if !keep {
			settled = false
			break
		}
	}
	if settled && len(step.Ran) > 0 {
		bad = append(bad, "I6: a settled run ran stages again")
	}
	return bad
}

// StateMachineDoc renders the machine for people (docs/STATE_MACHINE.md).
func StateMachineDoc() string {
	var b strings.Builder
	b.WriteString("# The `a2dlc run` state machine\n\n")
	b.WriteString("Generated from `internal/run/fsm.go` by `go test ./internal/run -run TestStateMachineDoc -update`; a test fails when this file and the code disagree. ")
	b.WriteString("The same definition is what the model checker explores (`fsm_test.go`) and what the conformance test holds the real `Run` to (`fsm_conformance_test.go`).\n\n")
	b.WriteString("## Stages\n\n")
	b.WriteString("`" + strings.Join(contract.Stages, "` → `") + "`, in this order, every run. A stage that does not apply to the mode ends `skipped` with its reason.\n\n")
	b.WriteString("## Stage statuses\n\n| From | May go to |\n|---|---|\n")
	for _, from := range []string{contract.StagePending, contract.StageRunning, contract.StageDone, contract.StageSkipped, contract.StageEscalated, contract.StageFailed} {
		b.WriteString("| `" + from + "` | `" + strings.Join(StageTransitions[from], "`, `") + "` |\n")
	}
	b.WriteString("\n`escalated` is reachable from any status without running, through the budget breaker (R4).\n\n")
	b.WriteString("```mermaid\nstateDiagram-v2\n    [*] --> pending\n    pending --> running\n    running --> done\n    running --> skipped\n    running --> escalated\n    running --> failed\n")
	b.WriteString("    done --> running: redo or artifact changed\n    skipped --> running: redo or stale reason\n    escalated --> running: resume\n    failed --> running: resume\n")
	b.WriteString("    pending --> escalated: CB-03\n    done --> escalated: CB-03\n    skipped --> escalated: CB-03\n    failed --> escalated: CB-03\n```\n\n")
	b.WriteString("## Rules of one invocation\n\n")
	for _, r := range RunRules {
		b.WriteString("- **" + r.ID + ".** " + r.Text + "\n")
	}
	b.WriteString("\n## Invariants\n\nChecked on every reachable state of the model, and on every state the real `Run` leaves on disk in the conformance test.\n\n")
	for _, r := range Invariants {
		b.WriteString("- **" + r.ID + ".** " + r.Text + "\n")
	}
	b.WriteString("\n## How it is checked\n\n")
	b.WriteString("- **Model checker** (`TestModelCheckRunMachine`): breadth-first search over every state the model reaches from a new run, through any number of invocations, with every outcome of every stage that runs, every point from which the budget breaker trips, and every world event between invocations; invocations are renumbered by rank, so the search reaches a fixpoint. By default 3 stages (3436 states, 450740 transitions, under a second); with `A2DLC_MODEL_STAGES=4`, 86841 states and 39919654 transitions (about two minutes). Every rule relates at most an earlier stage, the current one and a later one, which is why 3 is the default.\n")
	b.WriteString("- **Conformance** (`TestRunConformsToTheModel`): 120 seeded traces of the real `Run` with every stage replaced by a scripted one; after each invocation the stages called, their order, the statuses, the artifacts and the outcome on disk must be what the model predicts, and the invariants must hold on disk. Breaking R3 in `machine.go` makes 77 of the 120 traces fail.\n")
	b.WriteString("- **Self-test** (`TestModelCheckerCatchesAMachineWithoutRedo`): the invariants reject a machine that keeps a later stage after re-running an earlier one.\n")
	b.WriteString("\n## Outside the model\n\n")
	b.WriteString("- The loops inside a stage (Tier 0 rounds, Saboteur rounds, specification rounds): each is bounded by its own limit (`max_inner_iterations`, `--max-rounds`, `--max-spec-rounds`), so a stage terminates; their content is the stage's.\n")
	b.WriteString("- Which outcome a stage produces: that is the stage's judgment (§5), not the machine's.\n")
	b.WriteString("- The budget breaker in the conformance test: the model checks R4 and I4; the real breaker is covered by the run's budget tests, because its spend comes from the ledger.\n")
	b.WriteString("- A feature of several slices: the plan machine above this one runs each slice through it, in dependency order ([PLAN_MACHINE.md](PLAN_MACHINE.md), RFC-0008).\n")
	return b.String()
}
