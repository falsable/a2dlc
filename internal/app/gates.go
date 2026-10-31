package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/coverage"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/junit"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Cheap Tier 1 gates (protocol §5, Stage 4 step 0): NOP/Oracle overlaid on the new or modified tests and "does not
// worsen" against the base, with re-runs to tell flakies apart. The core knows no languages: it runs the repo's
// commands in its image and reads JUnit XML.

// cacheDir is the suite cache, OUTSIDE the tree: the key is computable, and a cache inside the repo could be
// planted by the agent (a regression set as "red before") or pushed with git add -f for CI to read. A2DLC_CACHE_DIR
// changes it (tests); otherwise it is the user's cache, per repository: every worktree of one repository shares it, so a
// run in one worktree answers the same tree in another.
func (a App) cacheDir() string {
	if d := os.Getenv("A2DLC_CACHE_DIR"); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	abs, err := a.Repo.CommonDir()
	if err != nil {
		abs, _ = filepath.Abs(a.Repo.Dir)
	}
	h := sha256.Sum256([]byte(abs))
	return filepath.Join(base, "a2dlc", hex.EncodeToString(h[:])[:16])
}

// run is an already-read execution of a test command.
type run struct {
	cases map[string]junit.Case
	exit  int
	log   string
	junit bool
	raw   []byte
	// coverage is the raw report the command left, kept so that a cached run can give it again.
	coverage []byte
	// reproduced is set on a cached run that two runs of the base agreed on (Stage -1).
	reproduced bool
}

// readable says why a run cannot be judged; nil if it can. A red run without any identifiable failed test is
// INCONCLUSIVE, never PASS (§5).
func (c run) readable() error {
	if !c.junit {
		return fmt.Errorf("the command left no JUnit XML in $A2DLC_JUNIT (exit %d): %s", c.exit, c.log)
	}
	if c.exit != 0 {
		for _, x := range c.cases {
			if junit.Red(x.Status) {
				return nil
			}
		}
		if len(c.cases) > 0 {
			return fmt.Errorf("red run (exit %d) without identifiable failed tests: %s", c.exit, c.log)
		}
	}
	return nil
}

// defaultInvalidBaseFailures are the failures on the base that make a new test invalid for NOP, per engine language. They
// apply when config.tests.invalid_base_failures is absent; an explicit list, even an empty one, replaces them. Python: the
// import, collection and missing-name failures of protocol §5 Stage 4. TypeScript: a missing module or export, and a call or
// reference to a name that does not exist yet.
var defaultInvalidBaseFailures = map[string][]string{
	"python":     {"ImportError", "ModuleNotFoundError", "AttributeError", "NameError", "^collection"},
	"typescript": {"is not a function", "Cannot find module", "ReferenceError", "does not provide an export named"},
}

// invalidBaseFailures is the list the NOP gate classifies with: config.tests.invalid_base_failures when declared, otherwise
// the defaults of the languages of config.engines and of config.tests.stub_engine, sorted and unique. When none of them
// names a language the gate knows (a tests-only repo), the list is the union of every language's defaults: with an empty
// list every failed testcase on the base would be a valid failure, and a runner that reports a failed import as a failure
// under the test's own id would make a test that proves nothing PASS. More patterns can only turn a PASS into INCONCLUSIVE.
func invalidBaseFailures(cfg contract.Config) []string {
	if cfg.Tests != nil && cfg.Tests.InvalidBaseFailures != nil {
		return cfg.Tests.InvalidBaseFailures
	}
	var out []string
	for _, e := range cfg.Engines {
		out = append(out, defaultInvalidBaseFailures[e.Language]...)
	}
	if cfg.Tests != nil && cfg.Tests.StubEngine != nil {
		out = append(out, defaultInvalidBaseFailures[cfg.Tests.StubEngine.Language]...)
	}
	if len(out) == 0 {
		for _, patterns := range defaultInvalidBaseFailures {
			out = append(out, patterns...)
		}
	}
	return unique(out)
}

