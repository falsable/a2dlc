package gitx

import (
	"os"
	"path/filepath"
	"sync"
)

// worktreeMu serializes the worktree changes of one process: git locks its worktree metadata, and two adds at once
// would fail on the lock instead of waiting for it.
var worktreeMu sync.Mutex

// WorktreeAt leaves a detached worktree at dir checked out at commit: it adds one when dir has none, and checks out
// commit in the one there otherwise. Untracked files in it (a run's folder) are kept across checkouts.
func (r Repo) WorktreeAt(dir, commit string) error {
	worktreeMu.Lock()
	defer worktreeMu.Unlock()
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		_, err := r.gitIn(dir, nil, "checkout", "-q", "--detach", commit)
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	_, err := r.git(nil, "worktree", "add", "-q", "--detach", dir, commit)
	return err
}
