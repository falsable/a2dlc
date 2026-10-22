package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/junit"
)

// Gate tests_diff (VARA-2, rule 2): the same set of tests T overlaid on the base, the candidate and the trees of other
// builders (--peer), with the repo's runner. T = changed tests of the candidate ∪ changed tests of each peer ∪
// --tests-overlay. A test red on the candidate and green on another tree is FAIL: the other tree proves the test is
// satisfiable; red on all proves nothing. It is the N-tree differential of the protocol, mediated by the tests, and
// knows no languages: it only runs commands and reads JUnit.

type tree struct{ name, treeish string }

type fileT struct {
	content []byte
	origin  string
}

// testSet builds T with precedence overlay > candidate > peers (the last write wins).
func (a App) testSet(in gateInput, peers []tree) (map[string]fileT, error) {
	T := map[string]fileT{}
	for _, p := range peers {
		changed, _, err := a.Repo.Changed(in.baseSha, p.treeish)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.name, err)
		}
		tests, _ := partition(in.cfg.Paths, changed)
		for _, f := range tests {
			b, err := a.Repo.Show(p.treeish, f)
			if err != nil {
				return nil, fmt.Errorf("%s:%s: %w", p.name, f, err)
			}
			T[f] = fileT{b, p.name}
		}
	}
	for _, f := range in.tests {
		b, err := a.Repo.Show(in.tree, f)
		if err != nil {
			return nil, err
		}
		T[f] = fileT{b, "candidate"}
	}
	for f, b := range in.overlay {
		T[f] = fileT{b, "overlay"}
	}
	return T, nil
}

// readOverlay reads the --tests-overlay folder: test files keyed by their path relative to the repo. Empty → nil.
func readOverlay(overlay string) (map[string][]byte, error) {
	if overlay == "" {
		return nil, nil
	}
	root, err := filepath.Abs(overlay)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("--tests-overlay %s: %w", overlay, err)
	}
	return out, nil
}

// gateTestsDiff runs T overlaid on each tree and judges each test from the candidate.
func (a App) gateTestsDiff(ctx context.Context, in gateInput, peers []tree, overlay string) (contract.Gate, []contract.TestResult) {
	if len(peers) == 0 && overlay == "" {
		return contract.Gate{Status: contract.GateNotApplicable, Reason: "no --peer or --tests-overlay"}, nil
	}
	T, err := a.testSet(in, peers)
	if err != nil {
		return gateError("build T", err), nil
	}
	if len(T) == 0 {
		return contract.Gate{Status: contract.GateNotApplicable, Reason: "T is empty: neither the candidate nor the peers change tests and there is no overlay"}, nil
	}
	paths := make([]string, 0, len(T))
	overlaid := map[string][]byte{}
	for f, x := range T {
		paths = append(paths, f)
		overlaid[f] = x.content
	}
	sort.Strings(paths)
	trees := append([]tree{{"base", in.baseSha}, {"candidate", in.tree}}, peers...)
	env := envTests(paths, in.sources)
	states := map[string]map[string]junit.Case{}
	names := []string{}
	for _, t := range trees {
		r, err := a.run(ctx, in, in.cfg.Command, t.treeish, overlaid, nil, env)
		if err == nil && !r.junit {
			err = fmt.Errorf("did not leave JUnit (exit %d): %s", r.exit, r.log)
		}
		if err != nil {
			return gateError(t.name, err), nil
		}
		states[t.name], names = r.cases, append(names, t.name)
	}
	g := contract.Gate{Status: contract.GateRan, Trees: names, Tests: paths}
	res := judgeTrees(names, states, T)
	res, err = a.preexisting(ctx, in, res, states["base"])
	if err != nil {
		return gateError("the base with its own tests", err), nil
	}
	// A red on the candidate is re-run only on the candidate: if it ever passes it is intermittent, not a FAIL.
	var reds []contract.TestResult
	for _, r := range res {
		if r.Verdict == contract.FAIL {
			reds = append(reds, r)
		}
	}
	if len(reds) > 0 {
		k := in.cfg.Reruns
		if k == 0 {
			k = 3
		}
		files := map[string]bool{}
		for _, r := range reds {
			if c, ok := states["candidate"][r.Target]; ok && c.File != "" {
				files[c.File] = true
			}
		}
		sel := paths
		if len(files) > 0 {
			sel = keysOfBoolSet(files)
		}
		var reruns []map[string]junit.Case
		for i := 0; i < k; i++ {
			r, err := a.run(ctx, in, in.cfg.Command, in.tree, overlaid, nil, envTests(sel, in.sources))
			if err != nil {
				return gateError(fmt.Sprintf("re-run %d on the candidate", i+1), err), nil
			}
			reruns = append(reruns, r.cases)
		}
		for i := range res {
			if res[i].Verdict != contract.FAIL {
				continue
			}
			res[i].Reruns = &contract.Reruns{Base: []string{}, Candidate: []string{}}
			for _, m := range reruns {
				e := contract.TestMissing
				if c, ok := m[res[i].Target]; ok {
					e = c.Status
				}
				res[i].Reruns.Candidate = append(res[i].Reruns.Candidate, e)
				if e == contract.TestPassed {
					res[i].Verdict, res[i].Flaky, res[i].Reason = contract.INCONCLUSIVE, true, "intermittent on the candidate: passes in some re-run"
				}
			}
		}
	}
	return g, res
}

const (
	redInAll        = "red in all trees"
	onlyInCandidate = "red only in the candidate"
)

