package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// engineRun is a fake engine for a2dlc run: Tier 0 gives a compile finding per line with ROTO (a fixture marker shared
// with the other run tests); the differential gives PASS on each changed file and notes which candidate it saw.
type engineRun struct {
	tier0   int
	sawCand []string
	// lemmas scripts the lemmas operation (lemmas_test.go); nil means the engine does not do it.
	lemmas     func(budget contract.LemmasBudget, f string) contract.LemmasResponse
	lemmaCalls int
}

func (f *engineRun) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.Response, error) {
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
	for _, file := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candidateDir, file))
		f.sawCand = append(f.sawCand, string(b))
		r.Targets = append(r.Targets, contract.TargetResult{Target: file + "::f", Engine: "crosshair-diffbehavior", EngineVersion: "fake",
			Verdict: contract.PASS, SearchBudget: json.RawMessage(`{"iterations":1,"per_condition_timeout_s":10}`), Generators: "typed_default",
			Equivalence: "strict"})
	}
	return r, nil
}

func (f *engineRun) Stub(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.StubResponse, map[string][]byte, error) {
	return contract.StubResponse{SchemaVersion: 1, Stubbed: []contract.StubFile{}, Unstubbed: []string{}}, nil, nil
}

func (f *engineRun) Signatures(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.SignaturesResponse, error) {
	return contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}, nil
}

func (f *engineRun) Mutate(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.MutateResponse, map[string][]byte, error) {
	return contract.MutateResponse{}, nil, fmt.Errorf("mutate operation not supported")
}

func (f *engineRun) Tier0(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.Tier0Response, error) {
	f.tier0++
	r := contract.Tier0Response{SchemaVersion: 1, Findings: []contract.Tier0Finding{}}
	for _, file := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candidateDir, file))
		for i, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, "ROTO") {
				n := i + 1
				r.Findings = append(r.Findings, contract.Tier0Finding{Kind: contract.Tier0Compile, File: file, Line: &n, Message: "invalid syntax"})
			}
		}
	}
	r.OK = len(r.Findings) == 0
	return r, engine.ValidateTier0(req, r)
}

const baseFile = "def f(x):\n    return x\n"

