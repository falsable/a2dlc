package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// scriptedTests answers each RunTests with the next run of its script and records what it was asked.
type scriptedTests struct {
	script   []engine.TestRun
	errs     []error
	envs     []map[string]string
	cmds     []string
	started  chan struct{} // closed when the first run starts
	delay    time.Duration // each run takes at least this long
	finished int           // runs that returned
}

func (s *scriptedTests) RunTests(_ context.Context, _ contract.TestsConfig, command string, env map[string]string, _ string) (engine.TestRun, error) {
	if s.started != nil {
		close(s.started)
		s.started = nil
	}
	time.Sleep(s.delay)
	defer func() { s.finished++ }()
	i := len(s.cmds)
	s.cmds, s.envs = append(s.cmds, command), append(s.envs, env)
	if i < len(s.errs) && s.errs[i] != nil {
		return engine.TestRun{}, s.errs[i]
	}
	return s.script[i], nil
}

// junitRun is a run whose JUnit has the given testcases: "file name" passes, "file name:message" fails with it.
func junitRun(exit int, cases ...string) engine.TestRun {
	var x strings.Builder
	x.WriteString("<testsuites><testsuite>")
	for _, c := range cases {
		id, msg, failed := strings.Cut(c, ":")
		file, name, _ := strings.Cut(id, " ")
		if failed {
			fmt.Fprintf(&x, `<testcase classname="%s" name="%s"><failure message="%s"/></testcase>`, file, name, msg)
		} else {
			fmt.Fprintf(&x, `<testcase classname="%s" name="%s"/>`, file, name)
		}
	}
	x.WriteString("</testsuite></testsuites>")
	return engine.TestRun{Exit: exit, JUnit: []byte(x.String())}
}

const testsOnlyConfig = `{"schema_version":1,"engines":[],"tests":{"image":"fake","paths":["test/**"],"command":"run-all"}}`

// testsRepo is a repo with no engine and config.tests only, like a repo that declares only its tests: nothing reviews a changed file but
// the tests.
func testsRepo(t *testing.T, script ...engine.TestRun) (App, *scriptedTests) {
	t.Helper()
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", testsOnlyConfig)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "tests only")
	st := &scriptedTests{script: script}
	a.Tests = st
	return a, st
}

var withTests = Tier0Extra{Tests: true}

// With no engine, the declared tests are the Tier 0 of the changed files: green and readable is a review (the file stays
// listed as one no engine covered), and the command ran once over the whole suite with the changed files in $A2DLC_CHANGED.
func TestTier0RunsTheTestsOfAFileNoEngineCovers(t *testing.T) {
	a, st := testsRepo(t, junitRun(0, "test/a.test.ts first", "test/a.test.ts second"))
	c := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
	r, unreviewed, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || !r.OK || !r.Reviewed || len(r.Findings) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
	if strings.Join(unreviewed, ",") != "src/server.ts" {
		t.Errorf("the file no engine covered must stay listed: %v", unreviewed)
	}
	if len(st.cmds) != 1 || st.cmds[0] != "run-all" || st.envs[0]["A2DLC_CHANGED"] != "src/server.ts" || st.envs[0]["A2DLC_TESTS"] != "" {
		t.Errorf("runs %v envs %v", st.cmds, st.envs)
	}
}

// A red testcase is a finding of kind test for the builder, and the next round can be green: one feedback round.
func TestTier0TurnsAFailedTestcaseIntoFeedbackAndLaterAccepts(t *testing.T) {
	a, _ := testsRepo(t,
		junitRun(1, "test/a.test.ts ok one", "test/a.test.ts stats of an unknown slug:expected 500 to be 404"),
		junitRun(0, "test/a.test.ts ok one", "test/a.test.ts stats of an unknown slug"))
	c := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || !r.Reviewed || len(r.Findings) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	f := r.Findings[0]
	if f.Kind != contract.Tier0Test || f.File != "test/a.test.ts" || f.Line != nil || !strings.Contains(f.Message, "stats of an unknown slug") || !strings.Contains(f.Message, "expected 500 to be 404") {
		t.Errorf("%+v", f)
	}
	if r, _, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err != nil || !r.OK || !r.Reviewed || len(r.Findings) != 0 {
		t.Fatalf("round 2: %v %+v", err, r)
	}
}

