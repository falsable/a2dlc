package run

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// divergesWhenDoubledPlusOne is a fake differential: the builder's version "x * 2 + 1" gives 5 where the base gave 4 for x=2.
func divergesWhenDoubledPlusOne(_, candidate string) *contract.Counterexample {
	if strings.Contains(candidate, "x * 2 + 1") {
		return &contract.Counterexample{Args: "x=2", Old: "4", New: "5"}
	}
	return nil
}

func runDivergence(t *testing.T, opts Options, content func(contract.BuilderRequest) map[string]string) (contract.RunState, []contract.BuilderRequest, *engineRun, string) {
	t.Helper()
	return runDivergenceWith(t, opts, "", nil, content)
}

// commitRunDivergence commits the config of repoBuilder with run_divergence and critical_code (the run reads the base's).
func commitRunDivergence(t *testing.T, dir, mode string, critical []string) {
	t.Helper()
	cfg := map[string]any{"schema_version": 1, "run_divergence": mode,
		"engines": []any{map[string]any{"language": "python", "image": "x", "paths": []string{"src/**"}, "budget": map[string]any{}}}}
	if critical != nil {
		cfg["critical_code"] = critical
	}
	b, _ := json.Marshal(cfg)
	write(t, dir, ".a2dlc/config.json", string(b))
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "run_divergence "+mode)
}

// runDivergenceWith is runDivergence on a base whose config says run_divergence mode (not when mode is "") and critical_code.
func runDivergenceWith(t *testing.T, opts Options, mode string, critical []string, content func(contract.BuilderRequest) map[string]string) (contract.RunState, []contract.BuilderRequest, *engineRun, string) {
	t.Helper()
	a, dir, eng := repoBuilder(t)
	if mode != "" {
		commitRunDivergence(t, dir, mode, critical)
	}
	eng.diverge = divergesWhenDoubledPlusOne
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, content)}
	opts.Slice, opts.Intent = "div", intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "div", contract.ModeLite, "f" })
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	return st, requests, eng, dir
}

// Round 1 holds Tier 0 but the differential finds an input where the new code differs from the base: the builder gets a
// finding (kind divergence) with the function, the input, both results and the two ways out. In round 2 it changes nothing,
// so the same divergence is not sent again and no round is spent on it: Stage 3 accepts the candidate and its reason says
// the builder kept it. What Stage 4 does with a divergence is not changed: a2dlc run judges strictly, so the verdict is the
// FAIL it would have been without the feedback.
func TestStage3DivergenceGoesBackOnceThenTheRunAcceptsWithTheNote(t *testing.T) {
	st, requests, eng, dir := runDivergence(t, Options{}, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2 + 1\n"}
	})
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || len(requests) != 2 || eng.probes != 2 {
		t.Fatalf("%+v: requests %d, probes %d", s3, len(requests), eng.probes)
	}
	if requests[0].Tier0Feedback != nil {
		t.Fatalf("round 1 has feedback: %+v", requests[0].Tier0Feedback)
	}
	fb := requests[1].Tier0Feedback
	if fb == nil || fb.OK || len(fb.Findings) != 1 || fb.Findings[0].Kind != contract.Tier0Divergence || fb.Findings[0].File != "src/f.py" {
		t.Fatalf("%+v", fb)
	}
	for _, want := range []string{"src/f.py::f", "input x=2", "old code returned 4", "new code returns 5",
		"If the intent does not ask for this change, restore the old behavior", "if it does, add a test that pins the new behavior"} {
		if !strings.Contains(fb.Findings[0].Message, want) {
			t.Fatalf("missing %q in %q", want, fb.Findings[0].Message)
		}
	}
	if !strings.Contains(s3.Reason, "divergence src/f.py::f (args: x=2; old: 4; new: 5): returned to the builder, which kept it") {
		t.Fatalf("%q", s3.Reason)
	}
	// Stage 4 judged the commit once, with the counterexample the builder was told about, and strictly as it always does in a run
	if len(eng.sawCand) != 1 {
		t.Fatalf("judged %d", len(eng.sawCand))
	}
	var v contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/div", ArtVerdict), &v)
	if v.Verdict != contract.FAIL || len(v.Targets) != 1 || v.Targets[0].Counterexample == nil || v.Targets[0].Counterexample.New != "5" {
		t.Fatalf("%s %+v", v.Verdict, v.Targets)
	}
}

