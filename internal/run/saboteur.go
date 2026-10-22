package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// The Stage 4 fuzzing round (decision 13): after the Saboteur, the changed public functions of the candidate run through the
// engines' fuzz operation with a fixed seed and this budget, so a finding reproduces. It does not change the coverage: the
// lines it reaches are not "examined" for the precondition, and the uncovered ones stay as they were.
const (
	FuzzSeconds = 10
	FuzzSeed    = 42
)

// fuzzer is the engine operation fuzz. An engine runner that does not have it (app.App.Runner) does not fuzz, and the
// round says so in the stage reason instead of passing silently. engine.Docker implements it (internal/engine/fuzz.go) and
// the Python engine answers the operation with Atheris (engines/py/fuzz.py); an engine image without it answers an error
// and the round records it. The test fake implements it too (saboteur_test.go).
type fuzzer interface {
	Fuzz(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.FuzzResponse, error)
}

// fuzzRound fuzzes the public functions whose lines the candidate changed. It returns the escalations of the findings
// (one per function, see app.FuzzEscalations) and a note for the stage reason when it did not run or did not cover a file.
// candidate is the builder's commit ("" = the working tree), as in CheckOptions.CandidateTree.
func (m *Machine) fuzzRound(ctx context.Context, candidate string) ([]contract.Escalation, string, error) {
	cfg, err := m.app.Config()
	if err != nil {
		return nil, "", err
	}
	tree := candidate
	if tree == "" {
		if tree, err = m.app.Repo.WorktreeTree(); err != nil {
			return nil, "", err
		}
	}
	sigs, _, err := m.app.SignaturesOf(ctx, m.baseSha, candidate)
	if err != nil {
		return nil, "", err
	}
	var files []string
	targetOf := map[string]string{}
	for _, f := range sigs {
		changed, err := m.app.Repo.ChangedLines(m.baseSha, tree, f.File)
		if err != nil {
			return nil, "", err
		}
		hit := false
		for _, s := range f.Symbols {
			if spanChanged(s.Lines, changed) {
				targetOf[s.Name] = f.File + "::" + s.Name
				hit = true
			}
		}
		if hit {
			files = append(files, f.File)
		}
	}
	if len(files) == 0 {
		return nil, "", nil
	}
	fz, ok := m.app.Runner.(fuzzer)
	if !ok {
		return nil, "fuzzing did not run: this binary's engine runner does not do the fuzz operation", nil
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, files)
	notes := []string{}
	if len(noEngine) > 0 {
		notes = append(notes, "fuzzing did not cover "+strings.Join(noEngine, ", ")+": no engine in config.engines covers it")
	}
	if len(byEngine) == 0 {
		return nil, strings.Join(notes, "; "), nil
	}
	baseDir, candDir, cleanup, err := m.exportFuzz(tree)
	if err != nil {
		return nil, "", err
	}
	defer cleanup()
	budget := json.RawMessage(fmt.Sprintf(`{"seconds":%d,"seed":%d}`, FuzzSeconds, FuzzSeed))
	indexes := make([]int, 0, len(byEngine))
	for i := range byEngine {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	var out []contract.Escalation
	for _, i := range indexes {
		e := cfg.Engines[i]
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: m.baseSha, BaseDir: "/base",
			CandidateDir: "/cand", ChangedFiles: byEngine[i], Budget: budget, Operation: "fuzz"}
		resp, err := fz.Fuzz(ctx, e, req, baseDir, candDir)
		if err != nil {
			// a failed engine is not a silent fallback: its changed functions are not fuzzed, so a person sees it as a tooling
			// escalation on the first of its files (equivalence does the same for a failed engine)
			notes = append(notes, "fuzzing did not run for "+e.Language+": "+err.Error())
			out = append(out, app.ToolingEscalation(byEngine[i][0]+"::*",
				fmt.Sprintf("Fuzzing did not run for %s (seed %d, %d s): the changed functions of its files were not fuzzed: %s",
					e.Language, FuzzSeed, FuzzSeconds, err.Error())))
			continue
		}
		out = append(out, app.FuzzEscalations(resp, targetOf, FuzzSeed, FuzzSeconds)...)
	}
	return out, strings.Join(notes, "; "), nil
}

// spanChanged says whether a changed line falls in the symbol's span [first, last]. A symbol without a span counts as
// changed: it is fuzzed rather than skipped.
func spanChanged(span, changed []int) bool {
	if len(span) != 2 {
		return true
	}
	for _, l := range changed {
		if span[0] <= l && l <= span[1] {
			return true
		}
	}
	return false
}

