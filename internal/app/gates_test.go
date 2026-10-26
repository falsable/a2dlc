package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/junit"
)

// withoutImpl removes the lines that are markers for the tests (IMPL <symbol>, LINES a-b), not behavior of f.
func withoutImpl(b []byte) []byte {
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(l, "IMPL ") && !strings.HasPrefix(l, "LINES ") {
			out = append(out, l)
		}
	}
	return []byte(strings.Join(out, "\n"))
}

// Stub of the fake engine: each IMPL the candidate adds goes to the base as STUB.
func (m *fakeEngine) Stub(_ context.Context, _ contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.StubResponse, map[string][]byte, error) {
	r := contract.StubResponse{SchemaVersion: 1, Stubbed: []contract.StubFile{}, Unstubbed: []string{}}
	files := map[string][]byte{}
	for _, f := range req.ChangedFiles {
		old, _ := os.ReadFile(filepath.Join(baseDir, f))
		newContent, _ := os.ReadFile(filepath.Join(candDir, f))
		var symbols []string
		text := string(old)
		for _, l := range strings.Split(string(newContent), "\n") {
			if s, ok := strings.CutPrefix(l, "IMPL "); ok && !strings.Contains(string(old), l) {
				symbols = append(symbols, s)
				text += "\nSTUB " + s
			}
		}
		if len(symbols) > 0 {
			r.Stubbed = append(r.Stubbed, contract.StubFile{File: f, Symbols: symbols})
			files[f] = []byte(text)
		}
	}
	return r, files, nil
}

// Signatures of the fake engine: each "IMPL <symbol>" of the candidate is a public signature, on the line where it is.
func (m *fakeEngine) Signatures(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candDir string) (contract.SignaturesResponse, error) {
	r := contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}
	for _, f := range req.ChangedFiles {
		newContent, _ := os.ReadFile(filepath.Join(candDir, f))
		sigFile := contract.SignatureFile{File: f, Symbols: []contract.Signature{}}
		for i, l := range strings.Split(string(newContent), "\n") {
			if s, ok := strings.CutPrefix(l, "IMPL "); ok {
				sigFile.Symbols = append(sigFile.Symbols, contract.Signature{Name: s, Signature: "def " + s + "()", Lines: []int{i + 1, i + 1}})
			}
		}
		r.Files = append(r.Files, sigFile)
	}
	return r, nil
}

// Mutate of the fake engine (block 5): one mutant per changed line, which replaces that line with "MUT <n>", up to
// max_mutants; what does not fit goes to not_generated.
func (m *fakeEngine) Mutate(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candDir string) (contract.MutateResponse, map[string][]byte, error) {
	var b contract.MutateBudget
	if err := json.Unmarshal(req.Budget, &b); err != nil {
		return contract.MutateResponse{}, nil, err
	}
	r := contract.MutateResponse{SchemaVersion: 1, Mutants: []contract.Mutant{}, NotGenerated: []contract.FileReason{}, Pruned: []contract.PrunedMutant{}}
	files := map[string][]byte{}
	for _, f := range req.ChangedFiles {
		newContent, _ := os.ReadFile(filepath.Join(candDir, f))
		for _, l := range b.ChangedLines[f] {
			if len(r.Mutants) == b.MaxMutants {
				r.NotGenerated = append(r.NotGenerated, contract.FileReason{File: f, Reason: fmt.Sprintf("max_mutants=%d", b.MaxMutants)})
				break
			}
			lines := strings.Split(string(newContent), "\n")
			lines[l-1] = fmt.Sprintf("MUT %d", l)
			id := fmt.Sprintf("m%03d", len(r.Mutants)+1)
			r.Mutants = append(r.Mutants, contract.Mutant{ID: id, File: f, Line: l, Operator: contract.OpConstant, Description: "numeric constant changed: +1"})
			files[id] = []byte(strings.Join(lines, "\n"))
		}
	}
	return r, files, nil
}

// fakeTests runs the .t files of tests/: one "name state" line per testcase. The state "uses:<symbol>" passes if some
// src file has "IMPL <symbol>", fails with NotImplementedError if it has "STUB <symbol>", and otherwise fails with
// ImportError. "nop" runs the files of $A2DLC_TESTS; "baseline", all of them. A .t with KILL leaves no JUnit. A line
// "covers <file>:<a>-<b>" leaves LCOV coverage with those lines executed (and lines 1-99 as statements). A line
// "imports <symbol>" is the module's import: without "IMPL" or "STUB" of that symbol in src the whole file does not
// collect (no testcase in the JUnit), like a module ImportError in pytest.
type fakeTests struct {
	mu       sync.Mutex // the candidate and the NOP run are concurrent
	runs     []string
	selected []string      // A2DLC_TESTS of each run, in the order they started
	module   bool          // classname is the module of the file, as pytest writes it, instead of "c"
	delay    time.Duration // each run takes at least this long
	inflight int
	peak     int // most runs in flight at once
}

