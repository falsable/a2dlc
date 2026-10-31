package app

import (
	"context"
	"reflect"
	"strings"
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

// With tests.tier0_command "baseline" (RFC-0004), Tier 0's run on a candidate is the baseline gate's run on it: the check of
// that candidate takes it from the cache, also when baseline_command uses the selection. Without the field it runs again.
func TestTier0BaselineRunServesTheCheckOfTheSameCandidate(t *testing.T) {
	for _, c := range []struct {
		name, baseline string
		cached         bool
	}{
		{"whole suite", `"baseline","tier0_command":"baseline"`, true},
		{"selection", `"baseline $A2DLC_CHANGED","tier0_command":"baseline"`, true},
		{"default", `"baseline"`, false},
	} {
		a, tf, dir := suiteRepo(t)
		// the fake's "nop" runs $A2DLC_TESTS only, and Tier 0's default leaves it empty: a command that runs the whole suite
		cfg := strings.Replace(gatesConfig, `"command":"nop"`, `"command":"all"`, 1)
		write(t, dir, ".a2dlc/config.json", strings.Replace(cfg, `"baseline_command":"baseline"`, `"baseline_command":`+c.baseline, 1))
		git(t, dir, "add", "-A")
		git(t, dir, "commit", "-qm", "config", "--allow-empty") // the default leaves the config as it was
		cand := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\nIMPL old\n"})
		if _, _, err := a.Tier0With(context.Background(), "HEAD", cand, nil, withTests); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		n := len(tf.runs)
		v, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{CandidateTree: cand})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if g := v.TestGates.Baseline; g.Status != contract.GateRan || g.CandidateCached != c.cached {
			t.Fatalf("%s: candidate cached %v, want %v: %+v (check ran %v)", c.name, g.CandidateCached, c.cached, g, tf.runs[n:])
		}
	}
}

// Without a digest the run cannot be keyed, and without baseline_command there is nothing to run instead: the first runs
// baseline_command uncached, the second runs tests.command as before.
func TestTier0BaselineFallsBack(t *testing.T) {
	for _, c := range []struct{ tests, want string }{
		{`"command":"run-all","baseline_command":"base-all","tier0_command":"baseline"`, "base-all"},
		{`"command":"run-all","tier0_command":"baseline"`, "run-all"},
	} {
		a, st := testsRepo(t, junitRun(0, "test/a.test.ts first"))
		write(t, a.Repo.Dir, ".a2dlc/config.json", strings.Replace(testsOnlyConfig, `"command":"run-all"`, c.tests, 1))
		git(t, a.Repo.Dir, "add", "-A")
		git(t, a.Repo.Dir, "commit", "-qm", "config")
		cand := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
		if r, _, err := a.Tier0With(context.Background(), "HEAD", cand, nil, withTests); err != nil || !r.Reviewed {
			t.Fatalf("%s: %v %+v", c.want, err, r)
		}
		if len(st.cmds) != 1 || st.cmds[0] != c.want {
			t.Fatalf("ran %v, want %s", st.cmds, c.want)
		}
	}
}

// Any other value is refused when the config is read, as rung is: a misspelt "baseline" does not fall back to the default.
func TestTier0CommandRefusesAnUnknownValue(t *testing.T) {
	a, _ := testsRepo(t)
	write(t, a.Repo.Dir, ".a2dlc/config.json", strings.Replace(testsOnlyConfig, `"command":"run-all"`, `"command":"run-all","tier0_command":"Baseline"`, 1))
	git(t, a.Repo.Dir, "add", "-A")
	git(t, a.Repo.Dir, "commit", "-qm", "config")
	cand := candidateOf(t, a, map[string]string{"src/server.ts": "export const s = 2\n"})
	if _, _, err := a.Tier0With(context.Background(), "HEAD", cand, nil, withTests); err == nil || !strings.Contains(err.Error(), "tests.tier0_command") {
		t.Fatalf("want the config refused, got %v", err)
	}
}

// A consumer that commits another roles file before the next step (the factory writes one per lane) keeps the cached run of
// the previous candidate: the roles file is not in the key. Any other file is.
func TestSetBaselineIgnoresACommittedRolesFile(t *testing.T) {
	a, tf, dir := suiteRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n")
	if _, err := a.Check(context.Background(), ""); err != nil { // leaves the candidate's run in the cache, unreproduced
		t.Fatal(err)
	}
	git(t, dir, "add", "src", "tests")
	git(t, dir, "commit", "-qm", "step 1")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{}}`)
	git(t, dir, "add", ".a2dlc/roles.json")
	git(t, dir, "commit", "-qm", "roles for step 2")
	n := len(tf.runs)
	out, err := a.SetBaseline(context.Background(), "HEAD")
	if err != nil || !out.Stable || out.Reason != "" {
		t.Fatalf("%v %+v", err, out)
	}
	if got := len(tf.runs) - n; got != 1 || !out.Cached {
		t.Fatalf("the candidate's cached run was due as the first of two, got %d runs (cached %v)", got, out.Cached)
	}
	write(t, dir, "README.md", "x\n")
	git(t, dir, "add", "README.md")
	git(t, dir, "commit", "-qm", "another file")
	n = len(tf.runs)
	out, err = a.SetBaseline(context.Background(), "HEAD")
	if err != nil || !out.Stable {
		t.Fatalf("%v %+v", err, out)
	}
	if got := len(tf.runs) - n; got != 2 || out.Cached {
		t.Fatalf("a change outside the roles file must miss the cache, got %d runs (cached %v)", got, out.Cached)
	}
}
