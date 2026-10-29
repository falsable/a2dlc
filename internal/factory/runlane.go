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

// runLane runs one issue to a lane record. It never panics the batch: whatever goes wrong becomes the lane's status.
func runLane(runCtx context.Context, d Deps, cfg contract.FactoryConfig, acct *accountant, wt *worktrees, runID, base string, is contract.FactoryIssue) contract.FactoryLane {
	slice := sliceID(is.ID, runID)
	branch := "factory/" + runID + "/" + is.ID
	dir := filepath.Join(d.Out, runID, "lanes", is.ID)
	lane := notRun(is.ID, contract.LaneError, "")
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
	intent := filepath.Join(d.Out, runID, "intents", is.ID+".json")
	if err := writeJSON(intent, intentFor(is, slice)); err != nil {
		lane.Reason = err.Error()
		return lane
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		lane.Reason = err.Error()
		return lane
	}

	laneCtx, cancel := context.WithTimeout(runCtx, time.Duration(cfg.Caps.LaneSeconds)*time.Second)
	defer cancel()
	st, runErr := run.Run(laneCtx, d.NewApp(repo), capLauncher{inner: d.Launcher, acct: acct, lane: is.ID, cfg: cfg},
		run.Options{Slice: slice, Mode: contract.ModeLite, Base: laneBase, Intent: intent, MaxCostUSD: cfg.Caps.LaneUSD})

	ledger := filepath.Join(dir, ".a2dlc", "run", slice, "ledger.json")
	lane.Usage, lane.UnreportedCalls, lane.Stages = laneUsage(ledger, st)
	keepLedger(ledger, filepath.Join(d.Out, runID, "ledgers", is.ID+".json"))
	lane.RunOutcome = st.Outcome
	lane.Status, lane.Reason = laneStatus(runCtx, laneCtx, acct, cfg, st, runErr)
	if lane.Status == contract.LaneAccepted {
		ref := "refs/a2dlc/run/" + slice + "/" + Role
		if _, err := gitIn(d.Repo.Dir, "rev-parse", "--verify", "-q", ref); err == nil {
			lane.CandidateRef = &ref
		}
	}
	return lane
}
