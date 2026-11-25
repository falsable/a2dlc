package plan

import (
	"bytes"
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

// ExtractGoContext parses Go files in repoDir to provide the Planner with AST signatures and internal import edges.
func ExtractGoContext(repoDir string, files []string) ([]contract.SignatureFile, []contract.DepsEdge) {
	if repoDir == "" {
		return nil, nil
	}
	modPath := readGoMod(filepath.Join(repoDir, "go.mod"))
	pkgFiles := map[string][]string{}
	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		dir := filepath.ToSlash(filepath.Dir(rel))
		if dir == "." {
			dir = ""
		}
		pkgFiles[dir] = append(pkgFiles[dir], rel)
	}

	fset := token.NewFileSet()
	var sigs []contract.SignatureFile
	var edges []contract.DepsEdge
	seenEdge := map[string]bool{}

	for _, rel := range files {
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			continue
		}
		abs := filepath.Join(repoDir, filepath.FromSlash(rel))
		src, err := os.ReadFile(abs)
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(fset, abs, src, parser.ParseComments)
		if err != nil {
			continue
		}
		sf := contract.SignatureFile{File: rel, Symbols: []contract.Signature{}}
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.IsExported() {
				clone := *fn
				clone.Body = nil
				clone.Doc = nil
				var buf bytes.Buffer
				_ = format.Node(&buf, fset, &clone)
				sf.Symbols = append(sf.Symbols, contract.Signature{
					Name:      fn.Name.Name,
					Signature: strings.TrimSpace(buf.String()),
					Lines:     []int{fset.Position(fn.Pos()).Line, fset.Position(fn.End()).Line},
				})
			}
		}
		if len(sf.Symbols) > 0 {
			sigs = append(sigs, sf)
		}
		if modPath != "" {
			for _, imp := range f.Imports {
				p, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				if p == modPath || strings.HasPrefix(p, modPath+"/") {
					sub := strings.TrimPrefix(strings.TrimPrefix(p, modPath), "/")
					for _, dst := range pkgFiles[sub] {
						if dst == rel {
							continue
						}
						key := rel + "->" + dst
						if !seenEdge[key] {
							seenEdge[key] = true
							edges = append(edges, contract.DepsEdge{From: rel, To: dst})
						}
					}
				}
			}
		}
	}
	return sigs, edges
}

func readGoMod(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return fields[1]
			}
		}
	}
	return ""
}