// exportFuzz exports the base and the candidate to temporary folders for the fuzz operation: /base and /cand.
func (m *Machine) exportFuzz(tree string) (string, string, func(), error) {
	baseDir, err := os.MkdirTemp("", "a2dlc-fuzz-base-*")
	if err != nil {
		return "", "", nil, err
	}
	candDir, err := os.MkdirTemp("", "a2dlc-fuzz-cand-*")
	if err != nil {
		os.RemoveAll(baseDir)
		return "", "", nil, err
	}
	cleanup := func() {
		os.RemoveAll(baseDir)
		os.RemoveAll(candDir)
	}
	if err := m.app.Repo.Export(m.baseSha, baseDir); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := m.app.Repo.Export(tree, candDir); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return baseDir, candDir, cleanup, nil
}

// Stage 4: Tier 1. The oracle (check) runs against the base of the run; if it leaves uncovered_change (changed lines
// that nobody examined) and a Saboteur is configured, the core asks it for tests against the public signatures and the
// contractJSON, puts them in the run's overlay (.a2dlc/run/<slice>/overlay/) and runs check again with --tests-overlay.
// It repeats until nothing without cover remains, the verdict is FAIL, the Saboteur refuses or the rounds run out
// (--max-rounds, protocol: max_tier1_adversarial_rounds). The Saboteur never sees the builder's code: only what
// policy.go lets it see (S*, signatures, escalations, intent) and the payload with the lines without cover.

const (
	MaxRoundsDefault = 3
	dirOverlay       = "overlay"
)

// signatures is the run's signatures.json artifact: what each engine gave and what it could not give.
type signatures struct {
	SchemaVersion int                      `json:"schema_version"`
	Files         []contract.SignatureFile `json:"files"`
	EngineErrors  []contract.EngineError   `json:"engine_errors"`
}

func stageTier1(ctx context.Context, m *Machine) (string, string, string, error) {
	candidate, err := m.candidate() // with a builder (Stage 3) its commit is judged, not the working tree
	if err != nil {
		return "", "", "", err
	}
	v, reason, _, err := m.judge(ctx, app.CheckOptions{CandidateTree: candidate}, ArtVerdict)
	if err != nil {
		return "", "", "", err
	}
	o := v.Verdict
	m.state.Outcome = &o
	return contract.StageDone, reason, ArtVerdict, nil
}

// judge is the oracle → Saboteur loop (judgeLoop), then the Stage 4 fuzzing round over the candidate's changed public
// functions (fuzzRound). It leaves the verdict in artV and its escalations in escalations.json, fuzzing findings included,
// and returns the verdict, the reason for the stop (with the note of a fuzzing round that did not run) and the fuzzing
// escalations, which closeN2 needs to add to the N=2 escalations file.
func (m *Machine) judge(ctx context.Context, opts app.CheckOptions, artV string) (contract.Verdict, string, []contract.Escalation, error) {
	v, reason, err := m.judgeLoop(ctx, opts, artV)
	if err != nil {
		return v, "", nil, err
	}
	fuzzed, note, err := m.fuzzRound(ctx, opts.CandidateTree)
	if err != nil {
		return v, "", nil, err
	}
	if len(fuzzed) > 0 {
		escalations := contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: v.TreeHash, CreatedAt: v.CreatedAt,
			Items: append(app.Escalations(v), fuzzed...)}
		if _, err := m.writeArtifact(ArtEscalations, escalations); err != nil {
			return v, "", nil, err
		}
	}
	return v, join(reason, note), fuzzed, nil
}

// recordedGolden reads the golden master Stage −1 wrote to this slice's folder. Absent (no golden master declared, or not a
// refactor or brownfield slice) is nil; a file that does not decode is an error, never a silent skip.
func (m *Machine) recordedGolden() (*contract.Golden, error) {
	b, err := os.ReadFile(filepath.Join(m.dir, ArtGolden))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var g contract.Golden
	if err := contract.DecodeStrictBytes(b, &g); err != nil {
		return nil, fmt.Errorf("%s: %w", ArtGolden, err)
	}
	return &g, nil
}

