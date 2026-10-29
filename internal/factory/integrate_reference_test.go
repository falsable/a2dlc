package factory

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/run"
)

// fakeTests is the repo's test runner: it passes unless fail says the tree is one the tests reject.
type fakeTests struct {
	mu   sync.Mutex
	fail func(tree string) bool
	runs int
}

func (f *fakeTests) RunTests(_ context.Context, _ contract.TestsConfig, _ string, _ map[string]string, tree string) (engine.TestRun, error) {
	f.mu.Lock()
	f.runs++
	f.mu.Unlock()
	if f.fail != nil && f.fail(tree) {
		return engine.TestRun{Exit: 1, Log: "1 failed"}, nil
	}
	return engine.TestRun{Exit: 0, Log: "all passed"}, nil
}

// agentFake is the role container of the integrator's agent. resolve is what it does in /work; fail, a launch that errors.
type agentFake struct {
	resolve func(work string)
	cost    *contract.Cost
	fail    error
	mu      sync.Mutex
	agents  []call
	prompts []string
}

func (f *agentFake) Launch(_ context.Context, r contract.Role, s run.LaunchSpec) (run.LaunchResult, error) {
	b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	var req contract.RoleRequest
	if err := contract.DecodeStrictBytes(b, &req); err != nil {
		return run.LaunchResult{}, err
	}
	f.mu.Lock()
	f.agents = append(f.agents, call{Role: s.Role, Env: r.Env})
	f.prompts = append(f.prompts, string(b))
	f.mu.Unlock()
	if f.fail != nil {
		return run.LaunchResult{}, f.fail
	}
	if f.resolve != nil {
		f.resolve(s.WorkDir)
	}
	out, _ := json.Marshal(contract.BuilderResponse{Summary: "resolved", FilesChanged: []string{"src/f.py"}})
	resp, _ := json.Marshal(contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
		Status: contract.RoleOK, Cost: f.cost, Payload: out})
	os.WriteFile(filepath.Join(s.OutDir, "response.json"), resp, 0o644)
	return run.LaunchResult{Argv: []string{"docker", "run", r.Image}, Network: run.NetworkNone}, nil
}

func (f *agentFake) seenAgents() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.agents...)
}

const mergedBody = "def f(x):\n    return x + 1 + 2\n"

func resolveTo(body string) func(string) {
	return func(work string) { os.WriteFile(filepath.Join(work, "src", "f.py"), []byte(body), 0o644) }
}

var (
	agentCost = &contract.Cost{USD: 0.4, InputTokens: 200, CacheReadInputTokens: 50, CacheCreationInputTokens: 20, OutputTokens: 20, Turns: 5}
	laneCost  = &contract.Cost{USD: 0.3, InputTokens: 100, OutputTokens: 10, Turns: 2}
)

// refFixture is a repository with a base, the candidates of some accepted lanes under refs/a2dlc/run/ the way a2dlc run
// leaves them, the report of the run that made them and a sealed holdout with a unit per lane. No lane runs: what is
// under test is the integration.
type refFixture struct {
	d     IntegrateDeps
	cfg   contract.FactoryConfig
	rep   contract.FactoryReport
	spec  contract.FactoryHoldout
	fake  *agentFake
	ft    *fakeTests
	hold  *fakeHoldout
	main  string
	units []string
}

// laneSpec is one accepted lane: the files its candidate adds or rewrites on top of the base.
type laneSpec struct {
	issue string
	files map[string]string
}

// conflicting makes n lanes that all rewrite the same line of src/f.py, so any two of them conflict.
func conflicting(n int) []laneSpec {
	var ls []laneSpec
	for i := 1; i <= n; i++ {
		ls = append(ls, laneSpec{"issue-" + string(rune('0'+i)), map[string]string{"src/f.py": "def f(x):\n    return x + " + string(rune('0'+i)) + "\n"}})
	}
	return ls
}

// disjoint makes n lanes that each add a file of their own, so they merge cleanly.
func disjoint(n int) []laneSpec {
	var ls []laneSpec
	for i := 1; i <= n; i++ {
		ls = append(ls, laneSpec{"issue-" + string(rune('0'+i)), map[string]string{"src/g" + string(rune('0'+i)) + ".py": "X = 1\n"}})
	}
	return ls
}

