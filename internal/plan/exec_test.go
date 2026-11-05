package plan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// passEngine judges every changed file PASS and has a clean Tier 0.
type passEngine struct{ runs int }

func (e *passEngine) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.Response, error) {
	e.runs++
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
	for _, f := range req.ChangedFiles {
		r.Targets = append(r.Targets, contract.TargetResult{Target: f + "::f", Engine: "fake", EngineVersion: "fake", Verdict: contract.PASS,
			SearchBudget: json.RawMessage(`{"iterations":1}`), Generators: "typed_default", Equivalence: "strict"})
	}
	return r, nil
}
func (e *passEngine) Stub(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.StubResponse, map[string][]byte, error) {
	return contract.StubResponse{SchemaVersion: 1, Stubbed: []contract.StubFile{}, Unstubbed: []string{}}, nil, nil
}
func (e *passEngine) Signatures(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.SignaturesResponse, error) {
	return contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}, nil
}
func (e *passEngine) Tier0(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Tier0Response, error) {
	return contract.Tier0Response{SchemaVersion: 1, OK: true, Findings: []contract.Tier0Finding{}}, nil
}
func (e *passEngine) Complexity(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.ComplexityResponse, error) {
	r := contract.ComplexityResponse{SchemaVersion: 1, Files: []contract.ComplexityFile{}}
	for _, f := range req.ChangedFiles {
		c, n := 1, 10
		r.Files = append(r.Files, contract.ComplexityFile{File: f, LocAdded: 2, Cyclomatic: &c, ASTNodes: &n})
	}
	return r, nil
}
func (e *passEngine) Mutate(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.MutateResponse, map[string][]byte, error) {
	return contract.MutateResponse{}, nil, os.ErrInvalid
}

// builder writes, for the slice its intent names, what files says.
type builder struct {
	files    func(slice string) map[string]string
	launches map[string]int
}

func (b *builder) Launch(_ context.Context, r contract.Role, s a2run.LaunchSpec) (a2run.LaunchResult, error) {
	var req contract.RoleRequest
	raw, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	if err := contract.DecodeStrictBytes(raw, &req); err != nil {
		return a2run.LaunchResult{}, err
	}
	var pl contract.BuilderRequest
	if err := contract.DecodeStrictBytes(req.Payload, &pl); err != nil {
		return a2run.LaunchResult{}, err
	}
	var in contract.Intent
	if err := json.Unmarshal(pl.Intent, &in); err != nil {
		return a2run.LaunchResult{}, err
	}
	b.launches[in.SliceID]++
	var touched []string
	for rel, c := range b.files(in.SliceID) {
		p := filepath.Join(s.WorkDir, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(c), 0o644)
		touched = append(touched, rel)
	}
	out, _ := json.Marshal(contract.BuilderResponse{Summary: "done", FilesChanged: touched})
	resp, _ := json.Marshal(contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
		Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.01}, Payload: out})
	os.WriteFile(filepath.Join(s.OutDir, "response.json"), resp, 0o644)
	return a2run.LaunchResult{Argv: []string{"docker", "run", r.Image}, Network: a2run.NetworkNone}, nil
}

func e2eRepo(t *testing.T) (app.App, string, *passEngine) {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, nil, "init", "-q", "-b", "main")
	for rel, c := range map[string]string{
		".a2dlc/config.json":    `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`,
		".a2dlc/approvals.json": `{"schema_version":1,"approvals":[]}`,
		"src/core.py":           "def core(x):\n    return x\n",
	} {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0o755)
		os.WriteFile(filepath.Join(dir, rel), []byte(c), 0o644)
	}
	gitRun(t, dir, nil, "add", "-A")
	gitRun(t, dir, nil, "commit", "-qm", "base")
	os.WriteFile(filepath.Join(dir, ".a2dlc/roles.json"),
		[]byte(`{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0}}}`), 0o644)
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	eng := &passEngine{}
	a := app.App{Repo: repo, Version: "test", Runner: eng, Now: func() time.Time { tick = tick.Add(time.Second); return tick }}
	return a, gitRun(t, dir, nil, "rev-parse", "HEAD"), eng
}