// A run that cannot be read is a tool error: not a review, and not feedback. It never counts as green.
func TestTier0TestsWithoutReadableJUnitAreAToolError(t *testing.T) {
	cases := map[string]scriptedTests{
		"no JUnit":                {script: []engine.TestRun{{Exit: 0, Log: "vitest ran"}}},
		"red without a testcase":  {script: []engine.TestRun{junitRun(1, "test/a.test.ts fine")}},
		"JUnit without testcases": {script: []engine.TestRun{junitRun(0)}},
		"the run itself failed":   {script: []engine.TestRun{{}}, errs: []error{fmt.Errorf("the tests exceeded timeout_s=300")}},
	}
	for name, st := range cases {
		a, _ := testsRepo(t)
		a.Tests = &st
		c := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
		r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
		if err == nil || !strings.Contains(err.Error(), "config.tests") {
			t.Errorf("%s: want a tool error, got %v", name, err)
		}
		if r.Reviewed || len(r.Findings) != 0 {
			t.Errorf("%s: a run nobody could read is neither review nor feedback: %+v", name, r)
		}
	}
	// config.tests without a runner in the binary
	a, _ := testsRepo(t)
	a.Tests = nil
	c := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
	if _, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err == nil || !strings.Contains(err.Error(), "no test runner") {
		t.Errorf("without a runner: %v", err)
	}
}

// The tests run once per Tier 0 over every changed file, the ones an engine covers included (their changed lines no test
// executes are a coverage finding, tier0_coverage_test.go); the engine's findings and the tests' add up.
func TestTier0RunsTheTestsOnceOverEngineCoveredFilesToo(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],
 "tests":{"image":"fake","paths":["tests/**"],"command":"run-all"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine and tests")
	st := &scriptedTests{script: []engine.TestRun{junitRun(0, "tests/a.t fine")}}
	a.Tests = st
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	r, unreviewed, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || !r.OK || !r.Reviewed || len(unreviewed) != 0 || len(st.cmds) != 1 || st.envs[0]["A2DLC_CHANGED"] != "src/lib/f.ts" {
		t.Fatalf("%v %+v %v ran %v %v", err, r, unreviewed, st.cmds, st.envs)
	}
	st.cmds, st.envs = nil, nil
	// a file the engine does not cover brings the tests in, and the engine's findings and the tests' add up
	st.script = []engine.TestRun{junitRun(1, "tests/a.t broke:boom")}
	c = candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\nBROKEN\n", "README.md": "bye\n"})
	r, unreviewed, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || len(r.Findings) != 2 || r.Findings[0].Kind != contract.Tier0Compile || r.Findings[1].Kind != contract.Tier0Test || strings.Join(unreviewed, ",") != "README.md" {
		t.Fatalf("%v %+v %v", err, r, unreviewed)
	}
	// an engine that does not do tier0 leaves its files to the tests too
	st.script = []engine.TestRun{junitRun(0, "tests/a.t fine")}
	st.cmds, st.envs = nil, nil
	c = candidateOf(t, a, map[string]string{"src/lib/f.ts": "NO_TIER0\n", "src/lib/g.ts": "export const g = 1\n"})
	r, _, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || !r.OK || len(st.cmds) != 1 || st.envs[0]["A2DLC_CHANGED"] != "src/lib/f.ts src/lib/g.ts" {
		t.Fatalf("%v %+v ran %v %v", err, r, st.cmds, st.envs)
	}
}

// waitingEngine reviews only after the tests started (or 2 s went by), and says whether they had.
type waitingEngine struct {
	*fakeEngine
	testsStarted chan struct{}
	overlapped   bool
	err          error
}

func (w *waitingEngine) Tier0(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Tier0Response, error) {
	select {
	case <-w.testsStarted:
		w.overlapped = true
	case <-time.After(2 * time.Second):
	}
	if w.err != nil {
		return contract.Tier0Response{}, w.err
	}
	return w.fakeEngine.Tier0(ctx, e, req, baseDir, candDir)
}

