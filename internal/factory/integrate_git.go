package factory

import (
	"os"
	"strings"
)

// The git side of the integration queue, all inside the integration worktree, which is a throwaway: a failed lane is
// rolled back with reset --hard, which would be wrong anywhere else.

// applyLane puts a lane's candidate on the integration worktree, staged and uncommitted, so the check and the holdout
// judge the integrated tree before anything is committed. A candidate commit's parent is the lane's base, so a
// cherry-pick carries the builder's diff and nothing else. It returns the files that did not apply, if any.
func applyLane(wt, ref string) (conflicts []string, err error) {
	if _, err = gitIn(wt, "-c", "core.autocrlf=false", "cherry-pick", "--no-commit", ref); err == nil {
		return nil, nil
	}
	out, _ := gitIn(wt, "diff", "--name-only", "--diff-filter=U")
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			conflicts = append(conflicts, f)
		}
	}
	rollback(wt)
	if len(conflicts) == 0 {
		return nil, err // it failed for another reason: the caller reports it
	}
	return conflicts, nil
}

// rollback drops whatever a lane left staged or in the working tree and the cherry-pick's own state.
func rollback(wt string) {
	gitIn(wt, "cherry-pick", "--quit")
	gitIn(wt, "reset", "-q", "--hard", "HEAD")
}

// stagedFiles lists what the lane changes against the integration head.
func stagedFiles(wt string) ([]string, error) {
	out, err := gitIn(wt, "diff", "--cached", "--name-only")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, f := range strings.Split(out, "\n") {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	return files, nil
}

// commitStaged commits the index as the factory: no hooks of the target repository, a fixed identity.
func commitStaged(wt, msg string) (string, error) {
	empty, err := os.MkdirTemp("", "a2dlc-nohooks-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(empty)
	pre := []string{"-c", "core.hooksPath=" + empty, "-c", "user.name=a2dlc-factory", "-c", "user.email=factory@a2dlc.invalid", "-c", "commit.gpgsign=false"}
	if _, err := gitIn(wt, append(pre, "commit", "-q", "-m", msg)...); err != nil {
		return "", err
	}
	return gitIn(wt, "rev-parse", "HEAD")
}