// fakeTestsDigest is the digest the fake tests image resolves to; the baseline gate keys its cache by it.
const fakeTestsDigest = "sha256:00000000000000000000000000000000000000000000000000000000000000f1"

// ImageDigest makes the fake image a pushed one.
func (f *fakeTests) ImageDigest(context.Context, string) (string, error) { return fakeTestsDigest, nil }

func (f *fakeTests) RunTests(_ context.Context, _ contract.TestsConfig, command string, env map[string]string, tree string) (engine.TestRun, error) {
	f.mu.Lock()
	f.runs = append(f.runs, command)
	f.selected = append(f.selected, env["A2DLC_TESTS"])
	f.inflight++
	f.peak = max(f.peak, f.inflight)
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.inflight--
		f.mu.Unlock()
	}()
	time.Sleep(f.delay)
	var files []string
	if command == "nop" {
		files = strings.Fields(env["A2DLC_TESTS"])
	} else {
		m, _ := filepath.Glob(filepath.Join(tree, "tests", "*.t"))
		for _, x := range m {
			files = append(files, "tests/"+filepath.Base(x))
		}
	}
	covers := map[string]map[int]bool{}
	src := ""
	filepath.WalkDir(filepath.Join(tree, "src"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			src += string(b) + "\n"
		}
		return nil
	})
	var x strings.Builder
	x.WriteString("<testsuites><testsuite>")
	exit := 0
	for _, a := range files {
		b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(a)))
		if err != nil {
			continue
		}
		lines := strings.Split(strings.TrimSpace(string(b)), "\n")
		collects := true
		for _, l := range lines {
			if s, ok := strings.CutPrefix(l, "imports "); ok && !strings.Contains(src, "IMPL "+s) && !strings.Contains(src, "STUB "+s) {
				collects, exit = false, 1
			}
		}
		if !collects {
			// like pytest: a <testcase> without a test name, with <error>, for the module that does not import
			fmt.Fprintf(&x, `<testcase classname="" name="%s" file="%s"><error message="collection failure"/></testcase>`, strings.TrimSuffix(a, ".t"), a)
			continue
		}
		for _, l := range lines {
			fields := strings.Fields(l)
			if len(fields) == 1 && fields[0] == "KILL" {
				return engine.TestRun{Exit: 3, Log: "dead process"}, nil
			}
			if len(fields) == 2 && fields[0] == "imports" {
				continue
			}
			if len(fields) == 2 && fields[0] == "covers" {
				var file string
				var d, h int
				if i := strings.LastIndex(fields[1], ":"); i > 0 {
					file = fields[1][:i]
					fmt.Sscanf(fields[1][i+1:], "%d-%d", &d, &h)
				}
				if covers[file] == nil {
					covers[file] = map[int]bool{}
				}
				for n := d; n <= h; n++ {
					covers[file][n] = true
				}
				continue
			}
			if len(fields) != 2 {
				continue
			}
			state := fields[1]
			if s, ok := strings.CutPrefix(state, "uses:"); ok {
				switch {
				case strings.Contains(src, "IMPL "+s):
					state = "passed"
				case strings.Contains(src, "STUB "+s):
					state = "failed:NotImplementedError"
				default:
					state = "failed:ImportError: cannot import name " + s
				}
			}
			class := "c"
			if f.module {
				class = moduleOf(a)
			}
			fmt.Fprintf(&x, `<testcase classname="%s" name="%s" file="%s">`, class, fields[0], a)
			if m, ok := strings.CutPrefix(state, "failed"); ok {
				exit = 1
				fmt.Fprintf(&x, `<failure message="%s"/>`, strings.TrimPrefix(m, ":"))
			}
			x.WriteString("</testcase>")
		}
	}
	x.WriteString("</testsuite></testsuites>")
	r := engine.TestRun{JUnit: []byte(x.String()), Exit: exit}
	if len(covers) > 0 {
		var cov strings.Builder
		for file, lines := range covers {
			fmt.Fprintf(&cov, "SF:/work/%s\n", file)
			for n := 1; n < 100; n++ {
				h := 0
				if lines[n] {
					h = 1
				}
				fmt.Fprintf(&cov, "DA:%d,%d\n", n, h)
			}
			cov.WriteString("end_of_record\n")
		}
		r.Coverage = []byte(cov.String())
	}
	return r, nil
}

