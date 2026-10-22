package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// engineN2 is the fake engine of the N=2 judgment, through a whole run. Each changed file has a function f. The oracle
// (base against a candidate) passes it. The differential reads A from the base folder and B from the candidate folder
// (app.Equivalence): it fails f when A's file holds the marker A_VERSION and B's holds B_VERSION. Fuzz finds an error on f
// in a candidate whose file holds FUZZ_LONG (input x=1000) or FUZZ_SHORT (input x=2).
type engineN2 struct{ engineRun }

func (f *engineN2) Signatures(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.SignaturesResponse, error) {
	r := contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}
	for _, file := range req.ChangedFiles {
		r.Files = append(r.Files, contract.SignatureFile{File: file, Symbols: []contract.Signature{{Name: "f", Signature: "(x)", Lines: []int{1, 2}}}})
	}
	return r, nil
}

func (f *engineN2) Run(ctx context.Context, cfg contract.EngineConfig, req contract.Request, baseDir, candidateDir string) (contract.Response, error) {
	r, err := f.engineRun.Run(ctx, cfg, req, baseDir, candidateDir)
	for i := range r.Targets {
		file := strings.Split(r.Targets[i].Target, "::")[0]
		base, _ := os.ReadFile(filepath.Join(baseDir, file))
		cand, _ := os.ReadFile(filepath.Join(candidateDir, file))
		if strings.Contains(string(base), "A_VERSION") && strings.Contains(string(cand), "B_VERSION") {
			r.Targets[i].Verdict = contract.FAIL
			r.Targets[i].Counterexample = &contract.Counterexample{Args: "x=2", Old: "4", New: "2"}
		}
	}
	return r, err
}

func (f *engineN2) Fuzz(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.FuzzResponse, error) {
	r := contract.FuzzResponse{SchemaVersion: 1, Findings: []contract.FuzzFinding{}, Runs: 100, Seconds: FuzzSeconds}
	for _, file := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candidateDir, file))
		switch {
		case strings.Contains(string(b), "FUZZ_LONG"):
			r.Findings = append(r.Findings, contract.FuzzFinding{Function: "f", Input: "x=1000", Error: "ZeroDivisionError", Seed: FuzzSeed})
		case strings.Contains(string(b), "FUZZ_SHORT"):
			r.Findings = append(r.Findings, contract.FuzzFinding{Function: "f", Input: "x=2", Error: "ZeroDivisionError", Seed: FuzzSeed})
		}
	}
	return r, nil
}

// n2Repo is a repo with N=2 routed and the two builders of rolesN2, judged by engineN2 under the slice name.
func n2Repo(t *testing.T) (app.App, string) {
	t.Helper()
	withRouterN2(t)
	a, dir, _ := repoBuilder(t)
	a.Runner = &engineN2{}
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	return a, dir
}

func TestN2EquivalenceDivergenceIsCB01EndToEnd(t *testing.T) {
	source := map[string]string{
		contract.RoleBuilderPrimary:    "def f(x):\n    return x * 2  # A_VERSION\n",
		contract.RoleBuilderOrthogonal: "def f(x):\n    return x + x  # B_VERSION\n",
	}
	a, dir := n2Repo(t)
	var seen []launchRecord
	launcher := &fakeLauncher{script: buildersN2(t, &seen, func(role string, _ contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": source[role]}
	})}
	st, err := Run(context.Background(), a, launcher, Options{Slice: "cb01eq"})
	if err != nil {
		t.Fatal(err)
	}
	s4 := st.Stages[6]
	if s4.Status != contract.StageEscalated || !strings.HasPrefix(s4.Reason, "CB-01: divergence between builders") ||
		!strings.Contains(s4.Reason, "equivalence_s: src/f.py::f") || s4.Artifact == nil || s4.Artifact.Path != ArtSelection ||
		st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", s4, st.Outcome)
	}
	run := filepath.Join(dir, ".a2dlc/run/cb01eq")
	var selection contract.Selection
	readJSONTest(t, filepath.Join(run, ArtSelection), &selection)
	if selection.Selected != nil || !strings.HasPrefix(selection.Reason, "CB-01") || !strings.Contains(selection.Reason, "equivalence_s: src/f.py::f") {
		t.Fatalf("%+v", selection)
	}
	// each candidate passed its own oracle; both verdicts carry the equivalence gate that found the difference
	for _, artifact := range []string{ArtVerdictPrimary, ArtVerdictOrthogonal} {
		var v contract.Verdict
		readJSONTest(t, filepath.Join(run, artifact), &v)
		if v.Verdict != contract.PASS || v.TestGates == nil || v.TestGates.EquivalenceS == nil || v.TestGates.EquivalenceS.Status != contract.GateRan {
			t.Fatalf("%s: %+v gates %+v", artifact, v.Verdict, v.TestGates)
		}
	}
	// the escalation: the divergence with its counterexample, once, with its cluster
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(run, ArtEscalations), &escalations)
	var divergences []contract.Escalation
	for _, e := range escalations.Items {
		if e.Kind == "behavior_divergence" {
			divergences = append(divergences, e)
		}
	}
	if len(divergences) != 1 || divergences[0].Target != "src/f.py::f" || divergences[0].ClusterID == "" ||
		divergences[0].Evidence.Counterexample == nil || divergences[0].Evidence.Counterexample.Args != "x=2" {
		t.Fatalf("%+v", escalations.Items)
	}
	if _, err := os.Stat(filepath.Join(run, ArtVerdict)); err == nil {
		t.Fatal("with CB-01 there is no chosen verdict")
	}
}

func TestN2FuzzingFindingsOfBothCandidatesOnOneFunctionAreOneEscalation(t *testing.T) {
	source := map[string]string{
		contract.RoleBuilderPrimary:    "def f(x):\n    return x * 2  # FUZZ_LONG\n",
		contract.RoleBuilderOrthogonal: "def f(x):\n    return x + x  # FUZZ_SHORT\n",
	}
	a, dir := n2Repo(t)
	var seen []launchRecord
	launcher := &fakeLauncher{script: buildersN2(t, &seen, func(role string, _ contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": source[role]}
	})}
	st, err := Run(context.Background(), a, launcher, Options{Slice: "fuzz2"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Stages[6].Status != contract.StageDone || strings.Contains(st.Stages[6].Reason, "fuzzing did not run") {
		t.Fatalf("both candidates pass and the fuzzing round ran: %+v", st.Stages[6])
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/fuzz2", ArtEscalations), &escalations)
	seenIDs := map[string]bool{}
	var onF []contract.Escalation
	for _, e := range escalations.Items {
		if seenIDs[e.ID] {
			t.Fatalf("two escalations with id %s: %+v", e.ID, escalations.Items)
		}
		seenIDs[e.ID] = true
		if e.Kind == "behavior_divergence" && e.Target == "src/f.py::f" {
			onF = append(onF, e)
		}
	}
	// the two fuzzing findings collide on kind plus target: one escalation, with the shortest input
	if len(onF) != 1 || onF[0].Evidence.Counterexample == nil || onF[0].Evidence.Counterexample.Args != "x=2" {
		t.Fatalf("one behavior_divergence on src/f.py::f with the shortest input expected: %+v", onF)
	}
}
