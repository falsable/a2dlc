package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

func writeFile(t *testing.T, root, rel, content string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGoEngineTier0Conformance(t *testing.T) {
	cand := t.TempDir()
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "pkg/math.go", `package pkg

import "os"

func Complex(x int) int {
	if x > 0 {
		if x > 10 {
			return x + 1
		}
	}
	if x == -99 {
		os.Exit(1)
	}
	return x
}
`)
	budget, _ := json.Marshal(contract.Tier0Budget{
		Policy: &contract.PolicyTier0{
			MaxComplexity:        2,
			ForbiddenSideEffects: []string{"os.Exit"},
		},
	})
	req := contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       cand,
		CandidateDir:  cand,
		ChangedFiles:  []string{"pkg/math.go"},
		Budget:        budget,
		Operation:     "tier0",
	}
	resp := opTier0(req)
	if err := engine.ValidateTier0(req, resp); err != nil {
		t.Fatalf("ValidateTier0: %v", err)
	}
	if resp.OK || len(resp.Findings) < 2 {
		t.Fatalf("expected complexity + policy findings, got %+v", resp)
	}
}

func TestGoEngineSignaturesAndStubAndDepsAndMutate(t *testing.T) {
	base := t.TempDir()
	cand := t.TempDir()
	io := t.TempDir()
	oldIO := ioDir
	ioDir = io
	defer func() { ioDir = oldIO }()

	writeFile(t, base, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, base, "util/util.go", "package util\n\nfunc Existing() int { return 1 }\n")
	writeFile(t, cand, "util/util.go", "package util\n\nfunc Existing() int { return 1 }\n\n// Added doubles x.\nfunc Added(x int) int {\n\tif x > 0 {\n\t\treturn x * 2\n\t}\n\treturn 0\n}\n")
	writeFile(t, cand, "app/app.go", "package app\n\nimport \"example.com/demo/util\"\n\nfunc Run(x int) int {\n\treturn util.Added(x)\n}\n")

	// 1. Signatures
	sigResp := opSignatures(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		CandidateDir:  cand,
		ChangedFiles:  []string{"util/util.go"},
		Operation:     "signatures",
	})
	if len(sigResp.Files) != 1 || len(sigResp.Files[0].Symbols) != 2 {
		t.Fatalf("signatures = %+v", sigResp)
	}

	// 2. Stub
	stubResp := opStub(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       base,
		CandidateDir:  cand,
		ChangedFiles:  []string{"util/util.go"},
		Operation:     "stub",
	})
	if len(stubResp.Stubbed) != 1 || stubResp.Stubbed[0].Symbols[0] != "Added" {
		t.Fatalf("stubResp = %+v", stubResp)
	}
	stubBytes, err := os.ReadFile(filepath.Join(io, "stubs", "util", "util.go"))
	if err != nil || !strings.Contains(string(stubBytes), `panic("a2dlc stub: not implemented")`) {
		t.Fatalf("stub file = %q, err = %v", string(stubBytes), err)
	}

	// 3. Deps graph
	depsReq := contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		CandidateDir:  cand,
		ChangedFiles:  []string{"app/app.go", "util/util.go"},
		Operation:     "deps_graph",
	}
	depsResp := opDepsGraph(depsReq)
	if err := engine.ValidateDeps(depsReq, depsResp); err != nil {
		t.Fatalf("ValidateDeps: %v", err)
	}
	if len(depsResp.Edges) != 1 || depsResp.Edges[0].From != "app/app.go" || depsResp.Edges[0].To != "util/util.go" {
		t.Fatalf("deps = %+v", depsResp)
	}

	// 4. Mutate
	mutBudget, _ := json.Marshal(contract.MutateBudget{
		ChangedLines: map[string][]int{"util/util.go": {6, 7, 8}},
		MaxMutants:   10,
	})
	mutReq := contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		CandidateDir:  cand,
		ChangedFiles:  []string{"util/util.go"},
		Budget:        mutBudget,
		Operation:     "mutate",
	}
	mutResp := opMutate(mutReq)
	files, err := engine.MutantFiles(filepath.Join(io, "mutants"), mutReq, mutResp)
	if err != nil {
		t.Fatalf("MutantFiles: %v", err)
	}
	if len(mutResp.Mutants) == 0 || len(files) != len(mutResp.Mutants) {
		t.Fatalf("mutants = %+v, files = %d", mutResp.Mutants, len(files))
	}
}

func TestGoEngineDiffPassAndFail(t *testing.T) {
	base := t.TempDir()
	cand := t.TempDir()
	writeFile(t, base, "go.mod", "module example.com/demo\n\ngo 1.23\n")
	writeFile(t, cand, "go.mod", "module example.com/demo\n\ngo 1.23\n")

	writeFile(t, base, "calc/calc.go", `package calc

func Same(x int) int { return x + 1 }
func Equiv(x int) int { return x + x }
func Broken(x int) int { return x * 2 }
`)
	writeFile(t, cand, "calc/calc.go", `package calc

func Same(x int) int { return x + 1 }
func Equiv(x int) int { return 2 * x }
func Broken(x int) int { return x + 2 }
`)

	resp := opDiff(contract.Request{
		SchemaVersion: contract.SchemaVersion,
		Language:      "go",
		BaseDir:       base,
		CandidateDir:  cand,
		ChangedFiles:  []string{"calc/calc.go"},
		Operation:     "diff",
	})
	if len(resp.Identical) != 1 || resp.Identical[0] != "calc/calc.go::Same" {
		t.Fatalf("Identical = %+v", resp.Identical)
	}
	if len(resp.Targets) != 2 {
		t.Fatalf("Targets = %+v", resp.Targets)
	}
	if resp.Targets[0].Verdict != contract.PASS {
		t.Fatalf("Equiv = %+v", resp.Targets[0])
	}
	if resp.Targets[1].Verdict != contract.FAIL || resp.Targets[1].Counterexample == nil {
		t.Fatalf("Broken = %+v", resp.Targets[1])
	}
}
