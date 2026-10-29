package factory

import (
	"context"
	"os"
	"path/filepath"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/run"
)

// keepLedger copies the lane's ledger out of its worktree, which is removed when the lane ends: the ledger is the
// per-call record the report's per-stage cost comes from. A lane that never got to write one leaves nothing to keep.
func keepLedger(from, to string) {
	b, err := os.ReadFile(from)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(to), 0o755) == nil {
		os.WriteFile(to, b, 0o644)
	}
}

// keepResponses copies the raw response.json of every launch of the lane (what claude answered, as the adapter wrote it)
// out of its worktree, which is removed when the lane ends: the adapter's ledger entry keeps only cost, turns and status,
// and without this nothing holds the model's answer after a paid run, so the leak scan (e2e/factory/check_leak.sh) of the
// lane's output could not read it. One file per launch, named after the launch folder, in the lane's own folder under
// responses/: <out>/<run id>/responses/<issue id>/<NNN>-<stage>-<role>-<attempt>.json. NNN is the launch's position in the
// ledger, so the file's sha256 is the sha256 of "/out/response.json" in entry NNN of ledgers/<issue id>.json, which
// hashed it when the launch returned. A launch that wrote no response.json (an error, a kill) leaves no file.
func keepResponses(launches, to string) {
	dirs, err := os.ReadDir(launches)
	if err != nil {
		return
	}
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(launches, d.Name(), "out", "response.json"))
		if err != nil || !d.IsDir() {
			continue
		}
		if os.MkdirAll(to, 0o755) == nil {
			os.WriteFile(filepath.Join(to, d.Name()+".json"), b, 0o644)
		}
	}
}

// runLane runs one issue to a lane record. It never panics the batch: whatever goes wrong becomes the lane's status.
func runLane(runCtx context.Context, d Deps, cfg contract.FactoryConfig, acct *accountant, wt *worktrees, runID, base string, is contract.FactoryIssue) (lane contract.FactoryLane) {
	slice := sliceID(is.ID, runID)
	branch := "factory/" + runID + "/" + is.ID
	dir := filepath.Join(d.Out, runID, "lanes", is.ID)
	lane = notRun(is.ID, contract.LaneError, "")
	lane.SliceID, lane.Branch = &slice, &branch
	started := time.Now()
	defer func() { lane.WallSeconds = time.Since(started).Seconds() }()

	if err := wt.add(dir, branch, base); err != nil {
		lane.Reason = err.Error()
		return lane
	}
	defer wt.remove(dir)
	if err := writeJSON(filepath.Join(dir, ".a2dlc", "roles.json"), LaneRoles(cfg)); err != nil {
		lane.Reason = err.Error()
		return lane
	}
	laneBase, err := commitFile(dir, ".a2dlc/roles.json", "factory: roles for "+slice)
	if err != nil {
		lane.Reason = err.Error()
		return lane
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		lane.Reason = err.Error()
		return lane
	}

	guard, err := newTestGuard(repo, laneBase)
	if err != nil {
		lane.Reason = err.Error()
		return lane
	}
	intent := filepath.Join(d.Out, runID, "intents", is.ID+".json")
	if err := writeJSON(intent, laneIntent(is, slice, guard.globs)); err != nil {
		lane.Reason = err.Error()
		return lane
	}
	laneCtx, cancel := context.WithTimeout(runCtx, time.Duration(cfg.Caps.LaneSeconds)*time.Second)
	defer cancel()
	st, runErr := run.Run(laneCtx, d.NewApp(repo), capLauncher{inner: d.Launcher, acct: acct, lane: is.ID, cfg: cfg, guard: guard},
		run.Options{Slice: slice, Mode: contract.ModeLite, Base: laneBase, Intent: intent, MaxCostUSD: cfg.Caps.LaneUSD})

	ledger := filepath.Join(dir, ".a2dlc", "run", slice, "ledger.json")
	lane.Usage, lane.UnreportedCalls, lane.Stages = laneUsage(ledger, st)
	keepLedger(ledger, filepath.Join(d.Out, runID, "ledgers", is.ID+".json"))
	keepResponses(filepath.Join(dir, ".a2dlc", "run", slice, "launches"), filepath.Join(d.Out, runID, "responses", is.ID))
	lane.RunOutcome = st.Outcome
	lane.TestsTouched = guard.result()
	lane.Status, lane.Reason = laneStatus(runCtx, laneCtx, acct, cfg, st, runErr, lane.TestsTouched)
	if lane.Status == contract.LaneAccepted {
		ref := "refs/a2dlc/run/" + slice + "/" + Role
		if _, err := gitIn(d.Repo.Dir, "rev-parse", "--verify", "-q", ref); err == nil {
			lane.CandidateRef = &ref
		}
	}
	return lane
}
