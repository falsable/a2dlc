// Package app holds the a2dlc commands: check, verify, approve, hook and install.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/verdict"
)

const (
	dirA2dlc      = ".a2dlc"
	pathConfig    = ".a2dlc/config.json"
	pathWaivers   = ".a2dlc/waivers.json"
	pathApprovals = ".a2dlc/approvals.json"
	pathVerdict   = ".a2dlc/verdict.json"
	pathReports   = ".a2dlc/reports"
	pathRun       = ".a2dlc/run"
	treePrefix    = "git-tree:"
)

type App struct {
	Repo    gitx.Repo
	Runner  engine.Runner
	Tests   engine.TestRunner  // runs config.tests; nil leaves the test gates in error
	Checks  engine.CheckRunner // runs config.checks; nil leaves every check in error (never a pass)
	Version string
	Now     func() time.Time
}

func (a App) path(rel string) string { return filepath.Join(a.Repo.Dir, filepath.FromSlash(rel)) }

// read decodes a JSON file from the working tree (commit == "") or from a commit. If it does not exist, it returns os.ErrNotExist.
func (a App) read(commit, rel string, v any) error {
	var b []byte
	var err error
	if commit == "" {
		b, err = os.ReadFile(a.path(rel))
	} else {
		b, err = a.Repo.Show(commit, rel)
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return os.ErrNotExist
		}
		return err
	}
	if err := contract.DecodeStrictBytes(b, v); err != nil {
		return fmt.Errorf("%s is out of contract: %w", rel, err)
	}
	return nil
}

func (a App) write(rel string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(a.path(rel)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(a.path(rel), append(b, '\n'), 0o644)
}

func (a App) waivers(commit string) (map[string]contract.Waiver, error) {
	var w contract.Waivers
	m := map[string]contract.Waiver{}
	if err := a.read(commit, pathWaivers, &w); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return m, nil
		}
		return nil, err
	}
	for _, x := range w.Waivers {
		m[x.Target] = x
	}
	return m, nil
}

func (a App) approvals(commit string) ([]contract.Approval, error) {
	var ap contract.Approvals
	if err := a.read(commit, pathApprovals, &ap); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return ap.Approvals, nil
}

// approved returns the targets whose waiver has a valid signature over the current blob of their file in tree, and
// the rejection reason of each one that does not.
func (a App) approved(tree string, waivers map[string]contract.Waiver, approvals []contract.Approval) (map[string]bool, map[string]string) {
	ok, reason := map[string]bool{}, map[string]string{}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	for _, ap := range approvals {
		w, found := waivers[ap.Target]
		switch {
		case !found:
			reason[ap.Target] = "the approval has no waiver"
			continue
		case approval.WaiverSHA(w) != ap.WaiverSHA256:
			reason[ap.Target] = "the waiver changed after it was signed"
			continue
		}
		blob, errb := a.Repo.Blob(tree, contract.TargetFile(ap.Target))
		if errb != nil || blob != ap.FileBlob {
			reason[ap.Target] = "the file changed after signing: it must be approved again"
			continue
		}
		if err != nil {
			reason[ap.Target] = err.Error()
			continue
		}
		if e := approval.Verify(signers, ap.Principal, approval.Payload(ap.Target, ap.WaiverSHA256, ap.FileBlob), ap.Signature); e != nil {
			reason[ap.Target] = e.Error()
			continue
		}
		ok[ap.Target] = true
		delete(reason, ap.Target)
	}
	return ok, reason
}

// Config reads .a2dlc/config.json (a2dlc run uses it to find where the tests live and which command runs them).
func (a App) Config() (contract.Config, error) { return a.config() }

// Partition splits the files that matched some glob from those that did not (a2dlc run uses it to accept only
// Saboteur tests that live in tests.paths).
func Partition(globs, files []string) (matched, rest []string) { return partition(globs, files) }

// config reads the working tree's config (the commands that do not judge a diff).
func (a App) config() (contract.Config, error) { return a.configAt("") }

// configAt reads the config from commit, or from the working tree when commit is "". check judges a diff with the
// base's config: a working tree that drops a glob must not take its files out of the engines' scope.
func (a App) configAt(commit string) (contract.Config, error) {
	var c contract.Config
	if err := a.read(commit, pathConfig, &c); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if commit != "" {
				return c, fmt.Errorf("%s is missing in the base %s: check reads the config from the base, so the base must have one (commit it, then check again)", pathConfig, short(commit))
			}
			return c, fmt.Errorf("%s is missing: run a2dlc install", pathConfig)
		}
		return c, err
	}
	if c.SchemaVersion != contract.SchemaVersion {
		return c, fmt.Errorf("%s: schema_version %d", pathConfig, c.SchemaVersion)
	}
	if c.Integration != nil {
		if err := c.Integration.Validate(); err != nil {
			return c, fmt.Errorf("%s: %v", pathConfig, err)
		}
	}
	return c, nil
}

// baseConfig is the config check judges with: the base's. A repo adopting a2dlc commits its first config while the
// base has none, so when the config never existed in the base's history the candidate's is used (there is no earlier
// scope to narrow). If it existed and was deleted, only a byte-identical restore of its last version is accepted:
// deleting it must not let a weaker one in.
func (a App) baseConfig(baseSha, candidate string) (contract.Config, error) {
	cfg, note, err := a.baseConfigNote(baseSha, candidate)
	if note != "" {
		fmt.Fprintln(os.Stderr, note)
	}
	return cfg, err
}