// judgeLoop is the oracle → Saboteur loop over a candidate (opts.CandidateTree, with its peers if there are any): it leaves
// each verdict in artV and its escalations in escalations.json (what the Saboteur sees), and returns the last verdict and
// the reason for the stop ("" if nothing without cover remained). Rounds are counted per candidate and added to m.iter.
// The π_S relaxations of contract.json (Stage 1.5) go to the engines in each check.
func (m *Machine) judgeLoop(ctx context.Context, opts app.CheckOptions, artV string) (contract.Verdict, string, error) {
	opts.EquivalenceRelaxations = m.relaxations()
	// The oracle's own inputs: the golden master recorded on the base by Stage -1 (replayed by CheckWith) and the change
	// kind the person declared (the gates that apply to feature, bugfix or refactor read it).
	if m.intent != nil {
		opts.ChangeKind = m.intent.ChangeKind
	}
	golden, err := m.recordedGolden()
	if err != nil {
		return contract.Verdict{}, "", err
	}
	opts.Golden = golden
	overlay := filepath.Join(m.dir, dirOverlay)
	if previous, _ := relativePathsUnder(overlay); len(previous) > 0 {
		opts.TestsOverlay = overlay // an interrupted run resumes with the tests the Saboteur already brought
	}
	reason, round := "", 0
	var v contract.Verdict
	for {
		var err error
		if v, err = m.app.CheckWith(ctx, m.baseSha, opts); err != nil {
			return v, "", err
		}
		if err := m.seal(&v); err != nil {
			return v, "", err
		}
		escalations := contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: v.TreeHash, CreatedAt: v.CreatedAt, Items: app.Escalations(v)}
		if _, err := m.writeArtifact(ArtEscalations, escalations); err != nil {
			return v, "", err
		}
		if _, err := m.writeArtifact(artV, v); err != nil {
			return v, "", err
		}
		without := hasUncovered(v)
		if v.Verdict != contract.INCONCLUSIVE || len(without) == 0 {
			break
		}
		if _, ok := m.roles.Roles[contract.RoleSaboteur]; !ok {
			reason = "uncovered_change without a saboteur role in " + pathRoles + ": nobody brings the test"
			break
		}
		if round >= m.maxRounds {
			reason = fmt.Sprintf("uncovered_change after %d saboteur rounds (--max-rounds)", round)
			break
		}
		round++
		m.iter++
		finished, err := m.roundSaboteur(ctx, "4", without, nil, overlay, round, opts.CandidateTree, opts.Peers)
		if err != nil {
			return v, "", err
		}
		if finished != "" {
			reason = finished
			break
		}
		opts.TestsOverlay = overlay
	}
	return v, reason, nil
}

