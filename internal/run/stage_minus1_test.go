package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// Without tests.baseline_command Stage −1 is left skipped with the reason that skipStale recognizes; the run goes on.
func TestStageMinusOneWithoutBaselineCommandIsSkipped(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{}}`) // no Saboteur: only Stage -1 matters
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s := st.Stages[0]
	if s.ID != "-1" || s.Status != contract.StageSkipped || !strings.HasPrefix(s.Reason, reasonWithoutBaseline) {
		t.Fatalf("%+v", s)
	}
}

// With baseline_command and a suite that runs, Stage −1 ends done and says how many tests and preexisting reds there are.
func TestStageMinusOneEstablishesTheBaseline(t *testing.T) {
	a, dir, runner := repoWithTests(t)
	write(t, dir, "tests/test_rojo.t", "rojo failed\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run","baseline_command":"base"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	write(t, dir, "src/f.py", "def f(x):\n    return x + 3\n")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{}}`) // no Saboteur: only Stage -1 matters
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.v2"})
	if err != nil {
		t.Fatal(err)
	}
	s := st.Stages[0]
	if s.ID != "-1" || s.Status != contract.StageDone || !strings.Contains(s.Reason, "2 tests, 1 preexisting reds") || runner.runs < 1 {
		t.Fatalf("%+v (%d runs)", s, runner.runs)
	}
	// Stage -1 leaves baseline.json with the count and the preexisting reds, and no escalation of its own (those in
	// escalations.json, if there are any, are Stage 4's)
	if s.Artifact == nil || s.Artifact.Path != ArtBaseline {
		t.Fatalf("Stage -1 was supposed to leave %s: %+v", ArtBaseline, s.Artifact)
	}
	var lb contract.Baseline
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.v2", ArtBaseline), &lb)
	if lb.Tests != 2 || len(lb.PreexistingRed) != 1 || lb.Command != "base" || lb.Image != "x" || lb.SliceID != "lb.v2" || lb.Cached ||
		lb.ImageDigest != testDigest || lb.Reruns != 2 || !lb.Stable {
		t.Fatalf("%+v", lb)
	}
	var escalations contract.Escalations
	if b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/lb.v2", ArtEscalations)); err == nil {
		if err := contract.DecodeStrictBytes(b, &escalations); err != nil {
			t.Fatal(err)
		}
		for _, it := range escalations.Items {
			if it.Kind == KindPrerequisiteFailure {
				t.Fatalf("there should be no CB-05: %+v", it)
			}
		}
	}
}

// testDigest is the digest the fake tests image resolves to; runnerWithoutDigest is the same image with no RepoDigests.
const testDigest = "sha256:0000000000000000000000000000000000000000000000000000000000000001"

// ImageDigest makes the fake tests image a pushed one, so the baseline can be pinned to it.
func (f *fakeRunner) ImageDigest(context.Context, string) (string, error) { return testDigest, nil }

type runnerWithoutDigest struct{ fakeRunner }

func (r *runnerWithoutDigest) ImageDigest(context.Context, string) (string, error) { return "", nil }

// runnerUnstable: the second run of the baseline gives a different status for the same test.
type runnerUnstable struct{ fakeRunner }

func (r *runnerUnstable) RunTests(ctx context.Context, cfg contract.TestsConfig, cmd string, env map[string]string, tree string) (engine.TestRun, error) {
	x, err := r.fakeRunner.RunTests(ctx, cfg, cmd, env, tree)
	if r.runs == 2 {
		x.JUnit = []byte(`<testsuites><testsuite><testcase classname="c" name="old" file="tests/test_old.t"><failure message="flaked"/></testcase></testsuite></testsuites>`)
		x.Exit = 1
	}
	return x, err
}

// goldenRunner is an engine runner that answers the golden operations; failRecord makes golden_record fail. Only
// Golden is used here, so the rest of engine.Runner is left nil.
type goldenRunner struct {
	engine.Runner
	failRecord bool
}

func (g *goldenRunner) Golden(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string, _ []byte) (contract.GoldenResponse, error) {
	if req.Operation == "golden_record" && g.failRecord {
		return contract.GoldenResponse{}, fmt.Errorf("the engine does not perform golden_record")
	}
	h := "sha256:" + strings.Repeat("0", 64)
	return contract.GoldenResponse{SchemaVersion: contract.SchemaVersion, Cases: []contract.GoldenCase{{ID: "cases/a.json", InputHash: h, OutputHash: h}}}, nil
}

const baselineConfig = `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run","baseline_command":"base"}}`

// Greenfield has no base behavior to make testable: Stage −1 is skipped with the §4 reason and runs nothing.
func TestStageMinusOneSkippedInGreenfield(t *testing.T) {
	a, dir, runner := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", baselineConfig)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.gf", Mode: contract.ModeGreenfield})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Stages[0]; s.Status != contract.StageSkipped || !strings.Contains(s.Reason, "§4") || runner.runs != 0 {
		t.Fatalf("%+v (%d runs)", s, runner.runs)
	}
}