// baseConfigNote is baseConfig without printing: the note about a first adoption or a restored config comes back as text.
func (a App) baseConfigNote(baseSha, candidate string) (contract.Config, string, error) {
	if _, err := a.Repo.Blob(baseSha, pathConfig); err == nil {
		cfg, err := a.configAt(baseSha)
		return cfg, "", err
	}
	last, err := a.Repo.LastBlob(baseSha, pathConfig)
	if err != nil {
		return contract.Config{}, "", fmt.Errorf("%s is missing in the base %s: %v", pathConfig, short(baseSha), err)
	}
	note := "never had %s: judging with the candidate's (first adoption)"
	if last != "" {
		var cand string
		if candidate == "" {
			cand, err = a.Repo.WorktreeBlob(pathConfig)
		} else {
			cand, err = a.Repo.Blob(candidate, pathConfig)
		}
		if err != nil || cand != last {
			return contract.Config{}, "", fmt.Errorf("%s is missing in the base %s but existed in its history: check only accepts it back unchanged (restore its last version, blob %s, then check again)", pathConfig, short(baseSha), short(last))
		}
		note = "had %s deleted: judging with the candidate's, an unchanged restore of its last version"
	}
	cfg, err := a.configAt(candidate)
	if err != nil {
		return cfg, "", err
	}
	return cfg, fmt.Sprintf("a2dlc: the base %s "+note, short(baseSha), pathConfig), nil
}

// testsBlockIgnored is the coverage reason when the diff adds config.tests to a base that has none (a2dlc-31w). The
// base's config judges, deliberately: a candidate that supplied its own test command and coverage report could raise its
// own verdict. The block takes effect once it is in the base.
const testsBlockIgnored = "the candidate's " + pathConfig + " adds a tests block that the base's config does not have, and check judges with the base's config: " +
	"no test gate ran and no test coverage counts. Land the config change on its own first (push it, or pass it as --base), then check again"

// candidateAddsTests says whether the candidate's config has a tests block that the config in use (the base's) lacks.
func (a App) candidateAddsTests(cfg contract.Config, candidate string) bool {
	if cfg.Tests != nil {
		return false
	}
	c, err := a.configAt(candidate)
	return err == nil && c.Tests != nil
}

func generators(candidate string) *string {
	m, _ := filepath.Glob(filepath.Join(candidate, dirA2dlc, "arbs.*"))
	if len(m) != 1 {
		return nil
	}
	s := dirA2dlc + "/" + filepath.Base(m[0])
	return &s
}

// defaultBase is the pushed base if there is one (so the verdict covers everything not yet pushed, not only what is
// uncommitted), and otherwise HEAD.
// DefaultBase is the base when none was given: the upstream, else the fork point from origin's default branch, else
// HEAD (a2dlc run also uses it).
func (a App) DefaultBase() string { return a.defaultBase() }

func (a App) defaultBase() string {
	if u := a.pushedBase(); u != "" {
		return u
	}
	return "HEAD"
}

// pushedBase is the upstream or, on a branch never pushed (an agent's worktree), where it forks from origin's default
// branch. Against HEAD, a commit made in the worktree would leave the diff and pass unjudged.
func (a App) pushedBase() string {
	if u := a.Repo.Upstream(); u != "" {
		return u
	}
	return a.Repo.RemoteDefaultBase()
}

// CheckOptions are the extra trees check looks at: Peers, commits of other builders on the same change (their tests
// run overlaid on the candidate, and the candidate's tests overlaid on them); TestsOverlay, a folder of test files,
// under the path they would have in the repo, which come from outside (the Inquisitor) and run overlaid on all trees
// without being committed. CandidateTree, a commit or tree that is judged instead of the working tree (a2dlc run: the
// builder's commit); empty means the working tree. The config is read from the base commit (a working tree cannot
// narrow what is judged); the waivers and approvals are read from the working tree, and the verdict is also written
// to .a2dlc/verdict.json, with the candidate's tree_hash.
type CheckOptions struct {
	Peers         []string
	TestsOverlay  string
	CandidateTree string
	// EquivalenceRelaxations (block 7): pi_s.relaxations of S*, which a2dlc run passes to the engines in the diff.
	EquivalenceRelaxations []string
	// ChangeKind is intent.change_kind (feature, bugfix or refactor; "" when not declared). A feature or a bugfix declares
	// a behavior change, so a new test that passes on the base is a FAIL (gateNOP, declaresBehavior).
	ChangeKind string
	// Golden is the golden master recorded on the base in Stage -1 (golden.json). When set, its cases are replayed on the
	// candidate and each difference is a FAIL target golden::<case id>, waivable with enforce_new_behavior.
	Golden *contract.Golden
	// FireOn is the firing point of config.checks: "" or "check" for a2dlc check; a2dlc run passes "stage:<id>", and only
	// the checks whose fire_on lists it run.
	FireOn string
	// StrictDivergence keeps a divergence on any code a FAIL, as if everything were critical. a2dlc run sets it: the
	// Stage 4 oracle ranks candidates by their verdict, and a note would let a candidate that changes the base's behavior
	// pass. a2dlc check leaves it off, and config.critical_code decides there.
	StrictDivergence bool
}

// allCritical makes every target critical (CheckOptions.StrictDivergence).
var allCritical = []string{"**"}