type gateInput struct {
	cfg        contract.TestsConfig
	baseSha    string
	tree       string
	tests      []string          // changed test files that exist in the candidate
	overlay    map[string][]byte // --tests-overlay: tests brought by the Saboteur, keyed by relative path; they are in no tree
	sources    []string          // the rest of what changed
	intent     bool              // the change declares the intent to change behavior (enforce_new_behavior waiver)
	changeKind string            // intent.change_kind: feature, bugfix or refactor; "" when not declared
	stubs      map[string][]byte
	symbols    []string                // symbols that received a stub
	unstubbed  []string                // new symbols the engine could not stub
	stubNote   string                  // why there were no stubs, if there were none
	cov        *accumulated            // coverage from the runs overlaid on the candidate (all that leave it)
	engines    []contract.EngineConfig // config.engines: the static listing of the base's tests asks them
	warm       *warmTree               // the warm volume holding the base tree; nil runs export the whole tree
}

// accumulated gathers the coverage left by the runs overlaid on the candidate, from any gate.
type accumulated struct {
	report *coverage.Report
	note   string
}

// run exports treeish to a new writable folder, puts overlay and the fromCandidate files of the candidate on top,
// and runs command. Each run, and each re-run too, starts from a clean tree.
func (a App) run(ctx context.Context, in gateInput, command, treeish string, overlay map[string][]byte, fromCandidate []string, env map[string]string) (run, error) {
	var c run
	defer func(t0 time.Time) {
		timerOf(ctx).testRun(runName(ctx, in, treeish, overlay, fromCandidate), time.Since(t0))
	}(time.Now())
	dir, err := os.MkdirTemp("", "a2dlc-tests-*")
	if err != nil {
		return c, err
	}
	defer func() {
		// the tree can hold files the container wrote as another user: a failed removal is reported, not dropped
		if err := os.RemoveAll(dir); err != nil {
			fmt.Fprintf(os.Stderr, "a2dlc: cannot remove the test tree %s: %v\n", dir, err)
		}
	}()
	var deleted []string
	warm := false
	if in.warm != nil {
		deleted, warm = a.warmDelta(in, treeish, dir)
	}
	if !warm {
		if err := a.Repo.Export(treeish, dir); err != nil {
			return c, err
		}
	}
	write := func(rel string, b []byte) error {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		return os.WriteFile(p, b, 0o644)
	}
	for rel, b := range overlay {
		if err := write(rel, b); err != nil {
			return c, err
		}
	}
	for _, rel := range fromCandidate {
		b, err := a.Repo.Show(in.tree, rel)
		if err != nil {
			return c, err
		}
		if err := write(rel, b); err != nil {
			return c, err
		}
	}
	var r engine.TestRun
	if warm {
		r, err = a.Tests.(engine.WarmTests).RunTestsWarm(ctx, in.cfg, command, env, in.warm.volume, dir, deleted)
	} else {
		r, err = a.Tests.RunTests(ctx, in.cfg, command, env, dir)
	}
	if err != nil {
		return c, err
	}
	c.exit, c.log = r.Exit, r.Log
	if r.JUnit != nil {
		cases, err := junit.Parse(r.JUnit)
		if err != nil {
			return c, fmt.Errorf("unreadable JUnit XML: %v", err)
		}
		c.cases, c.junit, c.raw = cases, true, r.JUnit
	}
	c.coverage = r.Coverage
	if treeish == in.tree {
		in.mergeCoverage(r.Coverage)
	}
	return c, nil
}

// mergeCoverage adds the coverage report a run of the candidate left to the coverage the check accumulates.
func (in gateInput) mergeCoverage(raw []byte) {
	if raw == nil || in.cov == nil {
		return
	}
	report, err := coverage.Parse(raw)
	if err != nil {
		in.cov.note = "unreadable coverage: " + err.Error()
		return
	}
	if in.cov.report == nil {
		in.cov.report = coverage.New()
	}
	in.cov.report.Merge(report)
}

