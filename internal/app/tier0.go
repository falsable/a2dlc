package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/graph"
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
	// ChangeKind is intent.change_kind (feature, bugfix, refactor, or ""). On feature intents, a candidate that
	// changes source files without adding or updating any test file gets a Tier 0 test finding.
	ChangeKind string
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
	var baseDir, candDir string
	var exportOnce sync.Once
	var exportErr error
	export := func() error {
		exportOnce.Do(func() {
			defer func(t0 time.Time) { timerOf(ctx).sync(time.Since(t0)) }(time.Now())
			if baseDir, exportErr = os.MkdirTemp("", "a2dlc-base-*"); exportErr != nil {
				return
			}
			if candDir, exportErr = os.MkdirTemp("", "a2dlc-cand-*"); exportErr != nil {
				return
			}
			if exportErr = a.Repo.Export(baseSha, baseDir); exportErr != nil {
				return
			}
			exportErr = a.Repo.Export(tree, candDir)
		})
		return exportErr
	}
	defer func() {
		if baseDir != "" {
			_ = os.RemoveAll(baseDir)
		}
		if candDir != "" {
			_ = os.RemoveAll(candDir)
		}
	}()
	if len(idx) == 0 {
		// the checks first: a round they flag goes back without paying the test run (RFC-0012)
		if chk := a.checksTier0(ctx, cfg, extra, tree, baseSha, changed, export, &baseDir, &candDir); len(chk) > 0 {
			resp.Findings, resp.OK = chk, false
			return resp, unique(unreviewed), nil
		}
		resp, err := a.testsTier0(ctx, resp, cfg, baseSha, tree, extra, changed, export, &baseDir, &candDir)
		return resp, unique(unreviewed), err
	}
	if a.Runner == nil {
		return resp, nil, fmt.Errorf("this binary has no engines for Tier 0")
	}
	// The declared tests do not depend on what the engines answer, so they run while the engines review; their findings
	// still come after the engines'. An engine's error still wins, and nothing returns before the tests have stopped.
	testsCtx, cancelTests := context.WithCancel(ctx)
	testsDone := make(chan struct{})
	var tests contract.Tier0Response
	var testsErr error
	go func() {
		defer close(testsDone)
		tests, testsErr = a.testsTier0(testsCtx, contract.Tier0Response{Findings: []contract.Tier0Finding{}}, cfg, baseSha, tree, extra, changed, export, &baseDir, &candDir)
	}()
	defer func() {
		cancelTests()
		<-testsDone
	}()
	if err := export(); err != nil {
		return resp, nil, err
	}
	var engImgs []string
	for _, i := range idx {
		engImgs = append(engImgs, cfg.Engines[i].Image)
	}
	engCtx, cleanWarmEng := a.attachWarmImages(ctx, a.Runner, engImgs, baseSha, tree)
	defer cleanWarmEng()
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
		t0 := time.Now()
		r, err := a.Runner.Tier0(engCtx, e, req, baseDir, candDir)
		timerOf(ctx).engineOp("tier0", time.Since(t0))
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
	// RFC-0012: the checks run while the tests still do, and a round the engines or the checks flag goes back to the
	// builder without waiting for the tests: they would run again on the next round's tree anyway
	chk := a.checksTier0(ctx, cfg, extra, tree, baseSha, changed, export, &baseDir, &candDir)
	if extra.Tests && len(resp.Findings)+len(chk) > 0 {
		cancelTests()
		<-testsDone
		resp.Findings = append(resp.Findings, chk...)
		resp.OK = false
		return resp, unique(unreviewed), nil
	}
	<-testsDone
	if testsErr != nil {
		return resp, unique(unreviewed), testsErr
	}
	resp.Findings = append(resp.Findings, tests.Findings...)
	resp.Findings = append(resp.Findings, chk...)
	resp.OK = len(resp.Findings) == 0
	resp.Reviewed = reviewed || tests.Reviewed
	return resp, unique(unreviewed), nil
}

// FireOnTier0 is the firing point of the checks Tier 0 runs on each builder round (RFC-0011, Draft).
const FireOnTier0 = "stage:3"