// preexisting separates, among the reds in all trees, those that were already red on the base with their OWN test
// file (without T on top): they are preexisting failures of the repo (xfail that the runner reports as failure, tests
// that have always been broken), not teeth-less tests of the candidate, and they do not touch the verdict ("does not
// worsen", as in the baseline). It costs one more run on the base, only with those files and only when there are reds
// in all. A test that does not exist on the base (new in T) remains INCONCLUSIVE: nobody passes it.
func (a App) preexisting(ctx context.Context, in gateInput, res []contract.TestResult, inBase map[string]junit.Case) ([]contract.TestResult, error) {
	files := map[string]bool{}
	for _, r := range res {
		if r.Verdict != contract.INCONCLUSIVE || !strings.HasPrefix(r.Reason, redInAll) {
			continue
		}
		if c, ok := inBase[r.Target]; ok && c.File != "" {
			if _, err := a.Repo.Blob(in.baseSha, c.File); err == nil {
				files[c.File] = true
			}
		}
	}
	if len(files) == 0 {
		return res, nil
	}
	own, err := a.run(ctx, in, in.cfg.Command, in.baseSha, nil, nil, envTests(keysOfBoolSet(files), in.sources))
	if err != nil {
		return res, err
	}
	for i := range res {
		if res[i].Verdict != contract.INCONCLUSIVE || !strings.HasPrefix(res[i].Reason, redInAll) {
			continue
		}
		if c, ok := own.cases[res[i].Target]; ok && junit.Red(c.Status) {
			res[i].Verdict, res[i].Reason = contract.PASS, "red in all trees and already red on the base with its own file: preexisting failure, not the candidate's"
		}
	}
	return res, nil
}

// judgeTrees decides each test of T from the candidate:
//   - red on the candidate and green on some other tree → FAIL (the other tree proves the test can pass);
//   - red on the candidate and red on all the other trees that ran it → INCONCLUSIVE (it does not discriminate: broken
//     test or spec);
//   - red on the candidate and no other tree showed it (new symbol: on the base the module does not import) →
//     INCONCLUSIVE with that reason; it is not "red in all", nobody else ran it. NOP/Oracle decides it (a new test
//     must pass on the candidate) and it is escalated as a divergence with the test's spec, not as a test without teeth;
//   - did not appear on the candidate → INCONCLUSIVE;
//   - green on the candidate → PASS, characterization if it also passes on the base.
func judgeTrees(names []string, states map[string]map[string]junit.Case, T map[string]fileT) []contract.TestResult {
	ids := map[string]bool{}
	for _, m := range states {
		for id := range m {
			ids[id] = true
		}
	}
	state := func(tree, id string) (string, junit.Case) {
		if c, ok := states[tree][id]; ok {
			return c.Status, c
		}
		return contract.TestMissing, junit.Case{}
	}
	var out []contract.TestResult
	for _, id := range keysOfBoolSet(ids) {
		c, testCase := state("candidate", id)
		if c == contract.TestSkipped {
			continue
		}
		if c == contract.TestMissing && onlyCollectionErrors(names, states, id) {
			// The output pytest leaves for a module that does not import (a <testcase> with <error> and no test name) in
			// another tree: in greenfield, the base lacks the symbol the Saboteur's test imports. It is not a test the
			// candidate is missing (measured 2026-10-17: it gave INCONCLUSIVE "did not appear on the candidate" and a
			// tooling escalation).
			continue
		}
		b, _ := state("base", id)
		r := contract.TestResult{Target: id, Gate: contract.GateTestsDiff, Base: b, Candidate: c, Trees: map[string]string{}, Message: testCase.Message}
		if x, ok := T[testCase.File]; ok {
			r.Origin = x.origin
		}
		var green, others []string
		for _, n := range names {
			s, _ := state(n, id)
			r.Trees[n] = s
			if n == "candidate" {
				continue
			}
			if s == contract.TestPassed {
				green = append(green, n)
			}
			if s != contract.TestMissing {
				others = append(others, n)
			}
		}
		switch {
		case junit.Red(c) && len(green) > 0:
			r.Verdict, r.Reason = contract.FAIL, "red on the candidate, green on "+strings.Join(green, ", ")
		case junit.Red(c) && len(others) == 0:
			r.Verdict, r.Reason = contract.INCONCLUSIVE, onlyInCandidate+": no other tree ran it (new symbol or module that does not import on the base); tests_diff does not decide and NOP/Oracle judges it"
		case junit.Red(c):
			r.Verdict, r.Reason = contract.INCONCLUSIVE, redInAll+": the test does not discriminate anything (broken test or spec)"
		case c == contract.TestMissing:
			r.Verdict, r.Reason = contract.INCONCLUSIVE, "the test did not appear on the candidate (collection or import error)"
		default:
			r.Verdict, r.Characterization = contract.PASS, b == contract.TestPassed
		}
		out = append(out, r)
	}
	return out
}

// onlyCollectionErrors says whether id appears, outside the candidate, only as a collection error (error status): the
// runner's output for a module that does not import in that tree, not a test.
func onlyCollectionErrors(names []string, states map[string]map[string]junit.Case, id string) bool {
	found := false
	for _, n := range names {
		if n == "candidate" {
			continue
		}
		c, ok := states[n][id]
		if !ok {
			continue
		}
		if c.Status != contract.TestError {
			return false
		}
		found = true
	}
	return found
}

func keysOfBoolSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