func newRefFixture(t *testing.T, cfg contract.FactoryConfig, cost *contract.Cost, lanes []laneSpec) *refFixture {
	t.Helper()
	d := targetWithTests(t)
	ft, fake, hold := &fakeTests{}, &agentFake{resolve: resolveTo(mergedBody), cost: agentCost}, &fakeHoldout{}
	old := d.NewApp
	d.NewApp = func(r gitx.Repo) app.App { a := old(r); a.Tests = ft; return a }
	d.Launcher = fake
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	var accepted []contract.FactoryLane
	var units []string
	for _, l := range lanes {
		ln := acceptedLane(l.issue, candidate(t, d, base, l.issue, l.files))
		ln.Usage.Add(cost)
		accepted = append(accepted, ln)
		units = append(units, l.issue)
	}
	rep := reportOf(d, accepted...)
	for _, ln := range rep.Lanes {
		rep.Usage.Plus(ln.Usage)
		rep.Accepted++
	}
	c := rep.Usage.USD / float64(rep.Accepted)
	rep.CostPerAcceptedUSD = &c
	if err := rep.Validate(); err != nil {
		t.Fatal(err)
	}
	dir, seal := sealedDir(t, map[string]string{"CANARY": "c"})
	return &refFixture{d: IntegrateDeps{Deps: d, Holdout: hold, MainRef: "main"}, cfg: cfg, rep: rep, spec: holdoutSpec(dir, seal, units...), fake: fake, ft: ft,
		hold: hold, main: git(t, d.Repo.Dir, "rev-parse", "refs/heads/main"), units: units}
}