func testCase(id, status, msg string) junit.Case {
	p := strings.SplitN(id, "::", 2)
	return junit.Case{File: p[0], Classname: "c", Name: p[1], Status: status, Message: msg}
}

func mapOf(cs ...junit.Case) map[string]junit.Case {
	m := map[string]junit.Case{}
	for _, c := range cs {
		m[c.ID()] = c
	}
	return m
}

func TestJudgeNOP(t *testing.T) {
	invalid, _ := classifier([]string{"ImportError"}, []string{"External.Internal.added"})
	candidate := mapOf(testCase("t.py::old", "passed", ""), testCase("t.py::valid", "passed", ""), testCase("t.py::characterizes", "passed", ""),
		testCase("t.py::import", "passed", ""), testCase("t.py::nostub", "passed", ""), testCase("t.py::absent", "passed", ""),
		testCase("t.py::red", "failed", "assert 1 == 2"), testCase("t.py::skipped", "skipped", ""))
	own := mapOf(testCase("t.py::old", "passed", ""))
	nop := mapOf(testCase("t.py::old", "passed", ""), testCase("t.py::valid", "failed", "NotImplementedError"),
		testCase("t.py::characterizes", "passed", ""), testCase("t.py::import", "failed", "ImportError: x"),
		testCase("t.py::nostub", "failed", "AttributeError: 'Internal' object has no attribute 'added'"),
		testCase("t.py::red", "failed", "assert"))
	want := map[string]string{"valid": "PASS", "characterizes": "PASS", "import": "INCONCLUSIVE", "nostub": "INCONCLUSIVE",
		"absent": "INCONCLUSIVE", "red": "FAIL"}
	for _, intent := range []bool{false, true} {
		got := map[string]contract.TestResult{}
		for _, r := range judgeNOP(candidate, own, nop, intent, invalid) {
			got[strings.TrimPrefix(r.Target, "t.py::c::")] = r
		}
		if _, ok := got["old"]; ok {
			t.Error("a test that already existed on the base was judged as new")
		}
		if _, ok := got["skipped"]; ok {
			t.Error("a test skipped on the candidate was judged")
		}
		for n, v := range want {
			if n == "characterizes" && intent {
				v = "FAIL"
			}
			if got[n].Verdict != v {
				t.Errorf("intent=%v %s: %+v, want %s", intent, n, got[n], v)
			}
		}
		if got["characterizes"].Characterization == intent {
			t.Errorf("intent=%v: characterization=%v", intent, got["characterizes"].Characterization)
		}
	}
}

func TestNewRedsDoNotCountPrevious(t *testing.T) {
	base := mapOf(testCase("t.py::previous", "failed", ""), testCase("t.py::breaks", "passed", ""))
	candidate := mapOf(testCase("t.py::previous", "failed", ""), testCase("t.py::breaks", "failed", ""), testCase("t.py::fresh", "error", ""),
		testCase("t.py::judged", "failed", ""))
	got := newReds(base, candidate, map[string]bool{"t.py::c::judged": true})
	if strings.Join(got, ",") != "t.py::c::breaks,t.py::c::fresh" {
		t.Fatal(got)
	}
}

// Flaky only if it is intermittent on the base; a stable green on the base and an intermittent red on the candidate
// blocks.
func TestJudgeReds(t *testing.T) {
	id := "t.py::c::x"
	runs := func(s ...string) []map[string]junit.Case {
		var out []map[string]junit.Case
		for _, e := range s {
			out = append(out, mapOf(testCase("t.py::x", e, "")))
		}
		return out
	}
	base, candidate := mapOf(testCase("t.py::x", "passed", "")), mapOf(testCase("t.py::x", "failed", "m"))
	cases := []struct {
		baseRuns, candRuns []map[string]junit.Case
		want               string
		flaky              bool
	}{
		{runs("failed", "passed", "passed"), runs("failed", "failed", "failed"), "PASS", true},
		{runs("passed", "passed", "passed"), runs("passed", "failed", "passed"), "FAIL", false},
		{runs("passed", "passed", "passed"), runs("failed", "failed", "failed"), "FAIL", false},
	}
	for i, c := range cases {
		r := judgeReds([]string{id}, base, candidate, c.baseRuns, c.candRuns)
		if len(r) != 1 || r[0].Verdict != c.want || r[0].Flaky != c.flaky || len(r[0].Reruns.Base) != 3 {
			t.Errorf("case %d: %+v", i, r)
		}
	}
}

