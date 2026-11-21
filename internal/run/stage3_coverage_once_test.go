package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// runnerUncovered is a green suite that executes no line of src/f.py: every round has one coverage finding.
type runnerUncovered struct{ runs int }

func (r *runnerUncovered) RunTests(context.Context, contract.TestsConfig, string, map[string]string, string) (engine.TestRun, error) {
	r.runs++
	return engine.TestRun{JUnit: []byte(`<testsuites><testsuite><testcase classname="c" name="doubles" file="test/t.test.py"/></testsuite></testsuites>`),
		Coverage: []byte("SF:src/f.py\nDA:1,0\nDA:2,0\nDA:3,0\nDA:4,0\nDA:5,0\nDA:6,0\nDA:7,0\nend_of_record\n")}, nil
}

func coverageOnlyRepo(t *testing.T, tier0Coverage string) (*runnerUncovered, string, func(slice string, rounds int) (contract.RunState, []contract.BuilderRequest)) {
	t.Helper()
	runner := &runnerUncovered{}
	a, dir := repoTestsOnly(t, runner)
	knob := ""
	if tier0Coverage != "" {
		knob = `,"tier0_coverage":"` + tier0Coverage + `"`
	}
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["test/**"],"source_paths":["src/**"],"command":"run"`+knob+`}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "coverage knob")
	return runner, dir, func(slice string, rounds int) (contract.RunState, []contract.BuilderRequest) {
		var requests []contract.BuilderRequest
		launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
			return map[string]string{"src/f.py": strings.Repeat("# r\n", pl.Round) + doublesFile}
		})}
		st, err := Run(context.Background(), a, launcherFake, Options{Slice: slice, MaxInner: rounds})
		if err != nil {
			t.Fatal(err)
		}
		return st, requests
	}
}

// Absent (feedback): coverage findings the builder cannot clear spend every round and the stage escalates (CB-02b).
func TestStage3CoverageFeedbackExhaustsTheRounds(t *testing.T) {
	_, _, run := coverageOnlyRepo(t, "")
	st, requests := run("cov.fb", 3)
	if s3 := st.Stages[5]; s3.Status != contract.StageEscalated || len(requests) != 3 {
		t.Fatalf("%+v %d", s3, len(requests))
	}
}

// once: the findings go back one time; the next round, still with only coverage findings, is the candidate, with a note.
func TestStage3CoverageOnceTakesTheSecondRoundAsTheCandidate(t *testing.T) {
	_, dir, run := coverageOnlyRepo(t, contract.Tier0CoverageOnce)
	st, requests := run("cov.once", 5)
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || s3.Iterations != 2 || len(requests) != 2 || !strings.Contains(s3.Reason, "tier0_coverage once: round 2") {
		t.Fatalf("%+v %d", s3, len(requests))
	}
	if fb := requests[1].Tier0Feedback; fb == nil || len(fb.Findings) != 1 || fb.Findings[0].Kind != contract.Tier0Coverage {
		t.Fatalf("round 2 got %+v", fb)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/cov.once", ArtCandidate)); err != nil {
		t.Fatal(err)
	}
}

// once with a single round: the last round's coverage-only tree is the candidate without a round of feedback.
func TestStage3CoverageOnceOnTheLastRound(t *testing.T) {
	_, _, run := coverageOnlyRepo(t, contract.Tier0CoverageOnce)
	st, requests := run("cov.last", 1)
	if s3 := st.Stages[5]; s3.Status != contract.StageDone || len(requests) != 1 {
		t.Fatalf("%+v %d", s3, len(requests))
	}
}

func TestOnlyCoverage(t *testing.T) {
	cov, test := contract.Tier0Finding{Kind: contract.Tier0Coverage}, contract.Tier0Finding{Kind: contract.Tier0Test}
	if onlyCoverage(nil) || !onlyCoverage([]contract.Tier0Finding{cov, cov}) || onlyCoverage([]contract.Tier0Finding{cov, test}) {
		t.Fatal("onlyCoverage")
	}
}
