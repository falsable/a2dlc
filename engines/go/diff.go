package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

func opDiff(req contract.Request) contract.Response {
	resp := contract.Response{
		SchemaVersion: contract.SchemaVersion,
		Targets:       []contract.TargetResult{},
		OutOfScope:    []contract.OutOfScope{},
	}
	budget := req.Budget
	if len(budget) == 0 || string(budget) == "null" {
		budget = json.RawMessage(`{"examples":200,"seed":42}`)
	}

	fset := token.NewFileSet()
	for _, rel := range req.ChangedFiles {
		if isGoTestFile(rel) {
			resp.OutOfScope = append(resp.OutOfScope, contract.OutOfScope{File: rel, Reason: "test_code"})
			continue
		}
		if !isGoFile(rel) {
			resp.OutOfScope = append(resp.OutOfScope, contract.OutOfScope{File: rel, Reason: "unsupported_extension"})
			continue
		}

		candAbs := absUnder(req.CandidateDir, rel)
		candSrc, err := os.ReadFile(candAbs)
		if err != nil {
			continue
		}
		candAST, err := parser.ParseFile(fset, candAbs, candSrc, 0)
		if err != nil {
			resp.Targets = append(resp.Targets, contract.TargetResult{
				Target:        rel,
				Engine:        engineName,
				EngineVersion: engineVersion,
				Verdict:       contract.INCONCLUSIVE,
				Reason:        "parse error: " + err.Error(),
				SearchBudget:  budget,
				Generators:    "typed_default",
				Typed:         true,
				Equivalence:   "strict",
			})
			continue
		}

		baseFuncs := map[string]string{}
		baseAbs := absUnder(req.BaseDir, rel)
		if baseSrc, err := os.ReadFile(baseAbs); err == nil {
			if baseAST, bErr := parser.ParseFile(fset, baseAbs, baseSrc, 0); bErr == nil {
				for _, decl := range baseAST.Decls {
					if fn, ok := decl.(*ast.FuncDecl); ok {
						baseFuncs[funcKey(fset, fn)] = formatNode(fset, fn)
					}
				}
			}
		}

		for _, decl := range candAST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := funcKey(fset, fn)
			targetID := rel + "::" + key
			if baseBody, ok := baseFuncs[key]; ok && baseBody == formatNode(fset, fn) {
				resp.Identical = append(resp.Identical, targetID)
				continue
			}
			start := fset.Position(fn.Pos()).Line
			end := fset.Position(fn.End()).Line
			verdict := contract.INCONCLUSIVE
			var ce *contract.Counterexample
			reason := "go differential runner requires isolated pure harness"
			if _, err := os.Stat(filepath.Join(req.CandidateDir, "go.mod")); err != nil {
				reason = "no go.mod in repository"
			} else if v, c, r, ok := tryDifferentialPure(req.BaseDir, req.CandidateDir, rel, candAST.Name.Name, fn, budget); ok {
				verdict, ce, reason = v, c, r
			} else if r != "" {
				reason = r
			}
			resp.Targets = append(resp.Targets, contract.TargetResult{
				Target:         targetID,
				Engine:         engineName,
				EngineVersion:  engineVersion,
				Verdict:        verdict,
				Reason:         reason,
				SearchBudget:   budget,
				Generators:     "typed_default",
				Typed:          true,
				Equivalence:    "strict",
				Lines:          []int{start, end},
				Counterexample: ce,
			})
		}
	}
	return resp
}

func funcKey(fset *token.FileSet, fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return formatNode(fset, fn.Recv.List[0].Type) + "." + fn.Name.Name
	}
	return fn.Name.Name
}
