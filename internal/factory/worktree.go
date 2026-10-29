package factory

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// worktrees creates and removes the lanes' git worktrees. git takes locks on the shared repository for these, so the
// calls are serialized; the lanes themselves run in parallel afterwards.
type worktrees struct {
	mu  sync.Mutex
	dir string // the main repository
}

func gitIn(dir string, args ...string) (string, error) {
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	b, err := c.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(b)))
	}
	return strings.TrimSpace(string(b)), nil
}

// add makes path a worktree on a new branch at base.
func (w *worktrees) add(path, branch, base string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := gitIn(w.dir, "worktree", "add", "-q", "-b", branch, path, base)
	return err
}

// remove deletes the worktree's directory and keeps its branch and the refs a2dlc run left under refs/a2dlc.
func (w *worktrees) remove(path string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, err := gitIn(w.dir, "worktree", "remove", "--force", path)
	return err
}

// commitFile commits one file the lane needs, on the lane's branch, and returns the new commit. It is the factory's own
// setup commit on a throwaway branch: hooks are off, so a hook of the target repository does not run for it, and the
// identity is fixed.
func commitFile(worktree, rel, msg string) (string, error) {
	empty, err := os.MkdirTemp("", "a2dlc-nohooks-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(empty)
	pre := []string{"-c", "core.hooksPath=" + empty, "-c", "user.name=a2dlc-factory", "-c", "user.email=factory@a2dlc.invalid", "-c", "commit.gpgsign=false"}
	if _, err := gitIn(worktree, append(pre, "add", "-f", "--", rel)...); err != nil {
		return "", err
	}
	if _, err := gitIn(worktree, append(pre, "commit", "-q", "-m", msg)...); err != nil {
		return "", err
	}
	return gitIn(worktree, "rev-parse", "HEAD")
}
