package app

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// install finds a test runner but a config with no tests section leaves every change to code no engine examines
// INCONCLUSIVE (bead a2dlc-6fj): it says so and prints a stub to edit, and never writes the section itself.
func TestInstallHintNamesTheRunnerAndGivesAStubWithoutWritingIt(t *testing.T) {
	cases := []struct {
		name   string
		files  map[string]string
		runner string // "" means no hint
		stub   bool   // the runner is one of those a2dlc has a stub for
	}{
		{"vitest by script", map[string]string{"package.json": `{"scripts":{"test":"vitest run"}}`, "test/a.test.ts": ""}, "vitest", true},
		{"vitest by dependency", map[string]string{"package.json": `{"scripts":{"test":"npm run unit"},"devDependencies":{"vitest":"^2.0.0"}}`}, "vitest", true},
		{"node --test", map[string]string{"package.json": `{"scripts":{"test":"node --test test/"}}`, "test/a.test.js": ""}, "node --test", true},
		{"pytest by pyproject", map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n[tool.pytest.ini_options]\naddopts = \"-q\"\n"}, "pytest", true},
		{"pytest by ini", map[string]string{"pytest.ini": "[pytest]\n", "setup.py": ""}, "pytest", true},
		{"pytest by tests dir", map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "tests/test_a.py": "def test_a(): pass\n"}, "pytest", true},
		{"a test script of another runner", map[string]string{"package.json": `{"scripts":{"test":"jest --ci"}}`}, "npm test", false},
		{"the default npm init script is not a runner", map[string]string{"package.json": `{"scripts":{"test":"echo \"Error: no test specified\" && exit 1"}}`}, "", false},
		{"a package with no tests", map[string]string{"package.json": `{"name":"x"}`}, "", false},
		{"python without tests", map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n"}, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, _, dir := newRepo(t)
			for rel, body := range c.files {
				write(t, dir, rel, body)
			}
			before, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/config.json"))
			if _, err := a.Install("/opt/a2dlc", nil); err != nil {
				t.Fatal(err)
			}
			hint, ok := a.TestsHint()
			if c.runner == "" {
				if ok {
					t.Fatalf("no runner here, got %+v", hint)
				}
				return
			}
			if !ok || hint.Runner != c.runner {
				t.Fatalf("runner: %+v ok=%v, want %q", hint, ok, c.runner)
			}
			// Stage 4 takes coverage from the tests that changed, so a fix that adds no test is covered only when
			// baseline_command runs the existing suite (bead a2dlc-m47): the warning says so, with or without a stub.
			for _, want := range []string{"INCONCLUSIVE", "tests section", ".a2dlc/config.json", "nothing was written", "baseline_command", "adds no test"} {
				if !strings.Contains(hint.Warning, want) {
					t.Errorf("the warning does not say %q:\n%s", want, hint.Warning)
				}
			}
			// never silently: the config on disk is what it was
			after, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/config.json"))
			if !bytes.Equal(before, after) || strings.Contains(string(after), `"tests"`) {
				t.Fatalf("install wrote a tests section:\n%s", after)
			}
			if !c.stub {
				if hint.Stub != "" {
					t.Fatalf("no stub for %s: %s", c.runner, hint.Stub)
				}
				return
			}
			// the stub is a tests section the contract accepts as it stands, with both outputs wired
			var cfg contract.Config
			if err := contract.DecodeStrictBytes([]byte(`{"schema_version":1,"engines":[],`+strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(hint.Stub), "{"), "}")+`}`), &cfg); err != nil {
				t.Fatalf("the stub does not decode as a config: %v\n%s", err, hint.Stub)
			}
			tc := cfg.Tests
			if tc == nil || tc.Image == "" || len(tc.Paths) == 0 || tc.Command == "" {
				t.Fatalf("incomplete stub: %+v", tc)
			}
			for _, v := range []string{"$A2DLC_JUNIT", "$A2DLC_COVERAGE", "$A2DLC_TESTS"} {
				if !strings.Contains(tc.Command, v) {
					t.Errorf("the command does not use %s: %s", v, tc.Command)
				}
			}
			// the baseline runs the existing suite, not only the changed test files, and writes the same outputs
			if tc.BaselineCommand == "" {
				t.Fatalf("the stub proposes no baseline_command:\n%s", hint.Stub)
			}
			for _, v := range []string{"$A2DLC_JUNIT", "$A2DLC_COVERAGE"} {
				if !strings.Contains(tc.BaselineCommand, v) {
					t.Errorf("the baseline_command does not use %s: %s", v, tc.BaselineCommand)
				}
			}
			if strings.Contains(tc.BaselineCommand, "$A2DLC_TESTS") {
				t.Errorf("the baseline_command is limited to the changed tests: %s", tc.BaselineCommand)
			}
		})
	}
}

// A repo that already declares tests gets no hint, and a repo with no config yet gets it after install writes one.
func TestInstallHintIsSilentWhenTheSectionExistsAndAppearsForANewConfig(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "package.json", `{"scripts":{"test":"vitest run"}}`)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["test/**"],"command":"true"}}`)
	if _, err := a.Install("/opt/a2dlc", nil); err != nil {
		t.Fatal(err)
	}
	if hint, ok := a.TestsHint(); ok {
		t.Fatalf("the repo declares tests: %+v", hint)
	}
	if err := os.Remove(filepath.Join(dir, ".a2dlc/config.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Install("/opt/a2dlc", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/config.json"))
	var c struct {
		Tests *json.RawMessage `json:"tests"`
	}
	if json.Unmarshal(b, &c) != nil || c.Tests != nil {
		t.Fatalf("a fresh config must not get a tests section either:\n%s", b)
	}
	if hint, ok := a.TestsHint(); !ok || hint.Runner != "vitest" {
		t.Fatalf("%+v %v", hint, ok)
	}
}

// check says why the lines no engine examined stay INCONCLUSIVE when the reason is the missing section, and stays quiet
// when tests are configured or when another reason was already given.
func TestNoTestsSectionNote(t *testing.T) {
	unexamined := &contract.Coverage{Status: contract.CoverageNotConfigured, Files: []contract.CoverageFile{{File: "src/a.ts", Uncovered: []int{2, 3}}}}
	if n := NoTestsSectionNote(contract.Verdict{Verdict: contract.INCONCLUSIVE, Coverage: unexamined}); !strings.Contains(n, "no tests section configured") {
		t.Fatalf("%q", n)
	}
	covered := &contract.Coverage{Status: contract.CoverageNotConfigured, Files: []contract.CoverageFile{{File: "src/a.ts", ByEngine: []int{2}, Uncovered: []int{}}}}
	given := &contract.Coverage{Status: contract.CoverageNotConfigured, Reason: testsBlockIgnored, Files: unexamined.Files}
	ran := &contract.Coverage{Status: contract.CoverageRan, Files: unexamined.Files}
	for name, cov := range map[string]*contract.Coverage{"nothing unexamined": covered, "another reason": given, "tests configured": ran, "no coverage": nil} {
		if n := NoTestsSectionNote(contract.Verdict{Coverage: cov}); n != "" {
			t.Errorf("%s: %q", name, n)
		}
	}
}
