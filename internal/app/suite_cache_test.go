package app

import (
	"context"
	"reflect"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// suiteRepo is gatesRepo with a covering test, committed, and a change of the source that keeps every test green.
func suiteRepo(t *testing.T) (App, *fakeTests, string) {
	t.Helper()
	a, _, tf, dir := gatesRepo(t)
	write(t, dir, "tests/b.t", "t_b uses:old\ncovers src/lib/f.ts:1-2\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "covering test")
	return a, tf, dir
}

// The candidate of one check is the base of the next one when its tree was committed as it was: the next check does not run
// the suite on the base again, and says the base came from the cache.
func TestSuiteCacheServesThePreviousCandidateAsTheNextBase(t *testing.T) {
	a, tf, dir := suiteRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n")
	v, err := a.Check(context.Background(), "")
	if err != nil || v.Verdict != contract.PASS {
		t.Fatalf("%v %s %+v", err, v.Verdict, v.TestGates)
	}
	if g := v.TestGates.Baseline; g.Status != contract.GateRan || g.Cached || g.CandidateCached {
		t.Fatalf("first check: %+v", g)
	}
	first := len(tf.runs)
	git(t, dir, "add", "src", "tests") // not .a2dlc: the gate outputs are outside the tree a check judges
	git(t, dir, "commit", "-qm", "step 1")
	write(t, dir, "src/lib/f.ts", "export const f = 3\nIMPL old\n")
	v, err = a.Check(context.Background(), "")
	if err != nil || v.Verdict != contract.PASS {
		t.Fatalf("%v %s %+v", err, v.Verdict, v.TestGates)
	}
	if g := v.TestGates.Baseline; !g.Cached || g.CandidateCached {
		t.Fatalf("second check: the base must come from the cache and the new candidate must run: %+v", g)
	}
	if got := len(tf.runs) - first; got != 1 {
		t.Fatalf("the second check ran the suite %d times, want 1 (the candidate only): %v", got, tf.runs)
	}
}

// A check of a tree that an earlier check already answered runs nothing for the baseline and judges the same coverage: the
// cached candidate run gives its report again.
func TestSuiteCacheGivesTheCoverageOfACachedCandidate(t *testing.T) {
	a, tf, dir := suiteRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n")
	v1, err := a.Check(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	n := len(tf.runs)
	v2, err := a.Check(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if g := v2.TestGates.Baseline; !g.Cached || !g.CandidateCached {
		t.Fatalf("second check of the same tree: %+v", g)
	}
	if len(tf.runs) != n {
		t.Fatalf("the baseline ran again on a tree it had answered: %v", tf.runs[n:])
	}
	if v1.Coverage == nil || len(v1.Coverage.Files) == 0 {
		t.Fatalf("the first check left no coverage to compare: %+v", v1.Coverage)
	}
	if !reflect.DeepEqual(v1.Coverage, v2.Coverage) || v1.Verdict != v2.Verdict {
		t.Fatalf("a cached candidate changed the verdict:\n%s %+v\n%s %+v", v1.Verdict, v1.Coverage, v2.Verdict, v2.Coverage)
	}
}

// Two worktrees of one repository share the cache, because it is keyed by the repository's common git directory.
func TestSuiteCacheIsSharedByTheWorktreesOfARepository(t *testing.T) {
	a, tf, dir := suiteRepo(t)
	t.Setenv("A2DLC_CACHE_DIR", "") // the default location, keyed by repository
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("LocalAppData", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n")
	if _, err := a.Check(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	wt := t.TempDir() + "/wt"
	git(t, dir, "worktree", "add", "-q", "--detach", wt)
	b := a
	b.Repo.Dir = wt
	if a.cacheDir() == "" || a.cacheDir() != b.cacheDir() {
		t.Fatalf("the worktrees have different caches: %q %q", a.cacheDir(), b.cacheDir())
	}
	_ = tf
}

// A cached run that nobody reproduced counts as one of the two Stage -1 runs: one run settles it, and the answer is then
// marked reproduced so that the next Stage -1 runs nothing.
func TestSetBaselineUsesAnUnreproducedCachedRunAsTheFirstOfTwo(t *testing.T) {
	a, tf, dir := suiteRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n")
	if _, err := a.Check(context.Background(), ""); err != nil { // leaves the candidate's run in the cache, unreproduced
		t.Fatal(err)
	}
	git(t, dir, "add", "src", "tests")
	git(t, dir, "commit", "-qm", "step 1")
	n := len(tf.runs)
	out, err := a.SetBaseline(context.Background(), "HEAD")
	if err != nil || !out.Stable || out.Reason != "" {
		t.Fatalf("%v %+v", err, out)
	}
	if got := len(tf.runs) - n; got != 1 || !out.Cached || out.Reruns != 2 {
		t.Fatalf("one run was due, got %d (cached %v, reruns %d)", got, out.Cached, out.Reruns)
	}
	n = len(tf.runs)
	out, err = a.SetBaseline(context.Background(), "HEAD")
	if err != nil || !out.Stable || len(tf.runs) != n || !out.Cached || out.Reruns != 1 {
		t.Fatalf("a reproduced cached run must cost nothing: %v %+v, %d runs", err, out, len(tf.runs)-n)
	}
}