// The tests run while the engines review: neither waits for the other, and the findings keep their order (the engines'
// first). An engine's error is still the answer, and Tier0 returns only once the tests have stopped.
func TestTier0RunsTheTestsWhileTheEnginesReview(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],
 "tests":{"image":"fake","paths":["tests/**"],"command":"run-all"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine and tests")
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\nBROKEN\n"})
	started := make(chan struct{})
	st := &scriptedTests{script: []engine.TestRun{junitRun(1, "tests/a.t broke:boom")}, started: started}
	we := &waitingEngine{fakeEngine: &fakeEngine{}, testsStarted: started}
	a.Tests, a.Runner = st, we
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || !r.Reviewed || len(r.Findings) != 2 || r.Findings[0].Kind != contract.Tier0Compile || r.Findings[1].Kind != contract.Tier0Test {
		t.Fatalf("%v %+v", err, r)
	}
	if !we.overlapped {
		t.Fatal("the engine reviewed before the tests started: they ran one after the other")
	}
	started = make(chan struct{})
	st = &scriptedTests{script: []engine.TestRun{junitRun(0, "tests/a.t fine")}, started: started, delay: 200 * time.Millisecond}
	a.Tests, a.Runner = st, &waitingEngine{fakeEngine: &fakeEngine{}, testsStarted: started, err: fmt.Errorf("engine down")}
	if _, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err == nil || !strings.Contains(err.Error(), "engine down") {
		t.Fatalf("the engine's error must be the answer: %v", err)
	}
	if st.finished != len(st.cmds) || len(st.cmds) != 1 {
		t.Fatalf("Tier0 returned with the tests still running: %d started, %d finished", len(st.cmds), st.finished)
	}
}

// The timings a caller asks for record the engines' tier0, the export of the trees and the test run (Stage 3 logs them).
func TestTier0RecordsItsTimings(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],
 "tests":{"image":"fake","paths":["tests/**"],"command":"run-all"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine and tests")
	a.Tests = &scriptedTests{script: []engine.TestRun{junitRun(0, "tests/a.t fine")}}
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	ctx, timings := WithTimings(context.Background())
	if _, _, err := a.Tier0With(ctx, "HEAD", c, nil, withTests); err != nil {
		t.Fatal(err)
	}
	if tm := timings(); len(tm.EngineOps) != 1 || tm.EngineOps[0].Name != "tier0" || len(tm.TestRuns) != 1 || tm.SyncMs == 0 || tm.TotalMs == 0 {
		t.Fatalf("%+v", tm)
	}
}

// Without the flag, Tier0 is the engines only: the review of a working tree that no builder wrote does not run the suite.
func TestTier0WithoutTheTestsFlagRunsNoTests(t *testing.T) {
	a, st := testsRepo(t)
	c := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
	r, unreviewed, err := a.Tier0(context.Background(), "HEAD", c, nil)
	if err != nil || !r.OK || r.Reviewed || len(st.cmds) != 0 || strings.Join(unreviewed, ",") != "src/server.ts" {
		t.Fatalf("%v %+v %v ran %v", err, r, unreviewed, st.cmds)
	}
	// and a repo without config.tests behaves as before even with the flag
	a, _, _ = newRepo(t)
	a.Tests = &scriptedTests{}
	c = candidateOf(t, a, map[string]string{"README.md": "bye\n"})
	if r, _, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err != nil || !r.OK || r.Reviewed {
		t.Fatalf("%v %+v", err, r)
	}
}

// The feedback the builder pays for is bounded: past maxTestFindings the rest is one line.
func TestTier0BoundsTheFailedTestcasesItReports(t *testing.T) {
	var cases []string
	for i := 0; i < maxTestFindings+5; i++ {
		cases = append(cases, fmt.Sprintf("test/a.test.ts case%03d:bad", i))
	}
	a, _ := testsRepo(t, junitRun(1, cases...))
	c := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || len(r.Findings) != maxTestFindings+1 || !strings.Contains(r.Findings[maxTestFindings].Message, "5 more testcases failed") {
		t.Fatalf("%v %d findings", err, len(r.Findings))
	}
}
