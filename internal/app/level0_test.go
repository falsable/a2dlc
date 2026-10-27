package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
)

// level0Fake is an engine that answers the three static operations from the files the core lays out for it: list_tests
// reads `def test_*` lines, deps_graph links a requested test file to every requested file whose dotted name its text
// mentions, and classify_lines notes the lines of `noted`.
type level0Fake struct {
	*fakeEngine
	noted   map[string][]int
	classed int
}

func (e *level0Fake) ListTests(_ context.Context, _ contract.EngineConfig, req contract.Request, dir string) (contract.ListTestsResponse, error) {
	r := contract.ListTestsResponse{SchemaVersion: 1, Files: []contract.ListedFile{}}
	for _, f := range req.ChangedFiles {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(f)))
		if err != nil {
			return r, err
		}
		lf := contract.ListedFile{File: f, Listable: true, Tests: []contract.ListedTest{}}
		for _, l := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(l, "def test_") {
				lf.Tests = append(lf.Tests, contract.ListedTest{Name: strings.SplitN(strings.TrimPrefix(l, "def "), "(", 2)[0]})
			}
		}
		r.Files = append(r.Files, lf)
	}
	return r, nil
}

func (e *level0Fake) Deps(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candDir string) (contract.DepsResponse, error) {
	r := contract.DepsResponse{SchemaVersion: 1, Edges: []contract.DepsEdge{}, Unresolved: []string{}}
	for _, from := range req.ChangedFiles {
		if !strings.HasPrefix(from, "tests/") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(candDir, filepath.FromSlash(from)))
		if err != nil {
			return r, err
		}
		for _, to := range req.ChangedFiles {
			if mod := strings.ReplaceAll(strings.TrimSuffix(to, ".py"), "/", "."); strings.HasPrefix(to, "app/") && strings.Contains(string(b), "import "+mod) {
				r.Edges = append(r.Edges, contract.DepsEdge{From: from, To: to})
			}
		}
	}
	return r, nil
}

func (e *level0Fake) ClassifyLines(ctx context.Context, ec contract.EngineConfig, req contract.Request, b, c string) (contract.ClassifyResponse, error) {
	e.classed++
	var out contract.ClassifyResponse
	for file, lines := range e.noted {
		out, _ = answering(file, map[string][]int{contract.NoteDeclarativeOnly: lines})(req)
	}
	if out.Files == nil {
		return answering("", nil)(req)
	}
	return out, nil
}

// failingTests is a test runner that must never be called by level 0.
type failingTests struct{ calls int }

func (f *failingTests) RunTests(context.Context, contract.TestsConfig, string, map[string]string, string) (engine.TestRun, error) {
	f.calls++
	return engine.TestRun{}, errors.New("level 0 must not run tests")
}

const level0Config = `{"schema_version":1,"engines":[{"language":"python","image":"fake","paths":["app/**"],"budget":{}}],` +
	`"tests":{"image":"t","paths":["tests/**"],"command":"true"},"protected_paths":["app/frozen.py"]}`

func newPyRepo(t *testing.T) (App, *level0Fake, *failingTests, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, ".a2dlc/config.json", level0Config)
	write(t, dir, ".gitignore", ".a2dlc/verdict.json\n.a2dlc/reports/\n")
	write(t, dir, "app/a.py", "def a():\n    return 1\n")
	write(t, dir, "app/b.py", "def b():\n    return 1\n")
	write(t, dir, "app/frozen.py", "FROZEN = 1\n")
	write(t, dir, "tests/test_a.py", "import app.a\n\ndef test_a_old():\n    assert app.a.a() == 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	fe := &level0Fake{fakeEngine: &fakeEngine{}}
	tests := &failingTests{}
	return App{Repo: repo, Runner: fe, Tests: tests, Version: "test", Now: func() time.Time { return time.Unix(0, 0) }}, fe, tests, dir
}

