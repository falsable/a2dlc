package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"

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
			resp.Targets = append(resp.Targets, fileInconclusive(rel, "cannot read candidate file: "+err.Error(), budget))
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
		var baseOrder []string
		var baseDecls string
		baseAbs := absUnder(req.BaseDir, rel)
		if baseSrc, err := os.ReadFile(baseAbs); err == nil {
			if baseAST, bErr := parser.ParseFile(fset, baseAbs, baseSrc, 0); bErr == nil {
				baseDecls = packageDecls(fset, baseAST)
				for _, decl := range baseAST.Decls {
					if fn, ok := decl.(*ast.FuncDecl); ok {
						k := funcKey(fset, fn)
						baseFuncs[k] = formatNode(fset, fn)
						baseOrder = append(baseOrder, k)
					}
				}
			}
		}
		// A changed package-level var, const or type can change what an unchanged function does, so with one the
		// function body being the same no longer proves the function is.
		declsChanged := len(baseFuncs) > 0 && baseDecls != packageDecls(fset, candAST)

		candKeys := map[string]bool{}
		for _, decl := range candAST.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			key := funcKey(fset, fn)
			candKeys[key] = true
			targetID := rel + "::" + key
			if baseBody, ok := baseFuncs[key]; ok && !declsChanged && baseBody == formatNode(fset, fn) {
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
		// A function the candidate dropped has no candidate to run: callers may still need it, so it is never a silent PASS.
		for _, key := range baseOrder {
			if !candKeys[key] {
				resp.Targets = append(resp.Targets, inconclusiveTarget(rel+"::"+key, "function removed in the candidate: nothing to compare", budget))
			}
		}
	}
	return resp
}

func inconclusiveTarget(id, reason string, budget json.RawMessage) contract.TargetResult {
	return contract.TargetResult{
		Target:        id,
		Engine:        engineName,
		EngineVersion: engineVersion,
		Verdict:       contract.INCONCLUSIVE,
		Reason:        reason,
		SearchBudget:  budget,
		Generators:    "typed_default",
		Typed:         true,
		Equivalence:   "strict",
	}
}

// fileInconclusive is the file-level target for a changed file the engine could not read, named like the Python engine's.
func fileInconclusive(rel, reason string, budget json.RawMessage) contract.TargetResult {
	return inconclusiveTarget(rel+"::"+filepath.Base(rel), reason, budget)
}

// packageDecls is the formatted package-level var, const and type declarations of a file plus its (name, path) imports,
// sorted so that moving one is not a change.
func packageDecls(fset *token.FileSet, f *ast.File) string {
	var decls []string
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if gd.Tok != token.IMPORT {
			decls = append(decls, formatNode(fset, gd))
			continue
		}
		for _, spec := range gd.Specs {
			is := spec.(*ast.ImportSpec)
			name := ""
			if is.Name != nil {
				name = is.Name.Name
			}
			decls = append(decls, "import "+name+" "+is.Path.Value)
		}
	}
	sort.Strings(decls)
	return strings.Join(decls, "\n")
}

func funcKey(fset *token.FileSet, fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return formatNode(fset, fn.Recv.List[0].Type) + "." + fn.Name.Name
	}
	return fn.Name.Name
}
