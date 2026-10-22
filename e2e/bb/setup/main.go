// setup materializes an instance of e2e/bb/check.schema.json for a2dlc's self-test with its black-box engine: a git repo
// in $A2DLC_WORK/repo with the base committed and the changes in the working tree, its .a2dlc/config.json and
// waivers.json, and $A2DLC_WORK/fake.json for e2e/bb/fakedocker. It imports nothing from the module: the harness must
// not change when the core it measures changes. Fixed git dates, so base_ref is deterministic.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

type file struct {
	Path    string `json:"path"`
	Change  string `json:"change"`
	Verdict string `json:"verdict"`
	Waived  bool   `json:"waived"`
}

type engineSpec struct {
	Mode  string   `json:"mode"`
	Paths []string `json:"paths"`
}

type instance struct {
	Files    []file       `json:"files"`
	Engines  []engineSpec `json:"engines"`
	MaxFiles int          `json:"max_files"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "setup:", err)
		os.Exit(1)
	}
}

func write(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func git(repo string, args ...string) error {
	c := exec.Command("git", append([]string{"-C", repo}, args...)...)
	c.Env = append(os.Environ(), "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z",
		"GIT_AUTHOR_NAME=bb", "GIT_AUTHOR_EMAIL=bb@a2dlc", "GIT_COMMITTER_NAME=bb", "GIT_COMMITTER_EMAIL=bb@a2dlc")
	if out, err := c.CombinedOutput(); err != nil {
		return fmt.Errorf("git %v: %v: %s", args, err, out)
	}
	return nil
}

func run() error {
	work := os.Getenv("A2DLC_WORK")
	raw, err := os.ReadFile(os.Getenv("A2DLC_INPUT"))
	if err != nil {
		return err
	}
	var in instance
	if err := json.Unmarshal(raw, &in); err != nil {
		return err
	}
	repo := filepath.Join(work, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		return err
	}
	if err := git(repo, "init", "-q", "-b", "main"); err != nil {
		return err
	}
	// The first occurrence of each path wins; repeated ones are ignored.
	seen := map[string]bool{}
	files := []file{}
	for _, f := range in.Files {
		if !seen[f.Path] {
			seen[f.Path] = true
			files = append(files, f)
		}
	}
	if err := write(filepath.Join(repo, "README"), []byte("base\n")); err != nil {
		return err
	}
	for _, f := range files {
		if f.Change != "add" {
			if err := write(filepath.Join(repo, f.Path), []byte("v1 "+f.Path+"\n")); err != nil {
				return err
			}
		}
	}
	cfg := map[string]any{"schema_version": 1, "engines": []any{}}
	engines := []any{}
	for i, m := range in.Engines {
		engines = append(engines, map[string]any{"language": fmt.Sprintf("l%d", i), "image": fmt.Sprintf("fake-%s-%d", m.Mode, i),
			"paths": m.Paths, "budget": map[string]any{}})
	}
	cfg["engines"] = engines
	if in.MaxFiles > 0 {
		cfg["max_files"] = in.MaxFiles
	}
	b, _ := json.MarshalIndent(cfg, "", " ")
	if err := write(filepath.Join(repo, ".a2dlc", "config.json"), b); err != nil {
		return err
	}
	if err := git(repo, "add", "-A"); err != nil {
		return err
	}
	if err := git(repo, "commit", "-q", "-m", "base"); err != nil {
		return err
	}
	verdicts := map[string]string{}
	waivers := []any{}
	for _, f := range files {
		path := filepath.Join(repo, f.Path)
		switch f.Change {
		case "delete":
			if err := os.Remove(path); err != nil {
				return err
			}
		default:
			if err := write(path, []byte("v2 "+f.Path+"\n")); err != nil {
				return err
			}
		}
		verdicts[f.Path] = f.Verdict
		if f.Waived {
			waivers = append(waivers, map[string]any{"target": f.Path + "::f", "decision": "enforce_new_behavior", "reason": "bb"})
		}
	}
	if len(waivers) > 0 {
		b, _ = json.MarshalIndent(map[string]any{"schema_version": 1, "waivers": waivers}, "", " ")
		if err := write(filepath.Join(repo, ".a2dlc", "waivers.json"), b); err != nil {
			return err
		}
	}
	b, _ = json.Marshal(map[string]any{"verdicts": verdicts})
	return os.WriteFile(filepath.Join(work, "fake.json"), b, 0o644)
}