// runName names a test run for verdict.timings: the label the caller set, else candidate or base by the tree it ran.
func runName(ctx context.Context, in gateInput, treeish string, overlay map[string][]byte, fromCandidate []string) string {
	if l := labelOf(ctx); l != "" {
		return l
	}
	switch {
	case treeish == in.tree:
		return "candidate"
	case len(overlay)+len(fromCandidate) > 0:
		return "base_with_overlay"
	}
	return "base"
}

func envTests(tests, sources []string) map[string]string {
	return map[string]string{"A2DLC_TESTS": strings.Join(tests, " "), "A2DLC_CHANGED": strings.Join(sources, " ")}
}

// gateNOP runs the changed tests and the overlay ones on the candidate, on the base as is (to know which ones already
// existed), and on the base with stubs and those tests on top, and judges each new testcase. The overlay ones (the
// ones the Saboteur brings) get the same stub as the candidate's: a test that passes against the stub proves nothing,
// whoever wrote it (measured 2026-10-17: without this the gate said not_applicable and nobody looked at them).
func (a App) gateNOP(ctx context.Context, in gateInput) (contract.Gate, []contract.TestResult) {
	tests := append([]string{}, in.tests...)
	for f := range in.overlay {
		tests = append(tests, f)
	}
	sort.Strings(tests)
	if len(tests) == 0 {
		return contract.Gate{Status: contract.GateNotApplicable, Reason: "no test file changed"}, nil
	}
	env := envTests(tests, in.sources)
	overlaid := map[string][]byte{}
	for f, b := range in.stubs {
		overlaid[f] = b
	}
	for f, b := range in.overlay {
		overlaid[f] = b
	}
	var inBase []string
	for _, f := range in.tests {
		if _, err := a.Repo.Blob(in.baseSha, f); err == nil {
			inBase = append(inBase, f)
		}
	}
	// The candidate run, the NOP run on the stubbed base and the static listing of the base's tests start together: none
	// needs another's result. They are judged below in the order the gate always judged them, so the same input gives the
	// same reason whichever finishes first.
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	defer func() { cancel(); wg.Wait() }()
	type ran struct {
		r   run
		err error
	}
	candCh, nopCh, listCh := make(chan ran, 1), make(chan ran, 1), make(chan listings, 1)
	wg.Add(3)
	go func() {
		defer wg.Done()
		r, err := a.run(withLabel(ctx, "candidate"), in, in.cfg.Command, in.tree, in.overlay, nil, env)
		if err == nil {
			err = r.readable()
		}
		candCh <- ran{r, err}
	}()
	go func() {
		defer wg.Done()
		r, err := a.run(withLabel(ctx, "base_with_stubs"), in, in.cfg.Command, in.baseSha, overlaid, in.tests, env)
		if err == nil && !r.junit {
			err = fmt.Errorf("did not leave JUnit (exit %d): %s", r.exit, r.log)
		}
		nopCh <- ran{r, err}
	}()
	go func() {
		defer wg.Done()
		listCh <- a.listBase(ctx, in, inBase)
	}()
	cand := <-candCh
	candidate, err := cand.r, cand.err
	if err != nil {
		return gateError("candidate", err), nil
	}
	if len(candidate.cases) == 0 {
		return contract.Gate{Status: contract.GateNotApplicable, Reason: "the changed test files have no testcases"}, nil
	}
	// Which candidate testcases already existed on the base: from the static listing; the base is run only for the files
	// the engine could not list, or whose testcases could not be matched to the listing.
	own := map[string]junit.Case{}
	if len(inBase) > 0 {
		listed := <-listCh
		var fallback map[string]bool
		own, fallback = existing(candidate.cases, inBase, listed)
		var need []string
		for _, f := range inBase {
			if _, ok := listed[f]; !ok || fallback[f] {
				need = append(need, f)
			}
		}
		if len(need) > 0 {
			r, err := a.run(withLabel(ctx, "base_as_is"), in, in.cfg.Command, in.baseSha, nil, nil, envTests(need, in.sources))
			if err == nil && !r.junit {
				err = fmt.Errorf("did not leave JUnit (exit %d): %s", r.exit, r.log)
			}
			if err != nil {
				return gateError("base without changes (to know which tests already existed)", err), nil
			}
			for id, c := range r.cases {
				own[id] = c
			}
		}
	}
	nopRan := <-nopCh
	nop, err := nopRan.r, nopRan.err
	if err != nil {
		return gateError("base with stubs", err), nil
	}
	added := addedWithoutStub(in.sources, in.stubs,
		func(f string) bool { _, err := a.Repo.Blob(in.baseSha, f); return err == nil },
		func(f string) bool { _, err := a.Repo.Show(in.tree, f); return err == nil })
	invalid, err := classifier(in.cfg.InvalidBaseFailures, in.unstubbed, added)
	if err != nil {
		return gateError("invalid_base_failures", err), nil
	}
	g := contract.Gate{Status: contract.GateRan, Stubs: in.symbols}
	if in.stubNote != "" {
		g.Reason = in.stubNote
	}
	res := judgeNOP(candidate.cases, own, nop.cases, declaresBehavior(in), invalid)
	for i := range res {
		if c, ok := candidate.cases[res[i].Target]; ok {
			if _, del := in.overlay[c.File]; del {
				res[i].Origin = "overlay"
			}
		}
	}
	return g, res
}

