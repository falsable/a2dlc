package app

import (
	"context"
	"reflect"
	"sort"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/junit"
)

// lister is a fake engine that also lists the tests of base files: listed maps a file to its tests, a file absent from it is
// answered as not listable.
type lister struct {
	*fakeEngine
	listed map[string][]contract.ListedTest
	asked  atomic.Int32
	fail   bool
}

func (l *lister) ListTests(_ context.Context, _ contract.EngineConfig, req contract.Request, _ string) (contract.ListTestsResponse, error) {
	l.asked.Add(1)
	if l.fail {
		return contract.ListTestsResponse{}, context.DeadlineExceeded
	}
	r := contract.ListTestsResponse{SchemaVersion: 1, Files: []contract.ListedFile{}}
	for _, f := range req.ChangedFiles {
		if ts, ok := l.listed[f]; ok {
			r.Files = append(r.Files, contract.ListedFile{File: f, Listable: true, Tests: ts})
		} else {
			r.Files = append(r.Files, contract.ListedFile{File: f, Reason: "dynamic", Tests: []contract.ListedTest{}})
		}
	}
	return r, nil
}

// The ids pytest writes (junit_family=xunit1, so with file; and xunit2, without it) for a parametrized function, a method
// of a class and a method of a nested class, against what the engine lists from the source of the base.
func TestExistingMatchesJUnitIDs(t *testing.T) {
	ls := listings{"tests/test_orders.py": setOf([]contract.ListedTest{
		{Class: "", Name: "test_total"}, {Class: "TestOrder", Name: "test_create"}, {Class: "TestOrder.TestLines", Name: "test_add"}})}
	inBase := []string{"tests/test_orders.py"}
	mk := func(file, class, name string) junit.Case {
		return junit.Case{File: file, Classname: class, Name: name, Status: "passed"}
	}
	cands := []junit.Case{
		mk("tests/test_orders.py", "tests.test_orders", "test_total"),                   // existed
		mk("tests/test_orders.py", "tests.test_orders", "test_total[1-2]"),              // existed, parametrized
		mk("tests/test_orders.py", "tests.test_orders", "test_total[a[0]]"),             // the suffix may contain brackets
		mk("tests/test_orders.py", "tests.test_orders", "test_refund"),                  // new
		mk("tests/test_orders.py", "tests.test_orders.TestOrder", "test_create"),        // existed, in a class
		mk("tests/test_orders.py", "tests.test_orders.TestOrder", "test_create[x]"),     // existed, in a class, parametrized
		mk("tests/test_orders.py", "tests.test_orders.TestOrder", "test_cancel"),        // new, in a class
		mk("tests/test_orders.py", "tests.test_orders.TestOrder.TestLines", "test_add"), // existed, nested class
		mk("tests/test_orders.py", "tests.test_orders.TestLines", "test_add"),           // same name, other class: new
		mk("tests/test_new.py", "tests.test_new", "test_total"),                         // another file: new
		mk("", "tests.test_orders", "test_total"),                                       // xunit2: no file attribute
		mk("", "tests.test_orders.TestOrder", "test_create[1]"),                         // xunit2, class, parametrized
		mk("", "tests.test_orders.TestOrder", "test_other"),                             // xunit2, new
		mk("", "tests.test_orders_v2", "test_total"),                                    // a module whose name starts with the base's
	}
	candidate := map[string]junit.Case{}
	for _, c := range cands {
		candidate[c.ID()] = c
	}
	own, fallback := existing(candidate, inBase, ls)
	var got []string
	for id := range own {
		got = append(got, id)
	}
	sort.Strings(got)
	want := []string{
		"tests.test_orders.TestOrder::test_create[1]", // without the file attribute (xunit2)
		"tests.test_orders::test_total",
		"tests/test_orders.py::tests.test_orders.TestOrder.TestLines::test_add",
		"tests/test_orders.py::tests.test_orders.TestOrder::test_create",
		"tests/test_orders.py::tests.test_orders.TestOrder::test_create[x]",
		"tests/test_orders.py::tests.test_orders::test_total",
		"tests/test_orders.py::tests.test_orders::test_total[1-2]",
		"tests/test_orders.py::tests.test_orders::test_total[a[0]]",
	}
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) || len(fallback) != 0 {
		t.Fatalf("existing:\n got %q\nwant %q\nfallback %v", got, want, fallback)
	}
}

// A testcase whose classname does not start with the module of its file (another rootdir) cannot be matched: its file goes
// back to the base run instead of being guessed.
func TestExistingFallsBackWhenClassnameDoesNotMatchTheFile(t *testing.T) {
	ls := listings{"tests/test_a.py": setOf([]contract.ListedTest{{Name: "test_x"}})}
	c := junit.Case{File: "tests/test_a.py", Classname: "pkg.tests.test_a", Name: "test_x", Status: "passed"}
	own, fallback := existing(map[string]junit.Case{c.ID(): c}, []string{"tests/test_a.py"}, ls)
	if len(own) != 0 || !fallback["tests/test_a.py"] {
		t.Fatalf("own %v fallback %v", own, fallback)
	}
}

