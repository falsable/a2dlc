package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The shape of a change that adds unexported helpers, a const, a typed var whose type needs a new import, a type and a
// method, plus a same-package test that calls them: the stubbed base must compile with that test.
func TestGoEngineStubCompilesTheBaseWithTheNewTests(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	oldIO := ioDir
	ioDir = t.TempDir()
	defer func() { ioDir = oldIO }()

	mod := "module example.com/demo\n\ngo 1.23\n"
	writeFile(t, base, "go.mod", mod)
	writeFile(t, cand, "go.mod", mod)
	writeFile(t, base, "eng/eng.go", "package eng\n\nimport \"strings\"\n\nfunc Old(s string) string { return strings.ToUpper(s) }\n\nfunc Changed(a int) int { return a }\n")
	writeFile(t, cand, "eng/eng.go", `package eng

import (
	"strings"
	"sync"
	"time"
)

const label = "a2dlc.copyin=1"

const (
	first = iota
	second
)

var swept sync.Map

var limit time.Duration = 2 * time.Hour

var computed = time.Now()

type box struct{ n int }

func Old(s string) string { return strings.ToUpper(s) }

func Changed(a, b int) int { return a + b }

func (b *box) size() int { return b.n }

func orphansIn(ps string, now time.Time) []string {
	return strings.Fields(ps)
}

func sweep(ids []string) (n int, err error) {
	return len(ids), nil
}
`)
	test := "package eng\n\nimport (\n\t\"testing\"\n\t\"time\"\n)\n\nfunc TestNew(t *testing.T) {\n\t_ = label\n\t_ = second\n\tswept.Store(1, 2)\n\t_ = limit\n\t_ = (&box{}).size()\n\tif got := orphansIn(\"a\", time.Now()); len(got) != 1 {\n\t\tt.Fatal(got)\n\t}\n}\n\nfunc TestStubErr(t *testing.T) {\n\tif _, err := sweep(nil); err == nil {\n\t\tt.Fatal(\"want the stub's error\")\n\t}\n}\n\nfunc TestAfter(t *testing.T) {}\n"
	writeFile(t, cand, "eng/eng_test.go", test)

	resp := opStub(contract.Request{SchemaVersion: contract.SchemaVersion, Language: "go", BaseDir: base, CandidateDir: cand,
		ChangedFiles: []string{"eng/eng.go", "eng/eng_test.go"}, Operation: "stub"})
	if len(resp.Stubbed) != 1 || resp.Stubbed[0].File != "eng/eng.go" {
		t.Fatalf("stubbed = %+v", resp.Stubbed)
	}
	got := append([]string{}, resp.Stubbed[0].Symbols...)
	sort.Strings(got)
	want := []string{"box", "box.size", "first", "label", "limit", "orphansIn", "second", "sweep", "swept"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %v, want %v", got, want)
	}
	unst := append([]string{}, resp.Unstubbed...)
	sort.Strings(unst)
	if !reflect.DeepEqual(unst, []string{"Changed", "computed"}) {
		t.Fatalf("unstubbed = %v, want the changed signature and the var whose untyped initializer calls code", unst)
	}
	stub, err := os.ReadFile(filepath.Join(ioDir, "stubs", "eng", "eng.go"))
	if err != nil {
		t.Fatal(err)
	}
	src := string(stub)
	if !strings.Contains(src, `a2dlcstuberrors.New("a2dlc stub: not implemented")`) || strings.Contains(src, "strings.Fields") ||
		strings.Contains(src, "2 * time.Hour") || !strings.Contains(src, "func Changed(a int) int") {
		t.Fatalf("stub file:\n%s", src)
	}

	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go toolchain to compile the stubbed base")
	}
	writeFile(t, base, "eng/eng.go", src)
	writeFile(t, base, "eng/eng_test.go", test)
	// the test that reaches a stub fails, and the test after it still runs: a stub must not kill the test binary
	cmd := exec.Command(goBin, "test", "-count=1", "-v", "./...")
	cmd.Dir = base
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOWORK=off")
	out, _ := cmd.CombinedOutput()
	if !strings.Contains(string(out), "--- FAIL: TestNew") || !strings.Contains(string(out), "--- PASS: TestAfter") || !strings.Contains(string(out), "--- PASS: TestStubErr") {
		t.Fatalf("stubbed base with the new tests:\n%s\nstub:\n%s", out, src)
	}
}

func TestGoEngineStubOfANewFileKeepsOnlyTheImportsItsDeclarationsName(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	oldIO := ioDir
	ioDir = t.TempDir()
	defer func() { ioDir = oldIO }()

	writeFile(t, cand, "p/new.go", "package p\n\nimport (\n\t\"fmt\"\n\t\"time\"\n)\n\nfunc Wait(d time.Duration) string { return fmt.Sprint(d) }\n")
	resp := opStub(contract.Request{SchemaVersion: contract.SchemaVersion, Language: "go", BaseDir: base, CandidateDir: cand,
		ChangedFiles: []string{"p/new.go"}, Operation: "stub"})
	if len(resp.Stubbed) != 1 || !reflect.DeepEqual(resp.Stubbed[0].Symbols, []string{"Wait"}) {
		t.Fatalf("resp = %+v", resp)
	}
	stub, err := os.ReadFile(filepath.Join(ioDir, "stubs", "p", "new.go"))
	if err != nil {
		t.Fatal(err)
	}
	if src := string(stub); !strings.Contains(src, `"time"`) || strings.Contains(src, `"fmt"`) {
		t.Fatalf("stub file:\n%s", src)
	}
}