// roundSaboteur launches the Saboteur once and copies to the overlay the tests it delivered. It returns the reason the
// loop cannot go on ("" if it can) or a machine error. stage is the caller ("4": without are the lines without cover;
// "5": alive are the mutants that survived the kill gate). candidate is the builder's commit ("" = working tree); peers
// are the other candidates (N=2), on which the overlay also runs.
func (m *Machine) roundSaboteur(ctx context.Context, stage string, without []contract.SaboteurUncovered, alive []contract.SaboteurSurvivor, overlay string, round int, candidate string, peers []string) (string, error) {
	cfg, err := m.app.Config()
	if err != nil {
		return "", err
	}
	if cfg.Tests == nil {
		return "uncovered_change without config.tests: there is no command to run the saboteur's tests with", nil
	}
	if _, err := os.Stat(filepath.Join(m.dir, ArtSignatures)); err != nil {
		files, errs, err := m.app.SignaturesOf(ctx, m.baseSha, candidate)
		if err != nil {
			return "", err
		}
		if _, err := m.writeArtifact(ArtSignatures, signatures{SchemaVersion: contract.SchemaVersion, Files: files, EngineErrors: errs}); err != nil {
			return "", err
		}
	}
	previous, err := relativePathsUnder(overlay)
	if err != nil {
		return "", err
	}
	if without == nil {
		without = []contract.SaboteurUncovered{} // contractJSON asks for the array even if it is empty (Stage 5)
	}
	payload := contract.SaboteurRequest{Round: round, MaxRounds: m.maxRounds, Uncovered: without, TestsPaths: cfg.Tests.Paths,
		Command: cfg.Tests.Command, PreviousTests: previous, Survivors: alive}
	invocation, err := m.Invoke(ctx, contract.RoleSaboteur, stage, payload, "")
	if err != nil {
		return "", err
	}
	switch {
	case invocation.Response == nil:
		return fmt.Sprintf("the saboteur did not give a valid answer in %d attempts", len(invocation.Entries)), nil
	case invocation.Response.Status == contract.RoleRefused:
		return "the saboteur refused: " + invocation.Response.Reason, nil
	}
	var resp contract.SaboteurResponse
	if err := contract.DecodeStrictBytes(invocation.Response.Payload, &resp); err != nil {
		return "saboteur payload out of contract: " + err.Error(), nil
	}
	if len(resp.TestFiles) == 0 {
		return "the saboteur delivered no tests", nil
	}
	tree := candidate
	if tree == "" {
		if tree, err = m.app.Repo.WorktreeTree(); err != nil {
			return "", err
		}
	}
	// What already exists, to compare ignoring case: the overlay is written to the host disk (NTFS on Windows) and there
	// tests/TEST_x.py is tests/test_x.py; Repo.Blob does distinguish them.
	var inRepo []string
	for _, t := range append([]string{m.baseSha, tree}, peers...) {
		fs, err := m.app.Repo.Files(t)
		if err != nil {
			return "", err
		}
		inRepo = append(inRepo, fs...)
	}
	delivered := previous
	for _, tf := range resp.TestFiles {
		rel := tf.Path
		// The path is relative to the repo and must live in tests.paths: this way the overlay cannot overwrite a source.
		// Nor can it overwrite a test that already exists in the base or in the candidate: that would change what the other gates measure.
		if rel == "" || path.Clean(rel) != rel || strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "../") || strings.Contains(rel, "\\") ||
			suspiciousPathOnWindows(rel) {
			return fmt.Sprintf("the saboteur declared the path %q: it is not a clean relative path", rel), nil
		}
		if matched, _ := app.Partition(cfg.Tests.Paths, []string{rel}); len(matched) == 0 {
			return fmt.Sprintf("the saboteur left %s outside tests.paths: not accepted", rel), nil
		}
		if exists(inRepo, rel) {
			return fmt.Sprintf("the saboteur wanted to overwrite %s, which already exists in the repo", rel), nil
		}
		// nor one it already delivered: rewriting it would change the T the previous candidates were judged with
		if exists(delivered, rel) {
			return fmt.Sprintf("the saboteur wanted to rewrite %s, which it already delivered", rel), nil
		}
		origin := filepath.Join(invocation.OutDir, "tests", filepath.FromSlash(rel))
		h, err := HashFile(origin)
		if err != nil {
			return fmt.Sprintf("the saboteur declared %s and it is not in /out/tests", rel), nil
		}
		if h != tf.SHA256 {
			return fmt.Sprintf("the hash of %s does not match what was declared", rel), nil
		}
		b, err := os.ReadFile(origin)
		if err != nil {
			return "", err
		}
		dest := filepath.Join(overlay, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			return "", err
		}
		delivered = append(delivered, rel)
	}
	return "", nil
}

// exists says whether rel is in paths, ignoring case (like the Windows and macOS disks).
func exists(paths []string, rel string) bool {
	for _, r := range paths {
		if strings.EqualFold(r, rel) {
			return true
		}
	}
	return false
}

// suspiciousPathOnWindows: ':' opens an NTFS stream (file::$DATA) or a drive, and a name that ends with '.' or ' '
// is written without them: in both cases the declared path and the overwritten file stop matching.
func suspiciousPathOnWindows(rel string) bool {
	if strings.Contains(rel, ":") {
		return true
	}
	for _, c := range strings.Split(rel, "/") {
		if strings.HasSuffix(c, ".") || strings.HasSuffix(c, " ") {
			return true
		}
	}
	return false
}

// hasUncovered are the changed lines that the verdict leaves unexamined, by file.
func hasUncovered(v contract.Verdict) []contract.SaboteurUncovered {
	var out []contract.SaboteurUncovered
	if v.Coverage == nil {
		return out
	}
	for _, f := range v.Coverage.Files {
		if len(f.Uncovered) > 0 {
			out = append(out, contract.SaboteurUncovered{File: f.File, Lines: f.Uncovered})
		}
	}
	return out
}

// relativePathsUnder lists the files of a folder with their path relative to it, using /; empty if it does not exist.
func relativePathsUnder(dir string) ([]string, error) {
	out := []string{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if p == dir && os.IsNotExist(err) {
				return filepath.SkipAll
			}
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			out = append(out, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}