// staticRepo is a repo with two base test files, tests/a.t (old, previous) and tests/b.t (oldb), and the change a feature
// brings: a new symbol with a new test in a.t, a new test in b.t.
func staticRepo(t *testing.T) (App, *fakeEngine, *fakeTests) {
	t.Helper()
	a, fe, ft, dir := gatesRepo(t)
	ft.module = true
	write(t, dir, "tests/b.t", "oldb uses:old\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "b")
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL added\n")
	write(t, dir, "tests/a.t", "old uses:old\nprevious failed\ntest_added uses:added\n")
	write(t, dir, "tests/b.t", "oldb uses:old\nnewb uses:added\n")
	return a, fe, ft
}

func baseRuns(ft *fakeTests) []string {
	var out []string
	for i, c := range ft.runs {
		if c == "nop" && ft.selected[i] != "tests/a.t tests/b.t" { // the candidate and the NOP run select both files
			out = append(out, ft.selected[i])
		}
	}
	return out
}

func judged(t *testing.T, a App) map[string]contract.TestResult {
	t.Helper()
	v, err := a.Check(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	return testsOf(v)
}

// With a listing the base is not run, and the verdict is the one the base run gave.
func TestStaticListingSkipsTheBaseRunAndKeepsTheVerdict(t *testing.T) {
	a, fe, ft := staticRepo(t)
	before := judged(t, a) // no lister: the base is run for both files
	if n := len(ft.runs); n < 3 {
		t.Fatalf("without a lister the base is run: runs %v", ft.runs)
	}
	ft.runs, ft.selected = nil, nil
	l := &lister{fakeEngine: fe, listed: map[string][]contract.ListedTest{
		"tests/a.t": {{Name: "old"}, {Name: "previous"}}, "tests/b.t": {{Name: "oldb"}}}}
	a.Runner = l
	after := judged(t, a)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("the verdict changed:\nrun   %+v\nlisted %+v", before, after)
	}
	if len(before) == 0 || before["test_added"].Verdict != contract.PASS || before["newb"].Verdict != contract.PASS {
		t.Fatalf("the new tests were not judged: %+v", before)
	}
	nop := 0
	for _, c := range ft.runs {
		if c == "nop" {
			nop++
		}
	}
	if nop != 2 || l.asked.Load() != 1 {
		t.Fatalf("with a listing only the candidate and the NOP run: nop runs %d (%v), list calls %d", nop, ft.selected, l.asked.Load())
	}
}

// The base is run for the file the engine could not list, and for that file only.
func TestStaticListingFallsBackToTheBaseRunPerFile(t *testing.T) {
	a, fe, ft := staticRepo(t)
	l := &lister{fakeEngine: fe, listed: map[string][]contract.ListedTest{"tests/a.t": {{Name: "old"}, {Name: "previous"}}}}
	a.Runner = l
	got := judged(t, a)
	if got["test_added"].Verdict != contract.PASS || got["newb"].Verdict != contract.PASS {
		t.Fatalf("%+v", got)
	}
	if b := baseRuns(ft); !reflect.DeepEqual(b, []string{"tests/b.t"}) {
		t.Fatalf("the base was run for %v, runs %v", b, ft.selected)
	}
	if _, wrong := got["oldb"]; wrong {
		t.Fatalf("an existing test of the unlisted file was judged as new: %+v", got)
	}
}

// An engine that fails, or does not answer, leaves every file to the base run.
func TestStaticListingFailureFallsBackToTheBaseRun(t *testing.T) {
	a, fe, ft := staticRepo(t)
	a.Runner = &lister{fakeEngine: fe, fail: true}
	got := judged(t, a)
	if _, wrong := got["oldb"]; wrong || got["test_added"].Verdict != contract.PASS {
		t.Fatalf("%+v", got)
	}
	if n := len(ft.runs); n < 3 {
		t.Fatalf("the base was not run: %v", ft.selected)
	}
}

// The candidate and the NOP run overlap in time, and the verdict does not depend on which one finishes first.
func TestCandidateAndNOPRunConcurrentlyWithTheSameResult(t *testing.T) {
	a, fe, ft := staticRepo(t)
	a.Runner = &lister{fakeEngine: fe, listed: map[string][]contract.ListedTest{
		"tests/a.t": {{Name: "old"}, {Name: "previous"}}, "tests/b.t": {{Name: "oldb"}}}}
	sequential := judged(t, a)
	ft.delay = 300 * time.Millisecond // longer than the export of either tree, so the overlap does not depend on scheduling
	for i := 0; i < 3; i++ {
		ft.runs, ft.selected, ft.peak = nil, nil, 0
		if got := judged(t, a); !reflect.DeepEqual(got, sequential) {
			t.Fatalf("round %d: concurrent runs changed the result:\n%+v\n%+v", i, got, sequential)
		}
		if ft.peak < 2 {
			t.Fatalf("round %d: candidate and NOP did not overlap, peak %d", i, ft.peak)
		}
	}
}
