package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

type mutationSite struct {
	line int
	op   string
	desc string
}

func opMutate(req contract.Request) contract.MutateResponse {
	resp := contract.MutateResponse{
		SchemaVersion: contract.SchemaVersion,
		Mutants:       []contract.Mutant{},
		NotGenerated:  []contract.FileReason{},
		Pruned:        []contract.PrunedMutant{},
	}
	var budget contract.MutateBudget
	if len(req.Budget) > 0 && string(bytes.TrimSpace(req.Budget)) != "null" {
		_ = json.Unmarshal(req.Budget, &budget)
	}
	maxMutants := budget.MaxMutants
	if maxMutants <= 0 {
		maxMutants = 30
	}

	seq := 0
	for _, rel := range req.ChangedFiles {
		if len(resp.Mutants) >= maxMutants {
			break
		}
		if !isGoFile(rel) || isGoTestFile(rel) {
			resp.NotGenerated = append(resp.NotGenerated, contract.FileReason{File: rel, Reason: "not a non-test go file"})
			continue
		}
		lines := map[int]bool{}
		for _, l := range budget.ChangedLines[rel] {
			lines[l] = true
		}
		if len(lines) == 0 {
			resp.NotGenerated = append(resp.NotGenerated, contract.FileReason{File: rel, Reason: "no changed lines in budget"})
			continue
		}
		abs := absUnder(req.CandidateDir, rel)
		src, err := os.ReadFile(abs)
		if err != nil {
			resp.NotGenerated = append(resp.NotGenerated, contract.FileReason{File: rel, Reason: err.Error()})
			continue
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
		if err != nil {
			resp.NotGenerated = append(resp.NotGenerated, contract.FileReason{File: rel, Reason: err.Error()})
			continue
		}

		// Collect pruned equivalent sites + mutation sites on changed lines.
		sites := collectMutationSites(fset, file, rel, lines, &resp.Pruned)
		if len(sites) == 0 {
			resp.NotGenerated = append(resp.NotGenerated, contract.FileReason{File: rel, Reason: "no mutable AST nodes on changed lines"})
			continue
		}

		for idx, site := range sites {
			if len(resp.Mutants) >= maxMutants {
				break
			}
			mutSrc, ok := applyNthMutation(abs, src, lines, idx)
			if !ok {
				continue
			}
			seq++
			id := fmt.Sprintf("m%03d", seq)
			dst := filepath.Join(ioDir, "mutants", id, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				continue
			}
			if err := os.WriteFile(dst, mutSrc, 0o644); err != nil {
				continue
			}
			resp.Mutants = append(resp.Mutants, contract.Mutant{
				ID:          id,
				File:        rel,
				Line:        site.line,
				Operator:    site.op,
				Description: site.desc,
			})
		}
	}
	return resp
}

func collectMutationSites(fset *token.FileSet, file *ast.File, rel string, lines map[int]bool, pruned *[]contract.PrunedMutant) []mutationSite {
	var sites []mutationSite
	prunedSeen := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil {
			return false
		}
		line := fset.Position(n.Pos()).Line
		if !lines[line] {
			return true
		}
		switch x := n.(type) {
		case *ast.IfStmt:
			if id, ok := x.Cond.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
				key := fmt.Sprintf("dead:%d", line)
				if !prunedSeen[key] {
					prunedSeen[key] = true
					*pruned = append(*pruned, contract.PrunedMutant{File: rel, Line: line, Reason: contract.PruneDeadBranch})
				}
				return true
			}
			sites = append(sites, mutationSite{line: line, op: contract.OpNegateCondition, desc: "negate if condition"})
		case *ast.ExprStmt:
			if call, ok := x.X.(*ast.CallExpr); ok {
				cn := callName(call.Fun)
				if strings.HasPrefix(cn, "log.") || strings.HasPrefix(cn, "fmt.Print") {
					key := fmt.Sprintf("log:%d", line)
					if !prunedSeen[key] {
						prunedSeen[key] = true
						*pruned = append(*pruned, contract.PrunedMutant{File: rel, Line: line, Reason: contract.PruneLogging})
					}
					return true
				}
				sites = append(sites, mutationSite{line: line, op: contract.OpRemoveCall, desc: "remove call " + cn})
			}
		case *ast.BinaryExpr:
			if newOp, opKind, ok := mutateBinaryOp(x.Op); ok {
				sites = append(sites, mutationSite{line: line, op: opKind, desc: fmt.Sprintf("replace %s with %s", x.Op, newOp)})
			}
		case *ast.BasicLit:
			if x.Kind == token.INT {
				sites = append(sites, mutationSite{line: line, op: contract.OpConstant, desc: "increment integer literal"})
			}
		case *ast.ReturnStmt:
			if len(x.Results) > 0 {
				sites = append(sites, mutationSite{line: line, op: contract.OpReturnNone, desc: "replace return value with zero/empty"})
			}
		}
		return true
	})
	return sites
}

