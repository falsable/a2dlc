package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Stage 5 of a2dlc run (block 5, protocol §5 Tier 2): what the state machine needs from the oracle to measure the suite
// with mutants. The core knows no languages: the engine generates the mutants (operation: mutate) and the tests run
// with the repo's command; here only the request is assembled and JUnit is read.

// Mutant is an engine mutant with its full mutated file.
type Mutant struct {
	contract.Mutant
	Content []byte
}

// DiffMutable is the change as seen by Stage 5: the candidate's tree, its sources and changed tests, the mutants of the
// changed lines, what the engine did not generate (it prevents PASS) and what no engine mutates (it is listed, it does
// not stop anything).
type DiffMutable struct {
	Tree         string
	Sources      []string
	Tests        []string
	Mutants      []Mutant
	NotGenerated []contract.FileReason
	NoEngine     []contract.FileReason
	Pruned       []contract.PrunedMutant // equivalents the engine pruned: not mutants, not counted in the kill rate
}

// Mutants asks each engine for mutants of the changed lines of the sources in scope (engines and tests.source_paths),
// with max as the request's limit. candidate is the builder's commit ("" = the working tree), as in
// CheckOptions.CandidateTree. An engine that fails or does not do operation: mutate leaves its files in NoEngine with
// the error: today only the Python one mutates.
func (a App) Mutants(ctx context.Context, base, candidate string, max int) (DiffMutable, error) {
	d := DiffMutable{NotGenerated: []contract.FileReason{}, NoEngine: []contract.FileReason{}, Pruned: []contract.PrunedMutant{}}
	if base == "" {
		base = a.defaultBase()
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return d, fmt.Errorf("base %q: %w", base, err)
	}
	// The config is the base's, as in check: the mutants are the ones of the scope the verdict judges.
	cfg, err := a.configAt(baseSha)
	if err != nil {
		return d, err
	}
	if d.Tree, err = a.candidateTree(candidate); err != nil {
		return d, err
	}
	changed, _, err := a.Repo.Changed(baseSha, d.Tree)
	if err != nil {
		return d, err
	}
	d.Sources = changed
	if cfg.Tests != nil {
		d.Tests, d.Sources = partition(cfg.Tests.Paths, changed)
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, inScope(cfg, d.Sources))
	for _, f := range noEngine {
		d.NoEngine = append(d.NoEngine, contract.FileReason{File: f, Reason: "no engine in config.engines covers the file"})
	}
	if len(byEngine) == 0 {
		return d, nil
	}
	if a.Runner == nil {
		for _, i := range sortedIndexes(byEngine) {
			for _, f := range byEngine[i] {
				d.NoEngine = append(d.NoEngine, contract.FileReason{File: f, Reason: "this binary has no engine runner"})
			}
		}
		return d, nil
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
	if err != nil {
		return d, err
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
	if err != nil {
		return d, err
	}
	defer os.RemoveAll(candDir)
	if err := a.Repo.Export(baseSha, baseDir); err != nil {
		return d, err
	}
	if err := a.Repo.Export(d.Tree, candDir); err != nil {
		return d, err
	}
	remaining := max
	for _, i := range sortedIndexes(byEngine) {
		e := cfg.Engines[i]
		changedLines, files := map[string][]int{}, []string{}
		for _, f := range byEngine[i] {
			lines, err := a.Repo.ChangedLines(baseSha, d.Tree, f)
			if err != nil {
				return d, fmt.Errorf("changed lines of %s: %w", f, err)
			}
			if len(lines) > 0 { // a file that only deletes lines has nowhere to mutate
				changedLines[f], files = lines, append(files, f)
			}
		}
		if len(files) == 0 {
			continue
		}
		if remaining <= 0 {
			for _, f := range files {
				d.NotGenerated = append(d.NotGenerated, contract.FileReason{File: f, Reason: fmt.Sprintf("max_mutants=%d reached before this file", max)})
			}
			continue
		}
		b, _ := json.Marshal(contract.MutateBudget{ChangedLines: changedLines, MaxMutants: remaining})
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: b}
		resp, mutantFiles, err := a.Runner.Mutate(ctx, e, req, baseDir, candDir)
		if err != nil {
			for _, f := range files {
				d.NoEngine = append(d.NoEngine, contract.FileReason{File: f, Reason: "the " + e.Language + " engine does not mutate: " + err.Error()})
			}
			continue
		}
		for _, m := range resp.Mutants {
			d.Mutants = append(d.Mutants, Mutant{Mutant: m, Content: mutantFiles[m.ID]})
		}
		d.NotGenerated = append(d.NotGenerated, resp.NotGenerated...)
		d.Pruned = append(d.Pruned, resp.Pruned...)
		remaining -= len(resp.Mutants)
	}
	return d, nil
}

// OverlayFile is a file placed on top of the tree before the tests run, with its path in the repo.
type OverlayFile struct {
	Path    string
	Content []byte
}

// TestsRun is a run of T: the status of each testcase by id, or the reason it cannot be judged.
type TestsRun struct {
	States map[string]string
	Reason string
}

// RunTests runs config.tests.command with the test files overlaid on a clean copy of treeish with overlay on top (the
// last file with the same path wins). changed goes in $A2DLC_CHANGED. A run without JUnit, without any testcase, or
// red without an identifiable test is not judged (Reason): it never counts as green. Without tests it does not run:
// the command with an empty $A2DLC_TESTS would run the whole suite.
func (a App) RunTests(ctx context.Context, treeish string, overlay []OverlayFile, tests, changed []string) (TestsRun, error) {
	var out TestsRun
	// The working tree's config, not the base's: RunTests takes no base, and the caller (a2dlc run, Stage 5) owns the
	// choice; it is listed in the lite-core needs.
	cfg, err := a.config()
	if err != nil {
		return out, err
	}
	switch {
	case cfg.Tests == nil:
		return out, fmt.Errorf("without config.tests there is no test command")
	case a.Tests == nil:
		return out, fmt.Errorf("this binary has no test runner")
	case len(tests) == 0:
		return out, fmt.Errorf("no tests to run: the command would run the whole suite")
	}
	overlaid := map[string][]byte{}
	for _, f := range overlay {
		overlaid[f.Path] = f.Content
	}
	c, err := a.run(ctx, gateInput{cfg: *cfg.Tests, tree: treeish}, cfg.Tests.Command, treeish, overlaid, nil, envTests(tests, changed))
	if err == nil {
		err = c.readable()
	}
	switch {
	case err != nil:
		out.Reason = err.Error()
	case len(c.cases) == 0:
		out.Reason = fmt.Sprintf("the run reported no testcase (exit %d)", c.exit)
	default:
		out.States = map[string]string{}
		for id, x := range c.cases {
			out.States[id] = x.Status
		}
	}
	return out, nil
}

// MutantEscalation is the mutant_survived escalation of a live mutant: it asks the Saboteur for a test that kills it.
// The target carries the mutant's id so that two live mutants of the same file do not share an escalation id.
func MutantEscalation(m contract.Mutant, summary string) contract.Escalation {
	e := escalation("mutant_survived", m.File+"::mutant:"+m.ID, summary, contract.Evidence{Mutant: &m})
	e.Needs = &contract.Needs{Role: contract.RoleSaboteur, Artifact: "test_files"}
	return e
}

// With returns, sorted, the ids of the run whose status satisfies state.
func (c TestsRun) With(state func(string) bool) []string {
	out := []string{}
	for id, s := range c.States {
		if state(s) {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}
