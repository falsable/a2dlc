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
	"regexp"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// opStub writes, for each changed source file, the base file plus what the candidate added at top level, so the base
// compiles against the new tests: new functions and methods keep their signature with a body that does nothing and
// returns zero values (stubBody), new types are
// copied as they are, new const groups as they are, new vars without their initializer when they have a type. The
// imports those declarations name are added. A changed signature of a base function, and a new var whose untyped
// initializer calls code, go to unstubbed: stubbing either would change what the base does.
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
		var baseAST *ast.File
		if baseErr == nil {
			if baseAST, err = parser.ParseFile(fset, baseAbs, baseSrc, 0); err != nil {
				resp.Unstubbed = append(resp.Unstubbed, rel)
				continue
			}
		}

		decls, syms, unstubbed := stubDecls(fset, baseAST, candAST)
		resp.Unstubbed = append(resp.Unstubbed, unstubbed...)
		if len(syms) == 0 {
			continue
		}
		full, err := stubFile(fset, baseAST, baseSrc, candAST, decls)
		if err != nil {
			resp.Unstubbed = append(resp.Unstubbed, syms...)
			continue
		}

		dst := filepath.Join(ioDir, "stubs", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			resp.Unstubbed = append(resp.Unstubbed, rel)
			continue
		}
		if err := os.WriteFile(dst, full, 0o644); err != nil {
			resp.Unstubbed = append(resp.Unstubbed, rel)
			continue
		}
		resp.Stubbed = append(resp.Stubbed, contract.StubFile{File: rel, Symbols: syms})
	}
	return resp
}

// stubDecls picks the candidate's top-level declarations the base lacks. base is nil for a new file.
func stubDecls(fset *token.FileSet, base, cand *ast.File) (decls []ast.Decl, syms, unstubbed []string) {
	baseFuncs, baseNames := map[string]string{}, map[string]bool{}
	if base != nil {
		for _, decl := range base.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				baseFuncs[stubKey(d)] = funcSig(fset, d)
			case *ast.GenDecl:
				for _, n := range specNames(d) {
					baseNames[n] = true
				}
			}
		}
	}
	for _, decl := range cand.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			key := stubKey(d)
			if sig, ok := baseFuncs[key]; ok {
				if sig != funcSig(fset, d) {
					unstubbed = append(unstubbed, key)
				}
				continue
			}
			clone := *d
			clone.Doc = nil
			clone.Body = stubBody(d.Type.Results)
			decls = append(decls, &clone)
			syms = append(syms, key)
		case *ast.GenDecl:
			if d.Tok == token.IMPORT {
				continue
			}
			var fresh, old []string
			for _, n := range specNames(d) {
				if baseNames[n] {
					old = append(old, n)
				} else {
					fresh = append(fresh, n)
				}
			}
			if len(fresh) == 0 {
				continue
			}
			if d.Tok == token.CONST {
				// a const group shares iota and implicit values: it goes whole or not at all
				if len(old) > 0 {
					unstubbed = append(unstubbed, fresh...)
					continue
				}
				decls = append(decls, &ast.GenDecl{Tok: d.Tok, Lparen: d.Lparen, Specs: d.Specs, Rparen: d.Rparen})
				syms = append(syms, fresh...)
				continue
			}
			var specs []ast.Spec
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.TypeSpec:
					if baseNames[s.Name.Name] {
						continue
					}
					specs = append(specs, s)
					syms = append(syms, s.Name.Name)
				case *ast.ValueSpec:
					var names []string
					anyOld := false
					for _, id := range s.Names {
						names = append(names, id.Name)
						anyOld = anyOld || baseNames[id.Name]
					}
					switch {
					case anyOld:
						for _, n := range names {
							if !baseNames[n] {
								unstubbed = append(unstubbed, n)
							}
						}
					case s.Type != nil:
						specs = append(specs, &ast.ValueSpec{Names: s.Names, Type: s.Type})
						syms = append(syms, names...)
					case callsCode(s.Values):
						unstubbed = append(unstubbed, names...)
					default:
						specs = append(specs, s)
						syms = append(syms, names...)
					}
				}
			}
			if len(specs) > 0 {
				g := &ast.GenDecl{Tok: d.Tok, Specs: specs}
				if len(specs) > 1 {
					g.Lparen, g.Rparen = 1, 1
				}
				decls = append(decls, g)
			}
		}
	}
	return decls, syms, unstubbed
}

