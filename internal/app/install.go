package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	pc, err := a.preCommit(exe)
	if err != nil {
		return done, err
	}
	done = append(done, "a2dlc block in "+pc)
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
		c.Engines = append(c.Engines, contract.EngineConfig{Language: "python", Image: enginePyImage,
			Paths: []string{"**/*.py"}, Budget: json.RawMessage(`{"per_condition_timeout_s":10}`)})
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

// preCommit writes the a2dlc block into the real pre-commit (respects core.hooksPath).
func (a App) preCommit(exe string) (string, error) {
	return a.writeHook("pre-commit",
		"# Runs differential verification over the working tree and generates .a2dlc/verdict.json.",
		fmt.Sprintf("%q check || exit 1", exe))
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
