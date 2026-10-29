package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

const (
	blockStart = "# --- BEGIN A2DLC ---"
	blockEnd   = "# --- END A2DLC ---"
)

// Engine images that install writes to a new config. The release overrides them with -ldflags "-X" and the
// ghcr.io name@digest built from the same commit as the binary: a local tag does not exist outside the machine
// that built it, and the engine would fail with "pull access denied". Dev builds keep the local tags.
var (
	enginePyImage = "a2dlc-engine-py:0.6"
	engineTSImage = "a2dlc-engine-ts:0.1"
)

// Install leaves the repo ready: config if missing, .gitignore, the pre-push block and the hooks of the requested agents.
// exe is the binary's absolute path: if it is missing, the hook fails and blocks, instead of skipping the gate.
func (a App) Install(exe string, agents []string) ([]string, error) {
	exe = filepath.ToSlash(exe)
	done := []string{}
	if _, err := os.Stat(a.path(pathConfig)); errors.Is(err, os.ErrNotExist) {
		if err := a.write(pathConfig, a.initialConfig()); err != nil {
			return done, err
		}
		done = append(done, "wrote "+pathConfig)
	}
	if n, err := a.gitignore(); err != nil {
		return done, err
	} else if n > 0 {
		done = append(done, fmt.Sprintf("added %d lines to .gitignore", n))
	}
	p, err := a.prePush(exe)
	if err != nil {
		return done, err
	}
	done = append(done, "a2dlc block in "+p)
	// The gate is the push, not the commit: a blocking pre-commit stops the agents' commits and the merges of their
	// worktrees, which no human steers. Earlier installs wrote one; it is removed.
	if pc, removed, err := a.removeHook("pre-commit"); err != nil {
		return done, err
	} else if removed {
		done = append(done, "removed the a2dlc block from "+pc)
	}
	for _, agent := range agents {
		p, err := a.hookAgent(agent, exe)
		if err != nil {
			return done, err
		}
		done = append(done, "hook for "+agent+" in "+p)
	}
	return done, nil
}

func (a App) initialConfig() contract.Config {
	c := contract.Config{SchemaVersion: contract.SchemaVersion, Engines: []contract.EngineConfig{},
		AgentHook: &contract.AgentHook{MaxBlocks: 4}}
	exists := func(rel string) bool { _, err := os.Stat(a.path(rel)); return err == nil }
	if exists("package.json") {
		c.Engines = append(c.Engines, contract.EngineConfig{Language: "typescript", Image: engineTSImage,
			Paths: []string{"src/**/*.ts"}, Budget: json.RawMessage(`{"examples":2000,"seed":42}`)})
	}
	if exists("pyproject.toml") || exists("setup.py") {
		paths := "**/*.py"
		if exists("src") { // src layout: leave tests/ out of the engine; declare config.tests to gate them
			paths = "src/**/*.py"
		}
		c.Engines = append(c.Engines, contract.EngineConfig{Language: "python", Image: enginePyImage,
			Paths: []string{paths}, Budget: json.RawMessage(`{"per_condition_timeout_s":10}`)})
	}
	return c
}

// TestsHint is what install says when the repo has a test runner and its config has no tests section (a2dlc-6fj): without
// the section a2dlc runs none of the repo's tests, so every change to code that no engine examines stays INCONCLUSIVE.
// It never writes the section: Stub is a tests section to paste into the config and edit, and is empty for a runner
// a2dlc has no stub for.
type TestsHint struct {
	Runner  string
	Warning string
	Stub    string
}

// TestsHint reports a hint when the config has no tests section and a runner is found by the files the repo already
// has, the way initialConfig picks engines: by what exists, never by reading a language.
func (a App) TestsHint() (TestsHint, bool) {
	var cfg struct {
		Tests json.RawMessage `json:"tests"`
	}
	if b, err := os.ReadFile(a.path(pathConfig)); err != nil || json.Unmarshal(b, &cfg) != nil || len(cfg.Tests) > 0 {
		return TestsHint{}, false
	}
	runner, why, ok := a.detectRunner()
	if !ok {
		return TestsHint{}, false
	}
	h := TestsHint{Runner: runner, Stub: a.testsStub(runner)}
	h.Warning = fmt.Sprintf("%s was found (%s) but %s has no tests section: a2dlc runs none of the repo's tests, so every change to "+
		"code that no engine examines stays INCONCLUSIVE, and the tests written for it do not count; nothing was written.", runner, why, pathConfig)
	h.Warning += " " + baselineWhy
	if h.Stub != "" {
		h.Warning += " Add this to " + pathConfig + ", with a baseline_command that runs the whole suite, and edit the image and paths:"
	} else {
		h.Warning += " a2dlc has no stub for it: declare tests with an image, paths, a command that writes $A2DLC_JUNIT and $A2DLC_COVERAGE, and a baseline_command that runs the existing suite."
	}
	return h, true
}