func (f *refFixture) integrate(t *testing.T) contract.FactoryIntegration {
	t.Helper()
	got, err := IntegrateReference(context.Background(), f.d, f.cfg, f.spec, f.rep)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func (f *refFixture) mainUntouched(t *testing.T) {
	t.Helper()
	if got := git(t, f.d.Repo.Dir, "rev-parse", "refs/heads/main"); got != f.main {
		t.Fatalf("main moved from %s to %s", f.main, got)
	}
}

func wantLanes(t *testing.T, rec contract.FactoryIntegration, want ...string) {
	t.Helper()
	var got []string
	for _, l := range rec.Lanes {
		got = append(got, l.IssueID+"="+l.Status)
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("lanes %q, want %q\n%+v", strings.Join(got, " "), strings.Join(want, " "), rec.Lanes)
	}
}

func TestReferenceConflictIsResolvedByTheAgentAndEachIssueIsScoredOnTheFinalTree(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, conflicting(2))
	var bodies []string
	f.hold.exit = func(tree string, _ []string) int {
		b, _ := os.ReadFile(filepath.Join(tree, "src", "f.py"))
		bodies = append(bodies, string(b))
		return 0
	}
	got := f.integrate(t)

	wantLanes(t, got, "issue-1=integrated", "issue-2=integrated")
	if got.Integrated != 2 || got.Integrator != contract.IntegratorReference || got.Usage == nil || got.Validate() != nil {
		t.Fatalf("%+v %v", got, got.Validate())
	}
	// the record is the queue's, on a branch of its own that is not main: the two can be written for the same run
	if got.IntegrationBranch != "factory/"+f.rep.RunID+"/integration-reference" || got.HeadSha == got.BaseSha || git(t, f.d.Repo.Dir, "rev-parse", got.IntegrationBranch) != got.HeadSha {
		t.Fatalf("%+v", got)
	}
	if got.Lanes[0].Commit == nil || got.Lanes[1].Commit == nil || *got.Lanes[1].Commit != got.HeadSha {
		t.Fatalf("%+v", got.Lanes)
	}
	// one holdout run per issue, each with that issue's files only, on the integrated tree: both lanes' work and the resolution
	if strings.Join(flatten(f.hold.runs), ",") != "issue-1.test,issue-2.test" || len(bodies) != 2 || bodies[0] != mergedBody || bodies[1] != mergedBody {
		t.Fatalf("%v %q", f.hold.runs, bodies)
	}
	// one agent call: default claude with the reference's USD 1.5 and none of the minimal flags, charged to the integration
	agents := f.fake.seenAgents()
	if len(agents) != 1 || agents[0].Env[EnvLLMCmd] != "claude --disallowedTools WebSearch,WebFetch --max-budget-usd 1.5000" || agents[0].Env["A2DLC_MODEL"] != "haiku" {
		t.Fatalf("%+v", agents)
	}
	if *got.Usage != (contract.Counters{USD: 0.4, Calls: 1, Turns: 5, InputTokens: 200, CacheReadInputTokens: 50, CacheCreationInputTokens: 20, OutputTokens: 20}) {
		t.Fatalf("%+v", got.Usage)
	}
	// the metric counts the lanes and the integration: (0.3 + 0.3 + 0.4) over two accepted issues
	if c := CostPerAccepted(f.rep, got); c == nil || *c < 0.4999 || *c > 0.5001 {
		t.Fatalf("%v", c)
	}
	// the request carries neither the reference's name nor its toolchain, and not the holdout or the reference's folders
	for _, p := range f.fake.prompts {
		for _, banned := range []string{"PromtEngineer", "software-factory", "hidden-tests", "npm", "factory-ref", "factory-holdout", "CANARY"} {
			if strings.Contains(p, banned) {
				t.Errorf("the agent's request mentions %q", banned)
			}
		}
		if !strings.Contains(p, "BOTH sides are kept") || !strings.Contains(p, "src/f.py") {
			t.Errorf("request: %s", p)
		}
	}
	f.mainUntouched(t)
	// the record is on disk beside a queue's, strictly decodable; the holdout's logs are apart from the queue's; the
	// worktree is gone while the branch stays; tests ran after every merge
	out := filepath.Join(f.d.Out, f.rep.RunID)
	raw, err := os.ReadFile(filepath.Join(out, "integration-reference.json"))
	var disk contract.FactoryIntegration
	if err != nil || contract.DecodeStrictBytes(raw, &disk) != nil || disk.Validate() != nil {
		t.Fatalf("%v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "reference", "holdout-logs", "issue-1.log")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "holdout-logs")); !os.IsNotExist(err) {
		t.Fatal("the queue's log folder was written by the reference integration")
	}
	if _, err := os.Stat(filepath.Join(out, "integration-reference")); !os.IsNotExist(err) {
		t.Fatal("the integration worktree was left behind")
	}
	if f.ft.runs != 2 {
		t.Fatalf("%d test runs", f.ft.runs)
	}
}

func flatten(runs [][]string) []string {
	var all []string
	for _, r := range runs {
		all = append(all, r...)
	}
	return all
}

func TestReferenceAgentThatLeavesMarkersStopsTheLoopAndAbortsTheMerge(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, conflicting(3))
	f.fake.resolve = func(string) {} // the conflicted file, markers and all, comes back unchanged
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=conflict", "issue-3=not_queued")
	l2, l3 := got.Lanes[1], got.Lanes[2]
	if !strings.Contains(l2.Reason, "conflict markers remain in src/f.py") || len(l2.Conflicts) != 1 || l2.QueuePosition == nil || l3.QueuePosition != nil || !strings.Contains(l3.Reason, "issue-2") {
		t.Fatalf("%+v %+v", l2, l3)
	}
	// the branch is at the first lane's merge and the aborted merge left nothing in it; only that lane reached the holdout
	if got.HeadSha == got.BaseSha || git(t, f.d.Repo.Dir, "rev-parse", got.IntegrationBranch) != got.HeadSha || got.Integrated != 1 || strings.Join(flatten(f.hold.runs), ",") != "issue-1.test" {
		t.Fatalf("%+v %v", got, f.hold.runs)
	}
	f.mainUntouched(t)
}

func TestReferenceAgentCallThatFailsStopsTheLoop(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, conflicting(3))
	f.fake.fail = errors.New("container did not start")
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=conflict", "issue-3=not_queued")
	// the failed launch counts as a call that reported nothing, as it does in a lane
	if !strings.Contains(got.Lanes[1].Reason, "agent call failed: container did not start") || got.Usage.Calls != 1 || got.Usage.USD != 0 {
		t.Fatalf("%+v", got)
	}
	f.mainUntouched(t)
}