func TestLevel0RunsNoTestAndFlagsASourceNoDiffTestImports(t *testing.T) {
	a, fe, tests, dir := newPyRepo(t)
	write(t, dir, "app/a.py", "def a():\n    return 2\n")
	write(t, dir, "app/b.py", "def b():\n    return 2\n")
	write(t, dir, "app/frozen.py", "FROZEN = 2\n")
	write(t, dir, "tests/test_a.py", "import app.a\n\ndef test_a_old():\n    assert app.a.a() == 2\n\ndef test_a_new():\n    assert True\n")
	before, err := a.candidateTree("")
	if err != nil {
		t.Fatal(err)
	}
	v, err := a.Level0(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if tests.calls != 0 || fe.calls != 0 {
		t.Fatalf("level 0 ran the repo's tests (%d) or the differential (%d)", tests.calls, fe.calls)
	}
	if v.Level == nil || *v.Level != 0 || v.Verdict != contract.INCONCLUSIVE || len(v.Targets) != 0 {
		t.Fatalf("a level 0 receipt is level 0, INCONCLUSIVE and judges no target: %+v", v)
	}
	s := v.Static
	if !reflect.DeepEqual(s.NotImportedByDiffTests, []string{"app/b.py", "app/frozen.py"}) || s.Imports.Status != contract.ImportsRan {
		t.Fatalf("sources no diff test imports: %+v", s)
	}
	if !s.BringsTests || !reflect.DeepEqual(s.ChangedTests, []string{"tests/test_a.py"}) || len(s.Tests) != 1 ||
		s.Tests[0].Count == nil || *s.Tests[0].Count != 2 || s.Tests[0].New == nil || *s.Tests[0].New != 1 {
		t.Fatalf("tests of the diff: %+v", s)
	}
	if len(s.ProtectedPaths) != 1 || s.ProtectedPaths[0].File != "app/frozen.py" {
		t.Fatalf("protected: %+v", s.ProtectedPaths)
	}
	if v.Timings == nil || len(v.Timings.EngineOps) == 0 {
		t.Fatalf("level 0 records its own timings: %+v", v.Timings)
	}
	// The receipt goes beside the reports, never over level 1's verdict.json, and it does not change what was judged.
	raw, err := os.ReadFile(filepath.Join(dir, ".a2dlc", "reports", "level0.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk map[string]any
	if json.Unmarshal(raw, &onDisk) != nil || onDisk["level"] != float64(0) || onDisk["verdict"] != "INCONCLUSIVE" {
		t.Fatalf("level0.json: %s", raw)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc", "verdict.json")); !os.IsNotExist(err) {
		t.Fatalf("level 0 wrote verdict.json: %v", err)
	}
	if after, _ := a.candidateTree(""); after != before {
		t.Fatalf("writing the level 0 receipt changed the tree hash: %s -> %s", before, after)
	}
}

func TestLevel0NotesDeclarationOnlyLines(t *testing.T) {
	a, fe, _, dir := newPyRepo(t)
	fe.noted = map[string][]int{"app/consts.py": {1, 2}}
	write(t, dir, "app/consts.py", "A = 1\nB = 2\ndef c():\n    return A\n")
	v, err := a.Level0(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	var f *contract.CoverageFile
	for i := range v.Coverage.Files {
		if v.Coverage.Files[i].File == "app/consts.py" {
			f = &v.Coverage.Files[i]
		}
	}
	if f == nil || len(f.Noted) != 1 || f.Noted[0].Category != contract.NoteDeclarativeOnly || !reflect.DeepEqual(f.Noted[0].Lines, []int{1, 2}) ||
		!reflect.DeepEqual(f.Uncovered, []int{3, 4}) {
		t.Fatalf("coverage of consts.py: %+v", v.Coverage.Files)
	}
	if v.Coverage.Status != contract.CoverageUnavailable || !strings.Contains(v.Coverage.Reason, "level 0 runs no test") {
		t.Fatalf("coverage status: %s %s", v.Coverage.Status, v.Coverage.Reason)
	}
}

func TestLevel0SaysWhatTheRunnerCannotRead(t *testing.T) {
	a, _, _, dir := newPyRepo(t)
	a.Runner = &fakeEngine{}
	write(t, dir, "app/a.py", "def a():\n    return 2\n")
	write(t, dir, "tests/test_a.py", "import app.a\n")
	v, err := a.Level0(context.Background(), "HEAD")
	if err != nil {
		t.Fatalf("an engine without the operations is not an error: %v", err)
	}
	if v.Static.Imports.Status != contract.ImportsUnavailable || v.Static.Imports.Reason == "" || len(v.Static.NotImportedByDiffTests) != 0 {
		t.Fatalf("imports: %+v", v.Static)
	}
	if len(v.Static.Tests) != 1 || v.Static.Tests[0].Listable {
		t.Fatalf("a test file the runner cannot list is reported as not listable: %+v", v.Static.Tests)
	}
}

// Stop runs level 0 every turn: whatever the change and even if level 0 itself fails, it exits 0 and blocks nothing.
func TestHookStopRunsLevel0AndNeverBlocks(t *testing.T) {
	a, fe, tests, dir := newPyRepo(t)
	write(t, dir, "app/a.py", "def a():\n    return 2\n") // level 1 would FAIL this (the fake engine sees a changed target)
	var out, errb bytes.Buffer
	if c := a.Hook(context.Background(), "claude", strings.NewReader(`{"session_id":"s1"}`), &out, &errb); c != 0 || errb.Len() != 0 {
		t.Fatalf("code %d, stderr %q", c, errb.String())
	}
	var msg map[string]string
	if json.Unmarshal(out.Bytes(), &msg) != nil || !strings.Contains(msg["systemMessage"], "level 0") {
		t.Fatalf("stdout: %s", out.String())
	}
	if fe.calls != 0 || tests.calls != 0 {
		t.Fatalf("the Stop hook ran level 1 work: engine %d, tests %d", fe.calls, tests.calls)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc", "verdict.json")); !os.IsNotExist(err) {
		t.Fatalf("the Stop hook wrote the level 1 receipt: %v", err)
	}
	// level 0 failing on its own (a base that does not resolve) is not a block either
	write(t, dir, "app/a.py", "CRASH\n")
	for _, agent := range []string{"claude", "gemini"} {
		out.Reset()
		errb.Reset()
		if c := a.Hook(context.Background(), agent, strings.NewReader(`{"session_id":"s2"}`), &out, &errb); c != 0 {
			t.Fatalf("%s: code %d", agent, c)
		}
	}
	if !strings.Contains(out.String(), `"decision":"allow"`) {
		t.Fatalf("gemini: %s", out.String())
	}
	// a2dlc failing inside the hook (a config that cannot be read) is said on stderr and is not a block either
	write(t, dir, ".a2dlc/config.json", "{not json")
	git(t, dir, "commit", "-qam", "broken config")
	out.Reset()
	errb.Reset()
	if c := a.Hook(context.Background(), "claude", strings.NewReader(`{"session_id":"s3"}`), &out, &errb); c != 0 || !strings.Contains(errb.String(), "level 0") {
		t.Fatalf("code %d, stderr %q", c, errb.String())
	}
}