// baselineWhy is why the tests section also carries a baseline_command (bead a2dlc-m47): Stage 4 takes line coverage
// from the tests that changed, so without a baseline run of the existing suite nothing covers a fix that adds no test.
const baselineWhy = "Without tests.baseline_command, a fix that adds no test but is covered by the existing suite ends INCONCLUSIVE, " +
	"because Stage 4 takes coverage only from the tests that changed."

// detectRunner finds the runner by the files at the repo root, in the order package.json then Python.
func (a App) detectRunner() (runner, why string, ok bool) {
	exists := func(rel string) bool { _, err := os.Stat(a.path(rel)); return err == nil }
	read := func(rel string) string { b, _ := os.ReadFile(a.path(rel)); return string(b) }
	if exists("package.json") {
		var pkg struct {
			Scripts         map[string]string `json:"scripts"`
			Dependencies    map[string]string `json:"dependencies"`
			DevDependencies map[string]string `json:"devDependencies"`
		}
		if json.Unmarshal([]byte(read("package.json")), &pkg) == nil {
			script := pkg.Scripts["test"]
			_, vitestDep := pkg.DevDependencies["vitest"]
			_, vitestRuntimeDep := pkg.Dependencies["vitest"]
			switch {
			case strings.Contains(script, "vitest"):
				return "vitest", `package.json "test" script`, true
			case strings.Contains(script, "node --test"):
				return "node --test", `package.json "test" script`, true
			case vitestDep || vitestRuntimeDep:
				return "vitest", "package.json dependency", true
			case strings.TrimSpace(script) != "" && !strings.Contains(script, "no test specified"): // npm init's placeholder
				return "npm test", `package.json "test" script`, true
			}
		}
	}
	switch {
	case strings.Contains(read("pyproject.toml"), "[tool.pytest"):
		return "pytest", "[tool.pytest] in pyproject.toml", true
	case exists("pytest.ini"):
		return "pytest", "pytest.ini", true
	case strings.Contains(read("tox.ini"), "[pytest]") || strings.Contains(read("setup.cfg"), "[tool:pytest]"):
		return "pytest", "pytest section in tox.ini or setup.cfg", true
	case (exists("pyproject.toml") || exists("setup.py")) && pyTestFiles(a.path("tests")):
		return "pytest", "test files under tests/", true
	}
	return "", "", false
}

