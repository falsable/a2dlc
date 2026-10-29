package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/junit"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Tier0 runs the builder's Tier 0 (§5 Stage 3) on a candidate (commit or tree): each engine reviews the files that
// changed from the base to the candidate and concern it (including the test files: a test that does not compile is the
// builder's) and the probes whose file concerns it (operation: tier0). It returns the combined findings, which is
// tier0_feedback.json, and what nobody reviewed: files without an engine or from an engine that does not do tier0, and
// probes without an engine (probe:<id>). An engine that fails for another reason is a tool error, not feedback for the
// builder. This entry point runs the engines only; Tier0With with Tier0Extra.Tests also runs the repo's tests.
func (a App) Tier0(ctx context.Context, base, candidate string, probes []contract.Probe) (contract.Tier0Response, []string, error) {
	return a.Tier0With(ctx, base, candidate, probes, Tier0Extra{})
}

// Tier0Extra is what the intent adds to config.tier0 for the engines: its forbidden imports (intent.capabilities.
// forbidden_imports) and its forbidden side effects (intent.boundary.forbidden_side_effects). Both are only unioned in, so
// the slice can add restrictions to the policy but never remove one.
type Tier0Extra struct {
	ForbiddenImports     []string
	ForbiddenSideEffects []string
	// Tests turns on the Tier 0 of the repo's own tests (testsTier0): the builder's loop sets it, the review of a working
	// tree that no builder wrote does not.
	Tests bool
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
		resp, err := a.testsTier0(ctx, resp, cfg, tree, extra, noEngine)
		return resp, unique(unreviewed), err
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
	noTests := append([]string{}, noEngine...) // what config.tests is left to review: the files no engine reviewed
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
			noTests = append(noTests, files...)
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
	resp, err = a.testsTier0(ctx, resp, cfg, tree, extra, noTests)
	return resp, unique(unreviewed), err
}

// maxTestFindings bounds the testcases one Tier 0 round sends back to the builder: the feedback is paid input.
const maxTestFindings = 30

// testsTier0 is the Tier 0 of the files no engine reviewed (§5 Stage 3): config.tests.command runs in config.tests.image
// on the candidate, and a failed or errored testcase is a finding (kind test) that goes back to the builder like an
// engine's. The run counts as a review only if it is readable: JUnit present, at least one testcase, and a red exit with an
// identifiable failed testcase. Anything else (no runner, a command that left no JUnit, a timeout) is an error of the tool,
// returned to the caller: it is neither a review nor feedback, because sending it to the builder would spend paid rounds on
// something that is not its own. The files stay in the list of what Tier 0 did not review (the caller keeps it): Stage 4
// has its own coverage precondition. Without config.tests, with no such files, or without extra.Tests it does nothing.
func (a App) testsTier0(ctx context.Context, resp contract.Tier0Response, cfg contract.Config, tree string, extra Tier0Extra, files []string) (contract.Tier0Response, error) {
	if !extra.Tests || cfg.Tests == nil || cfg.Tests.Command == "" || len(files) == 0 {
		return resp, nil
	}
	if a.Tests == nil {
		return resp, fmt.Errorf("this binary has no test runner: config.tests cannot review %s", strings.Join(unique(files), ", "))
	}
	c, err := a.run(ctx, gateInput{cfg: *cfg.Tests, tree: tree}, cfg.Tests.Command, tree, nil, nil, envTests(nil, unique(files)))
	if err == nil {
		err = c.readable()
	}
	if err == nil && len(c.cases) == 0 {
		err = fmt.Errorf("the run reported no testcase (exit %d): %s", c.exit, c.log)
	}
	if err != nil {
		return resp, fmt.Errorf("the tests of config.tests: %w", err)
	}
	ids := []string{}
	for id, x := range c.cases {
		if junit.Red(x.Status) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for i, id := range ids {
		x := c.cases[id]
		if i == maxTestFindings {
			resp.Findings = append(resp.Findings, contract.Tier0Finding{Kind: contract.Tier0Test, File: testFile(c.cases[ids[0]]),
				Message: fmt.Sprintf("%d more testcases failed; fix the ones above first", len(ids)-maxTestFindings)})
			break
		}
		msg := id + " " + x.Status
		if m := strings.TrimSpace(x.Message); m != "" {
			if len(m) > 500 {
				m = m[:500] + "…"
			}
			msg += ": " + m
		}
		resp.Findings = append(resp.Findings, contract.Tier0Finding{Kind: contract.Tier0Test, File: testFile(x), Message: msg})
	}
	resp.OK = len(resp.Findings) == 0
	resp.Reviewed = true
	return resp, nil
}

// testFile is the file a testcase's finding is about: the file the runner gives, else its classname (vitest puts the file
// there), else a placeholder for a collection error that names none.
func testFile(c junit.Case) string {
	switch {
	case c.File != "":
		return c.File
	case c.Classname != "":
		return c.Classname
	}
	return "junit"
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
