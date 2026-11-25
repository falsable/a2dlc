package main

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func diffReq(base, cand string, files ...string) contract.Request {
	return contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       base,
		CandidateDir:  cand,
		ChangedFiles:  files,
		Operation:     "diff",
	}
}

func targetByID(resp contract.Response, id string) *contract.TargetResult {
	for i := range resp.Targets {
		if resp.Targets[i].Target == id {
			return &resp.Targets[i]
		}
	}
	return nil
}

func TestGoEngineTier0VetFailureWithoutFileLineIsFinding(t *testing.T) {
	t.Setenv("GOTOOLCHAIN", "local")
	cand := t.TempDir()
	// go.mod asks for a newer Go than the toolchain: `go vet` exits non-zero with no file:line output.
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.99\n")
	writeFile(t, cand, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	resp := opTier0(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       cand,
		CandidateDir:  cand,
		ChangedFiles:  []string{"pkg/a.go"},
		Operation:     "tier0",
	})
	if resp.OK || len(resp.Findings) != 1 {
		t.Fatalf("go vet failed with no parsable line and Tier 0 was not told: %+v", resp)
	}
	f := resp.Findings[0]
	if f.Kind != contract.Tier0Compile || !strings.Contains(f.Message, "1.99") {
		t.Fatalf("finding = %+v", f)
	}
}

func TestGoEngineTier0UnreadableFileIsFinding(t *testing.T) {
	cand := t.TempDir()
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	resp := opTier0(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       cand,
		CandidateDir:  cand,
		ChangedFiles:  []string{"pkg/missing.go"},
		Operation:     "tier0",
	})
	if resp.OK || len(resp.Findings) == 0 || resp.Findings[0].File != "pkg/missing.go" {
		t.Fatalf("unreadable changed file was skipped: %+v", resp)
	}
}

func TestGoEngineStubUnreadableFileIsUnstubbed(t *testing.T) {
	old := ioDir
	ioDir = t.TempDir()
	defer func() { ioDir = old }()
	base, cand := t.TempDir(), t.TempDir()
	resp := opStub(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       base,
		CandidateDir:  cand,
		ChangedFiles:  []string{"util/missing.go", "util/missing_test.go"},
		Operation:     "stub",
	})
	if len(resp.Unstubbed) != 2 {
		t.Fatalf("unreadable files were skipped: %+v", resp)
	}
}

func TestGoEngineDiffUnreadableCandidateIsInconclusive(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	writeFile(t, base, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	resp := opDiff(diffReq(base, cand, "calc/missing.go"))
	if len(resp.Targets) != 1 || resp.Targets[0].Verdict != contract.INCONCLUSIVE ||
		!strings.HasPrefix(resp.Targets[0].Target, "calc/missing.go::") {
		t.Fatalf("unreadable changed file was skipped: %+v", resp)
	}
}

func TestGoEngineDiffRemovedFunctionIsNotSilent(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	writeFile(t, base, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, base, "calc/calc.go", "package calc\n\nfunc Keep(x int) int { return x + 1 }\nfunc Gone(x int) int { return x * 2 }\n")
	writeFile(t, cand, "calc/calc.go", "package calc\n\nfunc Keep(x int) int { return x + 1 }\n")
	resp := opDiff(diffReq(base, cand, "calc/calc.go"))
	got := targetByID(resp, "calc/calc.go::Gone")
	if got == nil || got.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("removed function yielded no INCONCLUSIVE target: %+v", resp)
	}
}

func TestGoEngineDiffChangedPackageVarIsNotIdentical(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	writeFile(t, base, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, base, "calc/calc.go", "package calc\n\nvar rate = 2\n\nfunc Price(n int) int { return n * rate }\n")
	writeFile(t, cand, "calc/calc.go", "package calc\n\nvar rate = 3\n\nfunc Price(n int) int { return n * rate }\n")
	resp := opDiff(diffReq(base, cand, "calc/calc.go"))
	for _, id := range resp.Identical {
		if id == "calc/calc.go::Price" {
			t.Fatalf("Price listed Identical although the package var it reads changed: %+v", resp)
		}
	}
	got := targetByID(resp, "calc/calc.go::Price")
	if got == nil || got.Verdict == contract.PASS {
		t.Fatalf("Price must be judged and must not PASS: %+v", resp)
	}
}

func TestGoEngineTier0VetFindingInUnrequestedFileStaysFiltered(t *testing.T) {
	cand := t.TempDir()
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "pkg/a.go", "package pkg\n\nfunc A() int { return 1 }\n")
	writeFile(t, cand, "pkg/old.go", "package pkg\n\nimport \"fmt\"\n\nfunc Old() string { return fmt.Sprintf(\"%d\", \"x\") }\n")
	resp := opTier0(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       cand,
		CandidateDir:  cand,
		ChangedFiles:  []string{"pkg/a.go"},
		Operation:     "tier0",
	})
	if !resp.OK {
		t.Fatalf("a vet finding in a file that was not changed must not fail Tier 0: %+v", resp)
	}
}

func TestGoEngineDiffSwappedImportIsNotIdentical(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	writeFile(t, base, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, base, "calc/calc.go", "package calc\n\nimport \"math/rand\"\n\nfunc Roll() int { return rand.Intn(10) }\n")
	writeFile(t, cand, "calc/calc.go", "package calc\n\nimport rand \"crypto/rand\"\n\nfunc Roll() int { return rand.Intn(10) }\n")
	resp := opDiff(diffReq(base, cand, "calc/calc.go"))
	for _, id := range resp.Identical {
		if id == "calc/calc.go::Roll" {
			t.Fatalf("Roll listed Identical although the import behind rand changed: %+v", resp)
		}
	}
	got := targetByID(resp, "calc/calc.go::Roll")
	if got == nil || got.Verdict == contract.PASS {
		t.Fatalf("Roll must be judged and must not PASS: %+v", resp)
	}
}

func TestGoEngineDiffReorderedImportsStayIdentical(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	writeFile(t, base, "calc/calc.go", "package calc\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\nfunc F() string { return strings.ToUpper(fmt.Sprint(1)) }\n")
	writeFile(t, cand, "calc/calc.go", "package calc\n\nimport (\n\t\"strings\"\n\t\"fmt\"\n)\n\nfunc F() string { return strings.ToUpper(fmt.Sprint(1)) }\n")
	resp := opDiff(diffReq(base, cand, "calc/calc.go"))
	if len(resp.Identical) != 1 || resp.Identical[0] != "calc/calc.go::F" {
		t.Fatalf("Identical = %+v", resp.Identical)
	}
}

func TestGoEngineDiffUnchangedDeclsKeepIdentical(t *testing.T) {
	base, cand := t.TempDir(), t.TempDir()
	src := "package calc\n\nvar rate = 2\n\nfunc Price(n int) int { return n * rate }\n"
	writeFile(t, base, "calc/calc.go", src)
	writeFile(t, cand, "calc/calc.go", src+"\nfunc Other() int { return 1 }\n")
	resp := opDiff(diffReq(base, cand, "calc/calc.go"))
	if len(resp.Identical) != 1 || resp.Identical[0] != "calc/calc.go::Price" {
		t.Fatalf("Identical = %+v", resp.Identical)
	}
}
