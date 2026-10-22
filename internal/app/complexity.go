package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Complexity measures a candidate (commit) against the base for the Occam selection between builders (§5 Stage 5, N=2):
// each engine measures the changed files that concern it (operation: complexity) and they are summed here. What nobody
// measured (no engine, or an engine without complexity) goes to Unmeasured; an engine that fails for another reason is
// a tool error.
func (a App) Complexity(ctx context.Context, base, candidate string) (contract.Complexity, error) {
	out := contract.Complexity{Files: []contract.ComplexityFile{}, Unmeasured: []string{}}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return out, fmt.Errorf("base %q: %w", base, err)
	}
	// The config is the base's, as in check: a working tree that drops a glob must not take a file out of the measure.
	cfg, err := a.configAt(baseSha)
	if err != nil {
		return out, err
	}
	tree, err := a.candidateTree(candidate)
	if err != nil {
		return out, err
	}
	changed, deleted, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return out, err
	}
	// Deleted files are also part of the change: removing code counts as removed lines. Tests are not: Occam measures
	// the implementation, and a builder that brings more tests must not lose points for that.
	sources := append(append([]string{}, changed...), deleted...)
	if cfg.Tests != nil {
		_, sources = partition(cfg.Tests.Paths, sources)
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, sources)
	out.Unmeasured = append(out.Unmeasured, noEngine...)
	if len(byEngine) > 0 {
		if a.Runner == nil {
			return out, fmt.Errorf("this binary has no engines to measure complexity")
		}
		baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
		if err != nil {
			return out, err
		}
		defer os.RemoveAll(baseDir)
		candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
		if err != nil {
			return out, err
		}
		defer os.RemoveAll(candDir)
		if err := a.Repo.Export(baseSha, baseDir); err != nil {
			return out, err
		}
		if err := a.Repo.Export(tree, candDir); err != nil {
			return out, err
		}
		for _, i := range sortedIndexes(byEngine) {
			e, files := cfg.Engines[i], byEngine[i]
			req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
				BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: json.RawMessage("{}")}
			r, err := a.Runner.Complexity(ctx, e, req, baseDir, candDir)
			if errors.Is(err, engine.ErrWithoutComplexity) {
				out.Unmeasured = append(out.Unmeasured, files...)
				continue
			}
			if err != nil {
				return out, fmt.Errorf("complexity of engine %s: %w", e.Language, err)
			}
			measured := map[string]bool{}
			for _, f := range r.Files {
				measured[f.File] = true
				out.Files = append(out.Files, f)
			}
			for _, f := range files {
				if !measured[f] {
					out.Unmeasured = append(out.Unmeasured, f)
				}
			}
		}
	}
	for _, f := range out.Files {
		out.LocAdded += f.LocAdded
		out.LocRemoved += f.LocRemoved
		out.Cyclomatic = sum(out.Cyclomatic, f.Cyclomatic)
		out.ASTNodes = sum(out.ASTNodes, f.ASTNodes)
	}
	out.Unmeasured = unique(out.Unmeasured)
	return out, nil
}

// sum accumulates a metric that may be missing: nil only if no file has it.
func sum(acc, x *int) *int {
	if x == nil {
		return acc
	}
	n := *x
	if acc != nil {
		n += *acc
	}
	return &n
}

// SaveVerdict leaves v (and its escalations) as .a2dlc/verdict.json and .a2dlc/escalations.json, as at the end of
// CheckWith. a2dlc run uses it with N=2: each candidate goes through check, which writes its verdict, and the one that
// has to remain is the selected one's, not the last one checked.
func (a App) SaveVerdict(v contract.Verdict) error {
	verdict.CompleteDiffBudget(&v)
	if err := a.write(pathVerdict, v); err != nil {
		return err
	}
	return a.write(pathEscalations, contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: v.TreeHash,
		CreatedAt: v.CreatedAt, Items: escalations(v)})
}