// checksTier0 runs the blocking config.checks whose fire_on lists stage:3 on the candidate, over the changed files they
// match (RFC-0011, Draft). Each unwaived finding goes back to the builder as a finding of kind check, so that it fixes what
// Stage 4 would fail on. A check in error or timeout gives no finding: that is not the builder's to fix, and Stage 4 reports
// it. An advisory check is not run here: Stage 4 only records it. Only the builder's loop (extra.Tests) runs them, and they
// never count as a review: the verdict still comes from Stage 4, where the same checks fire at check.
// shortcut: Tier 0 reads no waivers, so a finding with a signed accept_finding still comes back; read waivers.json if a
// repo ever relies on that inside a run.
func (a App) checksTier0(ctx context.Context, cfg contract.Config, extra Tier0Extra, tree, baseSha string, changed []string,
	export func() error, baseDir, candDir *string) []contract.Tier0Finding {
	out := []contract.Tier0Finding{}
	if !extra.Tests || len(changed) == 0 {
		return out
	}
	blocking := cfg
	blocking.Checks = nil
	var chkImgs []string
	for _, c := range cfg.Checks {
		if c.Severity == contract.CheckBlocking && fires(c, FireOnTier0) {
			blocking.Checks = append(blocking.Checks, c)
			chkImgs = append(chkImgs, c.Image)
		}
	}
	if len(blocking.Checks) == 0 {
		return out
	}
	chkCtx, cleanWarmChk := a.attachWarmImages(ctx, a.Checks, chkImgs, baseSha, tree)
	defer cleanWarmChk()
	checks, _ := a.runChecks(chkCtx, blocking, FireOnTier0, changed, export, baseDir, candDir, nil, nil)
	for _, c := range checks {
		if c.Status != contract.CheckRan {
			continue
		}
		for _, f := range c.FindingList() {
			if len(out) == maxTestFindings {
				return out
			}
			file, line := checkTarget(f.Target)
			out = append(out, contract.Tier0Finding{Kind: contract.Tier0Check, File: file, Line: line, Message: c.ID + ": " + f.Message})
		}
	}
	return out
}

// checkTarget splits a check finding's target, <file>::<check id> or <file>::L<n>, into the file and the line.
func checkTarget(target string) (string, *int) {
	file, rest, ok := strings.Cut(target, "::")
	if !ok {
		return target, nil
	}
	if n, err := strconv.Atoi(strings.TrimPrefix(rest, "L")); err == nil && strings.HasPrefix(rest, "L") {
		return file, &n
	}
	return file, nil
}

// maxTestFindings bounds the testcases one Tier 0 round sends back to the builder: the feedback is paid input.
const maxTestFindings = 30

