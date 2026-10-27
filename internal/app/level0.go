package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// pathLevel0 is the level 0 receipt. It lives in .a2dlc/reports, which is out of the tree_hash and git-ignored, and it is
// not pathVerdict: the Stop hook writes it every turn, and verify must keep reading the receipt level 1 wrote at "done".
const pathLevel0 = pathReports + "/level0.json"

// Level0 is the static level (owner's decision of 2026-10-27, bead a2dlc-uca). It runs nothing from the repo's tests and no
// differential: it reads the diff, asks the engines the questions they answer from source (list_tests, deps_graph,
// classify_lines) over the changed files only, and writes .a2dlc/reports/level0.json. Its verdict is always INCONCLUSIVE
// (nothing was proven) and it never blocks; the only errors are a2dlc's own (base, config, write). An engine that is
// missing or does not do an operation is said in the receipt, never an error.
func (a App) Level0(ctx context.Context, base string) (contract.Verdict, error) {
	zero := contract.Level0
	v := contract.Verdict{SchemaVersion: contract.SchemaVersion, A2dlcVersion: a.Version, Verdict: contract.INCONCLUSIVE, Level: &zero,
		Targets: []contract.VerdictTarget{}, OutOfScope: []contract.OutOfScope{}, PendingWaivers: []string{}}
	ctx, tm := withTimer(ctx)
	if base == "" {
		base = a.defaultBase()
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return v, fmt.Errorf("base %q: %w", base, err)
	}
	cfg, err := a.baseConfig(baseSha, "")
	if err != nil {
		return v, err
	}
	tree, err := a.candidateTree("")
	if err != nil {
		return v, err
	}
	changed, deleted, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return v, err
	}
	sources, changedTests := changed, []string(nil)
	if cfg.Tests != nil {
		changedTests, sources = partition(cfg.Tests.Paths, changed)
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, sources)
	for _, f := range noEngine {
		v.OutOfScope = append(v.OutOfScope, contract.OutOfScope{File: f, Reason: "no_engine"})
	}
	for _, f := range deleted {
		v.OutOfScope = append(v.OutOfScope, contract.OutOfScope{File: f, Reason: "deleted"})
	}
	st := &contract.StaticReport{ChangedSources: nonNil(sources), ChangedTests: nonNil(changedTests), BringsTests: len(changedTests) > 0,
		Tests: []contract.StaticTest{}, NotImportedByDiffTests: []string{}, ProtectedPaths: []contract.ProtectedPath{}}
	guarded := protectedMatches(cfg.ProtectedPaths, append(append([]string(nil), changed...), deleted...))
	for _, f := range sortedKeys(guarded) {
		st.ProtectedPaths = append(st.ProtectedPaths, contract.ProtectedPath{File: f, Glob: guarded[f]})
	}

	// Only the changed files are laid out for the engines, never the whole tree: a full export is what level 0 must not pay.
	var baseDir, candDir string
	var once sync.Once
	var exportErr error
	export := func() error {
		once.Do(func() {
			defer func(t0 time.Time) { tm.sync(time.Since(t0)) }(time.Now())
			if baseDir, exportErr = os.MkdirTemp("", "a2dlc-l0-base-*"); exportErr != nil {
				return
			}
			if candDir, exportErr = os.MkdirTemp("", "a2dlc-l0-cand-*"); exportErr != nil {
				return
			}
			for _, f := range changed {
				if err := a.lay(candDir, tree, f); err != nil {
					exportErr = err
					return
				}
				_ = a.lay(baseDir, baseSha, f) // a new file has no base version
			}
		})
		return exportErr
	}
	defer func() {
		for _, d := range []string{baseDir, candDir} {
			if d != "" {
				os.RemoveAll(d)
			}
		}
	}()

	cov, err := a.precondition(cfg, baseSha, tree, sources, nil, nil, "")
	if err != nil {
		return v, err
	}
	cov.Status = contract.CoverageUnavailable
	cov.Reason = "level 0 runs no test: uncovered lines are the changed lines whose evidence has to come from the tests (level 1)"

	var wg sync.WaitGroup
	var classified lineNotes
	wg.Add(3)
	go func() {
		defer wg.Done()
		classified = a.classifyLines(ctx, cfg, cov, export, &baseDir, &candDir)
	}()
	go func() {
		defer wg.Done()
		st.Tests = a.staticTests(ctx, cfg, baseSha, tree, changedTests)
	}()
	go func() {
		defer wg.Done()
		st.Imports, st.NotImportedByDiffTests = a.staticImports(ctx, cfg, byEngine, changedTests, export, &candDir)
	}()
	wg.Wait()
	if classified != nil {
		applyNotes(cov, classified)
	}
	v.Coverage, v.Static = cov, st
	v.BaseRef, v.TreeHash = baseSha, treePrefix+tree
	v.CreatedAt = a.Now().UTC().Format(time.RFC3339)
	v.Timings = tm.result()
	return v, a.write(pathLevel0, v)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// lay writes the version of rel at treeish into dir.
func (a App) lay(dir, treeish, rel string) error {
	b, err := a.Repo.Show(treeish, rel)
	if err != nil {
		return err
	}
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644)
}

