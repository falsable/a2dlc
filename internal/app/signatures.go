package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Signatures asks each engine for the public signatures of the candidate in the changed sources (operation:
// signatures). It is the only thing of the code that a2dlc run lets the Saboteur see (§3): the interface, never the
// implementation. An engine that gives no signatures stops nothing: its files remain without a signature and the error
// is returned so that the artifact says so.
func (a App) Signatures(ctx context.Context, base string) ([]contract.SignatureFile, []contract.EngineError, error) {
	return a.SignaturesOf(ctx, base, "")
}

// SignaturesOf is Signatures for a given candidate (commit or tree; empty means the working tree), as in
// CheckOptions.CandidateTree.
func (a App) SignaturesOf(ctx context.Context, base, candidate string) ([]contract.SignatureFile, []contract.EngineError, error) {
	// The working tree's config, deliberately: signatures are the Saboteur's view and the contract lint's reference, not
	// a verdict. Reading the base's would make Stage 1 fail on any base without a committed config (run's fixtures).
	cfg, err := a.config()
	if err != nil {
		return nil, nil, err
	}
	if base == "" {
		base = a.defaultBase()
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return nil, nil, fmt.Errorf("base %q: %w", base, err)
	}
	tree, err := a.candidateTree(candidate)
	if err != nil {
		return nil, nil, err
	}
	changed, _, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return nil, nil, err
	}
	sources := changed
	if cfg.Tests != nil {
		_, sources = partition(cfg.Tests.Paths, changed)
	}
	byEngine, _ := verdict.Assign(cfg.Engines, sources)
	files, errs := []contract.SignatureFile{}, []contract.EngineError{}
	if len(byEngine) == 0 || a.Runner == nil {
		return files, errs, nil
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(candDir)
	if err := a.Repo.Export(baseSha, baseDir); err != nil {
		return nil, nil, err
	}
	if err := a.Repo.Export(tree, candDir); err != nil {
		return nil, nil, err
	}
	for _, i := range sortedIndexes(byEngine) {
		e := cfg.Engines[i]
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: byEngine[i], Budget: json.RawMessage("{}")}
		resp, err := a.Runner.Signatures(ctx, e, req, baseDir, candDir)
		if err != nil {
			errs = append(errs, contract.EngineError{Language: e.Language, Files: byEngine[i], Error: err.Error()})
			continue
		}
		files = append(files, resp.Files...)
	}
	return files, errs, nil
}