// testsTier0 is the Tier 0 of the declared tests (§5 Stage 3): config.tests.command runs once in config.tests.image on the
// candidate, over every changed file, and a failed or errored testcase is a finding (kind test) that goes back to the
// builder like an engine's. Engine-covered files are in the list too: Tier 0 runs no diff, so it does not know which of
// their lines an engine will examine as PASS or FAIL at Stage 4, and it reports a changed line no test executed wherever
// the tests have the file in scope; it can say more than Stage 4 about a line an engine will judge, never less. The run counts as a review only if it is readable: JUnit present, at least one testcase, and a red exit with an
// identifiable failed testcase. Anything else (no runner, a command that left no JUnit, a timeout) is an error of the tool,
// returned to the caller: it is neither a review nor feedback, because sending it to the builder would spend paid rounds on
// something that is not its own. The files stay in the list of what Tier 0 did not review (the caller keeps it): Stage 4
// has its own coverage precondition. Without config.tests, with no changed files, or without extra.Tests it does nothing.
func (a App) testsTier0(ctx context.Context, resp contract.Tier0Response, cfg contract.Config, baseSha, tree string, extra Tier0Extra, files []string,
	export func() error, baseDir, candDir *string) (contract.Tier0Response, error) {
	if !extra.Tests || cfg.Tests == nil || cfg.Tests.Command == "" || len(files) == 0 {
		return resp, nil
	}
	if a.Tests == nil {
		return resp, fmt.Errorf("this binary has no test runner: config.tests cannot review %s", strings.Join(unique(files), ", "))
	}
	cov := &accumulated{}
	c, err := a.tier0Run(ctx, cfg, baseSha, tree, files, cov, export, baseDir, candDir)
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
	if len(ids) > 0 && baseSha != "" && cfg.Tests.BaselineCommand != "" {
		changedTests, _ := partition(cfg.Tests.Paths, unique(files))
		if cfg.Tests.Tier0Command == contract.Tier0CommandBaseline ||
			(cfg.Tests.Tier0Command == contract.Tier0CommandChangedTests && len(changedTests) == 0) {
			ids = a.filterPreexistingBaseReds(ctx, cfg, baseSha, files, c.cases, ids)
		}
	}
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
		cf, err := a.coverageFindings(ctx, cfg, baseSha, tree, files, cov, export, baseDir, candDir)
		if err != nil {
			return resp, err
		}
		resp.Findings = append(resp.Findings, cf...)
		if extra.ChangeKind == "feature" {
			changedTests, sources := partition(cfg.Tests.Paths, unique(files))
			if len(sources) > 0 && len(changedTests) == 0 {
				sort.Strings(sources)
				resp.Findings = append(resp.Findings, contract.Tier0Finding{
					Kind:    contract.Tier0Test,
					File:    sources[0],
					Message: "feature intent changed source files without adding or updating any test matching config.tests.paths; add a test that proves the new behavior",
				})
			}
		}
	}
	resp.OK = len(resp.Findings) == 0
	resp.Reviewed = true
	return resp, nil
}

// tier0Run is the run of step 4. By default it is tests.command with every changed file in $A2DLC_CHANGED. With
// tests.tier0_command "baseline" (RFC-0004) it runs baseline_command (or falls back to tests.command when
// baseline_command is omitted) through the same cache. With "changed_tests", it runs tests.command over changed test
// files when any test file changed (leaving impacted suite tests to Stage 4), or falls back when none changed.
func (a App) tier0Run(ctx context.Context, cfg contract.Config, baseSha, tree string, files []string, cov *accumulated,
	export func() error, baseDir, candDir *string) (run, error) {
	t := *cfg.Tests
	runCommandWith := func(selected []string) (run, error) {
		c, err := a.run(ctx, gateInput{cfg: t, tree: tree, cov: cov}, t.Command, tree, nil, nil, envTests(selected, unique(files)))
		if err == nil {
			err = c.readable()
		}
		return c, err
	}
	if t.Tier0Command == contract.Tier0CommandChangedTests {
		changed, _, err := a.Repo.Changed(baseSha, tree)
		if err != nil {
			return run{}, err
		}
		changedTests, _ := partition(t.Paths, changed)
		if len(changedTests) > 0 {
			sort.Strings(changedTests)
			return runCommandWith(changedTests)
		}
		if t.BaselineCommand == "" {
			return runCommandWith(a.selectTier0Tests(ctx, cfg, baseSha, tree, files, export, baseDir, candDir))
		}
	} else if t.Tier0Command != contract.Tier0CommandBaseline {
		return runCommandWith(a.selectTier0Tests(ctx, cfg, baseSha, tree, files, export, baseDir, candDir))
	}
	cmd := t.BaselineCommand
	if cmd == "" {
		cmd = t.Command
	}
	// the selection check makes: what changed from the base to the candidate, split by tests.paths
	changed, _, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return run{}, err
	}
	tests, sources := partition(t.Paths, changed)
	in := gateInput{cfg: t, baseSha: baseSha, tree: tree, tests: tests, sources: sources, cov: cov}
	env := envTests(tests, sources)
	digest, err := a.imageDigest(ctx, t.Image)
	if err != nil || digest == "" {
		c, err := a.run(ctx, in, cmd, tree, nil, nil, env)
		if err == nil {
			err = c.readable()
		}
		return c, err
	}
	c, _, err := a.suite(ctx, in, cmd, tree, digest, env)
	return c, err
}