// stubFile prints the base (or a new package clause) with the imports the stubbed declarations need and those
// declarations appended, gofmt'd.
func stubFile(fset *token.FileSet, base *ast.File, baseSrc []byte, cand *ast.File, decls []ast.Decl) ([]byte, error) {
	have := map[string]bool{}
	if base != nil {
		for _, im := range base.Imports {
			have[im.Path.Value] = true
		}
	}
	used := map[string]bool{}
	for _, d := range decls {
		ast.Inspect(d, func(n ast.Node) bool {
			if sel, ok := n.(*ast.SelectorExpr); ok {
				if id, ok := sel.X.(*ast.Ident); ok {
					used[id.Name] = true
				}
			}
			return true
		})
	}
	var imports bytes.Buffer
	if used[stubErrors] {
		imports.WriteString(stubErrors + " \"errors\"\n")
	}
	for _, im := range cand.Imports {
		if have[im.Path.Value] || !used[importName(im)] {
			continue
		}
		if im.Name != nil {
			imports.WriteString(im.Name.Name + " ")
		}
		imports.WriteString(im.Path.Value + "\n")
	}
	var body bytes.Buffer
	for _, d := range decls {
		if err := format.Node(&body, fset, d); err != nil {
			return nil, err
		}
		body.WriteString("\n\n")
	}

	var full bytes.Buffer
	if base == nil {
		full.WriteString("package " + cand.Name.Name + "\n\n")
		if imports.Len() > 0 {
			full.WriteString("import (\n" + imports.String() + ")\n\n")
		}
	} else {
		// new imports go right after the base's last import declaration (or its package clause): Go wants every
		// import before the first other declaration
		at := base.Name.End()
		for _, d := range base.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.IMPORT {
				at = g.End()
			}
		}
		off := fset.Position(at).Offset
		full.Write(baseSrc[:off])
		if imports.Len() > 0 {
			full.WriteString("\n\nimport (\n" + imports.String() + ")")
		}
		full.Write(baseSrc[off:])
		full.WriteString("\n\n")
	}
	full.Write(body.Bytes())
	return format.Source(full.Bytes())
}

// stubErrors names the errors import of a stubbed file, under an alias no package code uses.
const stubErrors = "a2dlcstuberrors"

// stubBody returns each result's zero value, and for a result of type error a non-nil error. shortcut: a test that
// asserts nothing about what the stub returns passes on the base (the NOP gate then says it does not prove the change),
// and one that dereferences a nil result still panics and takes the package's later tests with it; a panicking body
// would do that to every test that reaches a stub, and runtime.Goexit is turned into the same panic by testing.
func stubBody(results *ast.FieldList) *ast.BlockStmt {
	body := &ast.BlockStmt{}
	if results == nil {
		return body
	}
	ret := &ast.ReturnStmt{}
	for _, f := range results.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for j := 0; j < n; j++ {
			name := ast.NewIdent(fmt.Sprintf("a2dlcR%d", len(ret.Results)))
			spec := &ast.ValueSpec{Names: []*ast.Ident{name}, Type: f.Type}
			if id, ok := f.Type.(*ast.Ident); ok && id.Name == "error" {
				spec.Values = []ast.Expr{&ast.CallExpr{
					Fun:  &ast.SelectorExpr{X: ast.NewIdent(stubErrors), Sel: ast.NewIdent("New")},
					Args: []ast.Expr{&ast.BasicLit{Kind: token.STRING, Value: `"a2dlc stub: not implemented"`}},
				}}
			}
			body.List = append(body.List, &ast.DeclStmt{Decl: &ast.GenDecl{Tok: token.VAR, Specs: []ast.Spec{spec}}})
			ret.Results = append(ret.Results, ast.NewIdent(name.Name))
		}
	}
	body.List = append(body.List, ret)
	return body
}

// stubKey is "Name" for a function and "Type.Name" for a method.
func stubKey(d *ast.FuncDecl) string {
	if d.Recv == nil || len(d.Recv.List) == 0 {
		return d.Name.Name
	}
	t := d.Recv.List[0].Type
	for {
		switch x := t.(type) {
		case *ast.StarExpr:
			t = x.X
			continue
		case *ast.IndexExpr:
			t = x.X
			continue
		case *ast.IndexListExpr:
			t = x.X
			continue
		case *ast.Ident:
			return x.Name + "." + d.Name.Name
		}
		return d.Name.Name
	}
}

func funcSig(fset *token.FileSet, d *ast.FuncDecl) string {
	clone := *d
	clone.Doc, clone.Body = nil, nil
	return formatNode(fset, &clone)
}

func specNames(d *ast.GenDecl) []string {
	var out []string
	for _, spec := range d.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			out = append(out, s.Name.Name)
		case *ast.ValueSpec:
			for _, id := range s.Names {
				out = append(out, id.Name)
			}
		}
	}
	return out
}

// callsCode: an initializer with a call runs code at package init; on the base that code may be a stub that panics.
func callsCode(values []ast.Expr) bool {
	found := false
	for _, v := range values {
		ast.Inspect(v, func(n ast.Node) bool {
			if _, ok := n.(*ast.CallExpr); ok {
				found = true
			}
			return !found
		})
	}
	return found
}

var majorVersion = regexp.MustCompile(`^v[0-9]+$`)

// importName is the identifier an import binds. shortcut: without an alias it is the path's last element (minus a
// /vN suffix and a .vN or go- decoration), not the package clause, which would need the module on disk; a package
// named otherwise is left out of the added imports and the stubbed file fails to compile, as before this change.
func importName(im *ast.ImportSpec) string {
	if im.Name != nil {
		return im.Name.Name
	}
	p, _ := strconv.Unquote(im.Path.Value)
	parts := strings.Split(p, "/")
	last := parts[len(parts)-1]
	if majorVersion.MatchString(last) && len(parts) > 1 {
		last = parts[len(parts)-2]
	}
	last = strings.TrimPrefix(last, "go-")
	if i := strings.Index(last, "."); i > 0 {
		last = last[:i]
	}
	return last
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
