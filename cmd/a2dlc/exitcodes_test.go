package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// exitTable is checked against the source, not against a run: for each command, the integer literals it can return are
// collected from the AST of its case in run() and of the helpers it calls, and compared with the table's codes for that
// command. A function of package main that returns an int and that no command claims is a failure, so a new helper cannot
// hide a code.
const usageCommand = "usage"

// helpers are the int-returning functions of package main (besides run) and the command each belongs to.
var helpers = map[string]string{
	"answerCommand": "answer", "doctorCommand": "doctor", "findingCommand": "finding", "printJSON": "finding", "journalCommand": "journal",
	"approvePending": "approve", "planCommand": "plan", "readFeature": "plan", "readPlan": "plan", "report": "plan",
	"planner": "plan", "writeProposal": "plan", "planRun": "plan", "planApprove": "plan",
}

// appHelpers are the functions of internal/app whose result `a2dlc hook` returns.
var appHelpers = map[string]bool{"Hook": true, "HookOutsideRepo": true, "allow": true, "preTool": true}

func parseDir(t *testing.T, dir string) []*ast.FuncDecl {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, 0)
	if err != nil {
		t.Fatal(err)
	}
	var fns []*ast.FuncDecl
	for _, p := range pkgs {
		for n, f := range p.Files {
			_ = filepath.Base(n)
			for _, d := range f.Decls {
				if fn, ok := d.(*ast.FuncDecl); ok && fn.Body != nil {
					fns = append(fns, fn)
				}
			}
		}
	}
	return fns
}

// intResults are the positions of the int results of a function type.
func intResults(ft *ast.FuncType) []int {
	var at []int
	i := 0
	if ft.Results == nil {
		return nil
	}
	for _, f := range ft.Results.List {
		n := len(f.Names)
		if n == 0 {
			n = 1
		}
		if id, ok := f.Type.(*ast.Ident); ok && id.Name == "int" {
			for k := 0; k < n; k++ {
				at = append(at, i+k)
			}
		}
		i += n
	}
	return at
}

// literals adds to codes every int a return statement under node gives at an int result position; a call to
// verdict.ExitCode stands for 0, 1 and 2. A returned variable or another call adds nothing: the function that computes it
// is a helper of its own, and the test requires every helper to be claimed.
func literals(node ast.Node, positions []int, codes map[int]bool) {
	ast.Inspect(node, func(n ast.Node) bool {
		if _, ok := n.(*ast.FuncLit); ok {
			return false
		}
		r, ok := n.(*ast.ReturnStmt)
		if !ok {
			return true
		}
		for _, p := range positions {
			if p >= len(r.Results) {
				continue
			}
			switch e := r.Results[p].(type) {
			case *ast.BasicLit:
				if v, err := strconv.Atoi(e.Value); err == nil {
					codes[v] = true
				}
			case *ast.CallExpr:
				if s, ok := e.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "ExitCode" {
					codes[0], codes[1], codes[2] = true, true, true
				}
			}
		}
		return true
	})
}

// observed returns, per command, the codes the source can return.
func observed(t *testing.T) map[string]map[int]bool {
	t.Helper()
	obs := map[string]map[int]bool{}
	add := func(cmd string) map[int]bool {
		if obs[cmd] == nil {
			obs[cmd] = map[int]bool{}
		}
		return obs[cmd]
	}
	seenHelper := map[string]bool{}
	var run *ast.FuncDecl
	for _, fn := range parseDir(t, ".") {
		pos := intResults(fn.Type)
		if len(pos) == 0 {
			continue
		}
		if fn.Name.Name == "run" {
			run = fn
			continue
		}
		cmd, claimed := helpers[fn.Name.Name]
		if !claimed {
			t.Errorf("%s returns an int and no command claims it: add it to helpers (exitcodes_test.go)", fn.Name.Name)
			continue
		}
		seenHelper[fn.Name.Name] = true
		literals(fn.Body, pos, add(cmd))
	}
	for name := range helpers {
		if !seenHelper[name] {
			t.Errorf("helpers lists %s, which does not return an int in cmd/a2dlc", name)
		}
	}
	for _, fn := range parseDir(t, "../../internal/app") {
		if appHelpers[fn.Name.Name] {
			literals(fn.Body, intResults(fn.Type), add("hook"))
		}
	}
	if run == nil {
		t.Fatal("no run function")
	}
	var sw *ast.SwitchStmt
	for _, s := range run.Body.List {
		if x, ok := s.(*ast.SwitchStmt); ok {
			sw = x
		}
	}
	if sw == nil {
		t.Fatal("run has no switch on the command")
	}
	for _, s := range sw.Body.List {
		cc := s.(*ast.CaseClause)
		lit, ok := cc.List[0].(*ast.BasicLit)
		if !ok {
			t.Fatalf("case at %v is not a string literal", cc.Pos())
		}
		name, _ := strconv.Unquote(lit.Value)
		for _, st := range cc.Body {
			literals(st, []int{0}, add(name))
		}
	}
	// The returns outside the switch: the `version` branch is version's, every other one is usage.
	for _, s := range run.Body.List {
		if s == ast.Stmt(sw) {
			continue
		}
		if ifs, ok := s.(*ast.IfStmt); ok && isVersionCheck(ifs.Cond) {
			literals(ifs, []int{0}, add("version"))
			continue
		}
		literals(s, []int{0}, add(usageCommand))
	}
	return obs
}

