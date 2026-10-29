package factory

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/run"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeEngine is the engine of a lane's Tier 0 and oracle: Tier 0 finds a compile error per line with ROTO, the
// differential passes every changed file.
type fakeEngine struct{}

func (fakeEngine) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.Response, error) {
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
	for _, file := range req.ChangedFiles {
		r.Targets = append(r.Targets, contract.TargetResult{Target: file + "::f", Engine: "crosshair-diffbehavior", EngineVersion: "fake",
			Verdict: contract.PASS, SearchBudget: json.RawMessage(`{"iterations":1,"per_condition_timeout_s":10}`), Generators: "typed_default",
			Equivalence: "strict"})
	}
	return r, nil
}

func (fakeEngine) Stub(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.StubResponse, map[string][]byte, error) {
	return contract.StubResponse{SchemaVersion: 1, Stubbed: []contract.StubFile{}, Unstubbed: []string{}}, nil, nil
}

func (fakeEngine) Signatures(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.SignaturesResponse, error) {
	return contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}, nil
}

func (fakeEngine) Mutate(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.MutateResponse, map[string][]byte, error) {
	return contract.MutateResponse{}, nil, fmt.Errorf("mutate operation not supported")
}

func (fakeEngine) Complexity(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.ComplexityResponse, error) {
	return contract.ComplexityResponse{}, engine.ErrWithoutComplexity
}

func (fakeEngine) Tier0(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.Tier0Response, error) {
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

// target is a repository with a Python engine over src/ and one commit; a lane's worktree starts from it.
func target(t *testing.T) Deps {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	write(t, dir, ".a2dlc/approvals.json", `{"schema_version":1,"approvals":[]}`)
	write(t, dir, "src/f.py", "def f(x):\n    return x\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); tick = tick.Add(time.Second); return tick }
	return Deps{Repo: repo, Out: t.TempDir(), Now: now, ConfigSHA256: "sha256:" + strings.Repeat("c", 64),
		NewApp: func(r gitx.Repo) app.App { return app.App{Repo: r, Runner: fakeEngine{}, Version: "test", Now: now} }}
}

// call is what one launch looked like to the fake claude.
type call struct {
	Role string
	Env  map[string]string
}

// fakeClaude stands for the role container: it writes a builder response with the cost its script says. rotten makes
// the builder's file fail the fake Tier 0, so the lane keeps asking for another round.
type fakeClaude struct {
	mu     sync.Mutex
	calls  []call
	script func(n int, ctx context.Context) (cost *contract.Cost, rotten bool, err error)
}

func (f *fakeClaude) Launch(ctx context.Context, r contract.Role, s run.LaunchSpec) (run.LaunchResult, error) {
	f.mu.Lock()
	n := len(f.calls)
	f.calls = append(f.calls, call{Role: s.Role, Env: r.Env})
	f.mu.Unlock()
	cost, rotten, err := f.script(n, ctx)
	if err != nil {
		return run.LaunchResult{}, err
	}
	var req contract.RoleRequest
	b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	if err := contract.DecodeStrictBytes(b, &req); err != nil {
		return run.LaunchResult{}, err
	}
	body := fmt.Sprintf("def f(x):\n    return x + %d\n", n+1)
	if rotten {
		body += "ROTO\n"
	}
	p := filepath.Join(s.WorkDir, "src", "f.py")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		return run.LaunchResult{}, err
	}
	out, _ := json.Marshal(contract.BuilderResponse{Summary: "done", FilesChanged: []string{"src/f.py"}})
	resp, _ := json.Marshal(contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
		Status: contract.RoleOK, Cost: cost, Payload: out})
	os.WriteFile(filepath.Join(s.OutDir, "response.json"), resp, 0o644)
	return run.LaunchResult{Argv: []string{"docker", "run", r.Image}, Network: run.NetworkNone}, nil
}

func (f *fakeClaude) seen() []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]call(nil), f.calls...)
}

func testConfig() contract.FactoryConfig {
	return contract.FactoryConfig{SchemaVersion: 1, RolesImage: "a2dlc-roles:test", Argv: []string{"python3", "/a2dlc/adapter.py"},
		Network: "none", Model: "haiku", Tools: []string{"Read", "Edit"}, Concurrency: 1,
		Caps: contract.FactoryCaps{LaneUSD: 1, LaneSeconds: 300, LaneTurns: 40, RunStopNewUSD: 4, RunKillUSD: 6, TotalUSD: 25}}
}

func issue(id string) contract.FactoryIssue {
	return contract.FactoryIssue{ID: id, Title: "Make f add one", Body: "f(x) returns x.", DoneCommand: "pytest tests/test_f.py"}
}

func batchOf(issues ...contract.FactoryIssue) contract.FactoryBatch {
	return contract.FactoryBatch{SchemaVersion: 1, BatchID: "test.1", Issues: issues}
}

func lanesByIssue(r contract.FactoryReport) map[string]contract.FactoryLane {
	m := map[string]contract.FactoryLane{}
	for _, l := range r.Lanes {
		m[l.IssueID] = l
	}
	return m
}
