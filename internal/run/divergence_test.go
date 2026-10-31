package run

import (
	"context"
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
	a, dir, eng := repoBuilder(t)
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
// so the same divergence is not sent again: the run accepts the candidate, the stage reason says the builder kept it, and
// Stage 4 records it as the receipt note of an ordinary divergence (the verdict is not lowered).
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
	// Stage 4 judged the commit once; the divergence is the usual receipt note, and the run is not stopped by it
	if len(eng.sawCand) != 1 || st.Stages[6].Status != contract.StageDone {
		t.Fatalf("judged %d, %+v", len(eng.sawCand), st.Stages[6])
	}
	var v contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/div", ArtVerdict), &v)
	if len(v.Targets) != 1 || v.Targets[0].Verdict != contract.FAIL || v.Targets[0].Note != "behavior_divergence" || v.Verdict != contract.PASS {
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
