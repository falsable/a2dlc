package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// applyTraces writes the answer of the trace harness (operation capture_traces) into the refactor capture: the traces,
// and branch_coverage only when the engine measured it. It returns why the Stage 1.5 trace gate cannot pass on this
// capture, or "" when it can: the harness failed, the base tests did not exit 0 (or did not run), or the changed
// functions have no branch coverage. The threshold (config.tests.trace_coverage.min_branch) is the gate's, not this one's.
func applyTraces(c *contract.Capture, r contract.TracesResponse, changedFunctions bool) string {
	c.Traces = r.Traces
	c.BranchCoverage = r.BranchCoverage
	switch {
	case r.Error != nil:
		return "trace harness failed: " + *r.Error
	case r.TestExit == nil || *r.TestExit != 0:
		return "the base tests did not exit 0 under the trace harness"
	case r.BranchCoverage == nil && changedFunctions:
		return "no branch coverage was measured for the changed functions"
	}
	return ""
}

// tracesRunner is what the app's runner is when it can run the base tests under the trace harness (the engine's
// capture_traces operation, engine.Docker.Traces).
type tracesRunner interface {
	Traces(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.TracesResponse, error)
}

// captureTraces runs the base tests under the trace harness of the one engine that covers the changed sources, and writes
// the answer into the refactor capture (applyTraces). When that cannot happen, the capture says why through the harness
// error, carries no branch_coverage, and the Stage 1.5 gate ends INCONCLUSIVE with that record: nothing is assumed.
func (m *Machine) captureTraces(ctx context.Context, cfg contract.Config, c *contract.Capture) (string, error) {
	if len(c.ChangedFiles) == 0 {
		return "", nil
	}
	changedFunctions := len(c.Signatures) > 0
	fail := func(why string) (string, error) {
		return applyTraces(c, contract.TracesResponse{Traces: []contract.CaptureTrace{}, Skipped: []contract.TraceSkip{}, Error: &why}, changedFunctions), nil
	}
	if cfg.Tests == nil {
		return fail("no config.tests: the base tests cannot run under the trace harness")
	}
	tr, ok := m.app.Runner.(tracesRunner)
	if !ok {
		return fail("no engine answers capture_traces")
	}
	byEngine, _ := verdict.Assign(cfg.Engines, c.ChangedFiles)
	if len(byEngine) != 1 {
		return fail(fmt.Sprintf("the changed sources are covered by %d engines; the trace harness runs for one engine", len(byEngine)))
	}
	var idx int
	var files []string
	for i, fs := range byEngine {
		idx, files = i, fs
	}
	e := cfg.Engines[idx]
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return "", err
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-trace-base-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-trace-cand-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(candDir)
	if err := m.app.Repo.Export(m.baseSha, baseDir); err != nil {
		return "", err
	}
	if err := m.app.Repo.Export(tree, candDir); err != nil {
		return "", err
	}
	req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: m.baseSha, BaseDir: "/base",
		CandidateDir: "/cand", ChangedFiles: files, Budget: json.RawMessage("{}")}
	resp, err := tr.Traces(ctx, e, req, baseDir, candDir)
	if err != nil {
		return fail(err.Error())
	}
	return applyTraces(c, resp, changedFunctions), nil
}