func TestReadable(t *testing.T) {
	if (run{junit: false}).readable() == nil {
		t.Error("without JUnit passed")
	}
	if (run{junit: true, exit: 1, cases: mapOf(testCase("t.py::a", "passed", ""))}).readable() == nil {
		t.Error("red without identifiable failed tests passed")
	}
	if err := (run{junit: true, exit: 1, cases: mapOf(testCase("t.py::a", "failed", ""))}).readable(); err != nil {
		t.Error(err)
	}
}

const gatesConfig = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],
 "tests":{"image":"fake","paths":["tests/**"],"command":"nop","baseline_command":"baseline","invalid_base_failures":["ImportError"]}}`

func gatesRepo(t *testing.T) (App, *fakeEngine, *fakeTests, string) {
	t.Helper()
	t.Setenv("A2DLC_CACHE_DIR", t.TempDir())
	a, m, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", gatesConfig)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\n")
	write(t, dir, "tests/a.t", "old uses:old\nprevious failed\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base with tests")
	tf := &fakeTests{}
	a.Tests = tf
	return a, m, tf, dir
}

func testsOf(v contract.Verdict) map[string]contract.TestResult {
	m := map[string]contract.TestResult{}
	for _, t := range v.Tests {
		m[t.Target[strings.LastIndex(t.Target, "::")+2:]] = t
	}
	return m
}

func TestCheckFeatureWithTestPasses(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL added\n")
	write(t, dir, "tests/n.t", "test_added uses:added\n")
	v, err := a.Check(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	ts := testsOf(v)
	if v.Verdict != contract.PASS || ts["test_added"].Verdict != contract.PASS || ts["test_added"].Base != contract.TestFailed {
		t.Fatalf("%s %+v %+v", v.Verdict, v.TestGates, v.Tests)
	}
	if v.TestGates.NOPOracle.Status != contract.GateRan || len(v.TestGates.NOPOracle.Stubs) != 1 || v.TestGates.Baseline.Status != contract.GateRan {
		t.Fatalf("%+v", v.TestGates)
	}
	if _, ok := ts["previous"]; ok {
		t.Fatal("a red from before on the base was judged")
	}
	// verdict.timings: the check times itself (candidate and base with stubs at least, the stub operation, and a total)
	if v.Timings == nil || v.Timings.TotalMs <= 0 {
		t.Fatalf("timings: %+v", v.Timings)
	}
	names := map[string]bool{}
	for _, r := range v.Timings.TestRuns {
		names[r.Name] = true
	}
	if !names["candidate"] || !names["base_with_stubs"] {
		t.Fatalf("test runs not timed: %+v", v.Timings.TestRuns)
	}
	var ops []string
	for _, o := range v.Timings.EngineOps {
		ops = append(ops, o.Name)
	}
	if !strings.Contains(strings.Join(ops, ","), "stub") || v.Timings.StubsMs <= 0 {
		t.Fatalf("stub operation not timed: %+v", v.Timings)
	}
}

// A test that passes with stubs does not prove the change: FAIL and escalation; with a characterization_test waiver
// without a signature, check passes and verify stops.
func TestCheckTestWithoutTeethAndItsWaiver(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nIMPL added\n")
	write(t, dir, "tests/n.t", "test_any passed\n")
	v, _ := a.Check(context.Background(), "")
	if v.Verdict != contract.FAIL || testsOf(v)["test_any"].Reason == "" {
		t.Fatalf("%s %+v", v.Verdict, v.Tests)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil || len(esc.Items) != 1 || esc.Items[0].Kind != "test_without_teeth" {
		t.Fatalf("%v %+v", err, esc)
	}
	target := testsOf(v)["test_any"].Target
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"`+target+`","decision":"characterization_test","reason":"r"}]}`)
	v, _ = a.Check(context.Background(), "")
	if v.Verdict != contract.PASS || len(v.PendingWaivers) != 1 || !testsOf(v)["test_any"].Waived {
		t.Fatalf("%s %v %+v", v.Verdict, v.PendingWaivers, v.Tests)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "c")
	probs, _ := a.Verify("HEAD")
	if !strings.Contains(strings.Join(probs, "|"), "waiver for "+target) {
		t.Fatal(probs)
	}
	// a waiver of another class does not cover the test
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"`+target+`","decision":"quarantine_flaky","reason":"r"}]}`)
	if v, _ = a.Check(context.Background(), "HEAD~1"); v.Verdict != contract.FAIL {
		t.Fatal("quarantine_flaky covered a NOP FAIL")
	}
}

// A regression of an existing test blocks after its re-runs, and tests do not reach the engine.
func TestCheckRegressionBlocks(t *testing.T) {
	a, m, tf, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	v, _ := a.Check(context.Background(), "")
	r := testsOf(v)["old"]
	if v.Verdict != contract.FAIL || r.Gate != contract.GateBaseline || r.Reruns == nil || len(r.Reruns.Base) != 3 {
		t.Fatalf("%s %+v", v.Verdict, v.Tests)
	}
	if m.calls != 1 || v.TestGates.NOPOracle.Status != contract.GateNotApplicable {
		t.Fatalf("engine %d, gates %+v", m.calls, v.TestGates)
	}
	// 1 base + 1 candidate + 3×2 re-runs; the second time the base comes from the cache
	if len(tf.runs) != 8 {
		t.Fatal(tf.runs)
	}
	v, _ = a.Check(context.Background(), "")
	if !v.TestGates.Baseline.Cached || len(tf.runs) != 15 {
		t.Fatalf("%+v %d", v.TestGates.Baseline, len(tf.runs))
	}
}

// A baseline cache planted in the tree, with the exact key and the regression set as red previous, is not read: the
// cache lives outside the repo.
func TestPlantedCacheInTreeIsIgnored(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	cfg, _ := a.config()
	key := cacheKey(gateInput{cfg: *cfg.Tests, baseSha: base, sources: []string{"src/lib/f.ts"}}, "baseline", fakeTestsDigest)
	fake := `{"exit":1,"junit":"<testsuite><testcase classname=\"c\" name=\"old\" file=\"tests/a.t\"><failure message=\"m\"/></testcase></testsuite>"}`
	for _, d := range []string{filepath.Join(dir, ".a2dlc", "cache"), filepath.Join(dir, ".a2dlc")} {
		write(t, d, "baseline-"+key+".json", fake)
	}
	v, _ := a.Check(context.Background(), "")
	if v.Verdict != contract.FAIL || v.TestGates.Baseline.Cached {
		t.Fatalf("the planted cache washed the regression: %s %+v", v.Verdict, v.TestGates.Baseline)
	}
}

// A flaky test with its signed quarantine is covered and is not escalated again; a quarantine over a stable regression
// warns that the core saw no intermittency.
func TestQuarantineOfFlaky(t *testing.T) {
	v := contract.Verdict{Verdict: contract.PASS, PendingWaivers: []string{}, TestGates: &contract.TestGates{},
		Tests: []contract.TestResult{
			{Target: "t.py::c::flaky", Gate: contract.GateBaseline, Verdict: contract.PASS, Flaky: true},
			{Target: "t.py::c::stable", Gate: contract.GateBaseline, Verdict: contract.FAIL, Reason: "r"}}}
	w := map[string]contract.Waiver{
		"t.py::c::flaky":  {Target: "t.py::c::flaky", Decision: contract.QuarantineFlaky},
		"t.py::c::stable": {Target: "t.py::c::stable", Decision: contract.QuarantineFlaky}}
	addTests(&v, w, map[string]bool{"t.py::c::flaky": true})
	if !v.Tests[0].Waived || !v.Tests[1].Waived || v.Verdict != contract.PASS || strings.Join(v.PendingWaivers, ",") != "t.py::c::stable" {
		t.Fatalf("%+v %v", v.Tests, v.PendingWaivers)
	}
	esc := escalations(v)
	if len(esc) != 1 || esc[0].Kind != "signature_pending" || !strings.Contains(esc[0].Summary, "saw no intermittency") {
		t.Fatalf("%+v", esc)
	}
}

func TestCheckTestsDoNotReachEngineAndDeadRunIsInconclusive(t *testing.T) {
	a, m, _, dir := gatesRepo(t)
	write(t, dir, "tests/conftest.t", "KILL\n")
	v, _ := a.Check(context.Background(), "")
	if v.Verdict != contract.INCONCLUSIVE || v.TestGates.NOPOracle.Status != contract.GateError || m.calls != 0 {
		t.Fatalf("%s %+v engine=%d", v.Verdict, v.TestGates, m.calls)
	}
	for _, o := range v.OutOfScope {
		if strings.HasPrefix(o.File, "tests/") {
			t.Fatalf("a test was left out of scope of the engines: %+v", o)
		}
	}
}

func TestEscalationsWithStableID(t *testing.T) {
	v := contract.Verdict{Targets: []contract.VerdictTarget{{TargetResult: contract.TargetResult{Target: "a.ts::f", Verdict: contract.FAIL,
		Counterexample: &contract.Counterexample{Args: "1", Old: "1", New: "2"}}}}, PendingWaivers: []string{}}
	e1, e2 := escalations(v), escalations(v)
	if len(e1) != 1 || e1[0].ID != e2[0].ID || e1[0].Kind != "behavior_divergence" || !strings.HasPrefix(e1[0].ID, "esc-") {
		t.Fatal(e1)
	}
	ids := []string{escalation("tooling", "a::b", "", contract.Evidence{}).ID, escalation("flaky_test", "a::b", "", contract.Evidence{}).ID}
	sort.Strings(ids)
	if ids[0] == ids[1] {
		t.Fatal("two kinds of the same target with the same id")
	}
}

// A feature or a bugfix declares the behavior change by its change_kind, so a new test that passes on the base is a FAIL
// without any stub. A refactor declares none and keeps the characterization path.
func TestDeclaresBehaviorByChangeKind(t *testing.T) {
	for kind, want := range map[string]bool{"feature": true, "bugfix": true, "refactor": false, "": false} {
		if got := declaresBehavior(gateInput{changeKind: kind}); got != want {
			t.Fatalf("change_kind %q: %v", kind, got)
		}
	}
	if !declaresBehavior(gateInput{intent: true}) || !declaresBehavior(gateInput{symbols: []string{"src/lib/f.ts::g"}}) {
		t.Fatal("an enforce_new_behavior waiver and a stub still declare the behavior change")
	}
}

// End to end through gateNOP: the candidate adds a test that passes on the base. A feature makes it a FAIL; a refactor
// reads it as a characterization test and passes it.
func TestGateNOPJudgesAPassingNewTestByChangeKind(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	write(t, dir, "tests/new.t", "newcase uses:old\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "candidate test")
	head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	cfg, _ := a.config()
	for kind, want := range map[string]string{"feature": contract.FAIL, "refactor": contract.PASS} {
		_, res := a.gateNOP(context.Background(), gateInput{cfg: *cfg.Tests, baseSha: base, tree: head, tests: []string{"tests/new.t"}, changeKind: kind})
		if len(res) != 1 || res[0].Verdict != want {
			t.Fatalf("change_kind %s: %+v", kind, res)
		}
	}
}

// The baseline cache key carries the image digest: a moved tag is another environment.
func TestCacheKeyCarriesTheImageDigest(t *testing.T) {
	in := gateInput{baseSha: "b", cfg: contract.TestsConfig{Image: "tests:1"}}
	a, b, none := cacheKey(in, "base", "sha256:a"), cacheKey(in, "base", "sha256:b"), cacheKey(in, "base", "")
	if a == b || a == none || b == none {
		t.Fatalf("digests do not separate the keys: %s %s %s", a, b, none)
	}
}

// Two runs of the baseline that disagree are named by their first differing test; equal runs agree.
func TestUnreproducibleNamesTheFirstDifference(t *testing.T) {
	first := mapOf(testCase("tests/a.t::one", "passed", ""), testCase("tests/a.t::two", "passed", ""))
	same := mapOf(testCase("tests/a.t::one", "passed", ""), testCase("tests/a.t::two", "passed", ""))
	if got := unreproducible(first, same); got != "" {
		t.Fatalf("equal runs reported as different: %s", got)
	}
	flipped := mapOf(testCase("tests/a.t::one", "passed", ""), testCase("tests/a.t::two", "failed", ""))
	if got := unreproducible(first, flipped); !strings.Contains(got, "::two was passed on one run and failed") {
		t.Fatalf("status change not named: %q", got)
	}
	missing := mapOf(testCase("tests/a.t::one", "passed", ""))
	if got := unreproducible(first, missing); !strings.Contains(got, "::two was passed on one run and missing") {
		t.Fatalf("missing test not named: %q", got)
	}
}