func applyNthMutation(abs string, src []byte, lines map[int]bool, targetIdx int) ([]byte, bool) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
	if err != nil {
		return nil, false
	}
	cur := 0
	applied := false
	ast.Inspect(file, func(n ast.Node) bool {
		if n == nil || applied {
			return false
		}
		line := fset.Position(n.Pos()).Line
		if !lines[line] {
			return true
		}
		switch x := n.(type) {
		case *ast.IfStmt:
			if id, ok := x.Cond.(*ast.Ident); ok && (id.Name == "true" || id.Name == "false") {
				return true
			}
			if cur == targetIdx {
				x.Cond = &ast.UnaryExpr{Op: token.NOT, X: &ast.ParenExpr{X: x.Cond}}
				applied = true
				return false
			}
			cur++
		case *ast.ExprStmt:
			if call, ok := x.X.(*ast.CallExpr); ok {
				cn := callName(call.Fun)
				if strings.HasPrefix(cn, "log.") || strings.HasPrefix(cn, "fmt.Print") {
					return true
				}
				if cur == targetIdx {
					x.X = &ast.CallExpr{Fun: &ast.FuncLit{Type: &ast.FuncType{Params: &ast.FieldList{}}, Body: &ast.BlockStmt{}}}
					applied = true
					return false
				}
				cur++
			}
		case *ast.BinaryExpr:
			if newOp, _, ok := mutateBinaryOp(x.Op); ok {
				if cur == targetIdx {
					x.Op = newOp
					applied = true
					return false
				}
				cur++
			}
		case *ast.BasicLit:
			if x.Kind == token.INT {
				if cur == targetIdx {
					if v, err := strconv.Atoi(x.Value); err == nil {
						x.Value = strconv.Itoa(v + 1)
					} else {
						x.Value = "0"
					}
					applied = true
					return false
				}
				cur++
			}
		case *ast.ReturnStmt:
			if len(x.Results) > 0 {
				if cur == targetIdx {
					x.Results[0] = ast.NewIdent("0")
					applied = true
					return false
				}
				cur++
			}
		}
		return true
	})
	if !applied {
		return nil, false
	}
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, file); err != nil {
		return nil, false
	}
	return buf.Bytes(), true
}

func mutateBinaryOp(op token.Token) (token.Token, string, bool) {
	switch op {
	case token.ADD:
		return token.SUB, contract.OpArithmetic, true
	case token.SUB:
		return token.ADD, contract.OpArithmetic, true
	case token.MUL:
		return token.QUO, contract.OpArithmetic, true
	case token.QUO:
		return token.MUL, contract.OpArithmetic, true
	case token.REM:
		return token.ADD, contract.OpArithmetic, true
	case token.EQL:
		return token.NEQ, contract.OpComparison, true
	case token.NEQ:
		return token.EQL, contract.OpComparison, true
	case token.LSS:
		return token.GEQ, contract.OpComparison, true
	case token.LEQ:
		return token.GTR, contract.OpComparison, true
	case token.GTR:
		return token.LEQ, contract.OpComparison, true
	case token.GEQ:
		return token.LSS, contract.OpComparison, true
	default:
		return token.ILLEGAL, "", false
	}
}