// declaresBehavior says whether the change declares that it changes behavior, so that a new test which passes on the base
// is a FAIL. A feature or a bugfix declares it by its change_kind, without a stub; so do an enforce_new_behavior waiver
// and the stub of a new symbol. A refactor declares neither and keeps the characterization path.
func declaresBehavior(in gateInput) bool {
	return in.intent || len(in.symbols) > 0 || in.changeKind == "feature" || in.changeKind == "bugfix"
}

func gateError(where string, err error) contract.Gate {
	return contract.Gate{Status: contract.GateError, Reason: where + ": " + err.Error()}
}

// addedWithoutStub lists the changed sources that the candidate adds (they are not on the base, they are in the
// candidate) and that no engine stubbed. A test that imports one of them cannot load on the base, and nothing in the
// failure's wording is reliable across runners and languages, so the failure is recognized by naming the file.
func addedWithoutStub(sources []string, stubs map[string][]byte, onBase, inCandidate func(string) bool) []string {
	var out []string
	for _, f := range sources {
		if _, stubbed := stubs[f]; !stubbed && !onBase(f) && inCandidate(f) {
			out = append(out, f)
		}
	}
	return out
}

// fileStem is the name of a file without its directory and extensions: how a module is named by the import that fails.
func fileStem(path string) string {
	name := path[strings.LastIndex(path, "/")+1:]
	stem, _, _ := strings.Cut(name, ".")
	return stem
}

// classifier returns whether the message of a failure on the base makes it invalid for NOP: it matches a pattern from
// the config (import, collection), names a new symbol without a stub, or names a file the candidate adds without a stub
// (as a whole word, case-insensitive; stems under three characters are too common to say anything).
func classifier(patterns, unstubbed, added []string) (func(string) bool, error) {
	var res []*regexp.Regexp
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("pattern %q: %v", p, err)
		}
		res = append(res, re)
	}
	for _, s := range unstubbed {
		short := s[strings.LastIndex(s, ".")+1:]
		res = append(res, regexp.MustCompile(`\b`+regexp.QuoteMeta(short)+`\b`))
	}
	for _, f := range added {
		if stem := fileStem(f); len(stem) >= 3 {
			res = append(res, regexp.MustCompile(`(?i)\b`+regexp.QuoteMeta(stem)+`\b`))
		}
	}
	return func(msg string) bool {
		for _, re := range res {
			if re.MatchString(msg) {
				return true
			}
		}
		return false
	}, nil
}