// candidateTree is the tree that check judges: opts.CandidateTree without the gate outputs, or the working tree's.
func (a App) candidateTree(candidate string) (string, error) {
	if candidate == "" {
		return a.Repo.WorktreeTree()
	}
	t, err := a.Repo.CommitTree(candidate)
	if err != nil {
		return "", fmt.Errorf("candidate %q: %w", candidate, err)
	}
	return t, nil
}

// Check runs the engines overlaid on what changed from base to the working tree, and writes the verdict. An empty
// base is the upstream, else the fork point from origin's default branch, else HEAD.
func (a App) Check(ctx context.Context, base string) (contract.Verdict, error) {
	return a.CheckWith(ctx, base, CheckOptions{})
}

func (a App) CheckWith(ctx context.Context, base string, opts CheckOptions) (contract.Verdict, error) {
	v := contract.Verdict{SchemaVersion: contract.SchemaVersion, A2dlcVersion: a.Version}
	ctx, tm := withTimer(ctx)
	if base == "" {
		base = a.defaultBase()
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return v, fmt.Errorf("base %q: %w", base, err)
	}
	cfg, err := a.baseConfig(baseSha, opts.CandidateTree)
	if err != nil {
		return v, err
	}
	// The golden master lives in config.tests: a record with no tests block cannot be replayed in the test gates, so it is
	// refused rather than dropped.
	if opts.Golden != nil && cfg.Tests == nil {
		return v, fmt.Errorf("a golden master was recorded but %s declares no tests block: the replay has no gate to run in", pathConfig)
	}
	var peers []tree
	for _, p := range opts.Peers {
		sha, err := a.Repo.ResolveCommit(p)
		if err != nil {
			return v, fmt.Errorf("peer %q: %w", p, err)
		}
		peers = append(peers, tree{"peer:" + short(sha)[:8], sha})
	}
	if opts.TestsOverlay != "" {
		if st, err := os.Stat(opts.TestsOverlay); err != nil || !st.IsDir() {
			return v, fmt.Errorf("--tests-overlay %q is not a folder", opts.TestsOverlay)
		}
	}
	tree, err := a.candidateTree(opts.CandidateTree)
	if err != nil {
		return v, err
	}
	changed, deleted, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return v, err
	}
	// protected_paths: every changed or deleted file that matches one of the base's globs, whatever engine or test
	// gate would judge it.
	guarded := protectedMatches(cfg.ProtectedPaths, append(append([]string(nil), changed...), deleted...))
	// With config.tests, test files do not go to the engines: the test gates judge them.
	sources, changedTests := changed, []string(nil)
	if cfg.Tests != nil {
		changedTests, sources = partition(cfg.Tests.Paths, changed)
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, sources)
	targets := []contract.TargetResult{}
	oos := []contract.OutOfScope{}
	for _, f := range noEngine {
		oos = append(oos, contract.OutOfScope{File: f, Reason: "no_engine"})
	}
	for _, f := range deleted {
		oos = append(oos, contract.OutOfScope{File: f, Reason: "deleted"})
	}
	var baseDir, candDir string
	var exportOnce sync.Once
	var exportErr error
	// export prepares baseDir and candDir once; every caller sees the same error. It is called from the engine operations
	// and the test gates, which now run together, and each reads the folders only after export returned.
	export := func() error {
		exportOnce.Do(func() {
			defer func(t0 time.Time) { tm.sync(time.Since(t0)) }(time.Now())
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
			os.RemoveAll(baseDir)
		}
		if candDir != "" {
			os.RemoveAll(candDir)
		}
	}()
	var errs []contract.EngineError
	var diffBudget *contract.DiffBudget
	if covered := len(sources) - len(noEngine); cfg.MaxFiles > 0 && covered > cfg.MaxFiles {
		// Over the limit nothing is run and the output says what was left unrun: it is never skipped silently (§4.4).
		for _, i := range sortedIndexes(byEngine) {
			errs = append(errs, contract.EngineError{Language: cfg.Engines[i].Language, Files: byEngine[i],
				Error: fmt.Sprintf("not run: %d covered files exceed max_files=%d", covered, cfg.MaxFiles)})
		}
		byEngine = nil
	}
	waivers, err := a.waivers("")
	if err != nil {
		return v, err
	}
	approvals, err := a.approvals("")
	if err != nil {
		return v, err
	}
	// A waiver covers only the targets of its class: engine targets do not cover a test, nor the reverse.
	waived, intent := waiverFlags(waivers)
	approved, _ := a.approved(tree, waivers, approvals)
	runEngines := func() error {
		if len(byEngine) > 0 {
			if err := export(); err != nil {
				return err
			}
			for _, i := range sortedIndexes(byEngine) {
				e, files := cfg.Engines[i], byEngine[i]
				req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
					BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: e.Budget, GeneratorsOverride: generators(candDir),
					EquivalenceRelaxations: opts.EquivalenceRelaxations}
				if len(req.Budget) == 0 {
					req.Budget = json.RawMessage("{}")
				}
				t0 := time.Now()
				resp, err := a.Runner.Run(ctx, e, req, baseDir, candDir)
				timerOf(ctx).engineOp("diff", time.Since(t0))
				if err != nil {
					errs = append(errs, contract.EngineError{Language: e.Language, Files: files, Error: err.Error()})
					continue
				}
				// equivalence_unsupported: the engine did not apply some relaxation of pi_s, so the judgment of this
				// response is not the one the relaxations asked for. The relaxation is diff-wide, so every target it
				// judged is INCONCLUSIVE, and the reason names the relaxations.
				if len(resp.EquivalenceUnsupported) > 0 {
					reason := "the engine did not apply the equivalence relaxation(s) " + strings.Join(resp.EquivalenceUnsupported, "; ") + ": the target cannot be decided"
					for i := range resp.Targets {
						resp.Targets[i].Verdict, resp.Targets[i].Reason = contract.INCONCLUSIVE, reason
					}
				}
				// diff_budget: the engine's time budget adds up over the engines, and each function it cut is INCONCLUSIVE
				// where the engine answered it (a FAIL is still a finding).
				if b := resp.DiffBudget; b != nil {
					diffBudget = verdict.MergeDiffBudget(diffBudget, b)
					verdict.MarkCut(resp.Targets, b.Cut)
				}
				targets = append(targets, resp.Targets...)
				oos = append(oos, resp.OutOfScope...)
			}
		}
		return nil
	}
	// The engines (equivalence) and the test gates share nothing but the exported trees, so they run side by side. The line
	// classification joins them: it needs the candidate's coverage, not the engines' answer, and classifies every line the
	// candidate's tests left unexecuted; the lines the engines examined are dropped when the two are joined.
	var engErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		engErr = runEngines()
	}()
	cov := &accumulated{}
	var testGates *contract.TestGates
	var testResults []contract.TestResult
	var classified lineNotes
	if cfg.Tests != nil {
		tcfg := *cfg.Tests
		tcfg.InvalidBaseFailures = invalidBaseFailures(cfg)
		in := gateInput{cfg: tcfg, baseSha: baseSha, tree: tree, tests: changedTests, sources: sources, intent: intent, changeKind: opts.ChangeKind, cov: cov, engines: cfg.Engines}
		testGates, testResults = a.gates(ctx, cfg, in, export, &baseDir, &candDir, peers, opts.TestsOverlay)
		if opts.Golden != nil {
			testResults = append(testResults, a.goldenResults(ctx, in, *opts.Golden)...)
		}
		if cov.report != nil {
			if pre, err := a.precondition(cfg, baseSha, tree, sources, nil, cov.report, cov.note); err == nil {
				classified = a.classifyLines(ctx, cfg, pre, export, &baseDir, &candDir)
			}
		}
	}
	wg.Wait()
	if engErr != nil {
		return v, engErr
	}
	critical := cfg.CriticalCode
	if opts.StrictDivergence {
		critical = allCritical
	}
	v.Verdict, v.Targets, v.PendingWaivers = verdict.Aggregate(targets, waived, approved, len(errs), critical)
	v.BaseRef, v.TreeHash, v.OutOfScope, v.EngineErrors = baseSha, treePrefix+tree, oos, errs
	v.DiffBudget = diffBudget
	if cfg.Tests != nil {
		v.TestGates, v.Tests = testGates, testResults
		addTests(&v, waivers, approved)
	}
	// Pluggable checks (protocol §5 Checks): they lower the verdict, never raise it; what did not run stays in view.
	fireOn := opts.FireOn
	if fireOn == "" {
		fireOn = FireOnCheck
	}
	checks, pendingChecks := a.runChecks(ctx, cfg, fireOn, changed, export, &baseDir, &candDir, waivers, approved)
	applyChecks(&v, checks, pendingChecks)
	// Coverage precondition: what no engine examined and no test executed cannot be PASS.
	if v.Coverage, err = a.precondition(cfg, baseSha, tree, sources, v.Targets, cov.report, cov.note); err != nil {
		return v, err
	}
	if v.Coverage.Status == contract.CoverageNotConfigured && a.candidateAddsTests(cfg, opts.CandidateTree) {
		v.Coverage.Reason = testsBlockIgnored
	}
	if classified != nil {
		applyNotes(v.Coverage, classified)
	} else {
		a.noteUncovered(ctx, cfg, v.Coverage, export, &baseDir, &candDir)
	}
	if v.Verdict == contract.PASS && hasUncovered(v.Coverage) {
		v.Verdict = contract.INCONCLUSIVE
	}
	// A function the per-diff budget cut and no engine answered was not judged: the diff cannot PASS.
	if v.Verdict == contract.PASS && verdict.HasCut(v.DiffBudget) {
		v.Verdict = contract.INCONCLUSIVE
	}
	// A protected file without a valid approval cannot pass; FAIL stays FAIL.
	protectedEsc := []contract.Escalation{}
	for _, f := range sortedKeys(guarded) {
		if approved[f+protectedSuffix] {
			continue
		}
		if v.Verdict == contract.PASS {
			v.Verdict = contract.INCONCLUSIVE
		}
		g := guarded[f]
		summary := fmt.Sprintf("%s matches protected_paths %s and has no valid approval: write a waiver for %s in %s, then a person runs a2dlc approve %s",
			f, g, f+protectedSuffix, pathWaivers, f+protectedSuffix)
		protectedEsc = append(protectedEsc, escalation("protected_path", f+protectedSuffix, summary,
			contract.Evidence{Message: f + " matches protected_paths glob " + g}))
	}
	v.CreatedAt = a.Now().UTC().Format(time.RFC3339)
	verdict.CompleteDiffBudget(&v)
	v.Timings = tm.result()
	if err := a.write(pathVerdict, v); err != nil {
		return v, err
	}
	return v, a.write(pathEscalations, contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: v.TreeHash,
		CreatedAt: v.CreatedAt, Items: append(escalations(v), protectedEsc...)})
}

