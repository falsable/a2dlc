package main

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

func opSignatures(req contract.Request) contract.SignaturesResponse {
	resp := contract.SignaturesResponse{
		SchemaVersion: contract.SchemaVersion,
		Files:         []contract.SignatureFile{},
	}
	fset := token.NewFileSet()
	for _, rel := range req.ChangedFiles {
		if !isGoFile(rel) || isGoTestFile(rel) {
			continue
		}
		abs := absUnder(req.CandidateDir, rel)
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		file, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
		if err != nil {
			continue
		}
		sf := contract.SignatureFile{
			File:    rel,
			Symbols: []contract.Signature{},
		}
		for _, decl := range file.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				name := d.Name.Name
				if d.Recv != nil && len(d.Recv.List) > 0 {
					name = formatNode(fset, d.Recv.List[0].Type) + "." + name
				}
				clone := *d
				clone.Body = nil
				clone.Doc = nil
				sig := strings.TrimSpace(formatNode(fset, &clone))
				sf.Symbols = append(sf.Symbols, contract.Signature{
					Name:      name,
					Signature: sig,
					Doc:       docPtr(d.Doc),
					Lines:     []int{fset.Position(d.Pos()).Line, fset.Position(d.End()).Line},
				})
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if !s.Name.IsExported() {
							continue
						}
						sf.Symbols = append(sf.Symbols, contract.Signature{
							Name:      s.Name.Name,
							Signature: "type " + strings.TrimSpace(formatNode(fset, s)),
							Doc:       docPtr(firstDoc(s.Doc, d.Doc)),
							Lines:     []int{fset.Position(s.Pos()).Line, fset.Position(s.End()).Line},
						})
					case *ast.ValueSpec:
						for _, id := range s.Names {
							if !id.IsExported() {
								continue
							}
							typ := ""
							if s.Type != nil {
								typ = " " + formatNode(fset, s.Type)
							}
							sf.Symbols = append(sf.Symbols, contract.Signature{
								Name:      id.Name,
								Signature: d.Tok.String() + " " + id.Name + typ,
								Doc:       docPtr(firstDoc(s.Doc, d.Doc)),
								Lines:     []int{fset.Position(s.Pos()).Line, fset.Position(s.End()).Line},
							})
						}
					}
				}
			}
		}
		resp.Files = append(resp.Files, sf)
	}
	return resp
}

func formatNode(fset *token.FileSet, node any) string {
	var buf bytes.Buffer
	if err := format.Node(&buf, fset, node); err != nil {
		return ""
	}
	return buf.String()
}

func firstDoc(a, b *ast.CommentGroup) *ast.CommentGroup {
	if a != nil {
		return a
	}
	return b
}

func docPtr(cg *ast.CommentGroup) *string {
	if cg == nil {
		return nil
	}
	txt := strings.TrimSpace(cg.Text())
	if txt == "" {
		return nil
	}
	return &txt
}