// judgeNOP applies the NOP/Oracle gate to each new testcase (its id does not exist on the base). Whether an old test
// changes its body cannot be judged without knowing the language: it is left out.
//   - Red on the candidate: FAIL (Oracle).
//   - Valid failure on the base: PASS.
//   - Invalid failure on the base (import, collection, symbol without stub) or absent: INCONCLUSIVE.
//   - Passes on the base: FAIL if the change declares the intent to change behavior (there were stubs or an
//     enforce_new_behavior waiver); otherwise it is a characterization test (a refactor) and does not block.
func judgeNOP(candidate, own, nop map[string]junit.Case, intent bool, invalid func(string) bool) []contract.TestResult {
	var out []contract.TestResult
	for _, id := range keys(candidate) {
		if _, old := own[id]; old {
			continue
		}
		c := candidate[id]
		if c.Status == contract.TestSkipped {
			continue
		}
		b, ok := nop[id]
		if !ok {
			b = junit.Case{Status: contract.TestMissing}
		}
		r := contract.TestResult{Target: id, Gate: contract.GateNOP, Base: b.Status, Candidate: c.Status, Message: b.Message}
		switch {
		case junit.Red(c.Status):
			r.Verdict, r.Reason, r.Message = contract.FAIL, "the new test fails on the candidate", c.Message
		case b.Status == contract.TestFailed && !invalid(b.Message):
			r.Verdict = contract.PASS
		case b.Status == contract.TestPassed && intent:
			r.Verdict, r.Reason = contract.FAIL, "the test passes on the base with stubs: it does not prove the change"
		case b.Status == contract.TestPassed:
			r.Verdict, r.Characterization = contract.PASS, true
		case b.Status == contract.TestSkipped:
			r.Verdict, r.Reason = contract.INCONCLUSIVE, "the test was skipped on the base: it is not known whether it proves the change"
		case b.Status == contract.TestMissing:
			r.Verdict, r.Reason = contract.INCONCLUSIVE, "the test did not appear on the base (collection or import error): stub missing"
		default:
			r.Verdict, r.Reason = contract.INCONCLUSIVE, "fails on the base due to import, collection or a symbol without a stub, not because of the test: "+b.Message
		}
		out = append(out, r)
	}
	return out
}

// gateBaseline runs baseline_command on the base (cache keyed by base and selection) and on the candidate; each new
// red is re-run k times on both sides before blocking.
func (a App) gateBaseline(ctx context.Context, in gateInput, alreadyJudged map[string]bool) (contract.Gate, []contract.TestResult) {
	cmd := in.cfg.BaselineCommand
	if cmd == "" {
		return contract.Gate{Status: contract.GateNotConfigured, Reason: "tests.baseline_command is missing: the baseline does not run"}, nil
	}
	if len(in.tests)+len(in.sources) == 0 {
		return contract.Gate{Status: contract.GateNotApplicable, Reason: "nothing changed"}, nil
	}
	env := envTests(in.tests, in.sources)
	g := contract.Gate{Status: contract.GateRan}
	digest, err := a.imageDigest(ctx, in.cfg.Image)
	if err != nil {
		return gateError("image digest", err), nil
	}
	// Without a digest the cache key would not name the image: a moved local tag could hand back an old result. Stage −1
	// refuses the same case (CB-05); a run that skips Stage −1 gets the same refusal here.
	if digest == "" {
		return gateError("image digest", fmt.Errorf("tests.image %q has no RepoDigests (a local build that was never pushed): the baseline cannot be keyed to it", in.cfg.Image)), nil
	}
	base, cached, err := a.suite(ctx, in, cmd, in.baseSha, digest, env)
	if err != nil {
		return gateError("base", err), nil
	}
	g.Cached = cached
	candidate, cached, err := a.suite(ctx, in, cmd, in.tree, digest, env)
	if err != nil {
		return gateError("candidate", err), nil
	}
	g.CandidateCached = cached
	newOnes := newReds(base.cases, candidate.cases, alreadyJudged)
	if len(newOnes) == 0 {
		return g, nil
	}
	k := in.cfg.Reruns
	if k == 0 {
		k = 3
	}
	var baseRuns, candRuns []map[string]junit.Case
	for i := 0; i < k; i++ {
		x, err := a.run(ctx, in, cmd, in.baseSha, nil, nil, env)
		if err != nil {
			return gateError(fmt.Sprintf("re-run %d on the base", i+1), err), nil
		}
		y, err := a.run(ctx, in, cmd, in.tree, nil, nil, env)
		if err != nil {
			return gateError(fmt.Sprintf("re-run %d on the candidate", i+1), err), nil
		}
		baseRuns, candRuns = append(baseRuns, x.cases), append(candRuns, y.cases)
	}
	return g, judgeReds(newOnes, base.cases, candidate.cases, baseRuns, candRuns)
}

