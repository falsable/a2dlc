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
		resp, err := a.testsTier0(ctx, resp, cfg, baseSha, tree, extra, noEngine)
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
	resp, err = a.testsTier0(ctx, resp, cfg, baseSha, tree, extra, noTests)
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
func (a App) testsTier0(ctx context.Context, resp contract.Tier0Response, cfg contract.Config, baseSha, tree string, extra Tier0Extra, files []string) (contract.Tier0Response, error) {
	if !extra.Tests || cfg.Tests == nil || cfg.Tests.Command == "" || len(files) == 0 {
		return resp, nil
	}
	if a.Tests == nil {
		return resp, fmt.Errorf("this binary has no test runner: config.tests cannot review %s", strings.Join(unique(files), ", "))
	}
	cov := &accumulated{}
	c, err := a.run(ctx, gateInput{cfg: *cfg.Tests, tree: tree, cov: cov}, cfg.Tests.Command, tree, nil, nil, envTests(nil, unique(files)))
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
	if len(ids) == 0 {
		cf, err := a.coverageFindings(ctx, cfg, baseSha, tree, files, cov)
		if err != nil {
			return resp, err
		}
		resp.Findings = append(resp.Findings, cf...)
	}
	resp.OK = len(resp.Findings) == 0
	resp.Reviewed = true
	return resp, nil
}

// coverageFindings are the changed source lines that the green run left unexecuted (RFC-0002, step 4): the lines Stage 4's
// coverage precondition would list as unexamined, with the same classification (noteUncovered), one finding per file. It
// reads the coverage the run wrote; a run that wrote none, or none that can be read, gives no finding, because there is
// nothing to say to the builder, and Stage 4 keeps its own precondition. Only a green suite gets here: a failing run's
// coverage is partial and its testcases come first.
func (a App) coverageFindings(ctx context.Context, cfg contract.Config, baseSha, tree string, files []string, cov *accumulated) ([]contract.Tier0Finding, error) {
	if cov.report == nil {
		return nil, nil
	}
	_, sources := partition(cfg.Tests.Paths, unique(files))
	pre, err := a.precondition(cfg, baseSha, tree, sources, nil, cov.report, cov.note)
	if err != nil {
		return nil, err
	}
	var baseDir, candDir string
	defer func() {
		os.RemoveAll(baseDir)
		os.RemoveAll(candDir)
	}()
	export := func() error {
		var err error
		if baseDir, err = os.MkdirTemp("", "a2dlc-base-*"); err != nil {
			return err
		}
		if candDir, err = os.MkdirTemp("", "a2dlc-cand-*"); err != nil {
			return err
		}
		if err := a.Repo.Export(baseSha, baseDir); err != nil {
			return err
		}
		return a.Repo.Export(tree, candDir)
	}
	a.noteUncovered(ctx, cfg, pre, export, &baseDir, &candDir)
	out := []contract.Tier0Finding{}
	for _, f := range pre.Files {
		if len(f.Uncovered) == 0 {
			continue
		}
		if len(out) == maxTestFindings {
			out = append(out, contract.Tier0Finding{Kind: contract.Tier0Coverage, File: f.File,
				Message: "more files have changed lines no test executed; cover the ones above first"})
			break
		}
		first := f.Uncovered[0]
		out = append(out, contract.Tier0Finding{Kind: contract.Tier0Coverage, File: f.File, Line: &first,
			Message: "no test executed these changed lines: " + lineRanges(f.Uncovered) +
				"; add a test that runs them, in a new test file (existing test files stay as they are), or remove the code they belong to"})
	}
	return out, nil
}

// lineRanges writes sorted line numbers as "37-38, 65", at most maxRanges ranges.
func lineRanges(lines []int) string {
	const maxRanges = 20
	var parts []string
	for i := 0; i < len(lines); {
		j := i
		for j+1 < len(lines) && lines[j+1] == lines[j]+1 {
			j++
		}
		if len(parts) == maxRanges {
			parts = append(parts, "…")
			break
		}
		if j > i {
			parts = append(parts, fmt.Sprintf("%d-%d", lines[i], lines[j]))
		} else {
			parts = append(parts, fmt.Sprint(lines[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ", ")
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