// The builder restores the old behavior after the finding: no divergence is left, nothing is noted.
func TestStage3DivergenceRestoredByTheBuilder(t *testing.T) {
	st, requests, eng, _ := runDivergence(t, Options{}, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback != nil {
			return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2 + 1\n"}
	})
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || len(requests) != 2 || eng.probes != 2 || strings.Contains(s3.Reason, "divergence") {
		t.Fatalf("%+v: requests %d, probes %d", s3, len(requests), eng.probes)
	}
	if requests[1].Tier0Feedback == nil || requests[1].Tier0Feedback.Findings[0].Kind != contract.Tier0Divergence {
		t.Fatalf("%+v", requests[1].Tier0Feedback)
	}
}

// With a single round there is none left to act on a divergence: it is not sent, only noted.
func TestStage3DivergenceOnTheLastRoundIsOnlyANote(t *testing.T) {
	st, requests, _, _ := runDivergence(t, Options{MaxInner: 1}, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2 + 1\n"}
	})
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || len(requests) != 1 || !strings.Contains(s3.Reason, "no round is left to act on it") {
		t.Fatalf("%+v: requests %d", s3, len(requests))
	}
}

func keepsTheDivergence(contract.BuilderRequest) map[string]string {
	return map[string]string{"src/f.py": "def f(x):\n    return x * 2 + 1\n"}
}

// config.run_divergence: strict (and absent) is today's run, a divergence the builder keeps is a FAIL; critical_code judges it
// as check does: a FAIL only on config.critical_code, a note on its target elsewhere. The builder is sent the divergence once
// in every case, so the note is always the second time the divergence is seen.
func TestRunDivergenceStrictIsAFail(t *testing.T) {
	for _, mode := range []string{"", "strict"} {
		st, requests, _, dir := runDivergenceWith(t, Options{}, mode, nil, keepsTheDivergence)
		var v contract.Verdict
		readJSONTest(t, filepath.Join(dir, ".a2dlc/run/div", ArtVerdict), &v)
		if len(requests) != 2 || v.Verdict != contract.FAIL || len(v.Targets) != 1 || v.Targets[0].Note != "" || st.Outcome == nil || *st.Outcome != contract.FAIL {
			t.Fatalf("%q: %s %+v %v", mode, v.Verdict, v.Targets, st.Outcome)
		}
	}
}

func TestRunDivergenceCriticalCodeOutsideIsAPassWithTheNote(t *testing.T) {
	st, requests, _, dir := runDivergenceWith(t, Options{}, "critical_code", []string{"lib/**", "src/other.py::*"}, keepsTheDivergence)
	var v contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/div", ArtVerdict), &v)
	if len(requests) != 2 || v.Verdict != contract.PASS || len(v.Targets) != 1 || v.Targets[0].Verdict != contract.FAIL ||
		v.Targets[0].Note != contract.NoteBehaviorDivergence || v.Targets[0].Counterexample == nil || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%s %+v %v", v.Verdict, v.Targets, st.Outcome)
	}
	if esc := st.Stages[6]; esc.Status != contract.StageDone {
		t.Fatalf("%+v", esc)
	}
}

func TestRunDivergenceCriticalCodeInsideIsAFail(t *testing.T) {
	st, _, _, dir := runDivergenceWith(t, Options{}, "critical_code", []string{"src/**"}, keepsTheDivergence)
	var v contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/div", ArtVerdict), &v)
	if v.Verdict != contract.FAIL || v.Targets[0].Note != "" || st.Outcome == nil || *st.Outcome != contract.FAIL {
		t.Fatalf("%s %+v %v", v.Verdict, v.Targets, st.Outcome)
	}
}