// isVersionCheck matches `args[0] == "version"`.
func isVersionCheck(e ast.Expr) bool {
	b, ok := e.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	lit, ok := b.Y.(*ast.BasicLit)
	return ok && lit.Value == `"version"`
}

func tableCodes() map[string]map[int]bool {
	out := map[string]map[int]bool{}
	for _, r := range exitTable {
		c := strings.Fields(r.Command)[0]
		if out[c] == nil {
			out[c] = map[int]bool{}
		}
		out[c][r.Code] = true
	}
	return out
}

func sorted(m map[int]bool) []int {
	var s []int
	for k := range m {
		s = append(s, k)
	}
	sort.Ints(s)
	return s
}

// The skill is the agent's contract: every command with exit codes appears in its table, so an agent never meets a
// command the skill does not name.
func TestSkillNamesEveryCommand(t *testing.T) {
	b, err := os.ReadFile("../../skills/a2dlc/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	for cmd := range tableCodes() {
		if cmd == "usage" || cmd == "version" {
			continue
		}
		if !strings.Contains(string(b), "`a2dlc "+cmd+"`") {
			t.Errorf("skills/a2dlc/SKILL.md does not name `a2dlc %s` in its contract", cmd)
		}
	}
}

// A command returns a code the table does not list, or the table lists a code that no command returns.
func TestExitTableMatchesWhatTheCommandsReturn(t *testing.T) {
	obs, tab := observed(t), tableCodes()
	for cmd, codes := range obs {
		if len(codes) == 0 {
			continue
		}
		if fmt.Sprint(sorted(codes)) != fmt.Sprint(sorted(tab[cmd])) {
			t.Errorf("%s: the source returns %v, the table lists %v", cmd, sorted(codes), sorted(tab[cmd]))
		}
	}
	for cmd := range tab {
		if len(obs[cmd]) == 0 {
			t.Errorf("the table lists %s, which returns no code in the source", cmd)
		}
	}
	seen := map[string]bool{}
	for _, r := range exitTable {
		if k := fmt.Sprint(r.Command, r.Code); seen[k] {
			t.Errorf("%s %d is listed twice", r.Command, r.Code)
		} else {
			seen[k] = true
		}
		if r.Meaning == "" {
			t.Errorf("%s %d has no meaning", r.Command, r.Code)
		}
	}
}

// markdown is the table as docs/COMPATIBILITY.md prints it.
func markdown() string {
	var b strings.Builder
	b.WriteString("| Command | Exit code | Meaning |\n|---|---|---|\n")
	for _, r := range exitTable {
		fmt.Fprintf(&b, "| `%s` | %d | %s |\n", r.Command, r.Code, r.Meaning)
	}
	return b.String()
}

// The document prints exactly the table: command, code and meaning, in order.
func TestCompatibilityDocPrintsTheExitTable(t *testing.T) {
	b, err := os.ReadFile("../../docs/COMPATIBILITY.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.Contains(doc, markdown()) {
		t.Fatalf("docs/COMPATIBILITY.md does not contain the exit table of cmd/a2dlc/exitcodes.go; it must contain:\n%s", markdown())
	}
}
