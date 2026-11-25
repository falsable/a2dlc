package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

var vetLineRe = regexp.MustCompile(`^([^:\n]+):(\d+)(?::\d+)?:\s*(.+)$`)

func opTier0(req contract.Request) contract.Tier0Response {
	resp := contract.Tier0Response{
		SchemaVersion: contract.SchemaVersion,
		Findings:      []contract.Tier0Finding{},
	}
	var budget contract.Tier0Budget
	if len(req.Budget) > 0 && string(bytes.TrimSpace(req.Budget)) != "null" {
		_ = json.Unmarshal(req.Budget, &budget)
	}

	reqSet := map[string]bool{}
	for _, f := range req.ChangedFiles {
		reqSet[f] = true
	}

	fset := token.NewFileSet()
	for _, rel := range req.ChangedFiles {
		if !isGoFile(rel) {
			continue
		}
		abs := absUnder(req.CandidateDir, rel)
		src, err := os.ReadFile(abs)
		if err != nil {
			resp.Findings = append(resp.Findings, contract.Tier0Finding{
				Kind:    contract.Tier0Compile,
				File:    rel,
				Message: "cannot read file: " + err.Error(),
			})
			continue
		}
		file, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
		if err != nil {
			appendParseFindings(&resp.Findings, rel, err)
			continue
		}
		if budget.Policy != nil {
			checkPolicyAndComplexity(&resp.Findings, fset, rel, file, budget.Policy)
		}
	}

	// Run `go vet` when candidate has a go.mod and at least one Go file was requested.
	if hasGoFile(req.ChangedFiles) {
		if _, err := os.Stat(filepath.Join(req.CandidateDir, "go.mod")); err == nil {
			runGoVet(&resp.Findings, req.CandidateDir, reqSet)
		}
	}

	resp.OK = len(resp.Findings) == 0
	return resp
}

func hasGoFile(files []string) bool {
	for _, f := range files {
		if isGoFile(f) {
			return true
		}
	}
	return false
}

func appendParseFindings(out *[]contract.Tier0Finding, rel string, err error) {
	if list, ok := err.(scanner.ErrorList); ok {
		for _, e := range list {
			line := e.Pos.Line
			var linePtr *int
			if line > 0 {
				linePtr = &line
			}
			*out = append(*out, contract.Tier0Finding{
				Kind:    contract.Tier0Compile,
				File:    rel,
				Line:    linePtr,
				Message: e.Msg,
			})
		}
		return
	}
	*out = append(*out, contract.Tier0Finding{
		Kind:    contract.Tier0Compile,
		File:    rel,
		Message: err.Error(),
	})
}

func checkPolicyAndComplexity(out *[]contract.Tier0Finding, fset *token.FileSet, rel string, file *ast.File, pol *contract.PolicyTier0) {
	forbiddenImports := map[string]bool{}
	for _, imp := range pol.ForbiddenImports {
		forbiddenImports[strings.TrimSpace(imp)] = true
	}
	forbiddenCalls := map[string]bool{}
	for _, fx := range pol.ForbiddenSideEffects {
		forbiddenCalls[strings.TrimSpace(fx)] = true
	}

	if len(forbiddenImports) > 0 {
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				continue
			}
			if forbiddenImports[p] {
				l := fset.Position(imp.Pos()).Line
				*out = append(*out, contract.Tier0Finding{
					Kind:    contract.Tier0Policy,
					File:    rel,
					Line:    &l,
					Message: fmt.Sprintf("forbidden import %q", p),
				})
			}
		}
	}

	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		start := fset.Position(fn.Pos()).Line
		if pol.MaxComplexity > 0 {
			cc := cyclomatic(fn.Body)
			if cc > pol.MaxComplexity {
				l := start
				*out = append(*out, contract.Tier0Finding{
					Kind:    contract.Tier0Lint,
					File:    rel,
					Line:    &l,
					Message: fmt.Sprintf("function %s has cyclomatic complexity %d, max %d", fn.Name.Name, cc, pol.MaxComplexity),
				})
			}
		}
	}

	if len(forbiddenCalls) > 0 {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			name := callName(call.Fun)
			if forbiddenCalls[name] {
				l := fset.Position(call.Pos()).Line
				*out = append(*out, contract.Tier0Finding{
					Kind:    contract.Tier0Policy,
					File:    rel,
					Line:    &l,
					Message: fmt.Sprintf("forbidden side effect %q", name),
				})
			}
			return true
		})
	}
}

func cyclomatic(body *ast.BlockStmt) int {
	cc := 1
	ast.Inspect(body, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			cc++
		case *ast.CaseClause:
			if x.List != nil {
				cc++
			}
		case *ast.CommClause:
			if x.Comm != nil {
				cc++
			}
		case *ast.BinaryExpr:
			if x.Op == token.LAND || x.Op == token.LOR {
				cc++
			}
		}
		return true
	})
	return cc
}

func callName(expr ast.Expr) string {
	switch x := expr.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.SelectorExpr:
		if id, ok := x.X.(*ast.Ident); ok {
			return id.Name + "." + x.Sel.Name
		}
	}
	return ""
}

func runGoVet(out *[]contract.Tier0Finding, candDir string, reqSet map[string]bool) {
	cmd := exec.Command("go", "vet", "./...")
	cmd.Dir = candDir
	var combined bytes.Buffer
	cmd.Stdout = &combined
	cmd.Stderr = &combined
	err := cmd.Run()
	if err == nil {
		return
	}
	parsed := false
	for _, rawLine := range strings.Split(combined.String(), "\n") {
		line := strings.TrimSpace(rawLine)
		m := vetLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		parsed = true
		rel := filepath.ToSlash(strings.TrimPrefix(m[1], "./"))
		if !reqSet[rel] {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		var lnPtr *int
		if ln > 0 {
			lnPtr = &ln
		}
		*out = append(*out, contract.Tier0Finding{
			Kind:    contract.Tier0Lint,
			File:    rel,
			Line:    lnPtr,
			Message: "go vet: " + strings.TrimSpace(m[3]),
		})
	}
	// A non-zero exit with no file:line output at all (toolchain too old, broken go.mod, a load error) is still a
	// failure: it must not read as a clean Tier 0. Findings in files nobody asked about stay filtered out.
	if !parsed {
		msg := strings.TrimSpace(combined.String())
		if msg == "" {
			msg = err.Error()
		}
		*out = append(*out, contract.Tier0Finding{Kind: contract.Tier0Compile, File: firstGoFile(reqSet), Message: "go vet failed: " + msg})
	}
}

func firstGoFile(reqSet map[string]bool) string {
	var files []string
	for f := range reqSet {
		if isGoFile(f) {
			files = append(files, f)
		}
	}
	sort.Strings(files)
	if len(files) == 0 {
		return "go.mod"
	}
	return files[0]
}
