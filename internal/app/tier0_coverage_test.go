package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// Tier 0 of the declared tests also reads the coverage the command writes (bead a2dlc-oer): a changed source line that the
// whole suite did not execute is a finding of kind coverage, with the classification Stage 4 uses, so the builder hears
// about it in a round and not from an INCONCLUSIVE verdict.

const coverageConfig = `{"schema_version":1,"engines":[],"tests":{"image":"fake","paths":["test/**"],"source_paths":["src/**"],"command":"run-all"}}`

// withCoverage is run with lcov text: "file:hits,hits,..." gives one SF record whose DA lines are numbered from 1.
func withCoverage(r engine.TestRun, records ...string) engine.TestRun {
	var b strings.Builder
	for _, rec := range records {
		file, hits, _ := strings.Cut(rec, ":")
		b.WriteString("SF:" + file + "\n")
		for i, h := range strings.Split(hits, ",") {
			b.WriteString("DA:" + string(rune('0'+i+1)) + "," + h + "\n")
		}
		b.WriteString("end_of_record\n")
	}
	r.Coverage = []byte(b.String())
	return r
}

func coverageRepo(t *testing.T, config string, script ...engine.TestRun) (App, *scriptedTests) {
	t.Helper()
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", config)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "tests with source paths")
	st := &scriptedTests{script: script}
	a.Tests = st
	return a, st
}

func TestTier0ReturnsAnUnexecutedChangedLineAsAFindingAndALaterRoundAccepts(t *testing.T) {
	green := junitRun(0, "test/a.test.ts ok")
	a, st := coverageRepo(t, coverageConfig,
		withCoverage(green, "src/server.ts:1,0,0"),
		withCoverage(green, "src/server.ts:1,1,1"))
	c := candidateOf(t, a, map[string]string{"src/server.ts": "a\nb\nc\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || !r.Reviewed || len(r.Findings) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	f := r.Findings[0]
	if f.Kind != contract.Tier0Coverage || f.File != "src/server.ts" || f.Line == nil || *f.Line != 2 || !strings.Contains(f.Message, "2-3") ||
		!strings.Contains(f.Message, "new test file") {
		t.Errorf("%+v", f)
	}
	if r, _, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err != nil || !r.OK || !r.Reviewed || len(r.Findings) != 0 {
		t.Fatalf("round 2: %v %+v", err, r)
	}
	if len(st.cmds) != 2 {
		t.Errorf("one run per round: %v", st.cmds)
	}
}

func TestTier0CoverageGivesNoFindingWhenNothingIsLeftUnexecuted(t *testing.T) {
	green := junitRun(0, "test/a.test.ts ok")
	cases := map[string]engine.TestRun{
		"every changed line executed":                      withCoverage(green, "src/server.ts:1,1,4"),
		"the run left no coverage":                         green,
		"unreadable coverage":                              {Exit: 0, JUnit: green.JUnit, Coverage: []byte("not lcov at all\n")},
		"lines the tool does not count (comments, braces)": withCoverage(green, "src/server.ts:1"),
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			a, _ := coverageRepo(t, coverageConfig, run)
			c := candidateOf(t, a, map[string]string{"src/server.ts": "a\nb\nc\n"})
			r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
			if err != nil || !r.OK || !r.Reviewed || len(r.Findings) != 0 {
				t.Fatalf("%v %+v", err, r)
			}
		})
	}
}

