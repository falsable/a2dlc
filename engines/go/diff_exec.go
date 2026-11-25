package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// tryDifferentialPure runs a generated Go driver on both base and candidate on deterministic primitive inputs
// when both trees have go.mod and fn has a supported pure primitive signature (int/int64/float64/string/bool).
// Returns (verdict, counterexample, reason, ok). If ok is false, caller falls back to INCONCLUSIVE.
func tryDifferentialPure(baseDir, candDir, rel, pkgName string, fn *ast.FuncDecl, budgetRaw json.RawMessage) (string, *contract.Counterexample, string, bool) {
	if fn.Recv != nil || !fn.Name.IsExported() || fn.Type.TypeParams != nil {
		return "", nil, "", false
	}
	params, ok := extractPrimitiveParams(fn.Type.Params)
	if !ok {
		return "", nil, "", false
	}
	retType, ok := extractSinglePrimitiveResult(fn.Type.Results)
	if !ok {
		return "", nil, "", false
	}
	baseMod := readGoModPath(filepath.Join(baseDir, "go.mod"))
	candMod := readGoModPath(filepath.Join(candDir, "go.mod"))
	if baseMod == "" || candMod == "" || baseMod != candMod {
		return "", nil, "", false
	}
	basePkg, err := parsePkgDir(filepath.Dir(absUnder(baseDir, rel)))
	if err != nil || !hasExportedFunc(basePkg, fn.Name.Name) {
		return "", nil, "", false
	}

	var b struct {
		Examples int `json:"examples"`
		Seed     int `json:"seed"`
	}
	_ = json.Unmarshal(budgetRaw, &b)
	examples := b.Examples
	if examples <= 0 || examples > 200 {
		examples = 50
	}

	subDir := filepath.ToSlash(filepath.Dir(rel))
	importPath := baseMod
	if subDir != "." && subDir != "" {
		importPath = baseMod + "/" + subDir
	}

	_ = pkgName
	_ = retType
	driverSrc := buildSingleDriverSource(importPath, fn.Name.Name, params, examples)

	oldArgs, oldVals, errMsg := runDriver(baseMod, baseDir, driverSrc)
	if errMsg != "" {
		return "", nil, errMsg, false
	}
	_, newVals, errMsg := runDriver(candMod, candDir, driverSrc)
	if errMsg != "" || len(oldVals) != len(newVals) {
		return "", nil, errMsg, false
	}

	for i := range oldVals {
		if oldVals[i] != newVals[i] {
			return contract.FAIL, &contract.Counterexample{
				Args: oldArgs[i],
				Old:  oldVals[i],
				New:  newVals[i],
			}, "", true
		}
	}
	return contract.PASS, nil, "", true
}

func runDriver(modName, modDir, driverSrc string) ([]string, []string, string) {
	tmpDir, err := os.MkdirTemp("", "a2dlc-go-drv-*")
	if err != nil {
		return nil, nil, err.Error()
	}
	defer os.RemoveAll(tmpDir)

	goMod := fmt.Sprintf("module a2dlc_driver\n\ngo 1.23\n\nrequire %s v0.0.0\n\nreplace %s => %s\n", modName, modName, modDir)
	if err := os.WriteFile(filepath.Join(tmpDir, "go.mod"), []byte(goMod), 0o644); err != nil {
		return nil, nil, err.Error()
	}
	if err := os.WriteFile(filepath.Join(tmpDir, "main.go"), []byte(driverSrc), 0o644); err != nil {
		return nil, nil, err.Error()
	}

	cmd := exec.Command("go", "run", "-mod=mod", ".")
	cmd.Dir = tmpDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return nil, nil, fmt.Sprintf("go run: %v: %s", err, strings.TrimSpace(out.String()))
	}

	var res struct {
		Args []string `json:"args"`
		Vals []string `json:"vals"`
	}
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &res); err != nil {
		return nil, nil, err.Error()
	}
	return res.Args, res.Vals, ""
}

func buildSingleDriverSource(imp, fnName string, params []string, examples int) string {
	var argsDecls, callArgs, fmtArgs []string
	for i, p := range params {
		v := fmt.Sprintf("a%d", i)
		callArgs = append(callArgs, v)
		fmtArgs = append(fmtArgs, v)
		switch p {
		case "int":
			argsDecls = append(argsDecls, fmt.Sprintf("\t\t%s := int(intPool[(i+%d)%%len(intPool)])", v, i))
		case "int64":
			argsDecls = append(argsDecls, fmt.Sprintf("\t\t%s := int64(intPool[(i+%d)%%len(intPool)])", v, i))
		case "float64":
			argsDecls = append(argsDecls, fmt.Sprintf("\t\t%s := float64(intPool[(i+%d)%%len(intPool)])*0.5", v, i))
		case "string":
			argsDecls = append(argsDecls, fmt.Sprintf("\t\t%s := strPool[(i+%d)%%len(strPool)]", v, i))
		case "bool":
			argsDecls = append(argsDecls, fmt.Sprintf("\t\t%s := ((i+%d)%%2 == 0)", v, i))
		}
	}
	return fmt.Sprintf(`package main

import (
	"encoding/json"
	"fmt"

	target %q
)

func main() {
	intPool := []int{0, 1, -1, 2, 5, 10, -7, 42, 100}
	strPool := []string{"", "a", "abc", "hello world", "0", "-1"}
	_ = intPool
	_ = strPool
	argsOut := make([]string, 0, %d)
	valsOut := make([]string, 0, %d)
	for i := 0; i < %d; i++ {
%s
		val := target.%s(%s)
		aj, _ := json.Marshal([]any{%s})
		vj, _ := json.Marshal(val)
		argsOut = append(argsOut, string(aj))
		valsOut = append(valsOut, string(vj))
	}
	out, _ := json.Marshal(map[string]any{"args": argsOut, "vals": valsOut})
	fmt.Println(string(out))
}
`, imp, examples, examples, examples, strings.Join(argsDecls, "\n"), fnName, strings.Join(callArgs, ", "), strings.Join(fmtArgs, ", "))
}

func extractPrimitiveParams(fl *ast.FieldList) ([]string, bool) {
	if fl == nil || len(fl.List) == 0 {
		return nil, false
	}
	var out []string
	for _, f := range fl.List {
		id, ok := f.Type.(*ast.Ident)
		if !ok || !isSupportedPrim(id.Name) {
			return nil, false
		}
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		for i := 0; i < n; i++ {
			out = append(out, id.Name)
		}
	}
	return out, true
}

func extractSinglePrimitiveResult(fl *ast.FieldList) (string, bool) {
	if fl == nil || len(fl.List) != 1 || len(fl.List[0].Names) > 1 {
		return "", false
	}
	id, ok := fl.List[0].Type.(*ast.Ident)
	if !ok || !isSupportedPrim(id.Name) {
		return "", false
	}
	return id.Name, true
}

func isSupportedPrim(t string) bool {
	switch t {
	case "int", "int64", "float64", "string", "bool":
		return true
	}
	return false
}

func parsePkgDir(dir string) (*ast.Package, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		return nil, err
	}
	for _, p := range pkgs {
		if !strings.HasSuffix(p.Name, "_test") {
			return p, nil
		}
	}
	return nil, fmt.Errorf("no package in %s", dir)
}

func hasExportedFunc(pkg *ast.Package, name string) bool {
	for _, f := range pkg.Files {
		for _, decl := range f.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && fn.Name.Name == name {
				return true
			}
		}
	}
	return false
}