// Lanes that do not conflict: the only thing that can stop the loop is the tests.
func TestReferenceTestsThatFailAfterAMergeLeaveTheBranchAtTheLastPassingOne(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, disjoint(3))
	f.ft.fail = func(tree string) bool { _, err := os.Stat(filepath.Join(tree, "src", "g2.py")); return err == nil }
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=check_failed", "issue-3=not_queued")
	if got.Lanes[1].Reason != "config.tests exited 1" || got.Lanes[1].CheckVerdict == nil || *got.Lanes[1].CheckVerdict != "FAIL" || len(f.fake.seenAgents()) != 0 {
		t.Fatalf("%+v", got.Lanes[1])
	}
	// the branch moves forward only after the tests pass: it is at issue-1's merge, not at issue-2's
	if git(t, f.d.Repo.Dir, "rev-parse", got.IntegrationBranch) != got.HeadSha || got.HeadSha == got.BaseSha {
		t.Fatalf("head %s", got.HeadSha)
	}
	if out := git(t, f.d.Repo.Dir, "ls-tree", "-r", "--name-only", got.IntegrationBranch); !strings.Contains(out, "src/g1.py") || strings.Contains(out, "src/g2.py") {
		t.Fatalf("%s", out)
	}
	f.mainUntouched(t)
}

func TestReferenceCandidateAlreadyInTheBranchIsIntegratedWithoutAnotherMerge(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, disjoint(2))
	f.rep.Lanes[1].CandidateRef = f.rep.Lanes[0].CandidateRef
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=integrated")
	if len(f.fake.seenAgents()) != 0 || f.ft.runs != 1 || *got.Lanes[1].Commit != *got.Lanes[0].Commit {
		t.Fatalf("%+v", got.Lanes)
	}
}

// The agent's call shares the run's kill with the lanes: it gets what is left of it as its budget, and a call that spends
// past it ends the loop at the last good tip, with the conflicted merge undone, whatever the agent said.
func TestReferenceRunKillDuringTheAgentCallEndsTheLoopAtTheLastGoodTip(t *testing.T) {
	cfg := testConfig()
	cfg.Caps.LaneUSD, cfg.Caps.RunStopNewUSD, cfg.Caps.RunKillUSD = 0.2, 0.45, 0.5
	f := newRefFixture(t, cfg, &contract.Cost{USD: 0.15, InputTokens: 10, OutputTokens: 1, Turns: 1}, conflicting(3)) // the lanes spent 0.45 of the 0.5 kill
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=conflict", "issue-3=not_queued")
	agents := f.fake.seenAgents()
	if len(agents) != 1 || !strings.HasSuffix(agents[0].Env[EnvLLMCmd], "--max-budget-usd 0.0500") {
		t.Fatalf("%+v", agents)
	}
	if !strings.Contains(got.Lanes[1].Reason, "run usd") || got.Usage.USD != 0.4 {
		t.Fatalf("%+v", got)
	}
	if git(t, f.d.Repo.Dir, "rev-parse", got.IntegrationBranch) != got.HeadSha || got.HeadSha == got.BaseSha {
		t.Fatalf("head %s", got.HeadSha)
	}
	f.mainUntouched(t)
}

// The agent's budget is what is left under the shared caps, not the reference's USD 1.5 when less is left.
func TestReferenceAgentBudgetIsWhatTheSharedCapsLeave(t *testing.T) {
	cfg := testConfig()
	cfg.Caps.LaneUSD, cfg.Caps.RunStopNewUSD, cfg.Caps.RunKillUSD = 0.4, 0.7, 0.8
	f := newRefFixture(t, cfg, laneCost, conflicting(2)) // the lanes spent 0.6 of the 0.8 kill
	f.fake.cost = &contract.Cost{USD: 0.1, InputTokens: 1, OutputTokens: 1, Turns: 1}
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=integrated")
	agents := f.fake.seenAgents()
	if len(agents) != 1 || !strings.HasSuffix(agents[0].Env[EnvLLMCmd], "--max-budget-usd 0.2000") {
		t.Fatalf("%+v", agents)
	}
}