// repoBuilder: a Python engine over src/, the working tree clean at the base and builder_primary in roles.json.
func repoBuilder(t *testing.T) (app.App, string, *engineRun) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	write(t, dir, "src/f.py", baseFile)
	write(t, dir, ".a2dlc/approvals.json", `{"schema_version":1,"approvals":[]}`) // it is committed; it is in gitx.Excluded
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0}}}`)
	m := &engineRun{}
	a.Runner = m
	return a, dir, m
}

// fakeBuilder writes in /work what content says according to the request and answers ok; it notes each payload.
func fakeBuilder(t *testing.T, requests *[]contract.BuilderRequest, content func(contract.BuilderRequest) map[string]string) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		var req contract.RoleRequest
		b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
		if err := contract.DecodeStrictBytes(b, &req); err != nil || req.Workdir == nil || *req.Workdir != "/work" || s.WorkDir == "" {
			t.Fatalf("%v %+v %q", err, req.Workdir, s.WorkDir)
		}
		var pl contract.BuilderRequest
		if err := contract.DecodeStrictBytes(req.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		*requests = append(*requests, pl)
		var touched []string
		for rel, c := range content(pl) {
			p := filepath.Join(s.WorkDir, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(c), 0o644)
			touched = append(touched, rel)
		}
		out, _ := json.Marshal(contract.BuilderResponse{Summary: "done", FilesChanged: touched})
		return &contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
			Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.1}, Payload: out}, 0, false
	}
}

// sec-dependency-allowlist: a dependency that the builder adds or changes and the base's .a2dlc/deps.allow.json does not
// allow escalates the stage before Tier 0 (no engine run, no waiver); an allowed one does not.

// depsBase commits go.mod and the allowlist (each when not empty) on top of the repoBuilder base, so the builder's /work
// starts from them.
func depsBase(t *testing.T, goMod, allow string) (app.App, string, *engineRun) {
	t.Helper()
	a, dir, engine := repoBuilder(t)
	var add []string
	if goMod != "" {
		write(t, dir, "go.mod", goMod)
		add = append(add, "go.mod")
	}
	if allow != "" {
		write(t, dir, ".a2dlc/deps.allow.json", allow)
		add = append(add, ".a2dlc/deps.allow.json")
	}
	git(t, dir, append([]string{"add"}, add...)...)
	git(t, dir, "commit", "-qm", "deps base")
	return a, dir, engine
}

func TestStage3NewDependencyEscalatesBeforeTier0(t *testing.T) {
	a, dir, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"go.mod": "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n", "src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "deps", contract.ModeLite, "f doubles"
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "deps", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "dependency_not_allowed: ") || !strings.Contains(s3.Reason, "go:example.com/new") ||
		s3.Artifact == nil || s3.Artifact.Path != ArtEscalations || st.Stages[6].Status != contract.StagePending {
		t.Fatalf("%+v %+v", s3, st.Stages[6])
	}
	if engine.tier0 != 0 || len(launcherFake.seen) != 1 {
		t.Fatalf("tier0 ran %d times, the builder %d: the dependency had to escalate before Tier 0", engine.tier0, len(launcherFake.seen))
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/deps", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 {
		t.Fatalf("%+v", escalations.Items)
	}
	e := escalations.Items[0]
	if e.Kind != "dependency_not_allowed" || e.Target != "go:example.com/new" || e.Builder != contract.RoleBuilderPrimary ||
		e.ID != "esc-"+expectedID("dependency_not_allowed", e.Target) || !strings.Contains(e.Evidence.Message, ".a2dlc/deps.allow.json") {
		t.Fatalf("%+v", e)
	}
}

func TestStage3AllowedDependencyRunsTier0(t *testing.T) {
	a, _, engine := depsBase(t, "module x\n\ngo 1.23\n", `{"schema_version":1,"allow":[{"ecosystem":"go","name":"example.com/new","versions":"v1.0.0"}]}`)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"go.mod": "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n", "src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "deps", contract.ModeLite, "f doubles"
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "deps", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; strings.Contains(s3.Reason, "dependency_not_allowed") || engine.tier0 != 1 {
		t.Fatalf("%+v: tier0 ran %d times", s3, engine.tier0)
	}
}

func TestStage3ChangedVersionOutsideAllowlistEscalates(t *testing.T) {
	// the base has example.com/new at v1.0.0 and the allowlist names that version; the builder bumps it to v1.1.0
	a, dir, engine := depsBase(t, "module x\n\ngo 1.23\n\nrequire example.com/new v1.0.0\n", `{"schema_version":1,"allow":[{"ecosystem":"go","name":"example.com/new","versions":"v1.0.0"}]}`)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"go.mod": "module x\n\ngo 1.23\n\nrequire example.com/new v1.1.0\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "deps", contract.ModeLite, "f doubles"
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "deps", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.Status != contract.StageEscalated || !strings.Contains(s3.Reason, "go:example.com/new") || engine.tier0 != 0 {
		t.Fatalf("%+v: tier0 ran %d times", s3, engine.tier0)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/deps", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "dependency_not_allowed" {
		t.Fatalf("%+v", escalations.Items)
	}
}

func TestStage3BuilderConvergesAndStage4JudgesItsCommit(t *testing.T) {
	a, dir, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback == nil {
			return map[string]string{"src/f.py": "def f(x):\n    return x +\nROTO\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "b.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3, s4 := st.Stages[5], st.Stages[6]
	// the builder does not write an @a2dlc header: it is a note, not a failure (header.go)
	if s3.ID != "3" || s3.Status != contract.StageDone || s3.Iterations != 2 || s3.Artifact == nil || s3.Artifact.Path != ArtCandidate ||
		s3.Reason != "@a2dlc header missing in src/f.py (the engine's Tier 0 lint decides whether it is an error)" {
		t.Fatalf("%+v", s3)
	}
	// the second iteration brings the first one's feedback, by payload and by /in
	if len(requests) != 2 || requests[0].Round != 1 || requests[0].MaxRounds != MaxInnerDefault || requests[0].Tier0Feedback != nil ||
		requests[1].Tier0Feedback == nil || requests[1].Tier0Feedback.OK || requests[1].Tier0Feedback.Findings[0].Kind != contract.Tier0Compile ||
		*requests[1].Tier0Feedback.Findings[0].Line != 3 || string(requests[1].Intent) != "null" || string(requests[1].Contract) != "null" || requests[1].Probes != nil {
		t.Fatalf("%+v", requests)
	}
	if len(launcherFake.seen) != 2 || strings.Join(launcherFake.seen[0], ",") != "request.json" || strings.Join(launcherFake.seen[1], ",") != "request.json,tier0_feedback.json" {
		t.Fatalf("the builder saw %v", launcherFake.seen)
	}
	run := filepath.Join(dir, ".a2dlc/run/b.v1")
	var c contract.Candidate
	b, _ := os.ReadFile(filepath.Join(run, ArtCandidate))
	if err := contract.DecodeStrictBytes(b, &c); err != nil || c.Iterations != 2 || c.Builder != contract.RoleBuilderPrimary || len(c.Unchecked) != 0 {
		t.Fatalf("%v %+v", err, c)
	}
	parent := git(t, dir, "rev-parse", c.Commit+"^")
	hash, _ := a.Repo.CommitTree(c.Commit)
	// the commit keeps the base's signatures (the builder did not touch .a2dlc/); the tree_hash is without them, as in check
	if parent != st.BaseSha || hash != c.TreeHash || git(t, dir, "show", c.Commit+":.a2dlc/approvals.json") == "" ||
		git(t, dir, "rev-parse", "refs/a2dlc/run/b.v1/builder_primary") != c.Commit || git(t, dir, "show", c.Commit+":src/f.py") != "def f(x):\n    return x * 2" {
		t.Fatalf("commit %s badly built", c.Commit)
	}
	// Stage 4 judged the builder's commit, not the working tree (which is still the base)
	var v contract.Verdict
	b, _ = os.ReadFile(filepath.Join(run, ArtVerdict))
	if err := contract.DecodeStrictBytes(b, &v); err != nil || v.TreeHash != "git-tree:"+c.TreeHash || len(v.Targets) != 1 || v.Targets[0].Target != "src/f.py::f" {
		t.Fatalf("%v %+v", err, v)
	}
	if s4.Status != contract.StageDone || st.Outcome == nil || *st.Outcome != v.Verdict || len(engine.sawCand) != 1 || !strings.Contains(engine.sawCand[0], "x * 2") {
		t.Fatalf("%+v %v %v", s4, st.Outcome, engine.sawCand)
	}
	if w, _ := os.ReadFile(filepath.Join(dir, "src/f.py")); string(w) != baseFile {
		t.Fatalf("the builder touched the working tree: %q", w)
	}
	if engine.tier0 != 2 {
		t.Fatalf("tier0 ran %d times", engine.tier0)
	}
	var lg contract.RunLedger
	b, _ = os.ReadFile(filepath.Join(run, "ledger.json"))
	if err := contract.DecodeStrictBytes(b, &lg); err != nil || len(lg.Entries) != 2 || lg.Entries[0].Stage != "3" || lg.Entries[1].Stage != "3" ||
		lg.Entries[1].Role != contract.RoleBuilderPrimary || len(lg.Entries[1].Inputs) != 2 || st.Budget.SpentUSD != 0.2 {
		t.Fatalf("%v %+v %+v", err, lg.Entries, st.Budget)
	}
	if problems, err := VerifyRun(a, "b.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	// resuming does not relaunch the builder: candidate.json stays intact
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "b.v1"}); err != nil || len(requests) != 2 {
		t.Fatalf("%v %d", err, len(requests))
	}
}

func TestStage3ExhaustsRoundsAndEscalates(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	broken := func(contract.BuilderRequest) map[string]string { return map[string]string{"src/f.py": "ROTO\n"} }
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "agota", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || s3.Iterations != 2 || !strings.Contains(s3.Reason, "Tier 0 does not converge") || s3.Artifact == nil ||
		s3.Artifact.Path != ArtTier0 || st.Stages[6].Status != contract.StagePending || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %+v %v", s3, st.Stages[6], st.Outcome)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/agota", ArtCandidate)); err == nil {
		t.Fatal("candidate.json without Tier 0 ok")
	}
	// without the flag, intent.budget.max_inner_iterations governs, and the builder sees the intent
	a, _, _ = repoBuilder(t)
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal, in.Budget.MaxInnerIterations = "attempt", contract.ModeLite, "f doubles", 3
	})
	requests = nil
	launcherFake = &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "attempt", Intent: path}); err != nil || st.Stages[5].Iterations != 3 || requests[0].MaxRounds != 3 ||
		!strings.Contains(string(requests[0].Intent), "f doubles") || strings.Join(launcherFake.seen[0], ",") != "intent.json,request.json" {
		t.Fatalf("%v %+v %+v %v", err, st.Stages[5], requests, launcherFake.seen)
	}
}

func TestStage3RejectsWhatIsNotTheBuilders(t *testing.T) {
	// touching .a2dlc/ in /work (a waiver for its own change) escalates without running Tier 0
	a, _, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return 2\n", ".a2dlc/waivers.json": `{"schema_version":1,"waivers":[]}`}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "gate"})
	if err != nil || st.Stages[5].Status != contract.StageEscalated || !strings.Contains(st.Stages[5].Reason, "CB-04") || engine.tier0 != 0 {
		t.Fatalf("%v %+v tier0=%d", err, st.Stages[5], engine.tier0)
	}
	// a builder that leaves no changes does not have a candidate either
	a, _, _ = repoBuilder(t)
	launcherFake = &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string { return nil })}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "nothing"}); err != nil || !strings.Contains(st.Stages[5].Reason, "left no changes") {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
	// a refusal escalates with its reason; an invalid response is a tool failure (INCONCLUSIVE)
	a, _, _ = repoBuilder(t)
	launcherFake = &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
			Status: contract.RoleRefused, Reason: "no S* contract", Payload: json.RawMessage(`{}`)}, 0, false
	}}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "refuses"}); err != nil || st.Stages[5].Status != contract.StageEscalated || !strings.Contains(st.Stages[5].Reason, "no S* contract") {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
	a, _, _ = repoBuilder(t)
	launcherFake = &fakeLauncher{script: func(LaunchSpec) (*contract.RoleResponse, int, bool) { return nil, 1, false }}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "fails"}); err != nil || st.Stages[5].Status != contract.StageFailed || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
}

// engineDown is an engine whose Tier 0 fails (it does not answer, it writes no response.json): a tool error.
type engineDown struct{ engineRun }

func (f *engineDown) Tier0(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Tier0Response, error) {
	f.tier0++
	return contract.Tier0Response{}, errors.New("docker")
}

// A downed engine is not feedback: the stage fails (INCONCLUSIVE) after the first iteration, without spending another builder round.
func TestStage3DownEngineIsInconclusive(t *testing.T) {
	a, _, _ := repoBuilder(t)
	m := &engineDown{}
	a.Runner = m
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "down", MaxInner: 3})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageFailed || !strings.Contains(s3.Reason, "Tier 0 could not run") || !strings.Contains(s3.Reason, "docker") ||
		st.Stages[6].Status != contract.StagePending || st.Outcome == nil || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%+v %+v %v", s3, st.Stages[6], st.Outcome)
	}
	if len(requests) != 1 || len(launcherFake.seen) != 1 || m.tier0 != 1 {
		t.Fatalf("the builder ran %d times and Tier 0 %d (both had to be 1)", len(requests), m.tier0)
	}
}

// engineProbes gives a probe finding for each executable probe of the request while the candidate does not have 'x * 2'.
type engineProbes struct {
	engineRun
	probes int
}

func (f *engineProbes) Tier0(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.Tier0Response, error) {
	f.tier0++
	var b contract.Tier0Budget
	if err := json.Unmarshal(req.Budget, &b); err != nil {
		return contract.Tier0Response{}, err
	}
	f.probes += len(b.Probes)
	src, _ := os.ReadFile(filepath.Join(candidateDir, "src/f.py"))
	r := contract.Tier0Response{SchemaVersion: 1, Findings: []contract.Tier0Finding{}}
	for _, p := range b.Probes {
		if !strings.Contains(string(src), "x * 2") {
			r.Findings = append(r.Findings, contract.Tier0Finding{Kind: contract.Tier0Probe, File: *p.File, Message: p.ID + ": " + *p.Expr + " is false"})
		}
	}
	r.OK = len(r.Findings) == 0
	return r, engine.ValidateTier0(req, r)
}

// The builder sees S* and the probes in /in and in the payload, Tier 0 receives the probes, and a probe that fails comes back as feedback.
func TestStage3BuilderSeesContractAndProbes(t *testing.T) {
	file, expr := "src/f.py", "f(2) == 4"
	probe := contract.Probe{ID: "BP-01", Description: "doubles", Input: "f(2)", Expected: "4", Kind: "example", File: &file, Expr: &expr}
	withStage(t, "1", func(_ context.Context, m *Machine) (string, string, string, error) {
		c := contractOf(contract.SourceConsensus, "doubles")
		c.Probes = []contract.Probe{probe}
		if _, err := m.writeArtifact(ArtContract, c); err != nil {
			return "", "", "", err
		}
		h, err := HashFile(filepath.Join(m.dir, ArtContract))
		if err != nil {
			return "", "", "", err
		}
		artifact, err := m.writeArtifact(ArtProbes, contract.Probes{SchemaVersion: 1, SliceID: c.SliceID, ContractHash: h, Probes: []contract.Probe{probe}})
		return contract.StageDone, "", artifact, err
	})
	withStage(t, "1.5", func(context.Context, *Machine) (string, string, string, error) {
		return contract.StageDone, "", "", nil
	})
	a, _, _ := repoBuilder(t)
	m := &engineProbes{}
	a.Runner = m
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback == nil {
			return map[string]string{"src/f.py": "def f(x):\n    return x + 1\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "calc.media.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.Status != contract.StageDone || s3.Iterations != 2 || !strings.Contains(s3.Reason, "@a2dlc header missing") {
		t.Fatalf("%+v", s3)
	}
	// S* and the probes reach the builder in both rounds, by /in and by payload
	if len(requests) != 2 || len(launcherFake.seen) != 2 {
		t.Fatalf("%d requests, %d launches", len(requests), len(launcherFake.seen))
	}
	for i, p := range requests {
		var c contract.SliceContract
		if err := contract.DecodeStrictBytes(p.Contract, &c); err != nil || c.SliceID != "calc.media.v1" || len(p.Probes) != 1 || p.Probes[0].ID != "BP-01" {
			t.Fatalf("round %d: %v %s %+v", i+1, err, p.Contract, p.Probes)
		}
	}
	if strings.Join(launcherFake.seen[0], ",") != "contract.json,probes.json,request.json" ||
		strings.Join(launcherFake.seen[1], ",") != "contract.json,probes.json,request.json,tier0_feedback.json" {
		t.Fatalf("the builder saw %v", launcherFake.seen)
	}
	// Tier 0 ran the probes in both rounds, and the one that failed came back as feedback in the second
	if m.tier0 != 2 || m.probes != 2 {
		t.Fatalf("tier0 ran %d times with %d probes", m.tier0, m.probes)
	}
	fb := requests[1].Tier0Feedback
	if fb == nil || fb.OK || len(fb.Findings) != 1 || fb.Findings[0].Kind != contract.Tier0Probe || fb.Findings[0].File != "src/f.py" ||
		!strings.Contains(fb.Findings[0].Message, "BP-01") {
		t.Fatalf("%+v", fb)
	}
}

// Without builder_primary the candidate is the working tree, and the S* probes still run on it: a red probe escalates
// (nobody fixes it) and the run stops before Stage 4; with the tree fixed the stage ends done and Stage 4 runs.
func TestStage3WithoutBuilderRunsContractProbes(t *testing.T) {
	file, expr := "src/f.py", "f(2) == 4"
	probe := contract.Probe{ID: "BP-01", Description: "doubles", Input: "f(2)", Expected: "4", Kind: "example", File: &file, Expr: &expr}
	withStage(t, "1", func(_ context.Context, m *Machine) (string, string, string, error) {
		c := contractOf(contract.SourceConsensus, "doubles")
		c.Probes = []contract.Probe{probe}
		if _, err := m.writeArtifact(ArtContract, c); err != nil {
			return "", "", "", err
		}
		h, err := HashFile(filepath.Join(m.dir, ArtContract))
		if err != nil {
			return "", "", "", err
		}
		artifact, err := m.writeArtifact(ArtProbes, contract.Probes{SchemaVersion: 1, SliceID: c.SliceID, ContractHash: h, Probes: []contract.Probe{probe}})
		return contract.StageDone, "", artifact, err
	})
	withStage(t, "1.5", func(context.Context, *Machine) (string, string, string, error) {
		return contract.StageDone, "", "", nil
	})
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	write(t, dir, "src/f.py", baseFile)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "engine")
	write(t, dir, "src/f.py", "def f(x):\n    return x + 1\n") // the candidate breaks the probe
	m := &engineProbes{}
	a.Runner = m
	launcherFake := &fakeLauncher{script: func(LaunchSpec) (*contract.RoleResponse, int, bool) {
		t.Fatal("no role was supposed to run")
		return nil, 0, false
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "calc.media.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.Contains(s3.Reason, "no builder_primary role") || !strings.Contains(s3.Reason, "BP-01") ||
		s3.Artifact == nil || s3.Artifact.Path != ArtTier0 || st.Stages[6].Status != contract.StagePending || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %+v %v", s3, st.Stages[6], st.Outcome)
	}
	if m.tier0 != 1 || m.probes != 1 {
		t.Fatalf("tier0 ran %d times with %d probes", m.tier0, m.probes)
	}
	var res contract.Tier0Response
	if b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/calc.media.v1", ArtTier0)); err != nil || json.Unmarshal(b, &res) != nil || res.OK || len(res.Findings) != 1 || res.Findings[0].Kind != contract.Tier0Probe {
		t.Fatalf("%v %s", err, b)
	}
	// with the fixed tree, another run (other slice) passes the probe and goes on to Stage 4
	write(t, dir, "src/f.py", "def f(x):\n    return x * 2\n")
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "calc.media.v2"})
	if err != nil {
		t.Fatal(err)
	}
	s3 = st.Stages[5]
	if s3.Status != contract.StageDone || !strings.Contains(s3.Reason, "Tier 0 ran the S* probes") || s3.Artifact == nil || s3.Artifact.Path != ArtTier0 ||
		st.Stages[6].Status == contract.StagePending || m.tier0 != 2 {
		t.Fatalf("%+v %+v tier0=%d", s3, st.Stages[6], m.tier0)
	}
}

// engineWithoutTier0 is an engine that does no tier0 (like the TS one today). With it, Tier 0 runs nothing on the candidate.
type engineWithoutTier0 struct{ engineRun }

func (f *engineWithoutTier0) Tier0(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Tier0Response, error) {
	return contract.Tier0Response{}, engine.ErrWithoutTier0
}

// A Tier 0 that ran nothing (no engine reviewed a file or a probe) leaves no candidate: what nobody executed is not
// clean, and the stage escalates instead of giving done with everything unchecked (§5 Stage 3; audit of 2026-10-17).
func TestStage3Tier0RunningNothingEscalates(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	a.Runner = &engineWithoutTier0{}
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "b.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.ID != "3" || s3.Status != contract.StageEscalated || !strings.Contains(s3.Reason, "Tier 0 ran nothing: src/f.py") ||
		!strings.Contains(s3.Reason, "nobody reviewed the builder's candidate") || len(requests) != 1 {
		t.Fatalf("%+v (%d requests)", s3, len(requests))
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/b.v1", ArtCandidate)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("candidate.json was not supposed to exist: %v", err)
	}
}

// With an engine that did review, candidate.json says tier0_reviewed: true.
func TestStage3CandidateSaysTier0Reviewed(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "b.v1"}); err != nil {
		t.Fatal(err)
	}
	var c contract.Candidate
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/b.v1", ArtCandidate))
	if err := contract.DecodeStrictBytes(b, &c); err != nil || c.Tier0Reviewed == nil || !*c.Tier0Reviewed {
		t.Fatalf("%v %+v", err, c)
	}
}