// staticTests lists the test functions of every changed test file from its source (the same list_tests the NOP gate uses
// for the base, cached by blob) and counts how many are not in the base's file.
func (a App) staticTests(ctx context.Context, cfg contract.Config, baseSha, tree string, changedTests []string) []contract.StaticTest {
	out := []contract.StaticTest{}
	if len(changedTests) == 0 {
		return out
	}
	cand := a.listBase(ctx, gateInput{baseSha: tree, engines: cfg.Engines}, changedTests)
	var inBase []string
	for _, f := range changedTests {
		if _, err := a.Repo.Blob(baseSha, f); err == nil {
			inBase = append(inBase, f)
		}
	}
	base := a.listBase(ctx, gateInput{baseSha: baseSha, engines: cfg.Engines}, inBase)
	existed := map[string]bool{}
	for _, f := range inBase {
		existed[f] = true
	}
	for _, f := range changedTests {
		set, ok := cand[f]
		if !ok {
			out = append(out, contract.StaticTest{File: f, Listable: false, Reason: "no engine could list its tests from the source"})
			continue
		}
		count := len(set)
		t := contract.StaticTest{File: f, Listable: true, Count: &count}
		switch bset, listed := base[f]; {
		case !existed[f]:
			t.New = &count
		case listed:
			n := 0
			for k := range set {
				if !bset[k] {
					n++
				}
			}
			t.New = &n
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].File < out[j].File })
	return out
}

// staticImports reads the import edges of the engine over the changed sources and the changed tests, and returns the
// sources that no changed test imports. Direct imports only. Any engine that cannot answer makes the whole reading
// unavailable, with its reason: half a reading would flag as untested what it just could not see.
func (a App) staticImports(ctx context.Context, cfg contract.Config, byEngine map[int][]string, changedTests []string, export func() error, candDir *string) (contract.StaticImports, []string) {
	none := []string{}
	switch {
	case cfg.Tests == nil:
		return contract.StaticImports{Status: contract.ImportsNotApplicable, Reason: "the repo declares no config.tests, so its test files are unknown"}, none
	case len(changedTests) == 0:
		return contract.StaticImports{Status: contract.ImportsNotApplicable, Reason: "the diff changes no test file"}, none
	case len(byEngine) == 0:
		return contract.StaticImports{Status: contract.ImportsNotApplicable, Reason: "no changed source belongs to an engine"}, none
	}
	runner, ok := a.Runner.(engine.DepsRunner)
	if !ok {
		return contract.StaticImports{Status: contract.ImportsUnavailable, Reason: "this binary's runner does not do deps_graph"}, none
	}
	if err := export(); err != nil {
		return contract.StaticImports{Status: contract.ImportsUnavailable, Reason: "cannot lay out the changed files: " + err.Error()}, none
	}
	isTest := map[string]bool{}
	for _, f := range changedTests {
		isTest[f] = true
	}
	imported := map[string]bool{}
	var covered []string
	for _, i := range sortedIndexes(byEngine) {
		e := cfg.Engines[i]
		files := append(append([]string{}, byEngine[i]...), changedTests...)
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseDir: "/base", CandidateDir: "/cand",
			ChangedFiles: files, Budget: []byte("{}")}
		t0 := time.Now()
		resp, err := runner.Deps(ctx, e, req, *candDir, *candDir)
		timerOf(ctx).engineOp("deps_graph", time.Since(t0))
		if err != nil {
			return contract.StaticImports{Status: contract.ImportsUnavailable, Reason: fmt.Sprintf("engine %s: %v", e.Language, err)}, none
		}
		for _, edge := range resp.Edges {
			if isTest[edge.From] {
				imported[edge.To] = true
			}
		}
		covered = append(covered, byEngine[i]...)
	}
	var missing []string
	for _, f := range covered {
		if !imported[f] {
			missing = append(missing, f)
		}
	}
	sort.Strings(missing)
	return contract.StaticImports{Status: contract.ImportsRan}, nonNil(missing)
}

// Level0Summary is the one line of the human summary: the same facts as the receipt, for the terminal and the hook.
func Level0Summary(v contract.Verdict) string {
	if v.Static == nil {
		return "level 0: no static report"
	}
	s := v.Static
	parts := []string{fmt.Sprintf("%d changed sources, %d changed test files", len(s.ChangedSources), len(s.ChangedTests))}
	if !s.BringsTests {
		parts = append(parts, "the diff brings no tests")
	}
	if n := newTests(s); n > 0 {
		parts = append(parts, fmt.Sprintf("%d new test functions", n))
	}
	if len(s.NotImportedByDiffTests) > 0 {
		parts = append(parts, "no diff test imports: "+strings.Join(s.NotImportedByDiffTests, ", "))
	}
	noted, open := 0, 0
	if v.Coverage != nil {
		for _, f := range v.Coverage.Files {
			open += len(f.Uncovered)
			for _, n := range f.Noted {
				noted += len(n.Lines)
			}
		}
	}
	if noted > 0 || open > 0 {
		parts = append(parts, fmt.Sprintf("%d changed lines need tests, %d noted (defensive or declaration-only)", open, noted))
	}
	if len(s.ProtectedPaths) > 0 {
		files := make([]string, len(s.ProtectedPaths))
		for i, p := range s.ProtectedPaths {
			files[i] = p.File
		}
		parts = append(parts, "protected paths changed: "+strings.Join(files, ", "))
	}
	if v.Timings != nil {
		parts = append(parts, fmt.Sprintf("%d ms", v.Timings.TotalMs))
	}
	return "a2dlc level 0 (informs, does not block; details in " + pathLevel0 + "): " + strings.Join(parts, "; ")
}

func newTests(s *contract.StaticReport) int {
	n := 0
	for _, t := range s.Tests {
		if t.New != nil {
			n += *t.New
		}
	}
	return n
}