// A changed source that no test loaded has no record in the coverage at all: every changed line of it is unexecuted (the
// entry file of the P2 report, loaded by no test).
func TestTier0CoverageTreatsASourceNoTestLoadedAsUnexecuted(t *testing.T) {
	a, _ := coverageRepo(t, coverageConfig, withCoverage(junitRun(0, "test/a.test.ts ok"), "src/other.ts:1,1"))
	c := candidateOf(t, a, map[string]string{"src/main.ts": "a\nb\nc\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || len(r.Findings) != 1 || r.Findings[0].File != "src/main.ts" || !strings.Contains(r.Findings[0].Message, "1-3") {
		t.Fatalf("%v %+v", err, r)
	}
}

// A new test file is not a source: its own zero-hit lines are nobody's finding. And a source outside config.tests
// source_paths needs no coverage, as in Stage 4.
func TestTier0CoverageOnlyAsksAboutSourcesInScope(t *testing.T) {
	green := junitRun(0, "test/n.test.ts ok")
	a, _ := coverageRepo(t, coverageConfig, withCoverage(green, "test/n.test.ts:0,0", "docs/x.ts:0,0"))
	c := candidateOf(t, a, map[string]string{"test/n.test.ts": "a\nb\n", "docs/x.ts": "a\nb\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || !r.OK || len(r.Findings) != 0 {
		t.Fatalf("%v %+v", err, r)
	}
}

// A red suite is fixed first: its findings go back alone, and the coverage of a failing run says little.
func TestTier0CoverageWaitsForAGreenSuite(t *testing.T) {
	red := withCoverage(junitRun(1, "test/a.test.ts boom:expected 1 to be 2"), "src/server.ts:1,0,0")
	a, _ := coverageRepo(t, coverageConfig, red)
	c := candidateOf(t, a, map[string]string{"src/server.ts": "a\nb\nc\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || len(r.Findings) != 1 || r.Findings[0].Kind != contract.Tier0Test {
		t.Fatalf("%v %+v", err, r)
	}
}

// noTier0 is an engine that does not review (so its files go to the declared tests) but does classify lines.
type noTier0 struct{ *classifying }

func (noTier0) Tier0(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Tier0Response, error) {
	return contract.Tier0Response{}, engine.ErrWithoutTier0
}

// The classification is Stage 4's: lines the engine calls declaration-only are not findings, the rest are.
func TestTier0CoverageUsesTheClassificationOfStage4(t *testing.T) {
	config := `{"schema_version":1,"engines":[{"language":"python","image":"fake","paths":["app/**"],"budget":{}}],` +
		`"tests":{"image":"fake","paths":["test/**"],"source_paths":["app/**"],"command":"run-all"}}`
	a, _ := coverageRepo(t, config, withCoverage(junitRun(0, "test/a.test.ts ok"), "app/f.py:1,0,0,0"))
	fe := &classifying{fakeEngine: &fakeEngine{}, answer: answering("app/f.py", map[string][]int{contract.NoteDeclarativeOnly: {3, 4}})}
	a.Runner = noTier0{fe}
	c := candidateOf(t, a, map[string]string{"app/f.py": "a\nb\nc\nd\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || len(r.Findings) != 1 {
		t.Fatalf("%v %+v", err, r)
	}
	f := r.Findings[0]
	if f.Kind != contract.Tier0Coverage || f.Line == nil || *f.Line != 2 || strings.Contains(f.Message, "3") || strings.Contains(f.Message, "4") {
		t.Errorf("only line 2 is a finding: %+v", f)
	}
	if len(fe.asked) != 1 || len(asked(t, fe.asked[0])["app/f.py"]) != 3 {
		t.Errorf("the engine is asked about the three unexecuted lines: %+v", fe.asked)
	}
	// With every unexecuted line declaration-only there is no finding at all.
	a.Tests.(*scriptedTests).script = append(a.Tests.(*scriptedTests).script, withCoverage(junitRun(0, "test/a.test.ts ok"), "app/f.py:1,1,0,0"))
	if r, _, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err != nil || !r.OK || len(r.Findings) != 0 {
		t.Fatalf("declaration-only lines are not findings: %v %+v", err, r)
	}
}

// A file an engine covers gets the coverage finding too (a recorded run: Stage 4 rejected "changed lines no test
// executed: src/server.ts" while Tier 0 had said nothing, because the tests only ran for files no engine reviewed). The
// engine here is the shape of the TypeScript engine on src/server.ts: it reviews the file in Tier 0 and cannot judge its
// target at Stage 4, so the tests are the only thing that examines the changed lines.
const engineAndCoverageConfig = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/**"],"budget":{}}],` +
	`"tests":{"image":"fake","paths":["test/**"],"source_paths":["src/**"],"command":"run-all"}}`

func TestTier0CoverageFindsUnexecutedLinesOfAnEngineCoveredFile(t *testing.T) {
	green := junitRun(0, "test/a.test.ts ok")
	a, st := coverageRepo(t, engineAndCoverageConfig,
		withCoverage(green, "src/server.ts:1,0,0"),
		withCoverage(green, "src/server.ts:1,1,1"))
	c := candidateOf(t, a, map[string]string{"src/server.ts": "a\nb\nc\n"})
	r, unreviewed, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || !r.Reviewed || len(r.Findings) != 1 || len(unreviewed) != 0 {
		t.Fatalf("%v %+v %v", err, r, unreviewed)
	}
	f := r.Findings[0]
	if f.Kind != contract.Tier0Coverage || f.File != "src/server.ts" || f.Line == nil || *f.Line != 2 || !strings.Contains(f.Message, "2-3") {
		t.Errorf("%+v", f)
	}
	// The same lines executed by a test: no finding, and still one test run per Tier 0.
	if r, _, err = a.Tier0With(context.Background(), "HEAD", c, nil, withTests); err != nil || !r.OK || len(r.Findings) != 0 {
		t.Fatalf("round 2: %v %+v", err, r)
	}
	if len(st.cmds) != 2 {
		t.Errorf("one test run per Tier 0: %v", st.cmds)
	}
}

// Two engine-covered files and one with no engine are one test run, and each file with unexecuted lines has its finding.
func TestTier0CoverageIsOneRunForEveryChangedSource(t *testing.T) {
	config := `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],` +
		`"tests":{"image":"fake","paths":["test/**"],"source_paths":["src/**"],"command":"run-all"}}`
	green := junitRun(0, "test/a.test.ts ok")
	a, st := coverageRepo(t, config, withCoverage(green, "src/lib/f.ts:1,0", "src/lib/g.ts:1,1", "src/main.ts:1,0"))
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "a\nb\n", "src/lib/g.ts": "a\nb\n", "src/main.ts": "a\nb\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, withTests)
	if err != nil || r.OK || len(r.Findings) != 2 || r.Findings[0].File != "src/lib/f.ts" || r.Findings[1].File != "src/main.ts" {
		t.Fatalf("%v %+v", err, r)
	}
	if len(st.cmds) != 1 || st.envs[0]["A2DLC_CHANGED"] != "src/lib/f.ts src/lib/g.ts src/main.ts" {
		t.Errorf("runs %v envs %v", st.cmds, st.envs)
	}
}
