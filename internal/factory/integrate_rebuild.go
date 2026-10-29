package factory

import (
	"context"
	"fmt"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// RebuildDeps turns on the one rebuild of a conflicted lane (bead a2dlc-3fu.17, owner decision 2026-10-29). Config is the
// run's config, so the rebuild goes through the arm's own launcher, and Batch has the issues' text. Without it a lane
// that does not apply is dropped, as before.
type RebuildDeps struct {
	Config contract.FactoryConfig
	Batch  contract.FactoryBatch
}

// rebuilder runs the rebuilds of one integration. Its accountant starts from what the run already spent (lanes and
// triage), so the run's spend caps cover the rebuilds too, and the total cap counts every earlier run as well.
type rebuilder struct {
	cfg    contract.FactoryConfig
	issues map[string]contract.FactoryIssue
	acct   *accountant
	wts    *worktrees
	runID  string
	d      Deps
}

func newRebuilder(rd *RebuildDeps, d Deps, rep contract.FactoryReport, wts *worktrees, cancel context.CancelFunc) (*rebuilder, error) {
	if err := rd.Config.Validate(); err != nil {
		return nil, err
	}
	if err := rd.Batch.Validate(); err != nil {
		return nil, err
	}
	prior, err := PriorSpend(d.Out)
	if err != nil {
		return nil, err
	}
	// the run's own report is among the reports under Out, and its spend is the accountant's run spend below: count it once
	if prior -= rep.Usage.USD; prior < 0 {
		prior = 0
	}
	caps := rd.Config.Caps
	caps.RunWallSeconds = nil // the wall-time cap bounds the build phase, which is over by now
	acct := newAccountant(caps, prior, cancel)
	acct.run = rep.Usage
	issues := map[string]contract.FactoryIssue{}
	for _, is := range rd.Batch.Issues {
		issues[is.ID] = is
	}
	return &rebuilder{cfg: rd.Config, issues: issues, acct: acct, wts: wts, runID: rep.RunID, d: d}, nil
}

// build gives the issue one fresh build from the integration head, in a lane of its own (the issue's id plus -rb: its own
// branch, worktree, ledger, per-lane caps and test guard). The guard's base is the head, so the builder cannot edit a
// test file a landed lane added. It returns the rebuild's record and the candidate ref, empty if none came out of it.
func (r *rebuilder) build(ctx context.Context, head, issue string) (contract.IntegrationRebuild, string) {
	is, ok := r.issues[issue]
	if !ok {
		return contract.IntegrationRebuild{Outcome: contract.LaneSkipped, Reason: "the issue is not in the batch"}, ""
	}
	if ok, why := r.acct.mayStartLane(); !ok {
		return contract.IntegrationRebuild{Outcome: contract.LaneSkipped, Reason: why}, ""
	}
	is.ID += "-rb"
	lane := runLane(ctx, r.d, r.cfg, r.acct, r.wts, r.runID, head, is)
	rb := contract.IntegrationRebuild{Usage: lane.Usage, WallSeconds: lane.WallSeconds, Outcome: lane.Status, Reason: lane.Reason}
	if lane.Status == contract.LaneAccepted && lane.CandidateRef != nil {
		rb.CandidateRef = lane.CandidateRef
		return rb, *lane.CandidateRef
	}
	if rb.Outcome == contract.LaneAccepted {
		rb.Outcome, rb.Reason = contract.LaneError, "the rebuild was accepted but left no candidate"
	}
	return rb, ""
}

// integrateLane judges a queued lane's candidate and, if it does not apply on the integration head, gives the lane
// exactly one rebuild: a fresh build of the same issue starting from that head, judged the same way. A second conflict,
// a failed check or a failed holdout drops it. Either way the rebuild's spend is in the lane's record.
func integrateLane(ctx context.Context, d IntegrateDeps, spec contract.FactoryHoldout, wtRepo gitx.Repo, wt, out, runBase, ref string, integrated map[string]bool, l *contract.FactoryIntegrationLane) {
	judgeLane(ctx, d, spec, wtRepo, wt, out, runBase, ref, integrated, l)
	if l.Status != contract.IntegrationConflict || d.rebuilder == nil {
		return
	}
	head, err := gitIn(wt, "rev-parse", "HEAD")
	if err != nil {
		l.Reason += "; no rebuild: " + err.Error()
		return
	}
	firstConflicts, firstReason := l.Conflicts, l.Reason
	rb, newRef := d.rebuilder.build(ctx, head, l.IssueID)
	l.Rebuild = &rb
	if newRef == "" {
		l.Reason = fmt.Sprintf("%s; the rebuild ended %s: %s", firstReason, rb.Outcome, rb.Reason)
		return
	}
	l.Status, l.Reason, l.Conflicts, l.CheckVerdict, l.HoldoutExit, l.HoldoutFiles = "", "", nil, nil, nil, []string{}
	judgeLane(ctx, d, spec, wtRepo, wt, out, runBase, newRef, integrated, l)
	l.Conflicts = firstConflicts // the files of the first attempt, whatever the rebuild did
	if l.Status != contract.IntegrationIntegrated {
		l.Reason = fmt.Sprintf("the rebuild after %q: %s", strings.SplitN(firstReason, ":", 2)[0], l.Reason)
	}
}
