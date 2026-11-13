package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// runnerDoubles is the repo's suite as Tier 0 sees it: one testcase that passes only if src/f.py doubles its argument.
// noJUnit makes the command leave no JUnit, like a runner that crashed.
type runnerDoubles struct {
	runs    int
	envs    []map[string]string
	noJUnit bool
}

func (r *runnerDoubles) RunTests(_ context.Context, _ contract.TestsConfig, _ string, env map[string]string, tree string) (engine.TestRun, error) {
	r.runs++
	r.envs = append(r.envs, env)
	if r.noJUnit {
		return engine.TestRun{Exit: 0, Log: "the runner crashed"}, nil
	}
	src, _ := os.ReadFile(filepath.Join(tree, "src", "f.py"))
	if strings.Contains(string(src), "x * 2") {
		return engine.TestRun{JUnit: []byte(`<testsuites><testsuite><testcase classname="c" name="doubles" file="test/t.test.py"/></testsuite></testsuites>`)}, nil
	}
	return engine.TestRun{Exit: 1, JUnit: []byte(`<testsuites><testsuite><testcase classname="c" name="doubles" file="test/t.test.py"><failure message="f(2) is 2, expected 4"/></testcase></testsuite></testsuites>`)}, nil
}

// repoTestsOnly is a repo with no engine, config.tests as the only check, builder_primary in roles.json.
func repoTestsOnly(t *testing.T, runner engine.TestRunner) (app.App, string) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["test/**"],"command":"run"}}`)
	write(t, dir, "src/f.py", baseFile)
	write(t, dir, ".a2dlc/approvals.json", `{"schema_version":1,"approvals":[]}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "tests only")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0}}}`)
	a.Runner = &engineRun{}
	a.Tests = runner
	return a, dir
}

const wrongFile, doublesFile = "def f(x):\n    return x + 1\n", "def f(x):\n    return x * 2\n"

// Without an engine for the language the repo's tests are the Tier 0: a candidate whose tests pass is accepted, and the
// file no engine covered stays listed as one Tier 0 did not review (Stage 4 has its own coverage precondition).
func TestStage3AcceptsABuilderCandidateWhoseTestsPass(t *testing.T) {
	runner := &runnerDoubles{}
	a, dir := repoTestsOnly(t, runner)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": doublesFile}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.ok"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.ID != "3" || s3.Status != contract.StageDone || s3.Iterations != 1 || len(requests) != 1 || runner.runs != 1 || runner.envs[0]["A2DLC_CHANGED"] != "src/f.py" {
		t.Fatalf("%+v requests %d runs %d %v", s3, len(requests), runner.runs, runner.envs)
	}
	var c contract.Candidate
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/t.ok", ArtCandidate))
	if err := contract.DecodeStrictBytes(b, &c); err != nil || c.Tier0Reviewed == nil || !*c.Tier0Reviewed || strings.Join(c.Unchecked, ",") != "src/f.py" {
		t.Fatalf("%v %+v", err, c)
	}
}

// A red testcase goes back to the builder as Tier 0 feedback (kind test) and spends a round; the next round can be green.
func TestStage3FailedTestcaseIsFeedbackAndTheNextRoundAccepts(t *testing.T) {
	runner := &runnerDoubles{}
	a, _ := repoTestsOnly(t, runner)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback == nil {
			return map[string]string{"src/f.py": wrongFile}
		}
		return map[string]string{"src/f.py": doublesFile}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.fb"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || s3.Iterations != 2 || len(requests) != 2 || runner.runs != 2 {
		t.Fatalf("%+v requests %d runs %d", s3, len(requests), runner.runs)
	}
	fb := requests[1].Tier0Feedback
	if fb == nil || fb.OK || len(fb.Findings) != 1 || fb.Findings[0].Kind != contract.Tier0Test || fb.Findings[0].File != "test/t.test.py" ||
		!strings.Contains(fb.Findings[0].Message, "f(2) is 2, expected 4") {
		t.Fatalf("the builder's second round got %+v", fb)
	}
}

// A candidate whose tests stay red exhausts max_inner_iterations and escalates: the failing tests are the builder's.
func TestStage3TestsThatStayRedExhaustTheRounds(t *testing.T) {
	runner := &runnerDoubles{}
	a, dir := repoTestsOnly(t, runner)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": wrongFile}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.red", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || s3.Iterations != 2 || !strings.Contains(s3.Reason, "Tier 0 does not converge") || len(requests) != 2 {
		t.Fatalf("%+v requests %d", s3, len(requests))
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/t.red", ArtCandidate)); err == nil {
		t.Fatal("candidate.json without a green Tier 0")
	}
}

// A test run that leaves no JUnit is neither a review nor feedback: the stage fails with the tool's reason, the builder
// is not asked for another round, and nothing is accepted.
func TestStage3TestsWithoutJUnitAreAToolErrorNotFeedback(t *testing.T) {
	runner := &runnerDoubles{noJUnit: true}
	a, dir := repoTestsOnly(t, runner)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": doublesFile}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.nojunit"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageFailed || !strings.Contains(s3.Reason, "Tier 0 could not run") || !strings.Contains(s3.Reason, "config.tests") || !strings.Contains(s3.Reason, "no JUnit") || len(requests) != 1 {
		t.Fatalf("%+v requests %d", s3, len(requests))
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/t.nojunit", ArtCandidate)); err == nil {
		t.Fatal("candidate.json for a candidate nobody reviewed")
	}
}

// runnerLog is runnerDoubles that also records every (command, tree content) it was asked to run, and resolves the image digest
// so the suite cache can be keyed.
type runnerLog struct {
	runnerDoubles
	mu    sync.Mutex
	pairs []string
}

func (r *runnerLog) ImageDigest(context.Context, string) (string, error) { return testDigest, nil }

func (r *runnerLog) RunTests(ctx context.Context, cfg contract.TestsConfig, cmd string, env map[string]string, tree string) (engine.TestRun, error) {
	src, _ := os.ReadFile(filepath.Join(tree, "src", "f.py"))
	r.mu.Lock()
	r.pairs = append(r.pairs, cmd+"@"+hashBytes(src))
	r.mu.Unlock()
	return r.runnerDoubles.RunTests(ctx, cfg, cmd, env, tree)
}

// onTree is how many times any command ran on the tree whose src/f.py is src.
func (r *runnerLog) onTree(src string) int {
	n := 0
	for _, p := range r.pairs {
		if strings.HasSuffix(p, "@"+hashBytes([]byte(src))) {
			n++
		}
	}
	return n
}

// With tests.tier0_command "baseline" the suite Tier 0 ran on the candidate's tree is the one Stage 4's baseline gate takes
// from the cache: over a whole run, with two builder rounds, each candidate tree is run once. Stage -1 re-runs the base on
// purpose (flaky detection), so the base tree is left out. Without the option the accepted tree gets tests.command in Tier 0
// and baseline_command again in Stage 4.
func TestRunNeverRunsTheSameSuiteTwiceOnACandidateTree(t *testing.T) {
	const cfgTail = `"tests":{"image":"x","paths":["test/**"],"command":"run","baseline_command":"base"`
	for _, tc := range []struct {
		name, tier0 string
		wantTwice   bool
	}{{"baseline", `,"tier0_command":"baseline"`, false}, {"control: tier0_command absent", "", true}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("A2DLC_CACHE_DIR", t.TempDir())
			runner := &runnerLog{}
			a, dir := repoTestsOnly(t, runner)
			write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],`+cfgTail+tc.tier0+`}}`)
			git(t, dir, "add", "-A")
			git(t, dir, "commit", "-qm", "baseline config")
			var requests []contract.BuilderRequest
			launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
				if pl.Tier0Feedback == nil {
					return map[string]string{"src/f.py": wrongFile}
				}
				return map[string]string{"src/f.py": doublesFile}
			})}
			st, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.once"})
			if err != nil {
				t.Fatal(err)
			}
			if s3 := st.Stages[5]; s3.Status != contract.StageDone || s3.Iterations != 2 || len(requests) != 2 {
				t.Fatalf("%+v requests %d", s3, len(requests))
			}
			wantAccepted := 1
			if tc.wantTwice {
				wantAccepted = 2
			}
			if w, d := runner.onTree(wrongFile), runner.onTree(doublesFile); w != 1 || d != wantAccepted {
				t.Fatalf("runs on the failed tree %d (want 1), on the accepted tree %d (want %d): %v", w, d, wantAccepted, runner.pairs)
			}
			var v contract.Verdict
			readJSONTest(t, filepath.Join(dir, ".a2dlc/run/t.once", ArtVerdict), &v)
			if v.TestGates == nil || v.TestGates.Baseline.CandidateCached == tc.wantTwice {
				t.Fatalf("baseline gate %+v, want candidate_cached %v", v.TestGates, !tc.wantTwice)
			}
		})
	}
}