// filterPreexistingBaseReds drops red testcases that already fail on baseSha when they belong to unchanged test files,
// so Tier 0 only feeds regressions or failing new/changed tests back to the builder (Stage 4's baseline gate performs
// the authoritative comparison anyway). Fail-closed: any error verifying baseSha leaves ids unchanged.
func (a App) filterPreexistingBaseReds(ctx context.Context, cfg contract.Config, baseSha string, files []string, candCases map[string]junit.Case, ids []string) []string {
	changedTests, sources := partition(cfg.Tests.Paths, unique(files))
	if len(sources) == 0 {
		return ids
	}
	changedSet := changedTestSet(changedTests)
	needsBaseCheck := false
	for _, id := range ids {
		if !isInChangedTests(candCases[id], changedSet) {
			needsBaseCheck = true
			break
		}
	}
	if !needsBaseCheck {
		return ids
	}
	digest, err := a.imageDigest(ctx, cfg.Tests.Image)
	if err != nil || digest == "" {
		return ids
	}
	in := gateInput{cfg: *cfg.Tests, baseSha: baseSha, tree: baseSha, sources: sources}
	baseRun, _, err := a.suite(ctx, in, cfg.Tests.BaselineCommand, baseSha, digest, envTests(nil, sources))
	if err != nil {
		return ids
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if !isInChangedTests(candCases[id], changedSet) {
			if bc, ok := baseRun.cases[id]; ok && junit.Red(bc.Status) {
				continue
			}
		}
		kept = append(kept, id)
	}
	return kept
}

func changedTestSet(changedTests []string) map[string]bool {
	out := make(map[string]bool, len(changedTests)*2)
	for _, f := range changedTests {
		norm := strings.ReplaceAll(f, "\\", "/")
		out[norm] = true
		if ext := path.Ext(norm); ext != "" {
			out[strings.ReplaceAll(strings.TrimSuffix(norm, ext), "/", ".")] = true
		}
	}
	return out
}

func isInChangedTests(c junit.Case, changed map[string]bool) bool {
	if len(changed) == 0 {
		return false
	}
	if c.File != "" && changed[strings.ReplaceAll(c.File, "\\", "/")] {
		return true
	}
	tf := testFile(c)
	if changed[tf] {
		return true
	}
	for k := range changed {
		if strings.HasPrefix(tf, k+".") {
			return true
		}
	}
	return false
}

// selectTier0Tests narrows Tier 0 inner-loop tests to affected test files via .tests-impact.json or engine deps_graph.
// Returns nil when no signal covers all changed sources, safely falling back to running the full test suite.
func (a App) selectTier0Tests(ctx context.Context, cfg contract.Config, baseSha, tree string, files []string,
	export func() error, baseDir, candDir *string) []string {
	if cfg.Tests == nil {
		return nil
	}
	uniq := unique(files)
	changedTests, sources := partition(cfg.Tests.Paths, uniq)
	if len(sources) == 0 && len(changedTests) > 0 {
		sort.Strings(changedTests)
		return changedTests
	}
	if len(sources) == 0 {
		return nil
	}
	if selected := a.selectFromImpactFile(tree, changedTests, sources); len(selected) > 0 {
		return selected
	}
	return a.selectFromDepsGraph(ctx, cfg, baseSha, tree, changedTests, sources, export, baseDir, candDir)
}

