package plan

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// RunExec runs each slice as an ordinary `a2dlc run` (P2) in its own worktree, so slices that run at the same time never
// share a working tree, a .a2dlc/verdict.json or an escalations.json. The worktrees live in the repository's git directory
// (<common dir>/a2dlc-plan/<feature>/<slice>), where `git status` does not list them, and persist across invocations so a
// slice's run can be resumed and verified. The slice's intent is the one the runner derived from the plan, so Stage 3
// holds the Builder to the slice's owns (boundary.allowed_paths, CB-04).
type RunExec struct {
	App      app.App
	Launcher a2run.Launcher
	// Options fills what the CLI passes to a run besides slice, base and intent (the Z3 engine, the spec rounds).
	Options a2run.Options
	// Feature is the plan's feature id: the worktrees and the superseded runs go under it.
	Feature string
}

// Worktree is the folder of a slice's worktree.
func (e RunExec) Worktree(slice string) (string, error) {
	common, err := e.App.Repo.CommonDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(common, "a2dlc-plan", e.Feature, slice), nil
}

// sliceApp is the App of a slice: the same configuration over the slice's worktree.
func (e RunExec) sliceApp(slice string) (app.App, error) {
	wt, err := e.Worktree(slice)
	if err != nil {
		return app.App{}, err
	}
	a := e.App
	a.Repo = gitx.Repo{Dir: wt}
	return a, nil
}

// Run resumes the slice's run when it is on the same base, and otherwise moves the old run aside under the plan's
// folder (superseded/) and starts a new one: a dependency that ran again changes the base, and a run never changes base.
// roles.json is not committed in most repositories, so the repository's own is copied into the worktree before each run.
func (e RunExec) Run(ctx context.Context, s contract.PlanSlice, intentPath, base string) (SliceRun, error) {
	a, err := e.sliceApp(s.SliceID)
	if err != nil {
		return SliceRun{}, err
	}
	dir := a2run.RunDir(a, s.SliceID)
	if st, err := readRunState(dir); err == nil && st.BaseSha != base {
		dst := filepath.Join(StateDir(e.App.Repo.Dir, e.Feature), "superseded", s.SliceID+"-"+st.RunID)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return SliceRun{}, err
		}
		if err := os.Rename(dir, dst); err != nil {
			return SliceRun{}, fmt.Errorf("moving the run of %s on an old base aside: %w", s.SliceID, err)
		}
	}
	if err := e.App.Repo.WorktreeAt(a.Repo.Dir, base); err != nil {
		return SliceRun{}, fmt.Errorf("worktree of %s: %w", s.SliceID, err)
	}
	if b, err := os.ReadFile(filepath.Join(e.App.Repo.Dir, ".a2dlc", "roles.json")); err == nil {
		if err := os.MkdirAll(filepath.Join(a.Repo.Dir, ".a2dlc"), 0o755); err != nil {
			return SliceRun{}, err
		}
		if err := os.WriteFile(filepath.Join(a.Repo.Dir, ".a2dlc", "roles.json"), b, 0o644); err != nil {
			return SliceRun{}, err
		}
	}
	opts := e.Options
	opts.Slice, opts.Base, opts.Intent, opts.Mode = s.SliceID, base, intentPath, ""
	st, err := a2run.Run(ctx, a, e.Launcher, opts)
	if err != nil {
		return SliceRun{RunDir: dir}, err
	}
	res, err := outcome(a, st)
	res.RunDir = dir
	return res, err
}

func outcome(a app.App, st contract.RunState) (SliceRun, error) {
	reason := lastReason(st)
	switch {
	case st.Outcome == nil:
		return SliceRun{Status: contract.PlanFailed, Reason: "the run ended without a verdict: " + reason}, nil
	case *st.Outcome == contract.PASS:
		c, err := a2run.Candidate(a, st)
		if err != nil {
			return SliceRun{}, err
		}
		return SliceRun{Status: contract.PlanPassed, Candidate: c, Reason: "PASS (run " + st.RunID + ")"}, nil
	case *st.Outcome == "escalated":
		return SliceRun{Status: contract.PlanEscalated, Reason: reason, Exhausted: exhaustedRun(st)}, nil
	}
	return SliceRun{Status: contract.PlanFailed, Reason: reason}, nil
}

// exhaustedRun says whether the run stopped at CB-02b: Stage 3 escalated with every Builder's rounds spent.
func exhaustedRun(st contract.RunState) bool {
	for _, s := range st.Stages {
		if s.ID == "3" && s.Status == contract.StageEscalated && strings.HasPrefix(s.Reason, "CB-02b") {
			return true
		}
	}
	return false
}

// Verifies is verify-run over the slice's run, plus: it is still on the base and with the candidate the plan recorded.
func (e RunExec) Verifies(s contract.PlanSlice, prev contract.PlanSliceState) bool {
	if prev.Base == nil || prev.Candidate == nil {
		return false
	}
	a, err := e.sliceApp(s.SliceID)
	if err != nil {
		return false
	}
	st, err := readRunState(a2run.RunDir(a, s.SliceID))
	if err != nil || st.BaseSha != *prev.Base || st.Outcome == nil || *st.Outcome != contract.PASS {
		return false
	}
	if problems, err := a2run.VerifyRun(a, s.SliceID); err != nil || len(problems) > 0 {
		return false
	}
	c, err := a2run.Candidate(a, st)
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
