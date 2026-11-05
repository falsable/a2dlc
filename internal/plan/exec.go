package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// RunExec runs each slice as an ordinary `a2dlc run` (P2): its intent is the one the runner derived from the plan, so
// Stage 3 holds the Builder to the slice's owns (boundary.allowed_paths, CB-04).
type RunExec struct {
	App      app.App
	Launcher a2run.Launcher
	// Options fills what the CLI passes to a run besides slice, base and intent (the Z3 engine, the spec rounds).
	Options a2run.Options
	// Feature is the plan's feature id: superseded runs go under its folder.
	Feature string
}

// Run resumes the slice's run when it is on the same base, and otherwise moves the old run aside under the plan's
// folder (superseded/) and starts a new one: a dependency that ran again changes the base, and a run never changes base.
func (e RunExec) Run(ctx context.Context, s contract.PlanSlice, intentPath, base string) (SliceRun, error) {
	dir := a2run.RunDir(e.App, s.SliceID)
	if st, err := readRunState(dir); err == nil && st.BaseSha != base {
		dst := filepath.Join(StateDir(e.App.Repo.Dir, e.Feature), "superseded", s.SliceID+"-"+st.RunID)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return SliceRun{}, err
		}
		if err := os.Rename(dir, dst); err != nil {
			return SliceRun{}, fmt.Errorf("moving the run of %s on an old base aside: %w", s.SliceID, err)
		}
	}
	opts := e.Options
	opts.Slice, opts.Base, opts.Intent, opts.Mode = s.SliceID, base, intentPath, ""
	st, err := a2run.Run(ctx, e.App, e.Launcher, opts)
	if err != nil {
		return SliceRun{}, err
	}
	return e.outcome(st)
}

func (e RunExec) outcome(st contract.RunState) (SliceRun, error) {
	reason := lastReason(st)
	switch {
	case st.Outcome == nil:
		return SliceRun{Status: contract.PlanFailed, Reason: "the run ended without a verdict: " + reason}, nil
	case *st.Outcome == contract.PASS:
		c, err := a2run.Candidate(e.App, st)
		if err != nil {
			return SliceRun{}, err
		}
		return SliceRun{Status: contract.PlanPassed, Candidate: c, Reason: "PASS (run " + st.RunID + ")"}, nil
	case *st.Outcome == "escalated":
		return SliceRun{Status: contract.PlanEscalated, Reason: reason}, nil
	}
	return SliceRun{Status: contract.PlanFailed, Reason: reason}, nil
}

// Verifies is verify-run over the slice's run, plus: it is still on the base and with the candidate the plan recorded.
func (e RunExec) Verifies(s contract.PlanSlice, prev contract.PlanSliceState) bool {
	if prev.Base == nil || prev.Candidate == nil {
		return false
	}
	st, err := readRunState(a2run.RunDir(e.App, s.SliceID))
	if err != nil || st.BaseSha != *prev.Base || st.Outcome == nil || *st.Outcome != contract.PASS {
		return false
	}
	if problems, err := a2run.VerifyRun(e.App, s.SliceID); err != nil || len(problems) > 0 {
		return false
	}
	c, err := a2run.Candidate(e.App, st)
	return err == nil && c == *prev.Candidate
}

// AppFinalCheck is the final check of P6: `a2dlc check` against the feature's base on the merged commit, with the
// receipt in the plan's folder (final.json), never .a2dlc/verdict.json.
func AppFinalCheck(a app.App, feature contract.Intent) FinalCheck {
	return func(ctx context.Context, base, merged string) (bool, string, error) {
		rel := pathPlan + "/" + feature.SliceID + "/final.json" // CheckOptions.ReportPath is relative to the repository
		v, err := a.CheckWith(ctx, base, app.CheckOptions{CandidateTree: merged, ReportPath: rel, ChangeKind: feature.ChangeKind})
		if err != nil {
			return false, "", err
		}
		return v.Verdict == contract.PASS, v.Verdict + " (" + rel + ")", nil
	}
}

func readRunState(dir string) (contract.RunState, error) {
	var st contract.RunState
	b, err := os.ReadFile(filepath.Join(dir, "state.json"))
	if err != nil {
		return st, err
	}
	return st, contract.DecodeStrictBytes(b, &st)
}

func lastReason(st contract.RunState) string {
	for i := len(st.Stages) - 1; i >= 0; i-- {
		if s := st.Stages[i]; s.Status == contract.StageEscalated || s.Status == contract.StageFailed {
			return "stage " + s.ID + " " + s.Status + ": " + s.Reason
		}
	}
	return "no stage escalated or failed"
}