func (a App) selectFromImpactFile(tree string, changedTests, sources []string) []string {
	raw, err := a.Repo.Show(tree, ".tests-impact.json")
	if err != nil {
		return nil
	}
	var nested struct {
		ByFile   map[string][]string `json:"by_file"`
		ByTarget map[string][]string `json:"by_target"`
	}
	flat := map[string][]string{}
	_ = json.Unmarshal(raw, &nested)
	if len(nested.ByFile) == 0 && len(nested.ByTarget) == 0 {
		if err := json.Unmarshal(raw, &flat); err != nil {
			return nil
		}
	}
	selected := map[string]bool{}
	for _, tf := range changedTests {
		selected[tf] = true
	}
	for _, src := range sources {
		var mapped []string
		mapped = append(mapped, nested.ByFile[src]...)
		mapped = append(mapped, flat[src]...)
		for k, v := range nested.ByTarget {
			if strings.HasPrefix(k, src+"::") {
				mapped = append(mapped, v...)
			}
		}
		for k, v := range flat {
			if strings.HasPrefix(k, src+"::") {
				mapped = append(mapped, v...)
			}
		}
		hits := 0
		for _, tf := range mapped {
			if _, err := a.Repo.Blob(tree, tf); err == nil {
				selected[tf] = true
				hits++
			}
		}
		if hits == 0 {
			return nil
		}
	}
	out := make([]string, 0, len(selected))
	for tf := range selected {
		out = append(out, tf)
	}
	sort.Strings(out)
	return out
}

func (a App) selectFromDepsGraph(ctx context.Context, cfg contract.Config, baseSha, tree string,
	changedTests, sources []string, export func() error, baseDir, candDir *string) []string {
	deps, ok := a.Runner.(DepsRunner)
	if a.Runner == nil || !ok || len(cfg.Engines) == 0 {
		return nil
	}
	allFiles, err := a.Repo.Files(tree)
	if err != nil {
		return nil
	}
	allTests, _ := partition(cfg.Tests.Paths, allFiles)
	if len(allTests) == 0 {
		return nil
	}
	srcByEng, unmapped := verdict.Assign(cfg.Engines, sources)
	if len(unmapped) > 0 || len(srcByEng) == 0 {
		return nil
	}
	if err := export(); err != nil || candDir == nil || *candDir == "" || baseDir == nil || *baseDir == "" {
		return nil
	}
	allByEng, _ := verdict.Assign(cfg.Engines, allFiles)
	srcSet := map[string]bool{}
	for _, s := range sources {
		srcSet[s] = true
	}
	var allEdges []contract.DepsEdge
	for _, i := range sortedIndexes(srcByEng) {
		e := cfg.Engines[i]
		files := unique(append(append([]string{}, allByEng[i]...), allTests...))
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Operation: "deps_graph", Language: e.Language,
			BaseRef: baseSha, BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: json.RawMessage("{}")}
		r, err := deps.Deps(ctx, e, req, *baseDir, *candDir)
		if err != nil || r.Error != nil {
			return nil
		}
		allEdges = append(allEdges, r.Edges...)
	}
	reach := graph.Reach(allEdges, srcSet)
	selected := map[string]bool{}
	for _, tf := range changedTests {
		selected[tf] = true
	}
	hits := 0
	for _, tf := range allTests {
		if reach[tf] {
			selected[tf] = true
			hits++
		}
	}
	if hits == 0 {
		return nil
	}
	out := make([]string, 0, len(selected))
	for tf := range selected {
		out = append(out, tf)
	}
	sort.Strings(out)
	return out
}

// coverageFindings are the changed source lines that the green run left unexecuted (RFC-0002, step 4): the lines Stage 4's
// coverage precondition would list as unexamined, with the same classification (noteUncovered), one finding per file. It
// reads the coverage the run wrote; a run that wrote none, or none that can be read, gives no finding, because there is
// nothing to say to the builder, and Stage 4 keeps its own precondition. Only a green suite gets here: a failing run's
// coverage is partial and its testcases come first.
func (a App) coverageFindings(ctx context.Context, cfg contract.Config, baseSha, tree string, files []string, cov *accumulated,
	export func() error, baseDir, candDir *string) ([]contract.Tier0Finding, error) {
	if cov.report == nil {
		return nil, nil
	}
	_, sources := partition(cfg.Tests.Paths, unique(files))
	pre, err := a.precondition(cfg, baseSha, tree, sources, nil, cov.report, cov.note)
	if err != nil {
		return nil, err
	}
	a.noteUncovered(ctx, cfg, pre, export, baseDir, candDir)
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
