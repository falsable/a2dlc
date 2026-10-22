package app

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// A file in scope that no engine examines (a change inside a method) is no longer PASS: without config.tests nobody
// executed the changed lines, the verdict is INCONCLUSIVE and uncovered_change is escalated with the lines.
func TestEmptyPassIsInconclusive(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nNO_TARGET\nmore\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE || len(v.Targets) != 0 {
		t.Fatalf("v=%s err=%v targets=%d", v.Verdict, err, len(v.Targets))
	}
	if v.Coverage == nil || v.Coverage.Status != contract.CoverageNotConfigured || len(v.Coverage.Files) != 1 ||
		!reflect.DeepEqual(v.Coverage.Files[0].Uncovered, []int{2, 3}) {
		t.Fatalf("%+v", v.Coverage)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil || len(esc.Items) != 1 || esc.Items[0].Kind != "uncovered_change" ||
		esc.Items[0].Target != "src/lib/f.ts::*" || esc.Items[0].Needs == nil || esc.Items[0].Needs.Role != contract.RoleSaboteur ||
		!reflect.DeepEqual(esc.Items[0].Evidence.Lines, []int{2, 3}) {
		t.Fatalf("%v %+v", err, esc.Items)
	}
	// A file outside the declared scope (README) requires no coverage: the config sets the scope.
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	write(t, dir, "README.md", "other\n")
	if v, _ = a.Check(context.Background(), "HEAD"); v.Verdict != contract.PASS || len(v.Coverage.Files) != 0 {
		t.Fatalf("%s %+v", v.Verdict, v.Coverage)
	}
}

// With tests that leave coverage, what the engine does not look at is covered if some test executed it; if only part
// was executed, the lines that remain missing are uncovered and the verdict is INCONCLUSIVE.
func TestCoverageCoversWhatEngineDoesNotLookAt(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nNO_TARGET\nline4\nline5\n")
	write(t, dir, "tests/n.t", "t_covers passed\ncovers src/lib/f.ts:3-5\n")
	v, err := a.Check(context.Background(), "")
	if err != nil || v.Verdict != contract.PASS {
		t.Fatalf("v=%s err=%v cov=%+v tests=%+v", v.Verdict, err, v.Coverage, v.Tests)
	}
	f := v.Coverage.Files[0]
	if v.Coverage.Status != contract.CoverageRan || !reflect.DeepEqual(f.Covered, []int{3, 4, 5}) || len(f.Uncovered) != 0 {
		t.Fatalf("%+v", v.Coverage)
	}
	write(t, dir, "tests/n.t", "t_covers passed\ncovers src/lib/f.ts:3-4\n")
	v, _ = a.Check(context.Background(), "")
	if v.Verdict != contract.INCONCLUSIVE || !reflect.DeepEqual(v.Coverage.Files[0].Uncovered, []int{5}) {
		t.Fatalf("%s %+v", v.Verdict, v.Coverage)
	}
	// Without coverage in the run: unavailable, and everything the engine did not look at stays uncovered.
	write(t, dir, "tests/n.t", "t_none passed\n")
	v, _ = a.Check(context.Background(), "")
	if v.Verdict != contract.INCONCLUSIVE || v.Coverage.Status != contract.CoverageUnavailable || len(v.Coverage.Files[0].Uncovered) != 3 {
		t.Fatalf("%s %+v", v.Verdict, v.Coverage)
	}
}

// A target with lines examines only those lines; the rest of the file needs tests.
func TestTargetWithLinesLeavesRestUncovered(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nLINES 1-2\nIMPL outside\n")
	v, _ := a.Check(context.Background(), "HEAD")
	f := v.Coverage.Files[0]
	if v.Verdict != contract.INCONCLUSIVE || len(v.Targets) != 1 || v.Targets[0].Verdict != contract.PASS ||
		!reflect.DeepEqual(f.ByEngine, []int{2}) || !reflect.DeepEqual(f.Uncovered, []int{3}) {
		t.Fatalf("%s %+v targets=%+v", v.Verdict, v.Coverage, v.Targets)
	}
	// Without lines, the FAIL target counts for the whole file (engines from before the field).
	write(t, dir, "src/lib/f.ts", "export const f = 2\noutside\n")
	v, _ = a.Check(context.Background(), "HEAD")
	if v.Verdict != contract.FAIL || len(v.Coverage.Files[0].ByEngine) != 2 || len(v.Coverage.Files[0].Uncovered) != 0 {
		t.Fatalf("%s %+v", v.Verdict, v.Coverage)
	}
}

// tests_diff: the peer's tests run overlaid on the candidate. The one that the candidate fails and the peer passes is
// FAIL with the evidence per tree; the one that passes is PASS; an overlay test that fails on all trees decides nothing.
func TestTestsDiffWithPeerAndOverlay(t *testing.T) {
	a, _, tf, dir := gatesRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	// peer: implements alpha and beta and brings tests for both
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL alpha\nIMPL beta\n")
	write(t, dir, "tests/p.t", "t_alpha uses:alpha\nt_beta uses:beta\ncovers src/lib/f.ts:1-9\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "peer")
	peer := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "--detach", base)
	// candidate: only alpha, without its own tests
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL alpha\n")
	overlay := filepath.Join(t.TempDir(), "ov")
	write(t, overlay, "tests/o.t", "t_gamma uses:gamma\nt_alpha2 uses:alpha\n")
	v, err := a.CheckWith(context.Background(), base, CheckOptions{Peers: []string{peer}, TestsOverlay: overlay})
	if err != nil {
		t.Fatal(err)
	}
	g := v.TestGates.TestsDiff
	if g == nil || g.Status != contract.GateRan || strings.Join(g.Tests, ",") != "tests/o.t,tests/p.t" || len(g.Trees) != 3 || g.Trees[2] != "peer:"+peer[:8] {
		t.Fatalf("%+v", g)
	}
	ts := testsOf(v)
	beta := ts["t_beta"]
	if v.Verdict != contract.FAIL || beta.Verdict != contract.FAIL || beta.Origin != g.Trees[2] || beta.Trees["candidate"] != "failed" ||
		beta.Trees[g.Trees[2]] != "passed" || beta.Trees["base"] != "failed" || beta.Reruns == nil || len(beta.Reruns.Candidate) != 3 {
		t.Fatalf("%s %+v", v.Verdict, beta)
	}
	if ts["t_alpha"].Verdict != contract.PASS || ts["t_alpha"].Characterization || ts["t_alpha2"].Origin != "overlay" || ts["t_alpha2"].Verdict != contract.PASS {
		t.Fatalf("%+v %+v", ts["t_alpha"], ts["t_alpha2"])
	}
	if ts["t_gamma"].Verdict != contract.INCONCLUSIVE || !strings.HasPrefix(ts["t_gamma"].Reason, redInAll) {
		t.Fatalf("%+v", ts["t_gamma"])
	}
	// the peer's coverage overlaid on the candidate covers the changed lines (fakeEngine already examines them; the
	// coverage is summed)
	if v.Coverage.Status != contract.CoverageRan {
		t.Fatalf("%+v", v.Coverage)
	}
	var esc contract.Escalations
	a.read("", pathEscalations, &esc)
	kinds := map[string]int{}
	for _, e := range esc.Items {
		kinds[e.Kind]++
		if e.Kind == "behavior_divergence" && e.Target == beta.Target && (e.Evidence.Trees == nil || e.ResolveWith[0] != contract.EnforceNewBehavior) {
			t.Fatalf("%+v", e)
		}
	}
	// t_beta (tests_diff) and t_gamma (NOP: an overlay test that the candidate fails) diverge; t_gamma in tests_diff is
	// red in all trees (ImportError in all three)
	if kinds["behavior_divergence"] != 2 || kinds["test_without_teeth"] != 1 {
		t.Fatalf("%v", kinds)
	}
	if g := ts["t_gamma"]; g.Gate != contract.GateTestsDiff {
		t.Fatalf("%+v", g)
	}
	// 3 trees + 3 re-runs of the red, NOP overlaid on the overlay (candidate + base with stubs; no overlay file exists on
	// the base) and baseline (base + candidate)
	if n := len(tf.runs); n != 10 || v.TestGates.NOPOracle.Status != contract.GateRan {
		t.Fatalf("%d runs: %v; NOP %+v", n, tf.runs, v.TestGates.NOPOracle)
	}
	// A signed or pending enforce_new_behavior waiver covers the tests_diff FAIL (without the overlay test that decides
	// nothing, which would leave INCONCLUSIVE).
	write(t, overlay, "tests/o.t", "t_alpha2 uses:alpha\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"`+beta.Target+`","decision":"enforce_new_behavior","reason":"other spec"}]}`)
	v, _ = a.CheckWith(context.Background(), base, CheckOptions{Peers: []string{peer}, TestsOverlay: overlay})
	if v.Verdict != contract.PASS || len(v.PendingWaivers) != 1 || !testsOf(v)["t_beta"].Waived {
		t.Fatalf("%s %v %+v", v.Verdict, v.PendingWaivers, v.Tests)
	}
}

// The overlay tests (the Saboteur's) go through NOP/Oracle with the same stubs as the candidate's: one that passes
// against the stub is test_without_teeth; one that the candidate fails and no other tree runs (the module does not
// import on the base) is behavior_divergence (NOP FAIL, tests_diff "red only in the candidate", a single escalation),
// never "red in all trees". Measured 2026-10-17 in the first real run: NOP said not_applicable and 4 Saboteur tests
// that failed on the candidate came out as teeth-less.
func TestOverlayGoesThroughNOPAndRedOnlyInCandidate(t *testing.T) {
	a, _, tf, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL added\n")
	write(t, dir, "src/other.ts", "IMPL other\n") // outside the engine's paths: nobody stubs it on the base
	overlay := filepath.Join(t.TempDir(), "ov")
	write(t, overlay, "tests/o.t", "t_teeth uses:added\nt_toothless passed\n")
	write(t, overlay, "tests/o2.t", "imports other\nt_red failed\nt_green passed\n")
	v, err := a.CheckWith(context.Background(), "", CheckOptions{TestsOverlay: overlay})
	if err != nil {
		t.Fatal(err)
	}
	nop := v.TestGates.NOPOracle
	if v.Verdict != contract.FAIL || nop.Status != contract.GateRan || strings.Join(nop.Stubs, ",") != "src/lib/f.ts::added" {
		t.Fatalf("%s %+v", v.Verdict, nop)
	}
	byGate := map[string]map[string]contract.TestResult{}
	for _, r := range v.Tests {
		if byGate[r.Gate] == nil {
			byGate[r.Gate] = map[string]contract.TestResult{}
		}
		byGate[r.Gate][r.Target[strings.LastIndex(r.Target, "::")+2:]] = r
	}
	n, d := byGate[contract.GateNOP], byGate[contract.GateTestsDiff]
	// the collection-error output that the base leaves for o2.t (other does not import) is not a test missing from the
	// candidate
	for _, r := range v.Tests {
		if strings.Contains(r.Target, "::::") {
			t.Fatalf("the collection entry of another tree was judged: %+v", r)
		}
	}
	if x := n["t_teeth"]; x.Verdict != contract.PASS || x.Base != contract.TestFailed || x.Origin != "overlay" {
		t.Fatalf("%+v", x)
	}
	if x := n["t_toothless"]; x.Verdict != contract.FAIL || x.Base != contract.TestPassed || !strings.Contains(x.Reason, "does not prove the change") {
		t.Fatalf("%+v", x)
	}
	if x := n["t_red"]; x.Verdict != contract.FAIL || x.Base != contract.TestMissing || x.Origin != "overlay" {
		t.Fatalf("%+v", x)
	}
	if x := d["t_red"]; x.Verdict != contract.INCONCLUSIVE || !strings.HasPrefix(x.Reason, onlyInCandidate) || x.Trees["base"] != contract.TestMissing {
		t.Fatalf("%+v", x)
	}
	if x := n["t_green"]; x.Verdict != contract.INCONCLUSIVE || !strings.Contains(x.Reason, "stub missing") {
		t.Fatalf("%+v", x)
	}
	if x := d["t_green"]; x.Verdict != contract.PASS || x.Characterization {
		t.Fatalf("%+v", x)
	}
	var esc contract.Escalations
	a.read("", pathEscalations, &esc)
	kinds := map[string][]string{}
	for _, e := range esc.Items {
		kinds[e.Kind] = append(kinds[e.Kind], e.Target[strings.LastIndex(e.Target, "::")+2:])
	}
	if strings.Join(kinds["behavior_divergence"], ",") != "t_red" || strings.Join(kinds["test_without_teeth"], ",") != "t_toothless" ||
		strings.Join(kinds["tooling"], ",") != "t_green" {
		t.Fatalf("%v", kinds)
	}
	for _, e := range esc.Items {
		if e.Kind == "behavior_divergence" && (e.ResolveWith[0] != contract.EnforceNewBehavior || e.Evidence.Base != contract.TestMissing) {
			t.Fatalf("%+v", e)
		}
	}
	// NOP: candidate + base with stubs and overlay (no overlay file exists on the base); baseline: base + candidate;
	// tests_diff: base + candidate (t_red is INCONCLUSIVE there, not FAIL: it is not re-run)
	if len(tf.runs) != 6 {
		t.Fatalf("%d runs: %v", len(tf.runs), tf.runs)
	}
	// enforce_new_behavior covers the Saboteur's test that the candidate fails on purpose, in both gates
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"`+n["t_red"].Target+`","decision":"enforce_new_behavior","reason":"other spec"}]}`)
	v, _ = a.CheckWith(context.Background(), "", CheckOptions{TestsOverlay: overlay})
	waived := 0
	for _, r := range v.Tests {
		if strings.HasSuffix(r.Target, "::t_red") && r.Waived {
			waived++
		}
	}
	if waived != 2 || v.Verdict != contract.FAIL || strings.Join(v.PendingWaivers, ",") != n["t_red"].Target {
		t.Fatalf("waived=%d %s %v", waived, v.Verdict, v.PendingWaivers)
	}
}

// A test red in all trees that was already red on the base with its own file (xfail reported as failure, a test that
// has always been broken) is preexisting: it is not "teeth-less" and does not leave the verdict INCONCLUSIVE. Measured
// in the VARA-2 smoke overlaid on sympy: two XFAIL of test_relational.py left every check with peers INCONCLUSIVE.
func TestTestsDiffPreexistingRedIsNotWithoutTeeth(t *testing.T) {
	a, _, tf, dir := gatesRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	// peer: implements alpha and extends tests/a.t, which already had "previous failed"
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL alpha\n")
	write(t, dir, "tests/a.t", "old uses:old\nprevious failed\nt_alpha uses:alpha\ncovers src/lib/f.ts:1-9\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "peer")
	peer := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", "--detach", base)
	// candidate: the same implementation, without its own tests
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL alpha\n")
	v, err := a.CheckWith(context.Background(), base, CheckOptions{Peers: []string{peer}})
	if err != nil {
		t.Fatal(err)
	}
	ts := testsOf(v)
	previous := ts["previous"]
	if v.Verdict != contract.PASS || previous.Verdict != contract.PASS || previous.Trees["base"] != "failed" || previous.Trees["candidate"] != "failed" ||
		!strings.Contains(previous.Reason, "preexisting") || ts["t_alpha"].Verdict != contract.PASS {
		t.Fatalf("%s %+v %+v", v.Verdict, previous, ts["t_alpha"])
	}
	var esc contract.Escalations
	a.read("", pathEscalations, &esc)
	for _, e := range esc.Items {
		if e.Kind == "test_without_teeth" {
			t.Fatalf("%+v", e)
		}
	}
	// baseline (base + candidate) + 3 trees of T + the base with its own a.t
	if n := len(tf.runs); n != 6 {
		t.Fatalf("%d runs: %v", n, tf.runs)
	}
}

func TestRanges(t *testing.T) {
	if s := ranges([]int{1, 2, 3, 7, 9, 10}); s != "1-3, 7, 9-10" {
		t.Fatal(s)
	}
}