// pyTestFiles says whether dir holds a pytest-named file (test_*.py or *_test.py) at any depth.
func pyTestFiles(dir string) bool {
	found := false
	filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if !d.IsDir() && strings.HasSuffix(name, ".py") && (strings.HasPrefix(name, "test_") || strings.HasSuffix(name, "_test.py")) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// testsStub is a tests section for the runner, wired to JUnit and coverage through $A2DLC_JUNIT and $A2DLC_COVERAGE and
// to the test files through $A2DLC_TESTS. The image is a placeholder: it is the repo's toolchain with its dependencies,
// which only the repo knows. The vitest command is the one e2e/factory/c8_junit.sh ran against the real runner; the
// node --test one uses the junit and lcov reporters of Node 22 and the pytest one the coverage.py command of
// contracts/examples/valid/config__tests-py.json; neither was run here.
func (a App) testsStub(runner string) string {
	exists := func(rel string) bool { _, err := os.Stat(a.path(rel)); return err == nil }
	var command string
	paths := []string{"**/*.test.*"}
	switch runner {
	case "vitest":
		command = `vitest run --reporter=junit --outputFile.junit="$A2DLC_JUNIT" --coverage.enabled --coverage.provider=v8 --coverage.reporter=lcovonly --coverage.reportsDirectory=/tmp/cov --coverage.include="src/**" $A2DLC_TESTS; rc=$?; cp /tmp/cov/lcov.info "$A2DLC_COVERAGE" 2>/dev/null; exit $rc`
	case "node --test":
		command = `node --test --experimental-test-coverage --test-reporter=junit --test-reporter-destination="$A2DLC_JUNIT" --test-reporter=lcov --test-reporter-destination="$A2DLC_COVERAGE" $A2DLC_TESTS`
	case "pytest":
		command = `python -m coverage run -m pytest -q -p no:cacheprovider -o junit_family=xunit1 --junitxml="$A2DLC_JUNIT" $A2DLC_TESTS; python -m coverage xml -o "$A2DLC_COVERAGE"`
		paths = []string{"tests/**"}
	default:
		return ""
	}
	for _, dir := range []string{"test", "tests"} {
		if exists(dir) {
			paths = []string{dir + "/**"}
			break
		}
	}
	// The baseline is the same run over the whole suite, so a fix with no new test still has coverage. A repo with a slow
	// suite narrows it to the tests the change affects (`vitest related $A2DLC_CHANGED`, `go test` on the touched packages).
	baseline := strings.ReplaceAll(command, " $A2DLC_TESTS", "")
	tc := contract.TestsConfig{Image: "<image with the repo's toolchain and dependencies>", Paths: paths, Command: command, BaselineCommand: baseline}
	if exists("src") { // the code whose changed lines a test must run when no engine examines them
		tc.SourcePaths = []string{"src/**"}
	}
	var out strings.Builder
	enc := json.NewEncoder(&out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if enc.Encode(map[string]contract.TestsConfig{"tests": tc}) != nil {
		return ""
	}
	return strings.TrimRight(out.String(), "\n")
}

func (a App) gitignore() (int, error) {
	path := a.path(".gitignore")
	b, _ := os.ReadFile(path)
	actual := strings.ReplaceAll(string(b), "\r\n", "\n")
	missing := []string{}
	for _, l := range []string{pathVerdict, pathReports + "/", pathEscalations, pathRun + "/"} {
		if !strings.Contains("\n"+actual+"\n", "\n"+l+"\n") {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return 0, nil
	}
	if actual != "" && !strings.HasSuffix(actual, "\n") {
		actual += "\n"
	}
	return len(missing), os.WriteFile(path, []byte(actual+strings.Join(missing, "\n")+"\n"), 0o644)
}

// writeHook writes an a2dlc block into a git hook (respecting core.hooksPath). If there is already a foreign hook,
// such as beads', the block goes right after the shebang and does not read stdin.
func (a App) writeHook(hookName, comment, cmd string) (string, error) {
	dir, err := a.Repo.HooksDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, hookName)
	block := strings.Join([]string{blockStart, comment, cmd, blockEnd}, "\n") + "\n"
	b, err := os.ReadFile(path)
	actual := strings.ReplaceAll(string(b), "\r\n", "\n")
	switch {
	case errors.Is(err, os.ErrNotExist) || strings.TrimSpace(actual) == "":
		actual = "#!/bin/sh\n" + block
	case err != nil:
		return "", err
	case strings.Contains(actual, blockStart):
		i, j := strings.Index(actual, blockStart), strings.Index(actual, blockEnd)
		if j < i {
			return "", fmt.Errorf("%s has a broken a2dlc block", path)
		}
		actual = actual[:i] + block + strings.TrimPrefix(actual[j+len(blockEnd):], "\n")
	case strings.HasPrefix(actual, "#!"):
		k := strings.Index(actual, "\n") + 1
		actual = actual[:k] + block + actual[k:]
	default:
		actual = "#!/bin/sh\n" + block + actual
	}
	return path, os.WriteFile(path, []byte(actual), 0o755)
}

// prePush writes the a2dlc block into the real pre-push (respects core.hooksPath).
func (a App) prePush(exe string) (string, error) {
	return a.writeHook("pre-push",
		"# Stops the push if the verdict is not PASS, is stale or has unsigned waivers. It verifies HEAD, not each pushed ref.",
		fmt.Sprintf("%q verify --commit HEAD || exit 1", exe))
}

// removeHook takes the a2dlc block out of a git hook (respects core.hooksPath) and leaves the rest of it as it was. A
// hook left with only its shebang is deleted.
func (a App) removeHook(hookName string) (string, bool, error) {
	dir, err := a.Repo.HooksDir()
	if err != nil {
		return "", false, err
	}
	path := filepath.Join(dir, hookName)
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, false, nil
	} else if err != nil {
		return path, false, err
	}
	actual := strings.ReplaceAll(string(b), "\r\n", "\n")
	i, j := strings.Index(actual, blockStart), strings.Index(actual, blockEnd)
	if i < 0 {
		return path, false, nil
	}
	if j < i {
		return path, false, fmt.Errorf("%s has a broken a2dlc block", path)
	}
	actual = actual[:i] + strings.TrimPrefix(actual[j+len(blockEnd):], "\n")
	if rest := strings.TrimSpace(actual); rest == "" || strings.HasPrefix(rest, "#!") && !strings.Contains(rest, "\n") {
		return path, true, os.Remove(path)
	}
	return path, true, os.WriteFile(path, []byte(actual), 0o755)
}

func (a App) hookAgent(agent, exe string) (string, error) {
	cmd := fmt.Sprintf("%q hook %s", exe, agent)
	type hookDef struct {
		event string
		entry map[string]any
	}
	var rel string
	var defs []hookDef
	switch agent {
	case "claude":
		rel = ".claude/settings.json"
		defs = []hookDef{
			{
				event: "PreToolUse",
				entry: map[string]any{
					"matcher": "Edit|Write|NotebookEdit|Bash",
					"hooks":   []any{map[string]any{"type": "command", "command": cmd, "timeout": 60}},
				},
			},
			{
				event: "Stop",
				entry: map[string]any{
					"hooks": []any{map[string]any{"type": "command", "command": cmd, "timeout": 1800}},
				},
			},
		}
	case "gemini":
		rel = ".gemini/settings.json"
		defs = []hookDef{
			{
				event: "BeforeTool",
				entry: map[string]any{
					"matcher": "*",
					"hooks":   []any{map[string]any{"name": "a2dlc", "type": "command", "command": cmd}},
				},
			},
			{
				event: "AfterAgent",
				entry: map[string]any{
					"matcher": "*",
					"hooks":   []any{map[string]any{"name": "a2dlc", "type": "command", "command": cmd}},
				},
			},
		}
	default:
		return "", fmt.Errorf("agent %q: only claude or gemini", agent)
	}
	cfg := map[string]any{}
	if b, err := os.ReadFile(a.path(rel)); err == nil {
		if err := json.Unmarshal(b, &cfg); err != nil {
			return "", fmt.Errorf("%s is not JSON: %w", rel, err)
		}
	}
	hooks, _ := cfg["hooks"].(map[string]any)
	if hooks == nil {
		hooks = map[string]any{}
	}
	for _, d := range defs {
		list, _ := hooks[d.event].([]any)
		b, _ := json.Marshal(list)
		if !strings.Contains(string(b), " hook "+agent) {
			list = append(list, d.entry)
		}
		hooks[d.event] = list
	}
	cfg["hooks"] = hooks
	return a.path(rel), a.write(rel, cfg)
}

// HookBinaryWarning says, when the a2dlc block of the pre-push hook names a different binary than exe (the one running
// now), which two builds are in play. install writes the path of the binary that ran it, so a rebuilt or reinstalled
// a2dlc leaves the push gated by an older build (a2dlc-31w). It is a warning for check and verify, never a failure: ""
// when the paths match, there is no hook or the hook has no a2dlc block.
func (a App) HookBinaryWarning(exe string) string {
	dir, err := a.Repo.HooksDir()
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(dir, "pre-push"))
	if err != nil {
		return ""
	}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	i, j := strings.Index(text, blockStart), strings.Index(text, blockEnd)
	if i < 0 || j < i {
		return ""
	}
	for _, line := range strings.Split(text[i:j], "\n") {
		if !strings.HasPrefix(line, `"`) || !strings.Contains(line, " verify ") {
			continue
		}
		q := strings.Index(line[1:], `"`)
		if q < 0 {
			return ""
		}
		hook, err := strconv.Unquote(line[:q+2])
		if err != nil || samePath(hook, exe) {
			return ""
		}
		return fmt.Sprintf("the pre-push hook runs %s but this is %s: a push is gated by the hook's build, not by this one; run a2dlc install with the build you want", filepath.ToSlash(hook), filepath.ToSlash(exe))
	}
	return ""
}

func samePath(a, b string) bool {
	a, b = filepath.ToSlash(filepath.Clean(a)), filepath.ToSlash(filepath.Clean(b))
	if runtime.GOOS == "windows" {
		return strings.EqualFold(a, b)
	}
	return a == b
}
