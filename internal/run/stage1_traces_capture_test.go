package run

import (
	"context"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// tracesFake is a runner that answers capture_traces with a fixed response (the rest of the runner is never called).
type tracesFake struct {
	engine.Runner
	resp  contract.TracesResponse
	calls int
}

func (f *tracesFake) Traces(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.TracesResponse, error) {
	f.calls++
	if req.Language != "python" || len(req.ChangedFiles) != 1 || req.ChangedFiles[0] != "src/f.py" {
		return contract.TracesResponse{}, nil
	}
	return f.resp, nil
}

// The refactor capture asks the engine for traces: with an engine that answers, branch_coverage is written; with none, or
// with sources that no single engine covers, the capture says why and carries no branch_coverage (the gate then stops).
func TestCaptureTracesWritesTheAnswerOrSaysWhy(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],`+
		`"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "config")
	base, err := a.Repo.ResolveCommit("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, "src/f.py", "def f(x):\n    return x + 3\n")
	cfg := contract.Config{Engines: []contract.EngineConfig{{Language: "python", Image: "x", Paths: []string{"src/**"}}},
		Tests: &contract.TestsConfig{Image: "x", Paths: []string{"tests/**"}, SourcePaths: []string{"src/**"}, Command: "run"}}
	changed := contract.Capture{ChangedFiles: []string{"src/f.py"}, Signatures: []contract.SignatureFile{{File: "src/f.py"}}}

	none := &Machine{app: a, baseSha: base}
	c := changed
	why, err := none.captureTraces(context.Background(), cfg, &c)
	if err != nil || why == "" || c.BranchCoverage != nil {
		t.Fatalf("no engine answers: why %q, err %v, branch_coverage %v", why, err, c.BranchCoverage)
	}

	zero, bc := 0, 0.75
	fake := &tracesFake{resp: contract.TracesResponse{TestExit: &zero, Traces: []contract.CaptureTrace{}, BranchCoverage: &bc}}
	a.Runner = fake
	m := &Machine{app: a, baseSha: base}
	c = changed
	why, err = m.captureTraces(context.Background(), cfg, &c)
	if err != nil || why != "" || c.BranchCoverage == nil || *c.BranchCoverage != 0.75 || fake.calls != 1 {
		t.Fatalf("answer written: why %q, err %v, branch_coverage %v, calls %d", why, err, c.BranchCoverage, fake.calls)
	}
}
