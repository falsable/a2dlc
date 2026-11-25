package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

func opStub(req contract.Request) contract.StubResponse {
	resp := contract.StubResponse{
		SchemaVersion: contract.SchemaVersion,
		Stubbed:       []contract.StubFile{},
		Unstubbed:     []string{},
	}
	fset := token.NewFileSet()
	for _, rel := range req.ChangedFiles {
		if !isGoFile(rel) {
			continue
		}
		candAbs := absUnder(req.CandidateDir, rel)
		baseAbs := absUnder(req.BaseDir, rel)
		candSrc, err := os.ReadFile(candAbs)
		if err != nil {
			resp.Unstubbed = append(resp.Unstubbed, rel)
			continue
		}
		if isGoTestFile(rel) {
			baseSrc, bErr := os.ReadFile(baseAbs)
			if bErr == nil {
				resp.ModifiedTests = append(resp.ModifiedTests, diffTestFuncs(fset, rel, baseSrc, candSrc)...)
			}
			continue
		}

		candAST, err := parser.ParseFile(fset, candAbs, candSrc, 0)
		if err != nil {
			resp.Unstubbed = append(resp.Unstubbed, rel)
			continue
		}
		baseSrc, baseErr := os.ReadFile(baseAbs)
		baseSyms := map[string]bool{}
		if baseErr == nil {
			if baseAST, bErr := parser.ParseFile(fset, baseAbs, baseSrc, 0); bErr == nil {
				baseSyms = exportedSymbolSet(baseAST)
			}
		}

		var syms []string
		var stubDecls bytes.Buffer
		for _, decl := range candAST.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if d.Recv != nil || !d.Name.IsExported() || baseSyms[d.Name.Name] {
					continue
				}
				syms = append(syms, d.Name.Name)
				clone := *d
				clone.Doc = nil
				clone.Body = &ast.BlockStmt{
					List: []ast.Stmt{
						&ast.ExprStmt{
							X: &ast.CallExpr{
								Fun:  ast.NewIdent("panic"),
								Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"a2dlc stub: not implemented"`}},
							},
						},
					},
				}
				_ = format.Node(&stubDecls, fset, &clone)
				stubDecls.WriteString("\n\n")
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok && ts.Name.IsExported() && !baseSyms[ts.Name.Name] {
						syms = append(syms, ts.Name.Name)
						fmt.Fprintf(&stubDecls, "type %s struct{}\n\n", ts.Name.Name)
					}
				}
			}
		}
		if len(syms) == 0 {
			continue
		}

		var full bytes.Buffer
		if baseErr == nil {
			full.Write(baseSrc)
			if len(baseSrc) > 0 && baseSrc[len(baseSrc)-1] != '\n' {
				full.WriteByte('\n')
			}
			full.WriteByte('\n')
			full.Write(stubDecls.Bytes())
		} else {
			fmt.Fprintf(&full, "package %s\n\n", candAST.Name.Name)
			full.Write(stubDecls.Bytes())
		}

		dst := filepath.Join(ioDir, "stubs", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			resp.Unstubbed = append(resp.Unstubbed, rel)
			continue
		}
		if err := os.WriteFile(dst, full.Bytes(), 0o644); err != nil {
			resp.Unstubbed = append(resp.Unstubbed, rel)
			continue
		}
		resp.Stubbed = append(resp.Stubbed, contract.StubFile{File: rel, Symbols: syms})
	}
	return resp
}

func exportedSymbolSet(f *ast.File) map[string]bool {
	out := map[string]bool{}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.IsExported() {
				out[d.Name.Name] = true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if s.Name.IsExported() {
						out[s.Name.Name] = true
					}
				case *ast.ValueSpec:
					for _, id := range s.Names {
						if id.IsExported() {
							out[id.Name] = true
						}
					}
				}
			}
		}
	}
	return out
}

func diffTestFuncs(fset *token.FileSet, rel string, baseSrc, candSrc []byte) []string {
	bAST, err1 := parser.ParseFile(fset, rel, baseSrc, 0)
	cAST, err2 := parser.ParseFile(fset, rel, candSrc, 0)
	if err1 != nil || err2 != nil {
		return nil
	}
	baseBodies := map[string]string{}
	for _, decl := range bAST.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
			baseBodies[fn.Name.Name] = formatNode(fset, fn.Body)
		}
	}
	var modified []string
	for _, decl := range cAST.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
			if oldBody, ok := baseBodies[fn.Name.Name]; ok && oldBody != formatNode(fset, fn.Body) {
				modified = append(modified, rel+"::"+fn.Name.Name)
			}
		}
	}
	return modified
}