// RFC-0005: with run_differential "stage3", Stage 4 judges the tree of the builder's last round with the diff answer Stage 3
// got on it: the engine is not asked again (sawCand stays empty), and the verdict is the strict FAIL of the test above, with
// the counterexample the builder was told about. Without the field that test shows Stage 4 asking once.
func TestRunDifferentialStage3JudgesWithTheLastRoundsAnswer(t *testing.T) {
	a, dir, eng := repoBuilder(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"run_differential":"stage3","engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "run_differential stage3")
	eng.diverge = divergesWhenDoubledPlusOne
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, keepsTheDivergence)}
	path := intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "div", contract.ModeLite, "f" })
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "div", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || eng.probes != 2 || len(eng.sawCand) != 0 {
		t.Fatalf("requests %d, probes %d, Stage 4 asked %d times", len(requests), eng.probes, len(eng.sawCand))
	}
	var v contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/div", ArtVerdict), &v)
	if v.Verdict != contract.FAIL || len(v.Targets) != 1 || v.Targets[0].Counterexample == nil || v.Targets[0].Counterexample.New != "5" || st.Outcome == nil || *st.Outcome != contract.FAIL {
		t.Fatalf("%s %+v %v", v.Verdict, v.Targets, st.Outcome)
	}
	if v.Timings == nil {
		t.Fatal("the verdict has no timings")
	}
	reused := false
	for _, op := range v.Timings.EngineOps {
		reused = reused || op.Name == "diff_from_stage3"
	}
	if !reused {
		t.Fatalf("%+v", v.Timings)
	}
}

// A value the schema does not know is an error, not a silent strict.
func TestRunDivergenceUnknownValueIsRefused(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	commitRunDivergence(t, dir, "everything", nil)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, keepsTheDivergence)}
	path := intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "div", contract.ModeLite, "f" })
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "div", Intent: path})
	failed := err != nil && strings.Contains(err.Error(), `run_divergence "everything"`)
	for _, s := range st.Stages {
		failed = failed || strings.Contains(s.Reason, `run_divergence "everything"`)
	}
	if !failed {
		t.Fatalf("%v %+v", err, st.Stages)
	}
}

// N=2, run_divergence critical_code: both PASS, but the orthogonal (the simpler one, which Occam would pick) changes behavior
// where the primary does not; fewer divergence notes ranks higher than lower complexity.
func TestN2FewerDivergenceNotesRanksAboveComplexity(t *testing.T) {
	withRouterN2(t)
	a, dir, eng := repoBuilder(t)
	commitRunDivergence(t, dir, "critical_code", nil)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	eng.diverge = func(_, candidate string) *contract.Counterexample {
		if strings.Contains(candidate, "x + x") {
			return &contract.Counterexample{Args: "x=2", Old: "4", New: "4 (reordered)"}
		}
		return nil
	}
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, pl contract.BuilderRequest) map[string]string {
		if role == contract.RoleBuilderOrthogonal {
			return map[string]string{"src/f.py": "def f(x):\n    return x + x\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    if x:\n        return x * 2\n    return 0\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "n2.v1"})
	if err != nil {
		t.Fatal(err)
	}
	var sel contract.Selection
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/n2.v1", ArtSelection), &sel)
	if st.Stages[6].Status != contract.StageDone || sel.Selected == nil || *sel.Selected != contract.RoleBuilderPrimary ||
		!strings.Contains(sel.Reason, "has fewer divergence notes (builder_primary: 0, builder_orthogonal: 1)") {
		t.Fatalf("%+v selected=%v reason=%q", st.Stages[6], sel.Selected, sel.Reason)
	}
}