// A local image with no RepoDigests cannot pin the baseline: CB-05 before the first run.
func TestStageMinusOneImageWithoutDigestIsCB05(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", baselineConfig)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	runner := &runnerWithoutDigest{}
	a.Tests = runner
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.nd"})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Stages[0]; s.Status != contract.StageEscalated || !strings.HasPrefix(s.Reason, "CB-05: ") || runner.runs != 0 {
		t.Fatalf("%+v (%d runs)", s, runner.runs)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.nd", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || !strings.Contains(escalations.Items[0].Evidence.Message, "image without digest") {
		t.Fatalf("%+v", escalations.Items)
	}
}

// Two runs of the baseline that disagree are CB-05 and leave no baseline.json.
func TestStageMinusOneNotReproducibleIsCB05(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", baselineConfig)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	a.Tests = &runnerUnstable{}
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.nr"})
	if err != nil {
		t.Fatal(err)
	}
	s := st.Stages[0]
	if s.Status != contract.StageEscalated || !strings.HasPrefix(s.Reason, "CB-05: ") || s.Artifact == nil || s.Artifact.Path != ArtEscalations {
		t.Fatalf("%+v", s)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.nr", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != KindPrerequisiteFailure || !strings.Contains(escalations.Items[0].Evidence.Message, "not reproducible") {
		t.Fatalf("%+v", escalations.Items)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/lb.nr", ArtBaseline)); err == nil {
		t.Fatal("an unstable baseline must not write " + ArtBaseline)
	}
}

// In refactor the golden master is recorded on the base next to the baseline: golden.json holds the cases it recorded.
func TestStageMinusOneRecordsTheGoldenMasterInRefactor(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, "cases/a.json", "{}\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run","baseline_command":"base","golden_master":{"command":"run-cases","image":"bb:1","cases":"cases/*.json"}}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	a.Runner = &goldenRunner{}
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.g", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Stages[0]; s.Status != contract.StageDone || !strings.Contains(s.Reason, "golden master of 1 cases") {
		t.Fatalf("%+v", s)
	}
	var g contract.Golden
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.g", ArtGolden), &g)
	if len(g.Cases) != 1 || g.Cases[0].ID != "cases/a.json" || g.Command != "run-cases" || g.SliceID != "lb.g" {
		t.Fatalf("%+v", g)
	}
}

// A golden master that cannot be recorded stops the run in Stage −1 as CB-05: a refactor without its oracle is not judged.
func TestStageMinusOneGoldenFailureIsCB05(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, "cases/a.json", "{}\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run","baseline_command":"base","golden_master":{"command":"run-cases","image":"bb:1","cases":"cases/*.json"}}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	a.Runner = &goldenRunner{failRecord: true}
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.gf2", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Stages[0]; s.Status != contract.StageEscalated || !strings.HasPrefix(s.Reason, "CB-05: ") {
		t.Fatalf("%+v", s)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.gf2", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Target != "lb.gf2::golden_master" {
		t.Fatalf("%+v", escalations.Items)
	}
}

// runnerWithoutJUnit: the command runs but leaves no JUnit; the baseline cannot be judged.
type runnerWithoutJUnit struct{ fakeRunner }

func (r *runnerWithoutJUnit) RunTests(ctx context.Context, cfg contract.TestsConfig, cmd string, env map[string]string, tree string) (engine.TestRun, error) {
	x, err := r.fakeRunner.RunTests(ctx, cfg, cmd, env, tree)
	x.JUnit = nil
	x.Exit = 2
	return x, err
}

// A baseline that cannot be established is CB-05: Stage −1 escalates with prerequisite_failure and the run stops
// before launching any role.
func TestStageMinusOneWithoutBaselineIsCB05(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run","baseline_command":"base"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	write(t, dir, "src/f.py", "def f(x):\n    return x + 3\n")
	a.Tests = &runnerWithoutJUnit{}
	launcherFake := &fakeLauncher{}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "lb.v3"})
	if err != nil {
		t.Fatal(err)
	}
	s := st.Stages[0]
	if s.ID != "-1" || s.Status != contract.StageEscalated || !strings.HasPrefix(s.Reason, "CB-05: ") || s.Artifact == nil || s.Artifact.Path != ArtEscalations ||
		st.Outcome == nil || *st.Outcome != "escalated" || len(launcherFake.seen) != 0 {
		t.Fatalf("%+v %v (%d launches)", s, st.Outcome, len(launcherFake.seen))
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.v3", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != KindPrerequisiteFailure || escalations.Items[0].Target != "lb.v3::baseline" ||
		escalations.Items[0].Evidence.Message == "" || !strings.Contains(escalations.Items[0].Evidence.Message, "JUnit") || len(escalations.Items[0].ResolveWith) != 0 {
		t.Fatalf("%+v", escalations.Items)
	}
	if st.Stages[1].Status != contract.StagePending {
		t.Fatalf("Stage 0 was not supposed to run: %+v", st.Stages[1])
	}
}
