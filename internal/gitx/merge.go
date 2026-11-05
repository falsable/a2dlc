package gitx

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMergeConflict is what MergeCommits returns when two commits change the same lines: the paths are in the error.
var ErrMergeConflict = errors.New("merge conflict")

// MergeCommits merges commits, one after another, on top of base, without touching the working tree or the index: each
// step is `git merge-tree --write-tree` (git 2.38 or later) and the result is a commit whose parents are base and the
// commits, in the order given. A commit that already contains the result so far is taken as it is (fast-forward) and one
// the result already contains adds nothing. The merge commits have a fixed author and date, so the same base and commits
// always give the same commit: a plan that resumes finds its slices on the base they ran on. A conflict is
// ErrMergeConflict with the conflicting paths. With no commits, it returns base.
func (r Repo) MergeCommits(base string, commits []string, message string) (string, error) {
	if len(commits) == 0 {
		return base, nil
	}
	cur := base
	for _, c := range commits {
		if r.IsAncestor(c, cur) {
			continue
		}
		if r.IsAncestor(cur, c) {
			cur = c
			continue
		}
		out, err := r.git(nil, "merge-tree", "--write-tree", "--name-only", "--no-messages", cur, c)
		if err != nil {
			lines := strings.Split(strings.TrimSpace(out), "\n")
			if len(lines) > 1 && len(lines[0]) >= 40 {
				return "", fmt.Errorf("%w: %s", ErrMergeConflict, strings.Join(lines[1:], ", "))
			}
			return "", err
		}
		tree := strings.TrimSpace(strings.Split(out, "\n")[0])
		args := []string{"commit-tree", tree, "-p", cur, "-p", c, "-m", message}
		commit, err := r.git([]string{"GIT_AUTHOR_NAME=a2dlc", "GIT_AUTHOR_EMAIL=a2dlc@localhost", "GIT_COMMITTER_NAME=a2dlc",
			"GIT_COMMITTER_EMAIL=a2dlc@localhost",
			"GIT_AUTHOR_DATE=1970-01-01T00:00:00Z", "GIT_COMMITTER_DATE=1970-01-01T00:00:00Z"}, args...)
		if err != nil {
			return "", err
		}
		cur = strings.TrimSpace(commit)
	}
	return cur, nil
}