// waiverFlags splits the waivers in two: the engine targets they waive (a test waiver covers no engine target, and the
// reverse), and whether some waiver asks to enforce new behavior, which the tests_diff gate reads. A file-level
// waiver (target "file::*") is the approval of a protected path: it covers no target and sets no intent. An
// accept_finding covers a finding of a pluggable check and no engine target.
func waiverFlags(waivers map[string]contract.Waiver) (waived map[string]bool, intent bool) {
	waived = map[string]bool{}
	for t, w := range waivers {
		if strings.HasSuffix(t, protectedSuffix) {
			continue
		}
		if !contract.IsTestDecision(w.Decision) && w.Decision != contract.AcceptFinding {
			waived[t] = true
		}
		intent = intent || w.Decision == contract.EnforceNewBehavior
	}
	return waived, intent
}

// protectedSuffix names the file-level target of a protected path (the form an uncovered file's escalation also uses).
const protectedSuffix = "::*"

// protectedMatches maps each file that matches a protected_paths glob to the first glob that matches it.
func protectedMatches(globs, files []string) map[string]string {
	out := map[string]string{}
	for _, f := range files {
		for _, g := range globs {
			if verdict.Match(g, f) {
				out[f] = g
				break
			}
		}
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// protectedNotes lists the protected_path escalations of the last check, for the agent hook's message.
func (a App) protectedNotes() string {
	var e contract.Escalations
	if a.read("", pathEscalations, &e) != nil {
		return ""
	}
	var b strings.Builder
	for _, x := range e.Items {
		if x.Kind == "protected_path" {
			fmt.Fprintf(&b, "- %s\n", x.Summary)
		}
	}
	return b.String()
}

// partition splits the files that matched some glob from those that did not.
func partition(globs, files []string) (matched, rest []string) {
	for _, f := range files {
		si := false
		for _, g := range globs {
			if verdict.Match(g, f) {
				si = true
				break
			}
		}
		if si {
			matched = append(matched, f)
		} else {
			rest = append(rest, f)
		}
	}
	return matched, rest
}

// gates runs NOP/Oracle, the baseline and, with peers or an overlay, tests_diff. Stubs are only needed if some test
// changed.
func (a App) gates(ctx context.Context, cfg contract.Config, in gateInput, export func() error, baseDir, candDir *string, peers []tree, overlay string) (*contract.TestGates, []contract.TestResult) {
	if a.Tests == nil {
		e := contract.Gate{Status: contract.GateError, Reason: "this binary has no test runner"}
		return &contract.TestGates{NOPOracle: e, Baseline: e}, nil
	}
	overlayFiles, err := readOverlay(overlay)
	if err != nil {
		e := gateError("--tests-overlay", err)
		return &contract.TestGates{NOPOracle: e, Baseline: e}, nil
	}
	in.overlay = overlayFiles
	if len(in.tests)+len(in.overlay) > 0 || in.cfg.BaselineCommand != "" {
		in.warm = a.syncWarm(ctx, in)
	}
	if len(in.tests)+len(in.overlay) > 0 && len(in.sources) > 0 {
		if err := export(); err != nil {
			e := gateError("export base and candidate", err)
			return &contract.TestGates{NOPOracle: e, Baseline: e}, nil
		}
		a.stubs(ctx, cfg, &in, *baseDir, *candDir)
	}
	gNOP, rNOP := a.gateNOP(ctx, in)
	judged := map[string]bool{}
	for _, r := range rNOP {
		judged[r.Target] = true
	}
	gBase, rBase := a.gateBaseline(ctx, in, judged)
	gDiff, rDiff := a.gateTestsDiff(ctx, in, peers, overlay)
	return &contract.TestGates{NOPOracle: gNOP, Baseline: gBase, TestsDiff: &gDiff}, append(append(rNOP, rBase...), rDiff...)
}

// addTests crosses the tests with their waivers and redoes the verdict: FAIL if some FAIL test has no waiver for its
// class; otherwise INCONCLUSIVE if any test is inconclusive or a gate could not run. characterization_test covers NOP,
// quarantine_flaky covers the baseline, and enforce_new_behavior covers a test from another tree that the candidate
// fails on purpose (tests_diff, and in NOP the overlay ones from the Saboteur: the spec of the test and the candidate's
// differ) and a golden master case whose output the candidate changed on purpose (golden::<id>); without a signature, the waiver goes to pending and verify blocks.
func addTests(v *contract.Verdict, waivers map[string]contract.Waiver, approved map[string]bool) {
	fail, inc := v.Verdict == contract.FAIL, v.Verdict == contract.INCONCLUSIVE
	pending := map[string]bool{}
	for _, t := range v.PendingWaivers {
		pending[t] = true
	}
	for i := range v.Tests {
		t := &v.Tests[i]
		w, ok := waivers[t.Target]
		// A flaky test is PASS, but its signed quarantine also covers it: otherwise it would be escalated again on every run.
		t.Waived = ok && (t.Verdict != contract.PASS || t.Flaky) &&
			(w.Decision == contract.CharacterizationTest && t.Gate == contract.GateNOP || w.Decision == contract.QuarantineFlaky && t.Gate == contract.GateBaseline ||
				w.Decision == contract.EnforceNewBehavior && (t.Gate == contract.GateTestsDiff || t.Gate == contract.GateNOP && t.Origin == "overlay" || t.Gate == GateGolden))
		switch {
		case t.Waived && !approved[t.Target]:
			pending[t.Target] = true
		case t.Waived:
		case t.Verdict == contract.FAIL:
			fail = true
		case t.Verdict == contract.INCONCLUSIVE:
			inc = true
		}
	}
	inc = inc || v.TestGates.NOPOracle.Status == contract.GateError || v.TestGates.Baseline.Status == contract.GateError ||
		v.TestGates.TestsDiff != nil && v.TestGates.TestsDiff.Status == contract.GateError
	v.PendingWaivers = v.PendingWaivers[:0]
	for t := range pending {
		v.PendingWaivers = append(v.PendingWaivers, t)
	}
	sort.Strings(v.PendingWaivers)
	switch {
	case fail:
		v.Verdict = contract.FAIL
	case inc:
		v.Verdict = contract.INCONCLUSIVE
	default:
		v.Verdict = contract.PASS
	}
}

// Verify is the strict policy (a2dlc verify --strict, and the MCP verify tool): it checks in milliseconds, without
// engines, that the receipt is valid for the commit: PASS, not stale, and each waiver used with a valid signature. It
// returns the problems; none means it passes. The pre-push uses VerifyPolicy, which blocks only on evidence.
func (a App) Verify(commit string) ([]string, error) {
	var v contract.Verdict
	if err := a.read("", pathVerdict, &v); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []string{"there is no " + pathVerdict + ": run a2dlc check"}, nil
		}
		return nil, err
	}
	sha, err := a.Repo.ResolveCommit(commit)
	if err != nil {
		return nil, err
	}
	tree, err := a.Repo.CommitTree(sha)
	if err != nil {
		return nil, err
	}
	var probs []string
	if old := strings.TrimPrefix(v.TreeHash, treePrefix); old != tree {
		p := "the verdict is stale (it was issued over another tree): run a2dlc check"
		if diffs, err := a.Repo.DiffNames(old, tree); err == nil && len(diffs) > 0 {
			p += "; changed: " + strings.Join(truncate(diffs, 10), ", ")
		}
		probs = append(probs, p)
	}
	if v.Verdict != contract.PASS {
		probs = append(probs, "the verdict is "+v.Verdict)
	}
	// A check against HEAD after committing does not see what is already committed: the base must be in what was pushed.
	if u := a.pushedBase(); u != "" && !a.Repo.IsAncestor(v.BaseRef, u) {
		probs = append(probs, "the verdict's base ("+short(v.BaseRef)+") is not in what was pushed: it does not cover unpushed commits; run a2dlc check")
	}
	waivers, err := a.waivers(sha)
	if err != nil {
		return nil, err
	}
	approvals, err := a.approvals(sha)
	if err != nil {
		return nil, err
	}
	approved, reason := a.approved(tree, waivers, approvals)
	probs = append(probs, a.noteProblems(v)...)
	waived := []string{}
	for _, t := range v.Targets {
		if t.Waived {
			waived = append(waived, t.Target)
		}
	}
	for _, t := range v.Tests {
		if t.Waived {
			waived = append(waived, t.Target)
		}
	}
	// Two checks can waive the same finding target: one problem per target.
	seen := map[string]bool{}
	for _, c := range v.Checks {
		for _, f := range c.FindingList() {
			if f.Waived && !seen[f.Target] {
				seen[f.Target] = true
				waived = append(waived, f.Target)
			}
		}
	}
	for _, t := range waived {
		if approved[t] {
			continue
		}
		m := reason[t]
		if m == "" {
			m = "unsigned: approve it with a2dlc approve " + t
		}
		probs = append(probs, "waiver for "+t+": "+m)
	}
	return probs, nil
}

