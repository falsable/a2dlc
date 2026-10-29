package factory

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestClaudeCommandIsExactlyTheMinimalFlagSet(t *testing.T) {
	got := ClaudeCommand(testConfig(), 0.75)
	want := "claude --setting-sources '' --strict-mcp-config --disable-slash-commands --tools Read,Edit --exclude-dynamic-system-prompt-sections --disallowedTools WebSearch,WebFetch --max-budget-usd 0.7500"
	if got != want {
		t.Fatalf("%q", got)
	}
	if strings.Contains(got, "--max-turns") {
		t.Fatal("claude has no --max-turns: the turn cap is the core's")
	}
	if q := shellQuote("a b'c"); q != `'a b'"'"'c'` {
		t.Fatal(q)
	}
}

func TestLaneRolesHasOnlyTheBuilderAndItsWallTime(t *testing.T) {
	cfg := testConfig()
	r := LaneRoles(cfg)
	b, ok := r.Roles[contract.RoleBuilderPrimary]
	if len(r.Roles) != 1 || !ok || b.TimeoutS != cfg.Caps.LaneSeconds || b.Retries == nil || *b.Retries != 0 || b.Family != "claude" || b.Image != cfg.RolesImage {
		t.Fatalf("%+v", r)
	}
	if _, hasSaboteur := r.Roles[contract.RoleSaboteur]; hasSaboteur {
		t.Fatal("saboteur configured: lane v1 is lite without adversarial rounds")
	}
}

// The factory sits above run and app: neither imports it, so the a2dlc binary and the protocol cannot depend on it.
func TestRunAndAppDoNotImportFactory(t *testing.T) {
	for _, dir := range []string{"../run", "../app", "../engine", "../contract", "../gitx"} {
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, f := range files {
			if _, err := os.Stat(f); err != nil {
				continue
			}
			ast, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, imp := range ast.Imports {
				if strings.Contains(imp.Path.Value, "internal/factory") || strings.Contains(imp.Path.Value, "cmd/factory") {
					t.Errorf("%s imports %s", f, imp.Path.Value)
				}
			}
		}
	}
	files, _ := filepath.Glob("../../cmd/a2dlc/*.go")
	for _, f := range files {
		ast, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range ast.Imports {
			if strings.Contains(imp.Path.Value, "internal/factory") {
				t.Errorf("%s imports the factory", f)
			}
		}
	}
}
