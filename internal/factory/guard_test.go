package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

const testsConfig = `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],` +
	`"tests":{"image":"x","paths":["tests/**"],"command":"true"}}`

// targetWithTests is target plus two committed test files; the repo declares tests/** as its test files in config.
func targetWithTests(t *testing.T) Deps {
	t.Helper()
	d := target(t)
	dir := d.Repo.Dir
	write(t, dir, ".a2dlc/config.json", testsConfig)
	write(t, dir, "tests/test_f.py", "def test_f():\n    assert f(1) == 1\n")
	write(t, dir, "tests/test_keep.py", "def test_keep():\n    assert True\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "tests")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	d.Repo = repo
	return d
}

func spend() func(int, context.Context) (*contract.Cost, bool, error) {
	return okCost(&contract.Cost{USD: 0.3, InputTokens: 100, OutputTokens: 10, Turns: 2})
}

func TestLaneThatEditsABaseTestIsRejectedAndTheFilesRecorded(t *testing.T) {
	d := targetWithTests(t)
	claude := &fakeClaude{script: spend(), edit: func(work string) {
		os.WriteFile(filepath.Join(work, "tests", "test_f.py"), []byte("def test_f():\n    assert True\n"), 0o644)
		os.Remove(filepath.Join(work, "tests", "test_keep.py"))
		os.WriteFile(filepath.Join(work, "tests", "test_new.py"), []byte("def test_new():\n    assert f(2) == 3\n"), 0o644)
	}}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-1"]
	want := []contract.TestTouch{{Path: "tests/test_f.py", Change: contract.TestModified}, {Path: "tests/test_keep.py", Change: contract.TestDeleted}}
	if l.Status != contract.LaneRejected || len(l.TestsTouched) != 2 || l.TestsTouched[0] != want[0] || l.TestsTouched[1] != want[1] {
		t.Fatalf("%+v", l)
	}
	if !strings.Contains(l.Reason, "tests/test_f.py (modified)") || !strings.Contains(l.Reason, "tests/test_keep.py (deleted)") || strings.Contains(l.Reason, "test_new") {
		t.Fatal(l.Reason)
	}
	// it stops at the builder: one call, its cost counted, no candidate, nothing accepted, and the refusal is the factory's
	if len(claude.seen()) != 1 || l.Usage.USD != 0.3 || l.CandidateRef != nil || rep.Accepted != 0 || rep.CostPerAcceptedUSD != nil {
		t.Fatalf("calls=%d %+v", len(claude.seen()), rep)
	}
	for _, s := range l.Stages {
		if s.Stage == "4" && s.Status == contract.StageDone {
			t.Fatalf("the oracle ran: %+v", s)
		}
	}
	b, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "ledgers", "issue-1.json"))
	// the lane's own ledger has the builder call as refused, with what it cost; the file list is in the report's lane
	if err != nil || !strings.Contains(string(b), `"status": "refused"`) || !strings.Contains(string(b), `"usd": 0.3`) {
		t.Fatalf("%v %s", err, b)
	}
}

func TestLaneThatOnlyAddsTestFilesIsNotRejectedForIt(t *testing.T) {
	d := targetWithTests(t)
	claude := &fakeClaude{script: spend(), edit: func(work string) {
		os.WriteFile(filepath.Join(work, "tests", "test_new.py"), []byte("def test_new():\n    assert f(2) == 3\n"), 0o644)
	}}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-1"]
	if len(l.TestsTouched) != 0 || strings.Contains(l.Reason, "base test files") || len(claude.seen()) != 1 {
		t.Fatalf("%+v", l)
	}
}

func TestGuardGlobsComeFromTheRepoConfigNotFromCode(t *testing.T) {
	d := targetWithTests(t)
	// same files, but the repo declares only docs/** as tests: editing tests/test_f.py is no longer the guard's concern
	write(t, d.Repo.Dir, ".a2dlc/config.json", strings.Replace(testsConfig, `"paths":["tests/**"]`, `"paths":["docs/**"]`, 1))
	git(t, d.Repo.Dir, "commit", "-qam", "other test globs")
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	g, err := newTestGuard(d.Repo, base)
	if err != nil || len(g.files) != 0 {
		t.Fatalf("%v %v", err, g.files)
	}
	// and a repo with no tests block has nothing to guard
	write(t, d.Repo.Dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[]}`)
	git(t, d.Repo.Dir, "commit", "-qam", "no tests")
	g, err = newTestGuard(d.Repo, git(t, d.Repo.Dir, "rev-parse", "HEAD"))
	if err != nil || len(g.files) != 0 || len(g.check(t.TempDir())) != 0 {
		t.Fatalf("%v %v", err, g.files)
	}
}

func TestGuardCheckKeepsTheFirstFindingAndCleanWorkIsClean(t *testing.T) {
	d := targetWithTests(t)
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	g, err := newTestGuard(d.Repo, base)
	if err != nil || len(g.files) != 2 {
		t.Fatalf("%v %v", err, g.files)
	}
	work := t.TempDir()
	if err := d.Repo.Export(base, work); err != nil {
		t.Fatal(err)
	}
	if got := g.check(work); len(got) != 0 {
		t.Fatalf("an untouched copy of the base: %+v", got)
	}
	os.Remove(filepath.Join(work, "tests", "test_keep.py"))
	if got := g.check(work); len(got) != 1 || got[0].Change != contract.TestDeleted {
		t.Fatalf("%+v", got)
	}
}