// noteProblems are the reasons verify refuses the divergence notes of a receipt: a note is only valid on a FAIL target with
// a counterexample, no waiver, and code that the base's config.critical_code does not name. The receipt is a file the
// agent could edit, so verify decides again with the config of the receipt's base, the one check read.
func (a App) noteProblems(v contract.Verdict) []string {
	var probs []string
	var critical []string
	configRead := false
	for _, t := range v.Targets {
		if t.Note == "" {
			continue
		}
		if t.Note != contract.NoteBehaviorDivergence || t.Verdict != contract.FAIL || t.Counterexample == nil || t.Waived {
			probs = append(probs, "note on "+t.Target+" is not a behavior divergence with a counterexample and no waiver")
			continue
		}
		if !configRead {
			configRead = true
			cfg, err := a.configAt(v.BaseRef)
			if err != nil {
				probs = append(probs, "cannot confirm the divergence notes: "+err.Error())
				critical = []string{"**"}
			} else {
				critical = cfg.CriticalCode
			}
		}
		if verdict.IsCritical(critical, t.Target) {
			probs = append(probs, "behavior divergence on critical code "+t.Target+" has no waiver: write one and a person runs a2dlc approve "+t.Target)
		}
	}
	return probs
}

func sortedIndexes(byEngine map[int][]string) []int {
	idx := make([]int, 0, len(byEngine))
	for i := range byEngine {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	return idx
}

func short(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}

// ranges writes sorted lines as "3-5, 9".
func ranges(lines []int) string {
	var parts []string
	for i := 0; i < len(lines); {
		j := i
		for j+1 < len(lines) && lines[j+1] == lines[j]+1 {
			j++
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

func truncate(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
	}
	return s
}

// Approve signs the waiver for target against the current blob of its file and saves it to approvals.json.
func (a App) Approve(target, key, principal string) error {
	waivers, err := a.waivers("")
	if err != nil {
		return err
	}
	w, ok := waivers[target]
	if !ok {
		return fmt.Errorf("there is no waiver for %s in %s", target, pathWaivers)
	}
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		return err
	}
	blob, err := a.Repo.Blob(tree, contract.TargetFile(target))
	if err != nil {
		return fmt.Errorf("the file for %s is not in the tree: %w", target, err)
	}
	ap := contract.Approval{Target: target, WaiverSHA256: approval.WaiverSHA(w), FileBlob: blob, Principal: principal}
	payload := approval.Payload(ap.Target, ap.WaiverSHA256, ap.FileBlob)
	if ap.Signature, err = approval.Sign(key, payload); err != nil {
		return err
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return err
	}
	if err := approval.Verify(signers, principal, payload, ap.Signature); err != nil {
		return fmt.Errorf("signed, but the signature does not pass against the allowed signers (is your key missing from allowed_signers?): %w", err)
	}
	current, err := a.approvals("")
	if err != nil {
		return err
	}
	out := contract.Approvals{SchemaVersion: contract.SchemaVersion, Approvals: []contract.Approval{}}
	for _, x := range current {
		if x.Target != target {
			out.Approvals = append(out.Approvals, x)
		}
	}
	out.Approvals = append(out.Approvals, ap)
	return a.write(pathApprovals, out)
}

// Hook adapts check to an agent's lifecycle hooks (PreToolUse and end-of-turn Stop). It returns the process exit code.
func (a App) Hook(ctx context.Context, agent string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Wired globally (a Claude Code plugin), the hook runs in repos that never installed a2dlc: there it allows and
	// writes nothing. The config must be missing from HEAD too, so deleting it in the working tree does not disarm it.
	if !a.Installed() {
		return allow(agent, stdout)
	}
	var in map[string]any
	_ = json.NewDecoder(stdin).Decode(&in)
	tool, _ := in["tool_name"].(string)
	if tool == "" {
		tool, _ = in["tool"].(string)
	}
	if tool != "" {
		return a.preTool(agent, tool, in, stdout, stderr)
	}
	// Stop is the end of every agent turn, so it runs the static level: it informs and never blocks (owner's decision of
	// 2026-10-27). The blocking gate, level 1, is `a2dlc check` when the agent says "done", the pre-push verify and the PR
	// check. Claude Code does not show a Stop hook's stdout to the model, only systemMessage to the person: the agent finds
	// out through .a2dlc/reports/level0.json. A failure of level 0 itself is also not a block (exit 2 would be).
	v, err := a.Level0(ctx, "")
	if err != nil {
		fmt.Fprintln(stderr, "a2dlc: level 0:", err)
		return allow(agent, stdout)
	}
	if agent == "gemini" {
		return allow(agent, stdout)
	}
	json.NewEncoder(stdout).Encode(map[string]string{"systemMessage": Level0Summary(v)})
	return 0
}

// Installed reports whether this repo has an a2dlc config in the working tree or in HEAD.
func (a App) Installed() bool {
	var c contract.Config
	if err := a.read("", pathConfig, &c); !errors.Is(err, os.ErrNotExist) {
		return true
	}
	_, err := a.Repo.Show("HEAD", pathConfig)
	return err == nil
}

// HookOutsideRepo is the hook's answer when the working directory is not a git repo: allow, like an uninstalled repo.
func HookOutsideRepo(agent string, stdout io.Writer) int { return allow(agent, stdout) }

func allow(agent string, stdout io.Writer) int {
	if agent == "gemini" {
		json.NewEncoder(stdout).Encode(map[string]string{"decision": "allow"})
	}
	return 0
}

func (a App) preTool(agent, tool string, in map[string]any, stdout, stderr io.Writer) int {
	input, _ := in["tool_input"].(map[string]any)
	if input == nil {
		input, _ = in["input"].(map[string]any)
	}
	// 1. File modification tools
	targetFile := ""
	for _, k := range []string{"file_path", "path", "target_file", "file", "notebook_path"} {
		if v, ok := input[k].(string); ok && v != "" {
			targetFile = v
			break
		}
	}
	if targetFile != "" {
		rel := filepath.ToSlash(targetFile)
		if filepath.IsAbs(targetFile) {
			if r, err := filepath.Rel(a.Repo.Dir, targetFile); err == nil {
				rel = filepath.ToSlash(r)
			}
		}
		rel = strings.TrimPrefix(rel, "./")
		cfg, err := a.config()
		if err == nil && len(cfg.ProtectedPaths) > 0 {
			matches := protectedMatches(cfg.ProtectedPaths, []string{rel})
			if glob, ok := matches[rel]; ok {
				waivers, _ := a.waivers("")
				approvals, _ := a.approvals("")
				approved, _ := a.approved("HEAD", waivers, approvals)
				targetKey := rel + protectedSuffix
				if !approved[targetKey] {
					msg := fmt.Sprintf("a2dlc PreToolUse: cannot %s %q: it matches protected_paths (%s) and has no valid signed approval", tool, rel, glob)
					if agent == "gemini" {
						json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": msg})
						return 0
					}
					fmt.Fprintln(stderr, msg)
					return 2
				}
			}
		}
	}

	// 2. Shell commands (Bash)
	if strings.EqualFold(tool, "Bash") {
		cmd, _ := input["command"].(string)
		cmdLower := strings.ToLower(cmd)
		// The gate is the pre-push; a commit with --no-verify skips no a2dlc gate and stays allowed.
		if strings.Contains(cmdLower, "--no-verify") && pushesWithoutVerify(cmdLower) {
			msg := "a2dlc PreToolUse: cannot execute git push with --no-verify: a2dlc verification gates cannot be bypassed by an autonomous agent"
			if agent == "gemini" {
				json.NewEncoder(stdout).Encode(map[string]string{"decision": "block", "reason": msg})
				return 0
			}
			fmt.Fprintln(stderr, msg)
			return 2
		}
	}

	return allow(agent, stdout)
}

// pushesWithoutVerify says whether a shell line runs git push with --no-verify. It looks at the git subcommand of each
// segment (split on ;, & and |), so a commit whose message says "push" is not taken for a push.
func pushesWithoutVerify(cmd string) bool {
	for _, seg := range strings.FieldsFunc(cmd, func(r rune) bool { return r == ';' || r == '&' || r == '|' || r == '\n' }) {
		words := strings.Fields(seg)
		for i := 0; i < len(words); i++ {
			if words[i] != "git" && !strings.HasSuffix(words[i], "/git") && !strings.HasSuffix(words[i], "git.exe") {
				continue
			}
			j := i + 1
			for j < len(words) && strings.HasPrefix(words[j], "-") {
				if words[j] == "-c" || words[j] == "-C" {
					j++
				}
				j++
			}
			if j < len(words) && words[j] == "push" && strings.Contains(seg, "--no-verify") {
				return true
			}
			break
		}
	}
	return false
}

func sanitize(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '-' || r == '_' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return r
		}
		return '_'
	}, s)
}

