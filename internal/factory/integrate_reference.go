package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// IntegrateReference reimplements the integration stage of the reference software factory, as a control for the merge
// queue (Integrate): the accepted lanes' candidates are merged one by one into an integration branch that is never main,
// a default claude -p agent resolves a conflict keeping both sides, and the repo's tests must pass before the next merge.
// Its record is the queue's (contract.FactoryIntegration, written as integration-reference.json), with the agent's spend
// in Usage, and the lanes are scored through the same sealed holdout runner, so the two integrators are compared by one
// path.
//
// Source: PromtEngineer/software-factory (MIT), src/integrate.ts at commit f73ad8ab455410f0b0ace8e3e3d873b2b01c556d, the
// commit P0a vendored (e2e/testdata/factory-ref/NOTICE.md). Line numbers below are that file's.
//
//	19-23   the PRs the factory opened are merged oldest first: here, the accepted lanes in the report's order
//	34-41   the branch starts at main (or resumes): here, at the run's base commit, always fresh; no resume, a run is one process
//	43-48   a branch already an ancestor of HEAD is skipped as integrated
//	49-51   `git merge --no-ff --no-edit`
//	52-75   on a conflict an agent resolves it keeping both sides (prompt 57-59, maxTurns 40, maxBudgetUsd 1.5, 15 min at
//	        60-62); if the call fails or conflicts remain, `merge --abort` and the loop stops (67-71, 75)
//	77-81   the tests run after every merge, the loop stops at the first failure, and the branch is pushed only after a
//	        passing merge, so a failure leaves it at the last passing one
//	83      main is never touched
//
// Where this differs from the reference, on purpose:
//   - The tests are the repo's config.tests command, judged by its exit code through the same test runner the lanes use,
//     not `npm test` read by a regex over its last lines (77-79): the core knows no language.
//   - The agent works on a copy of the conflicted tree mounted as /work, as every role does, and does not run the tests
//     or `git commit` (59): the role image has neither the repo's toolchain nor the repository. The core copies the
//     resolved files back, refuses a file that still has conflict markers, commits the merge and runs the tests itself.
//   - Its model is the config's, not the reference's Sonnet, and its limits are the reference's: 40 turns, USD 1.5 and 15
//     minutes per conflict, further bounded by the run's kill and the total cap, whose spend it shares with the lanes.
//   - The reference has no holdout. Here the sealed holdout runs once per issue on the final tree (the queue runs it per
//     lane, on a tree that holds only the lanes before it), and an issue is integrated when its candidate is in the tree,
//     its unit passes there and no base test file is modified or deleted in that tree.
func IntegrateReference(ctx context.Context, d IntegrateDeps, cfg contract.FactoryConfig, spec contract.FactoryHoldout, rep contract.FactoryReport) (contract.FactoryIntegration, error) {
	if err := cfg.Validate(); err != nil {
		return contract.FactoryIntegration{}, err
	}
	if err := spec.Validate(); err != nil {
		return contract.FactoryIntegration{}, err
	}
	if err := rep.Validate(); err != nil {
		return contract.FactoryIntegration{}, fmt.Errorf("report: %w", err)
	}
	if d.Holdout == nil || d.NewApp == nil || d.Launcher == nil {
		return contract.FactoryIntegration{}, errors.New("the reference integration needs a holdout runner, a launcher and NewApp")
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if seal, err := VerifySeal(spec.Dir); err != nil {
		return contract.FactoryIntegration{}, err
	} else if seal != spec.Seal {
		return contract.FactoryIntegration{}, fmt.Errorf("the holdout in %s is sealed as %s and the spec names %s", spec.Dir, seal, spec.Seal)
	}
	tests, err := testsConfigAt(d.Deps, rep.BaseSha)
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	if d.NewApp(d.Repo).Tests == nil {
		return contract.FactoryIntegration{}, errors.New("the reference integration has no test runner: a tree whose tests cannot run is never a pass")
	}
	mainRef := d.MainRef
	if mainRef == "" {
		mainRef = "HEAD"
	}
	mainBefore, err := gitIn(d.Repo.Dir, "rev-parse", "--verify", mainRef+"^{commit}")
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	empty, err := os.MkdirTemp("", "a2dlc-nohooks-")
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	defer os.RemoveAll(empty)
	out, err := filepath.Abs(filepath.Join(d.Out, rep.RunID))
	if err != nil {
		return contract.FactoryIntegration{}, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agent := cfg
	agent.Arm = contract.ArmControl
	agent.Caps.LaneUSD, agent.Caps.LaneSeconds, agent.Caps.LaneTurns = referenceMaxBudgetUSD, referenceAgentSeconds, referenceMaxTurns
	r := &refRun{d: d.Deps, h: d.Holdout, rep: rep, tests: tests, agent: agent, acct: stageAccountant(agent.Caps, rep, cancel), out: out,
		hooks: []string{"-c", "core.hooksPath=" + empty, "-c", "user.name=a2dlc-factory", "-c", "user.email=factory@a2dlc.invalid", "-c", "commit.gpgsign=false"},
		dir:   filepath.Join(out, "integration-reference")}

	started := d.Now()
	branch := "factory/" + rep.RunID + "/integration-reference"
	wts := &worktrees{dir: d.Repo.Dir}
	if err := wts.add(r.dir, branch, rep.BaseSha); err != nil {
		return contract.FactoryIntegration{}, err
	}
	defer wts.remove(r.dir)

	steps, tip, err := r.mergeAll(runCtx, ctx)
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	guard, err := newTestGuard(d.Repo, rep.BaseSha)
	if err != nil {
		return contract.FactoryIntegration{}, err
	}
	touched := guard.check(r.dir)

	usage := contract.Counters{}
	for _, l := range rep.Lanes {
		usage.Plus(r.acct.counters(r.laneID(l.IssueID)))
	}
	rec := contract.FactoryIntegration{SchemaVersion: 1, RunID: rep.RunID, BatchID: rep.BatchID, BaseSha: rep.BaseSha, HoldoutSeal: spec.Seal,
		MainRef: mainRef, MainShaBefore: mainBefore, Integrator: contract.IntegratorReference, Usage: &usage, IntegrationBranch: branch,
		HeadSha: tip, StartedAt: started.UTC().Format(time.RFC3339), Lanes: []contract.FactoryIntegrationLane{}}
	position := 0
	for _, lane := range rep.Lanes {
		if lane.Status != contract.LaneAccepted || lane.CandidateRef == nil {
			rec.Lanes = append(rec.Lanes, contract.FactoryIntegrationLane{IssueID: lane.IssueID, Status: contract.IntegrationNotQueued,
				Reason: fmt.Sprintf("the lane is %s: %s", lane.Status, lane.Reason), HoldoutFiles: []string{}})
			continue
		}
		l := r.laneRecord(ctx, spec, steps[lane.IssueID], touched, &position)
		if l.Status == contract.IntegrationIntegrated {
			rec.Integrated++
		}
		rec.Lanes = append(rec.Lanes, l)
	}
	if rec.MainShaAfter, err = gitIn(d.Repo.Dir, "rev-parse", "--verify", mainRef+"^{commit}"); err != nil {
		return rec, err
	}
	rec.FinishedAt = d.Now().UTC().Format(time.RFC3339)
	if err := rec.Validate(); err != nil {
		return rec, err
	}
	return rec, writeJSON(filepath.Join(out, "integration-reference.json"), rec)
}

// CostPerAccepted is the run's whole spend over the issues the integration accepted: the lanes' usage, failed and capped
// lanes included, plus the integrator's own agent calls. It is nil when nothing was accepted. This is the metric of the
// preregistered comparison, which neither report.json (it stops at the lanes) nor a queue record (no model calls) holds.
func CostPerAccepted(rep contract.FactoryReport, rec contract.FactoryIntegration) *float64 {
	if rec.Integrated == 0 {
		return nil
	}
	usd := rep.Usage.USD
	if rec.Usage != nil {
		usd += rec.Usage.USD
	}
	c := usd / float64(rec.Integrated)
	return &c
}

const (
	referenceMaxTurns     = 40
	referenceMaxBudgetUSD = 1.5
	referenceAgentSeconds = 15 * 60
)

// What happened to one lane's candidate in the merge loop.
const (
	stepMerged      = "merged"
	stepResolved    = "conflict_resolved"
	stepAlreadyIn   = "already_integrated"
	stepConflict    = "conflict_unresolved"
	stepTestsFailed = "tests_failed"
	stepCapped      = "capped"
	stepNotReached  = "not_reached"
)

type refStep struct {
	issue, status, reason string
	conflicts             []string
	commit                string // the branch head once this lane's merge passed the tests
	rebased               bool   // the branch had moved past the base when it was merged
}

func (s refStep) inTree() bool {
	return s.status == stepMerged || s.status == stepResolved || s.status == stepAlreadyIn
}

// refRun is the state of one reference integration: the worktree it merges in and what a merge step needs.
type refRun struct {
	d     Deps
	h     HoldoutRunner
	rep   contract.FactoryReport
	dir   string
	out   string // <out>/<run id>: the holdout's logs go under it, apart from the queue's
	tests contract.TestsConfig
	acct  *accountant
	agent contract.FactoryConfig // the config of the agent's launches: default claude, the reference's limits
	hooks []string               // git -c options: no hooks of the target repo, a fixed identity
}

func (r *refRun) laneID(issue string) string { return "integrate-" + issue }

// mergeAll is the reference's loop over the accepted lanes. It returns what happened to each, keyed by issue, and the
// branch head, which only a merge whose tests passed moves. An error is the factory's (git or the test runner could not
// run), not a result.
func (r *refRun) mergeAll(runCtx, parent context.Context) (map[string]refStep, string, error) {
	steps := map[string]refStep{}
	tip := r.rep.BaseSha
	stopped := ""
	for _, l := range r.rep.Lanes {
		if l.Status != contract.LaneAccepted || l.CandidateRef == nil {
			continue
		}
		switch {
		case stopped != "":
			steps[l.IssueID] = refStep{issue: l.IssueID, status: stepNotReached, reason: "the integration stopped at " + stopped}
			continue
		case runCtx.Err() != nil:
			stopped = "a spend cap: " + r.stopReason(parent)
			steps[l.IssueID] = refStep{issue: l.IssueID, status: stepNotReached, reason: "the integration stopped at " + stopped}
			continue
		}
		s, err := r.oneLane(runCtx, l, &tip)
		if err != nil {
			return nil, "", err
		}
		steps[l.IssueID] = s
		if !s.inTree() {
			stopped = l.IssueID + " (" + s.status + ")"
		}
	}
	return steps, tip, nil
}

// laneRecord is one queued lane as the integration record says it. A lane in the tree is judged by the sealed holdout on
// the final tree, unless the tree touches a base test, which is a failure of the whole integration.
func (r *refRun) laneRecord(ctx context.Context, spec contract.FactoryHoldout, s refStep, touched []contract.TestTouch, position *int) contract.FactoryIntegrationLane {
	if s.status == stepNotReached {
		return contract.FactoryIntegrationLane{IssueID: s.issue, Status: contract.IntegrationNotQueued, Reason: s.reason, HoldoutFiles: []string{}}
	}
	pos := *position
	*position++
	l := contract.FactoryIntegrationLane{IssueID: s.issue, QueuePosition: &pos, Rebased: s.rebased, HoldoutFiles: []string{}}
	pass, fail := contract.PASS, "FAIL"
	switch s.status {
	case stepConflict, stepCapped:
		l.Status, l.Reason, l.Conflicts = contract.IntegrationConflict, s.reason, s.conflicts
		return l
	case stepTestsFailed:
		l.Status, l.Reason, l.CheckVerdict = contract.IntegrationCheckFailed, s.reason, &fail
		return l
	}
	l.CheckVerdict = &pass
	switch {
	case len(touched) > 0:
		l.Status, l.Reason, l.CheckVerdict = contract.IntegrationCheckFailed, "the integrated tree has "+touchReason(touched), &fail
		return l
	case !holdoutUnit(spec, s.issue):
		l.Status, l.Reason = contract.IntegrationNoHoldout, fmt.Sprintf("the holdout has no unit for %s, so it cannot be accepted", s.issue)
		return l
	}
	l.HoldoutFiles = holdoutFiles(spec, map[string]bool{s.issue: true})
	code, log, err := mountHoldout(ctx, r.h, spec, r.dir, l.HoldoutFiles)
	if lerr := writeHoldoutLog(filepath.Join(r.out, "reference"), s.issue, log); lerr != nil && err == nil {
		err = lerr
	}
	switch {
	case err != nil:
		l.Status, l.Reason = contract.IntegrationHoldoutError, err.Error()
	case code != 0:
		l.HoldoutExit = &code
		l.Status, l.Reason = contract.IntegrationHoldoutFailed, fmt.Sprintf("the holdout exited %d with the files of %s on the final tree", code, s.issue)
	default:
		sha := s.commit
		l.HoldoutExit, l.Commit, l.Status = &code, &sha, contract.IntegrationIntegrated
	}
	return l
}

// stopReason is why the loop stopped before a lane: the run's kill if it was one, otherwise the caller's context.
func (r *refRun) stopReason(parent context.Context) string {
	if killed, why := r.acct.wasKilled(); killed {
		return why
	}
	if parent.Err() != nil {
		return "context ended: " + parent.Err().Error()
	}
	return "integration cancelled"
}

// oneLane merges one candidate. The branch moves to the new tip only after the tests pass: a failure puts it back at the
// last passing merge.
func (r *refRun) oneLane(ctx context.Context, l contract.FactoryLane, tip *string) (refStep, error) {
	ref := *l.CandidateRef
	s := refStep{issue: l.IssueID, rebased: *tip != r.rep.BaseSha}
	switch _, code := r.git("merge-base", "--is-ancestor", ref, "HEAD"); code {
	case 0:
		s.status, s.commit = stepAlreadyIn, *tip
		return s, nil
	case 1:
	default:
		return s, fmt.Errorf("git merge-base --is-ancestor %s HEAD exited %d", ref, code)
	}
	if msg, code := r.git("merge", "--no-ff", "--no-edit", ref); code != 0 {
		s.conflicts = r.unmerged()
		if len(s.conflicts) == 0 {
			r.git("merge", "--abort")
			s.status, s.reason = stepConflict, "the merge failed without conflicts: "+firstLine(msg)
			return s, nil
		}
		ok, why, capped := r.resolve(ctx, l, s.conflicts)
		if !ok {
			r.git("merge", "--abort")
			s.status, s.reason = stepConflict, why
			if capped {
				s.status = stepCapped
			}
			return s, nil
		}
		// the agent's own spend may have reached the run's kill: that ends the integration at the last good tip, undoing
		// the merge it just committed
		if ctx.Err() != nil {
			r.git("reset", "--hard", *tip)
			s.status, s.reason = stepCapped, r.stopReason(ctx)
			return s, nil
		}
		s.status, s.reason = stepResolved, "conflicts in "+strings.Join(s.conflicts, ", ")+" resolved by the agent"
	} else {
		s.status = stepMerged
	}
	run, err := r.d.NewApp(r.d.Repo).Tests.RunTests(ctx, r.tests, r.tests.Command, nil, r.dir)
	if err != nil {
		return s, fmt.Errorf("config.tests: %w", err)
	}
	if run.Exit != 0 {
		r.git("reset", "--hard", *tip)
		s.status, s.reason = stepTestsFailed, fmt.Sprintf("config.tests exited %d", run.Exit)
		return s, nil
	}
	head, code := r.git("rev-parse", "HEAD")
	if code != 0 {
		return s, fmt.Errorf("git rev-parse HEAD exited %d", code)
	}
	*tip, s.commit = head, head
	return s, nil
}

// git runs git in the integration worktree with hooks off and a fixed identity. The code is the exit code, or -1 if git
// did not start.
func (r *refRun) git(args ...string) (string, int) {
	c := exec.Command("git", append(append([]string{}, r.hooks...), args...)...)
	c.Dir = r.dir
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	b, err := c.CombinedOutput()
	if err == nil {
		return strings.TrimSpace(string(b)), 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return strings.TrimSpace(string(b)), ee.ExitCode()
	}
	return err.Error(), -1
}

func (r *refRun) unmerged() []string {
	out, _ := r.git("diff", "--name-only", "--diff-filter=U")
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

// stageAccountant is the accountant of an integration: it starts from what the run's lanes already spent, so the run
// kill and the total cap apply to the integration's calls too. caps are the stage's own (the lane figures bound one
// agent call here), and the shared run and total thresholds stay as the config has them.
func stageAccountant(caps contract.FactoryCaps, rep contract.FactoryReport, cancel context.CancelFunc) *accountant {
	a := newAccountant(caps, rep.PriorSpendUSD, cancel)
	a.run = rep.Usage
	return a
}

// counters is a copy of what one lane id has spent.
func (a *accountant) counters(lane string) contract.Counters {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.lane(lane)
}

// testsConfigAt reads config.tests from the config committed at the base: the reference's repo declares tests and no
// differential engine, and the same file is read for every arm.
func testsConfigAt(d Deps, base string) (contract.TestsConfig, error) {
	b, err := d.Repo.Show(base, ".a2dlc/config.json")
	if err != nil {
		return contract.TestsConfig{}, fmt.Errorf(".a2dlc/config.json at %s: %w", base, err)
	}
	var cfg contract.Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return contract.TestsConfig{}, fmt.Errorf(".a2dlc/config.json at %s: %w", base, err)
	}
	if cfg.Tests == nil || strings.TrimSpace(cfg.Tests.Command) == "" {
		return contract.TestsConfig{}, errors.New("the integration needs config.tests with a command: the reference runs the tests after every merge")
	}
	return *cfg.Tests, nil
}
