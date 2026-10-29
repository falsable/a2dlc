package factory

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// IntegrateDeps is what Integrate needs besides a run's own Deps (the target repository, NewApp, Now, Out).
type IntegrateDeps struct {
	Deps
	Holdout HoldoutRunner
	MainRef string // the branch lanes would be merged into, only read: empty = HEAD
}

// Integrate is the merge queue of a run (bead a2dlc-3fu.4, ADR meta/0008). The accepted lanes of rep are tried one at a
// time, in the order of the report, on an integration branch that starts at the run's base: each lane's candidate is
// applied (rebased) on the branch as it stands, then judged by a2dlc check against the branch and by the sealed holdout
// on the integrated tree, and only a lane that passes both is committed. A lane that conflicts, fails the check or fails
// the holdout is rolled back and never lands. Main is never written: the record carries its sha before and after, and a
// difference is an error.
//
// "Accepted" for the factory is this: the lane's a2dlc run passed (the report's accepted status, which already excludes
// a lane that edited base tests), the check passes on the integrated tree without a pending waiver, and the holdout
// passes there too. A waiver never counts, so a lane that touches .a2dlc/ is not integrated.
func Integrate(ctx context.Context, d IntegrateDeps, spec contract.FactoryHoldout, rep contract.FactoryReport) (contract.FactoryIntegration, error) {
	if err := spec.Validate(); err != nil {
		return contract.FactoryIntegration{}, err
	}
	if d.Holdout == nil || d.NewApp == nil {
		return contract.FactoryIntegration{}, fmt.Errorf("integrate needs a holdout runner and NewApp")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	// the seal is also checked before every mount; checking it first spares a run whose holdout is already wrong
	if seal, err := VerifySeal(spec.Dir); err != nil {
		return contract.FactoryIntegration{}, err
	} else if seal != spec.Seal {
		return contract.FactoryIntegration{}, fmt.Errorf("the holdout in %s is sealed as %s and the spec names %s", spec.Dir, seal, spec.Seal)
	}
	mainRef := d.MainRef
	if mainRef == "" {
		mainRef = "HEAD"
	}
	mainBefore, err := gitIn(d.Repo.Dir, "rev-parse", "--verify", mainRef+"^{commit}")
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	out, err := filepath.Abs(filepath.Join(d.Out, rep.RunID))
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	started := d.Now()
	branch := "factory/" + rep.RunID + "/integration"
	path := filepath.Join(out, "integration")
	wts := &worktrees{dir: d.Repo.Dir}
	if err := wts.add(path, branch, rep.BaseSha); err != nil {
		return contract.FactoryIntegration{}, err
	}
	defer wts.remove(path)
	wtRepo, err := gitx.Open(path)
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	rec := contract.FactoryIntegration{SchemaVersion: 1, RunID: rep.RunID, BatchID: rep.BatchID, BaseSha: rep.BaseSha, HoldoutSeal: spec.Seal,
		MainRef: mainRef, MainShaBefore: mainBefore, IntegrationBranch: branch, StartedAt: started.UTC().Format(time.RFC3339),
		Lanes: []contract.FactoryIntegrationLane{}}
	integrated := map[string]bool{}
	position := 0
	for _, lane := range rep.Lanes {
		if lane.Status != contract.LaneAccepted || lane.CandidateRef == nil {
			rec.Lanes = append(rec.Lanes, contract.FactoryIntegrationLane{IssueID: lane.IssueID, Status: contract.IntegrationNotQueued,
				Reason: fmt.Sprintf("the lane is %s: %s", lane.Status, lane.Reason), HoldoutFiles: []string{}})
			continue
		}
		pos := position
		position++
		l := contract.FactoryIntegrationLane{IssueID: lane.IssueID, QueuePosition: &pos, HoldoutFiles: []string{}}
		integrateLane(ctx, d, spec, wtRepo, path, out, rep.BaseSha, *lane.CandidateRef, integrated, &l)
		if l.Status == contract.IntegrationIntegrated {
			integrated[lane.IssueID] = true
			rec.Integrated++
		}
		rec.Lanes = append(rec.Lanes, l)
	}
	if rec.HeadSha, err = gitIn(path, "rev-parse", "HEAD"); err != nil {
		return rec, err
	}
	if rec.MainShaAfter, err = gitIn(d.Repo.Dir, "rev-parse", "--verify", mainRef+"^{commit}"); err != nil {
		return rec, err
	}
	rec.FinishedAt = d.Now().UTC().Format(time.RFC3339)
	if err := rec.Validate(); err != nil {
		return rec, err
	}
	return rec, writeJSON(filepath.Join(out, "integration.json"), rec)
}

// integrateLane judges one queued lane on the integration worktree and fills l; whatever it does not integrate it rolls
// back, so the next lane starts from the branch head.
func integrateLane(ctx context.Context, d IntegrateDeps, spec contract.FactoryHoldout, wtRepo gitx.Repo, wt, out, runBase, ref string, integrated map[string]bool, l *contract.FactoryIntegrationLane) {
	stop := func(status, format string, a ...any) {
		l.Status, l.Reason = status, fmt.Sprintf(format, a...)
		rollback(wt)
	}
	if !holdoutUnit(spec, l.IssueID) {
		stop(contract.IntegrationNoHoldout, "the holdout has no unit for %s, so it cannot be accepted", l.IssueID)
		return
	}
	head, err := gitIn(wt, "rev-parse", "HEAD")
	if err != nil {
		stop(contract.IntegrationCheckFailed, "%v", err)
		return
	}
	// a lane starts from the run's base (its own setup commit sits between, but carries no code), so it is rebased when
	// something has landed ahead of it
	l.Rebased = head != runBase
	conflicts, err := applyLane(wt, ref)
	switch {
	case err != nil:
		stop(contract.IntegrationCheckFailed, "%v", err)
		return
	case len(conflicts) > 0:
		l.Conflicts = conflicts
		stop(contract.IntegrationConflict, "the lane does not apply on the integration branch: %s", strings.Join(conflicts, ", "))
		return
	}
	files, err := stagedFiles(wt)
	if err != nil {
		stop(contract.IntegrationCheckFailed, "%v", err)
		return
	}
	if len(files) == 0 {
		stop(contract.IntegrationCheckFailed, "the lane changes nothing on top of what is integrated")
		return
	}
	for _, f := range files {
		if strings.HasPrefix(f, ".a2dlc/") {
			stop(contract.IntegrationCheckFailed, "the lane edits %s: the gate's config, waivers and approvals are not a lane's to change", f)
			return
		}
	}
	v, err := d.NewApp(wtRepo).CheckWith(ctx, head, app.CheckOptions{})
	if err != nil {
		stop(contract.IntegrationCheckFailed, "a2dlc check: %v", err)
		return
	}
	l.CheckVerdict = &v.Verdict
	if v.Verdict != contract.PASS || len(v.PendingWaivers) > 0 {
		stop(contract.IntegrationCheckFailed, "a2dlc check on the integrated tree is %s with %d pending waivers", v.Verdict, len(v.PendingWaivers))
		return
	}
	run := map[string]bool{l.IssueID: true}
	for id := range integrated {
		run[id] = true
	}
	l.HoldoutFiles = holdoutFiles(spec, run)
	code, log, err := mountHoldout(ctx, d.Holdout, spec, wt, l.HoldoutFiles)
	if lerr := writeHoldoutLog(out, l.IssueID, log); lerr != nil && err == nil {
		err = lerr
	}
	if err != nil {
		stop(contract.IntegrationHoldoutError, "%v", err)
		return
	}
	l.HoldoutExit = &code
	if code != 0 {
		stop(contract.IntegrationHoldoutFailed, "the holdout exited %d with the files of %s", code, strings.Join(issueList(run), ", "))
		return
	}
	sha, err := commitStaged(wt, "factory: integrate "+l.IssueID)
	if err != nil {
		stop(contract.IntegrationCheckFailed, "%v", err)
		return
	}
	l.Status, l.Commit = contract.IntegrationIntegrated, &sha
}

// writeHoldoutLog keeps the holdout's output beside the record and out of it: it carries the holdout's text and canary,
// so nothing that feeds a builder reads it.
func writeHoldoutLog(out, issue, log string) error {
	if log == "" {
		return nil
	}
	dir := filepath.Join(out, "holdout-logs")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, issue+".log"), []byte(log), 0o644)
}

func issueList(set map[string]bool) []string {
	ids := make([]string, 0, len(set))
	for id := range set {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