// threeSlices: left and right at the same time, join after both.
func threeSlices() (contract.Plan, contract.Intent) {
	mk := func(id, file string, deps []string, cover int) contract.PlanSlice {
		return contract.PlanSlice{SliceID: "feat." + id, Goal: "add " + id, Owns: []string{file}, DependsOn: deps, Criticality: "low",
			Budget: contract.PlanBudget{MaxCostUSD: 0.3}, Acceptance: []string{}, Invariants: []string{}, Covers: []int{cover}}
	}
	p := contract.Plan{SchemaVersion: 1, FeatureID: "feat", Shared: []string{}, Slices: []contract.PlanSlice{
		mk("left", "src/left.py", []string{}, 0), mk("right", "src/right.py", []string{}, 1),
		mk("join", "src/join.py", []string{"feat.left", "feat.right"}, 2)}}
	cap := 1.0
	f := contract.Intent{SchemaVersion: 1, SliceID: "feat", Mode: contract.ModeLite, Goal: "three parts", Invariants: []string{}, NonGoals: []string{},
		Criticality: "low", CriticalPaths: []string{}, Acceptance: []string{"left", "right", "join"},
		Budget: &contract.IntentBudget{MaxCostUSD: &cap, MaxInnerIterations: 3, MaxTier1AdversarialRounds: 1}}
	return p, f
}

func ownFile(slice string) map[string]string {
	name := strings.TrimPrefix(slice, "feat.")
	return map[string]string{"src/" + name + ".py": "def " + name + "(x):\n    return x\n"}
}

