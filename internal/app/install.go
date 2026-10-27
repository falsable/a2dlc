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