// An integrated tree that modifies a base test is not one any issue is accepted on, whatever the holdout says.
func TestReferenceAgentThatEditsABaseTestMakesTheTreeUnacceptable(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, conflicting(2))
	f.fake.resolve = func(work string) {
		resolveTo(mergedBody)(work)
		os.WriteFile(filepath.Join(work, "tests", "test_f.py"), []byte("def test_f():\n    assert True\n"), 0o644)
	}
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=check_failed", "issue-2=check_failed")
	if got.Integrated != 0 || !strings.Contains(got.Lanes[0].Reason, "tests/test_f.py (modified)") || CostPerAccepted(f.rep, got) != nil || len(f.hold.runs) != 0 {
		t.Fatalf("%+v", got)
	}
}

func TestReferenceHoldoutVerdictsArePerIssueAndAnErrorIsNotAPass(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, disjoint(3))
	f.spec.Units = f.spec.Units[:2]                    // issue-3 has no unit
	f.hold.exit = func(_ string, files []string) int { // every unit runs on the same final tree; only issue-2's files fail
		if len(files) == 1 && files[0] == "issue-2.test" {
			return 1
		}
		return 0
	}
	got := f.integrate(t)
	wantLanes(t, got, "issue-1=integrated", "issue-2=holdout_failed", "issue-3=no_holdout")
	if got.Integrated != 1 || got.Lanes[1].HoldoutExit == nil || *got.Lanes[1].HoldoutExit != 1 || got.Lanes[1].Commit != nil {
		t.Fatalf("%+v", got.Lanes)
	}
	// the whole branch is there, as in the reference, which does not roll a merge back for what a holdout says
	if out := git(t, f.d.Repo.Dir, "ls-tree", "-r", "--name-only", got.IntegrationBranch); !strings.Contains(out, "src/g2.py") || !strings.Contains(out, "src/g3.py") {
		t.Fatalf("%s", out)
	}
	if c := CostPerAccepted(f.rep, got); c == nil || *c < 0.8999 || *c > 0.9001 { // 0.9 spent, one issue accepted
		t.Fatalf("%v", c)
	}
}

func TestReferenceRefusesAHoldoutThatIsNotTheOneTheSpecNames(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, disjoint(1))
	f.spec.Seal = strings.Repeat("0", 64)
	if _, err := IntegrateReference(context.Background(), f.d, f.cfg, f.spec, f.rep); err == nil || !strings.Contains(err.Error(), "sealed as") {
		t.Fatalf("%v", err)
	}
	if len(f.hold.runs) != 0 {
		t.Fatal("the holdout ran")
	}
}

func TestReferenceNeedsATestRunnerAndConfigTests(t *testing.T) {
	f := newRefFixture(t, testConfig(), laneCost, disjoint(1))
	f.d.NewApp = target(t).NewApp // an app with no Tests
	if _, err := IntegrateReference(context.Background(), f.d, f.cfg, f.spec, f.rep); err == nil || !strings.Contains(err.Error(), "no test runner") {
		t.Fatalf("%v", err)
	}
	d := target(t) // its config has engines and no tests
	rep := contract.FactoryReport{SchemaVersion: 1, RunID: "run-0123456789ab", BaseSha: git(t, d.Repo.Dir, "rev-parse", "HEAD")}
	if _, err := testsConfigAt(d, rep.BaseSha); err == nil || !strings.Contains(err.Error(), "config.tests") {
		t.Fatalf("%v", err)
	}
}

func TestCostPerAcceptedCountsTheIntegratorsSpendAndIsNilWithoutAnAcceptedIssue(t *testing.T) {
	rep := contract.FactoryReport{Usage: contract.Counters{USD: 1}}
	if CostPerAccepted(rep, contract.FactoryIntegration{}) != nil {
		t.Fatal("cost with nothing integrated")
	}
	queue := contract.FactoryIntegration{Integrated: 2}
	if c := CostPerAccepted(rep, queue); c == nil || *c != 0.5 {
		t.Fatalf("%v", c)
	}
	ref := contract.FactoryIntegration{Integrated: 2, Usage: &contract.Counters{USD: 0.5}}
	if c := CostPerAccepted(rep, ref); c == nil || *c != 0.75 {
		t.Fatalf("%v", c)
	}
}