// newReds are the red ids in the candidate that were not red on the base. A red already present before does not
// block ("does not worsen").
func newReds(base, candidate map[string]junit.Case, alreadyJudged map[string]bool) []string {
	var out []string
	for _, id := range keys(candidate) {
		if alreadyJudged[id] || !junit.Red(candidate[id].Status) || junit.Red(base[id].Status) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// judgeReds decides each new red with its re-runs. It is flaky only if it is also intermittent on the base (it passes
// and fails between the run and the re-runs): it does not block and quarantine is proposed. A stable green on the
// base and red on the candidate blocks even if the red is intermittent: it may be a race the change brought in.
func judgeReds(ids []string, base, candidate map[string]junit.Case, baseRuns, candRuns []map[string]junit.Case) []contract.TestResult {
	state := func(m map[string]junit.Case, id string) string {
		if c, ok := m[id]; ok {
			return c.Status
		}
		return contract.TestMissing
	}
	var out []contract.TestResult
	for _, id := range ids {
		r := contract.TestResult{Target: id, Gate: contract.GateBaseline, Base: state(base, id), Candidate: candidate[id].Status,
			Message: candidate[id].Message, Reruns: &contract.Reruns{Base: []string{}, Candidate: []string{}}}
		green, red := r.Base == contract.TestPassed, false
		for i := range baseRuns {
			eb, ec := state(baseRuns[i], id), state(candRuns[i], id)
			r.Reruns.Base, r.Reruns.Candidate = append(r.Reruns.Base, eb), append(r.Reruns.Candidate, ec)
			green, red = green || eb == contract.TestPassed, red || junit.Red(eb)
		}
		if green && red {
			r.Verdict, r.Flaky = contract.PASS, true
		} else {
			r.Verdict, r.Reason = contract.FAIL, "new red: stable on the base, fails on the candidate"
		}
		out = append(out, r)
	}
	return out
}

func keys(m map[string]junit.Case) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// suiteKey identifies a run of the baseline command by the content of the tree it ran on, not by the commit that holds it:
// the tree the previous step's candidate left is the base of the next step whatever commits carry it. The image digest is
// part of it: a tag that moved to another image is another environment, and a cached result from the old one must not stand
// for the new one. The selection ($A2DLC_TESTS, $A2DLC_CHANGED) is part of it only when the command names one of them: a
// baseline that runs the whole suite gives the same answer whichever files changed.
func suiteKey(tree string, in gateInput, cmd, digest string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s", tree, in.cfg.Image, digest, cmd)
	if strings.Contains(cmd, "A2DLC_TESTS") || strings.Contains(cmd, "A2DLC_CHANGED") {
		fmt.Fprintf(h, "\x00%s\x00%s", strings.Join(in.tests, " "), strings.Join(in.sources, " "))
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// suite is the run of the baseline command on treeish (a commit or a tree): from the cache when the same tree content already
// answered it with the same command and image, else by running it and keeping the answer. A hit on the candidate's tree gives
// its coverage again, so the check judges the same lines it would have judged after running. The re-runs that tell a flaky
// test apart do not come here: each of those must run.
func (a App) suite(ctx context.Context, in gateInput, cmd, treeish, digest string, env map[string]string) (run, bool, error) {
	tree, err := a.Repo.TreeID(treeish)
	if err != nil {
		return run{}, false, err
	}
	key := suiteKey(tree, in, cmd, digest)
	if c, ok := a.readCache(key); ok {
		if treeish == in.tree {
			in.mergeCoverage(c.coverage)
		}
		return c, true, nil
	}
	r, err := a.run(ctx, in, cmd, treeish, nil, nil, env)
	if err == nil {
		err = r.readable()
	}
	if err != nil {
		return r, false, err
	}
	a.saveCache(key, r)
	return r, false, nil
}

type cacheBaseline struct {
	Exit       int    `json:"exit"`
	JUnit      string `json:"junit"`
	Coverage   []byte `json:"coverage,omitempty"`
	Reproduced bool   `json:"reproduced,omitempty"`
}

func (a App) readCache(key string) (run, bool) {
	dir := a.cacheDir()
	if dir == "" {
		return run{}, false
	}
	b, err := os.ReadFile(filepath.Join(dir, "suite-"+key+".json"))
	if err != nil {
		return run{}, false
	}
	var c cacheBaseline
	if json.Unmarshal(b, &c) != nil {
		return run{}, false
	}
	cases, err := junit.Parse([]byte(c.JUnit))
	if err != nil {
		return run{}, false
	}
	return run{cases: cases, exit: c.Exit, junit: true, raw: []byte(c.JUnit), coverage: c.Coverage, reproduced: c.Reproduced}, true
}

func (a App) saveCache(key string, c run) {
	// The cache stores the XML, not the parsed result: this way a parser change does not leave an old cache with a
	// different meaning.
	dir := a.cacheDir()
	if c.raw == nil || dir == "" || os.MkdirAll(dir, 0o755) != nil {
		return
	}
	b, _ := json.Marshal(cacheBaseline{Exit: c.exit, JUnit: string(c.raw), Coverage: c.coverage, Reproduced: c.reproduced})
	// written whole or not at all: another worktree of the repository may be reading the same key
	tmp, err := os.CreateTemp(dir, "suite-*.tmp")
	if err != nil {
		return
	}
	_, werr := tmp.Write(b)
	if cerr := tmp.Close(); werr != nil || cerr != nil || os.Rename(tmp.Name(), filepath.Join(dir, "suite-"+key+".json")) != nil {
		os.Remove(tmp.Name())
	}
}

// stubs asks each engine for the base with stubs for what the candidate added in its sources. An engine that does not
// produce them stops nothing: NOP runs without stubs and import failures remain INCONCLUSIVE. The stub engine of
// config.tests comes last: it gets the sources no reviewing engine claimed, and only for stubs.
func (a App) stubs(ctx context.Context, cfg contract.Config, in *gateInput, baseDir, candDir string) {
	in.stubs = map[string][]byte{}
	engines := cfg.Engines
	if cfg.Tests != nil && cfg.Tests.StubEngine != nil {
		engines = append(append([]contract.EngineConfig{}, cfg.Engines...), *cfg.Tests.StubEngine)
	}
	byEngine, _ := verdict.Assign(engines, in.sources)
	var notes []string
	for _, i := range sortedIndexes(byEngine) {
		e := engines[i]
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: in.baseSha,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: byEngine[i], Budget: json.RawMessage("{}")}
		t0 := time.Now()
		resp, files, err := a.Runner.Stub(ctx, e, req, baseDir, candDir)
		timerOf(ctx).engineOp("stub", time.Since(t0))
		if err != nil {
			notes = append(notes, e.Language+": "+err.Error())
			continue
		}
		for f, b := range files {
			in.stubs[f] = b
		}
		for _, s := range resp.Stubbed {
			for _, x := range s.Symbols {
				in.symbols = append(in.symbols, s.File+"::"+x)
			}
		}
		in.unstubbed = append(in.unstubbed, resp.Unstubbed...)
	}
	sort.Strings(in.symbols)
	if len(notes) > 0 {
		in.stubNote = "no stubs from " + strings.Join(notes, "; ")
	}
}

var _ engine.TestRunner = engine.DockerTests{}