// message is what the agent reads when the gate blocks; notes lists the protected paths that have no valid approval.
func message(v contract.Verdict, err error, notes string) string {
	var b strings.Builder
	if err != nil {
		fmt.Fprintf(&b, "a2dlc check could not decide (INCONCLUSIVE): %v\n", err)
		return b.String()
	}
	fmt.Fprintf(&b, "a2dlc: %s. Do not finish yet.\n", v.Verdict)
	for _, t := range v.Targets {
		if t.Waived || t.Verdict == contract.PASS {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s", t.Target, t.Verdict)
		if t.Counterexample != nil {
			fmt.Fprintf(&b, " with args %s: before %s, now %s", t.Counterexample.Args, t.Counterexample.Old, t.Counterexample.New)
		}
		if t.Reason != "" {
			fmt.Fprintf(&b, " (%s)", t.Reason)
		}
		b.WriteString("\n")
	}
	for _, e := range v.EngineErrors {
		fmt.Fprintf(&b, "- engine %s did not respond for %s: %s\n", e.Language, strings.Join(e.Files, ", "), e.Error)
	}
	for _, t := range v.Tests {
		if t.Waived || t.Verdict == contract.PASS {
			continue
		}
		fmt.Fprintf(&b, "- test %s (%s): %s: %s", t.Target, t.Gate, t.Verdict, t.Reason)
		if t.Message != "" {
			fmt.Fprintf(&b, " [%s]", t.Message)
		}
		b.WriteString("\n")
	}
	if g := v.TestGates; g != nil {
		gates := []contract.Gate{g.NOPOracle, g.Baseline}
		if g.TestsDiff != nil {
			gates = append(gates, *g.TestsDiff)
		}
		for _, x := range gates {
			if x.Status == contract.GateError {
				fmt.Fprintf(&b, "- test gate could not run: %s\n", x.Reason)
			}
		}
	}
	if v.DiffBudget != nil {
		answered := map[string]bool{}
		for _, t := range v.Targets {
			answered[t.Target] = true
		}
		for _, c := range v.DiffBudget.Cut {
			if !answered[c.Target] {
				fmt.Fprintf(&b, "- %s: not judged, the per-diff budget cut it (%s)\n", c.Target, c.Reason)
			}
		}
	}
	if c := v.Coverage; c != nil {
		for _, f := range c.Files {
			if len(f.Uncovered) > 0 {
				fmt.Fprintf(&b, "- %s: %d changed lines that no engine examined and no test executed (%s): write a test that executes them\n",
					f.File, len(f.Uncovered), ranges(f.Uncovered))
			}
		}
	}
	if c := v.Coverage; c != nil && c.Status == contract.CoverageNotConfigured && c.Reason != "" {
		b.WriteString("No test ran: " + c.Reason + ".\n")
	}
	if notes != "" {
		b.WriteString("Protected paths without a valid approval (a person signs them, not you):\n" + notes)
	}
	b.WriteString("If the behavior change is intentional, declare it in .a2dlc/waivers.json " +
		`({"schema_version":1,"waivers":[{"target":"<file>::<symbol>","decision":"enforce_new_behavior","reason":"..."}]}); ` +
		"for a test that must pass on the base (characterization of a refactor) the decision is characterization_test. " +
		"A person signs the approval, not you. If it is not intentional, fix the code or the test.\n")
	return b.String()
}
