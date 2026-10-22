package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Golden master (decision 14; protocol §5 Stage −1 step 1). In refactor and legacy slices the black-box engine records
// its I/O over the committed cases of config.tests.golden_master on the base, before any role runs (golden.json). The
// record is taken from the base and is the oracle: the candidate cannot write it. The record side is wired (RecordGolden,
// called by Stage −1). The replay runs in CheckWith when CheckOptions.Golden carries the record (goldenResults); the run
// passes the recorded golden.json in every Stage 4 candidate check (internal/run: judgeLoop and recordedGolden in
// saboteur.go), so a recorded golden master is replayed in a real run.

// ErrGoldenNotConfigured is returned by RecordGolden when config.tests.golden_master is not declared.
var ErrGoldenNotConfigured = errors.New("no tests.golden_master in config.json")

// GateGolden is the name of the golden master gate in the verdict's test results.
const GateGolden = "golden_replay"

// RecordGolden runs golden_record on the base over the committed cases that match golden_master.cases. The case id is
// the case file's path relative to the repo, so the replay names the same cases.
func (a App) RecordGolden(ctx context.Context, base string) (contract.Golden, error) {
	var out contract.Golden
	cfg, err := a.config()
	if err != nil {
		return out, err
	}
	if cfg.Tests == nil || cfg.Tests.GoldenMaster == nil {
		return out, ErrGoldenNotConfigured
	}
	gm := cfg.Tests.GoldenMaster
	gr, ok := a.Runner.(engine.GoldenRunner)
	if !ok {
		return out, fmt.Errorf("this binary has no golden master runner")
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return out, fmt.Errorf("base %q: %w", base, err)
	}
	cases, err := a.goldenCases(baseSha, gm.Cases)
	if err != nil {
		return out, err
	}
	if len(cases) == 0 {
		return out, fmt.Errorf("no committed case on the base matches golden_master.cases %q", gm.Cases)
	}
	dir, err := os.MkdirTemp("", "a2dlc-golden-*")
	if err != nil {
		return out, err
	}
	defer removeTree(dir)
	if err := a.Repo.Export(baseSha, dir); err != nil {
		return out, err
	}
	req, err := goldenRequest("golden_record", baseSha, cases)
	if err != nil {
		return out, err
	}
	resp, err := gr.Golden(ctx, goldenEngine(gm.Image), req, dir, dir, nil)
	if err != nil {
		return out, err
	}
	if len(resp.Cases) == 0 {
		return out, fmt.Errorf("the golden record has no case (%d matched on the base)", len(cases))
	}
	return contract.Golden{SchemaVersion: contract.SchemaVersion, Base: baseSha, Image: gm.Image, Command: gm.Command,
		Cases: resp.Cases, RecordedAt: time.Now().UTC().Format(time.RFC3339)}, nil
}

// gateGolden replays the recorded golden master on the candidate. Each case whose output differs is a FAIL target
// "golden::<case id>", which a waiver covers like any FAIL. A recorded case the candidate did not replay is a FAIL too:
// nothing that was recorded may go unchecked. An engine that answers with an error did not replay anything: that is a
// gate error, never an empty list of differences.
func (a App) gateGolden(ctx context.Context, in gateInput, golden contract.Golden) (contract.Gate, []contract.TestResult) {
	gr, ok := a.Runner.(engine.GoldenRunner)
	if !ok {
		return gateError(GateGolden, fmt.Errorf("this binary has no golden master runner")), nil
	}
	ids := make([]string, 0, len(golden.Cases))
	for _, c := range golden.Cases {
		ids = append(ids, c.ID)
	}
	sort.Strings(ids)
	baseDir, err := os.MkdirTemp("", "a2dlc-golden-base-*")
	if err != nil {
		return gateError(GateGolden, err), nil
	}
	defer removeTree(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-golden-cand-*")
	if err != nil {
		return gateError(GateGolden, err), nil
	}
	defer removeTree(candDir)
	if err := a.Repo.Export(in.baseSha, baseDir); err != nil {
		return gateError(GateGolden, err), nil
	}
	if err := a.Repo.Export(in.tree, candDir); err != nil {
		return gateError(GateGolden, err), nil
	}
	recorded, err := json.Marshal(golden)
	if err != nil {
		return gateError(GateGolden, err), nil
	}
	req, err := goldenRequest("golden_replay", in.baseSha, ids)
	if err != nil {
		return gateError(GateGolden, err), nil
	}
	resp, err := gr.Golden(ctx, goldenEngine(golden.Image), req, baseDir, candDir, recorded)
	if err != nil {
		return gateError(GateGolden, err), nil
	}
	if resp.Error != nil {
		return gateError(GateGolden, errors.New(*resp.Error)), nil
	}
	replayed := map[string]bool{}
	for _, c := range resp.Cases {
		replayed[c.ID] = true
	}
	var out []contract.TestResult
	for _, d := range resp.Diffs {
		out = append(out, contract.TestResult{Target: "golden::" + d.ID, Gate: GateGolden, Verdict: contract.FAIL,
			Reason:  "the output of this case differs from the golden master recorded on the base",
			Message: fmt.Sprintf("recorded %s, replayed %s", d.Old, d.New)})
	}
	for _, id := range ids {
		if !replayed[id] {
			out = append(out, contract.TestResult{Target: "golden::" + id, Gate: GateGolden, Verdict: contract.FAIL,
				Reason: "the recorded case was not replayed on the candidate"})
		}
	}
	return contract.Gate{Status: contract.GateRan}, out
}

// goldenResults is the golden replay of a check as test results. A gate that could not run is an INCONCLUSIVE result
// with its reason, so the golden master is never silently unchecked.
func (a App) goldenResults(ctx context.Context, in gateInput, golden contract.Golden) []contract.TestResult {
	gate, res := a.gateGolden(ctx, in, golden)
	if gate.Status == contract.GateError {
		res = append(res, contract.TestResult{Target: "golden_replay", Gate: GateGolden, Verdict: contract.INCONCLUSIVE, Reason: gate.Reason})
	}
	return res
}

// goldenCases lists the files of base that match the glob of golden_master.cases, sorted.
func (a App) goldenCases(baseSha, glob string) ([]string, error) {
	files, err := a.Repo.Files(baseSha)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, f := range files {
		if verdict.Match(glob, f) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return out, nil
}

// goldenRequest is the engine request of a golden operation: the cases travel in budget.cases (engine-request schema).
func goldenRequest(op, baseSha string, cases []string) (contract.Request, error) {
	budget, err := json.Marshal(struct {
		Cases []string `json:"cases"`
	}{cases})
	if err != nil {
		return contract.Request{}, err
	}
	return contract.Request{SchemaVersion: contract.SchemaVersion, Operation: op, BaseRef: baseSha, BaseDir: "/base",
		CandidateDir: "/cand", Budget: budget}, nil
}

// goldenEngine is the engine that records and replays the golden master: the image of golden_master.image. The core
// names no language here: the engine is whatever image the config declares.
func goldenEngine(image string) contract.EngineConfig {
	return contract.EngineConfig{Image: image}
}

// removeTree removes a temporary tree and reports a failed removal instead of dropping it.
func removeTree(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "a2dlc: cannot remove the temporary tree %s: %v\n", dir, err)
	}
}