// Three slices, two at the same time and one after both, through real a2dlc runs: PASS with the merged tree checked once
// more; a second invocation runs nothing; the plan's files stay out of the tree_hash.
func TestPlanRunEndToEnd(t *testing.T) {
	a, base, eng := e2eRepo(t)
	p, f := threeSlices()
	planBytes, _ := json.Marshal(p)
	b := &builder{files: ownFile, launches: map[string]int{}}
	r := Runner{Repo: a.Repo, Exec: RunExec{App: a, Launcher: b, Feature: p.FeatureID}, Final: AppFinalCheck(a, f)}
	st, ran, err := r.Run(context.Background(), planBytes, f, base)
	if err != nil {
		t.Fatal(err)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS || strings.Join(ran, ",") != "feat.left,feat.right,feat.join" {
		t.Fatalf("outcome %v (%s), ran %v: %+v", st.Outcome, st.Reason, ran, st.Slices)
	}
	join := st.Slices[2]
	for _, d := range []int{0, 1} {
		if !a.Repo.IsAncestor(*st.Slices[d].Candidate, *join.Base) {
			t.Fatalf("join ran without %s", st.Slices[d].SliceID)
		}
	}
	for _, file := range []string{"src/left.py", "src/right.py", "src/join.py"} {
		if _, err := a.Repo.Show(*st.Merged, file); err != nil {
			t.Fatalf("the merged commit lacks %s", file)
		}
	}
	// final.json is the receipt of the merged tree (a slice's run leaves its own Stage 4 receipt in .a2dlc/verdict.json,
	// as a2dlc run always does; the plan's is final.json)
	var final contract.Verdict
	if b, err := os.ReadFile(filepath.Join(StateDir(a.Repo.Dir, "feat"), "final.json")); err != nil {
		t.Fatalf("no final receipt: %v", err)
	} else if err := json.Unmarshal(b, &final); err != nil || final.Verdict != contract.PASS || final.BaseRef != base || len(final.Targets) != 3 {
		t.Fatalf("final receipt %v: verdict %s base %s, %d targets", err, final.Verdict, final.BaseRef, len(final.Targets))
	}
	runs := eng.runs
	st2, ran2, err := r.Run(context.Background(), planBytes, f, base)
	if err != nil || len(ran2) != 0 || *st2.Outcome != contract.PASS || b.launches["feat.left"] != 1 {
		t.Fatalf("the second invocation ran %v (%v), outcome %v, launches %v", ran2, err, st2.Outcome, b.launches)
	}
	if eng.runs <= runs {
		t.Fatal("the second invocation did not check the merged tree again")
	}
	// left's run is tampered with: verify-run fails, so left runs again, and join, on the new base, starts a new run and
	// moves its old one to superseded/ (P4)
	cand := filepath.Join(a2run.RunDir(a, "feat.left"), "candidate.json")
	raw, _ := os.ReadFile(cand)
	os.WriteFile(cand, append(raw, ' '), 0o644)
	oldJoin := *st2.Slices[2].Base
	st3, ran3, err := r.Run(context.Background(), planBytes, f, base)
	if err != nil || strings.Join(ran3, ",") != "feat.left,feat.join" || *st3.Outcome != contract.PASS {
		t.Fatalf("after tampering: ran %v (%v), outcome %v (%s)", ran3, err, st3.Outcome, st3.Reason)
	}
	if *st3.Slices[2].Base == oldJoin {
		t.Fatal("join ran again on the same base")
	}
	if m, _ := filepath.Glob(filepath.Join(StateDir(a.Repo.Dir, "feat"), "superseded", "feat.join-*")); len(m) != 1 {
		t.Fatalf("the old run of join was not moved aside: %v", m)
	}
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	if ls := gitRun(t, a.Repo.Dir, nil, "ls-tree", "-r", "--name-only", tree); strings.Contains(ls, "_plan") {
		t.Fatalf("the plan's files entered the tree_hash:\n%s", ls)
	}
}

// A Builder that steps outside its slice's owns escalates that slice (CB-04 via allowed_paths); its dependent is blocked
// and its sibling still passes. Fixing the builder and resuming finishes the plan, and the dependent runs then.
func TestPlanRunEscalatesOutsideOwnsAndResumes(t *testing.T) {
	a, base, _ := e2eRepo(t)
	p, f := threeSlices()
	planBytes, _ := json.Marshal(p)
	stray := true
	b := &builder{launches: map[string]int{}, files: func(slice string) map[string]string {
		m := ownFile(slice)
		if slice == "feat.left" && stray {
			m["src/core.py"] = "def core(x):\n    return x + 1\n"
		}
		return m
	}}
	r := Runner{Repo: a.Repo, Exec: RunExec{App: a, Launcher: b, Feature: p.FeatureID}, Final: AppFinalCheck(a, f)}
	st, _, err := r.Run(context.Background(), planBytes, f, base)
	if err != nil {
		t.Fatal(err)
	}
	got := []string{st.Slices[0].Status, st.Slices[1].Status, st.Slices[2].Status}
	if strings.Join(got, ",") != "escalated,passed,blocked" || !strings.Contains(st.Slices[0].Reason, "allowed_paths") || *st.Outcome != "escalated" {
		t.Fatalf("statuses %v, left %q, outcome %v", got, st.Slices[0].Reason, st.Outcome)
	}
	if b.launches["feat.join"] != 0 {
		t.Fatal("a blocked slice launched a builder")
	}
	// The cause is fixed (here, the builder stops straying); the next invocation resumes the escalated run, whose Stage 3
	// runs again, and the blocked slice runs once its dependency passed.
	stray = false
	st, ran, err := r.Run(context.Background(), planBytes, f, base)
	if err != nil {
		t.Fatal(err)
	}
	if *st.Outcome != contract.PASS || strings.Join(ran, ",") != "feat.left,feat.join" {
		t.Fatalf("resume: outcome %v (%s), ran %v", st.Outcome, st.Reason, ran)
	}
}
