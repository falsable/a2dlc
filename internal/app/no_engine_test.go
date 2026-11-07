package app

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// noEngineRepo is a repo whose config has the given extra keys and a TypeScript engine on src/lib only, with a Go file, a
// doc, a data file and a Go test that no engine covers (a2dlc-bt9).
func noEngineRepo(t *testing.T, extra string) (App, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	write(t, dir, "cmd/main.go", "package main\n")
	write(t, dir, "cmd/main_test.go", "package main\n")
	write(t, dir, "README.md", "hello\n")
	write(t, dir, "data/x.json", "{}\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]`+extra+`}`)
	write(t, dir, ".gitignore", ".a2dlc/verdict.json\n.a2dlc/reports/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return App{Repo: repo, Runner: &fakeEngine{}, Version: "test", Now: func() time.Time { return time.Unix(0, 0) }}, dir
}

func reasonOf(v contract.Verdict, file string) string {
	for _, o := range v.OutOfScope {
		if o.File == file {
			return o.Reason
		}
	}
	return ""
}

func TestSourceNoEngineCoversIsInconclusive(t *testing.T) {
	a, dir := noEngineRepo(t, "")
	write(t, dir, "cmd/main.go", "package main\n\nfunc main() {}\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE || reasonOf(v, "cmd/main.go") != "no_engine" {
		t.Fatalf("a changed Go file with no Go engine must be INCONCLUSIVE no_engine: v=%s oos=%+v err=%v", v.Verdict, v.OutOfScope, err)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil || len(esc.Items) != 1 || esc.Items[0].Kind != "tooling" ||
		esc.Items[0].Target != "cmd/main.go::*" || !strings.Contains(esc.Items[0].Summary, "out_of_scope_paths") {
		t.Fatalf("the escalation must name the file and the key that declares it out of scope: %+v err=%v", esc, err)
	}
}

func TestSourceDeclaredOutOfScopeIsPassAndRecorded(t *testing.T) {
	a, dir := noEngineRepo(t, `,"out_of_scope_paths":["cmd/**"]`)
	write(t, dir, "cmd/main.go", "package main\n\nfunc main() {}\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || reasonOf(v, "cmd/main.go") != "excluded_by_config" {
		t.Fatalf("a path the repo declares out of scope is PASS and says so: v=%s oos=%+v err=%v", v.Verdict, v.OutOfScope, err)
	}
}

// The declaration never reaches a file an engine covers, and it does not hide a file it does not match.
func TestDeclaredOutOfScopeDoesNotHideOtherSources(t *testing.T) {
	a, dir := noEngineRepo(t, `,"out_of_scope_paths":["docs/**"]`)
	write(t, dir, "cmd/main.go", "package main\n\nfunc main() {}\n")
	v, _ := a.Check(context.Background(), "HEAD")
	if v.Verdict != contract.INCONCLUSIVE || reasonOf(v, "cmd/main.go") != "no_engine" {
		t.Fatalf("v=%s oos=%+v", v.Verdict, v.OutOfScope)
	}
}

// Docs, data and test code keep the verdict they had: out of scope, visible, PASS.
func TestNonSourceNoEngineKeepsPass(t *testing.T) {
	for _, file := range []string{"README.md", "data/x.json", "cmd/main_test.go"} {
		a, dir := noEngineRepo(t, "")
		write(t, dir, file, "changed\n")
		v, err := a.Check(context.Background(), "HEAD")
		if err != nil || v.Verdict != contract.PASS || reasonOf(v, file) != "no_engine" {
			t.Fatalf("%s: v=%s oos=%+v err=%v", file, v.Verdict, v.OutOfScope, err)
		}
	}
}

// A FAIL stays a FAIL (the new rule only lowers a PASS), and a file an engine covers is judged as before.
func TestNoEngineSourceDoesNotRaiseAFail(t *testing.T) {
	a, dir := noEngineRepo(t, `,"critical_code":["src/lib/**"]`)
	write(t, dir, "cmd/main.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	v, _ := a.Check(context.Background(), "HEAD")
	if v.Verdict != contract.FAIL {
		t.Fatalf("v=%s", v.Verdict)
	}
}

func TestEngineCoveredChangeIsUnaffected(t *testing.T) {
	a, dir := noEngineRepo(t, "")
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL g\nLINES 1-3\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.Targets) != 1 {
		t.Fatalf("v=%s targets=%d err=%v", v.Verdict, len(v.Targets), err)
	}
}
