package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Tier0 runs the builder's Tier 0 (§5 Stage 3) on a candidate (commit or tree): each engine reviews the files that
// changed from the base to the candidate and concern it (including the test files: a test that does not compile is the
// builder's) and the probes whose file concerns it (operation: tier0). It returns the combined findings, which is
// tier0_feedback.json, and what nobody reviewed: files without an engine or from an engine that does not do tier0, and
// probes without an engine (probe:<id>). An engine that fails for another reason is a tool error, not feedback for the
// builder.
func (a App) Tier0(ctx context.Context, base, candidate string, probes []contract.Probe) (contract.Tier0Response, []string, error) {
	return a.Tier0With(ctx, base, candidate, probes, Tier0Extra{})
}

// Tier0Extra is what the intent adds to config.tier0 for the engines: its forbidden imports (intent.capabilities.
// forbidden_imports) and its forbidden side effects (intent.boundary.forbidden_side_effects). Both are only unioned in, so
// the slice can add restrictions to the policy but never remove one.
type Tier0Extra struct {
	ForbiddenImports     []string
	ForbiddenSideEffects []string
}

// Tier0With is Tier0 with the intent's extra policy (see Tier0Extra), unioned into the policy of config.tier0 that the
// engines get.
func (a App) Tier0With(ctx context.Context, base, candidate string, probes []contract.Probe, extra Tier0Extra) (contract.Tier0Response, []string, error) {
	resp := contract.Tier0Response{SchemaVersion: contract.SchemaVersion, OK: true, Findings: []contract.Tier0Finding{}}
	unreviewed := []string{}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return resp, nil, fmt.Errorf("base %q: %w", base, err)
	}
	// The engines' policy comes from the base's config, as check reads it: a working tree that edits config.json does not
	// change what the engines are told to judge.
	cfg, err := a.configAt(baseSha)
	if err != nil {
		return resp, nil, err
	}
	policy := mergePolicy(cfg.Tier0, extra)
	tree, err := a.candidateTree(candidate)
	if err != nil {
		return resp, nil, err
	}
	changed, _, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return resp, nil, err
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, changed)
	unreviewed = append(unreviewed, noEngine...)
	probesByEngine := map[int][]contract.Probe{}
	for _, p := range probes {
		if !p.Executable() {
			unreviewed = append(unreviewed, "probe:"+p.ID) // without file/expr there is no assertion to run
			continue
		}
		probesOfFile, without := verdict.Assign(cfg.Engines, []string{*p.File})
		if len(without) > 0 {
			unreviewed = append(unreviewed, "probe:"+p.ID)
			continue
		}
		for i := range probesOfFile {
			probesByEngine[i] = append(probesByEngine[i], p)
		}
	}
	idx := []int{}
	for i := range cfg.Engines {
		if len(byEngine[i]) > 0 || len(probesByEngine[i]) > 0 {
			idx = append(idx, i)
		}
	}
	if len(idx) == 0 {
		return resp, unique(unreviewed), nil
	}
	if a.Runner == nil {
		return resp, nil, fmt.Errorf("this binary has no engines for Tier 0")
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
	if err != nil {
		return resp, nil, err
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
	if err != nil {
		return resp, nil, err
	}
	defer os.RemoveAll(candDir)
	if err := a.Repo.Export(baseSha, baseDir); err != nil {
		return resp, nil, err
	}
	if err := a.Repo.Export(tree, candDir); err != nil {
		return resp, nil, err
	}
	reviewed := false
	for _, i := range idx {
		e, files := cfg.Engines[i], byEngine[i]
		if files == nil {
			files = []string{}
		}
		b, err := json.Marshal(contract.Tier0Budget{Probes: probesByEngine[i], Policy: policy})
		if err != nil {
			return resp, nil, err
		}
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: b}
		r, err := a.Runner.Tier0(ctx, e, req, baseDir, candDir)
		if errors.Is(err, engine.ErrWithoutTier0) {
			unreviewed = append(unreviewed, files...)
			for _, p := range probesByEngine[i] {
				unreviewed = append(unreviewed, "probe:"+p.ID)
			}
			continue
		}
		if err != nil {
			return resp, nil, fmt.Errorf("tier0 of engine %s: %w", e.Language, err)
		}
		// idx only lists engines with files or probes to review: an engine having answered tier0 counts as reviewed
		reviewed = true
		resp.Findings = append(resp.Findings, r.Findings...)
	}
	resp.OK = len(resp.Findings) == 0
	resp.Reviewed = reviewed
	return resp, unique(unreviewed), nil
}

// mergePolicy is config.tier0 with the intent's extra forbidden imports and side effects unioned in (sorted, unique). It
// returns nil when there is nothing to say, so the engines apply their default policy as they did before the merge.
func mergePolicy(base *contract.PolicyTier0, extra Tier0Extra) *contract.PolicyTier0 {
	var p contract.PolicyTier0
	if base != nil {
		p = *base
	}
	p.ForbiddenImports = unique(append(append([]string{}, p.ForbiddenImports...), extra.ForbiddenImports...))
	p.ForbiddenSideEffects = unique(append(append([]string{}, p.ForbiddenSideEffects...), extra.ForbiddenSideEffects...))
	if len(p.ForbiddenImports) == 0 {
		p.ForbiddenImports = nil
	}
	if len(p.ForbiddenSideEffects) == 0 {
		p.ForbiddenSideEffects = nil
	}
	if p.ForbiddenImports == nil && p.ForbiddenSideEffects == nil && !p.RequireHeader && p.MaxComplexity == 0 {
		return nil
	}
	return &p
}

func unique(s []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range s {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}
