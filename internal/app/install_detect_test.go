package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// installIn runs install on a repo made of files (no commit, no manifest unless the map has one) and returns the config it
// wrote, the lines install reported and the coverage warning.
func installIn(t *testing.T, files map[string]string) (contract.Config, []string, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	for rel, s := range files {
		write(t, dir, rel, s)
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := App{Repo: repo, Version: "test", Now: func() time.Time { return time.Unix(0, 0) }}
	done, err := a.Install("/opt/a2dlc", nil)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c contract.Config
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	w, _ := a.CoverageWarning()
	return c, done, w
}

func languages(c contract.Config) []string {
	out := []string{}
	for _, e := range c.Engines {
		out = append(out, e.Language)
	}
	return out
}

// a2dlc-fjl: a repo with src/pricing.py and no manifest got engines: [] and no word about it, and the first check on a
// behavior change was PASS with 0 targets.
func TestInstallDetectsPythonByExtensionWithoutManifest(t *testing.T) {
	c, done, w := installIn(t, map[string]string{"src/pricing.py": "def price(x):\n    return x * 2\n"})
	if got := languages(c); len(got) != 1 || got[0] != "python" {
		t.Fatalf("engines = %v, want [python]", got)
	}
	if p := c.Engines[0].Paths; len(p) != 1 || p[0] != "src/**/*.py" {
		t.Fatalf("paths = %v, want [src/**/*.py]", p)
	}
	if !strings.Contains(strings.Join(done, "\n"), "engine python (src/**/*.py)") {
		t.Fatalf("install does not say which engine it configured: %v", done)
	}
	if w != "" {
		t.Fatalf("everything is covered, yet a warning: %s", w)
	}
}

func TestInstallDetectsTypeScriptByExtensionWithoutManifest(t *testing.T) {
	c, _, _ := installIn(t, map[string]string{"lib/price.ts": "export const price = (x: number) => x * 2\n"})
	if got := languages(c); len(got) != 1 || got[0] != "typescript" {
		t.Fatalf("engines = %v, want [typescript]", got)
	}
	if p := c.Engines[0].Paths; len(p) != 1 || p[0] != "**/*.ts" {
		t.Fatalf("paths = %v, want [**/*.ts]", p)
	}
}

// The control: with a manifest the config is the one install wrote before this bead, byte for byte.
func TestInstallWithManifestKeepsTheConfigItAlwaysWrote(t *testing.T) {
	cases := map[string]struct {
		files map[string]string
		want  string
	}{
		"python": {map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "src/a.py": "x = 1\n"},
			`"engines":[{"language":"python","image":"` + enginePyImage + `","paths":["src/**/*.py"],"budget":{"per_condition_timeout_s":10}}]`},
		"typescript": {map[string]string{"package.json": "{}\n", "src/a.ts": "export const a = 1\n"},
			`"engines":[{"language":"typescript","image":"` + engineTSImage + `","paths":["src/**/*.ts"],"budget":{"examples":2000,"seed":42}}]`},
		"manifest and no source": {map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n"},
			`"engines":[{"language":"python","image":"` + enginePyImage + `","paths":["**/*.py"],"budget":{"per_condition_timeout_s":10}}]`},
	}
	for name, tc := range cases {
		c, _, _ := installIn(t, tc.files)
		b, _ := json.Marshal(c)
		if !strings.Contains(string(b), tc.want) {
			t.Errorf("%s: config\n%s\ndoes not contain\n%s", name, b, tc.want)
		}
	}
}

func TestInstallDoesNotCountVendoredOrIgnoredDirs(t *testing.T) {
	c, _, w := installIn(t, map[string]string{
		"node_modules/dep/index.ts": "export const d = 1\n",
		".venv/lib/site.py":         "x = 1\n",
		"dist/bundle.js":            "var a = 1\n",
		"vendor/lib/lib.go":         "package lib\n",
		"ignored/gen.py":            "x = 1\n",
		".gitignore":                "ignored/\n",
	})
	if len(c.Engines) != 0 || w != "" {
		t.Fatalf("engines %v, warning %q: vendored and ignored files must not count", languages(c), w)
	}
}

func TestInstallWarnsOfSourceNoEngineCovers(t *testing.T) {
	c, _, w := installIn(t, map[string]string{
		"cmd/main.go": "package main\n", "internal/a.go": "package x\n", "internal/b.go": "package x\n",
		"web/app.js": "var a = 1\n", "web/app.jsx": "var b = 1\n", "src/pricing.py": "x = 1\n",
		"README.md": "# docs\n",
	})
	if got := languages(c); len(got) != 1 || got[0] != "python" {
		t.Fatalf("engines = %v, want [python]", got)
	}
	for _, want := range []string{"3 Go files (.go)", "2 JavaScript files (.js: 1, .jsx: 1)", "INCONCLUSIVE", "never PASS", "config.out_of_scope_paths"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning does not contain %q:\n%s", want, w)
		}
	}
	if strings.Contains(w, "Python") {
		t.Errorf("Python is covered but the warning names it:\n%s", w)
	}
}

// A file under an engine's language but outside its paths is uncovered too (package.json repo, code outside src/).
func TestInstallWarnsOfFilesOutsideTheEnginePaths(t *testing.T) {
	_, _, w := installIn(t, map[string]string{"package.json": "{}\n", "src/a.ts": "export const a = 1\n", "tools/b.ts": "export const b = 1\n"})
	if !strings.Contains(w, "1 TypeScript file (.ts)") {
		t.Fatalf("warning: %q", w)
	}
}

// Test files belong to config.tests, not to an engine: a src layout with tests/ is the normal case and says nothing.
func TestInstallIsSilentWhenOnlyTestsAreOutsideTheEnginePaths(t *testing.T) {
	_, _, w := installIn(t, map[string]string{"pyproject.toml": "[project]\n", "src/a.py": "x = 1\n",
		"tests/test_a.py": "def test_a(): pass\n", "tests/conftest.py": "", "lib/a.test.ts": "", "web/b.spec.ts": ""})
	if w != "" {
		t.Fatalf("warning for test files only: %s", w)
	}
}

func TestInstallIsSilentOnEmptyAndDocsOnlyRepos(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"empty": {}, "docs": {"README.md": "# x\n", "docs/guide.md": "text\n", "data.json": "{}\n", "LICENSE": "MIT\n"},
	} {
		c, done, w := installIn(t, files)
		if len(c.Engines) != 0 || w != "" {
			t.Errorf("%s: engines %v, warning %q", name, languages(c), w)
		}
		if !strings.Contains(strings.Join(done, "\n"), "no engine") {
			t.Errorf("%s: install does not say it configured no engine: %v", name, done)
		}
	}
}
