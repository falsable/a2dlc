package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitIn runs git with cwd in dir (not in the repo) and the extra environment: to work on a folder from outside, like a
// builder's /work, with the repo's GIT_DIR.
func (r Repo) gitIn(dir string, env []string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), env...)
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

// TreeFromDir is the git tree of a folder that started as Export(base) (a builder's /work), without the gate outputs:
// the one that gets hashed (tree_hash). It starts from the base index, so the base's file modes are kept
// (core.filemode=false: Export writes everything 0644) and the symlinks Export skipped come back as they were; if the
// builder deletes a symlink, that is not seen. It respects the folder's .gitignore files. The objects stay in the repo;
// neither the real index nor the working tree is touched.
func (r Repo) TreeFromDir(base, dir string) (string, error) { return r.treeFromDir(base, dir, true) }

// TreeFromDirComplete is TreeFromDir with everything there is, gate outputs included: the one that gets committed.
// Without the outputs, the commit would delete the approvals.json the base has committed, and whoever merged it would
// lose the signatures.
func (r Repo) TreeFromDirComplete(base, dir string) (string, error) {
	return r.treeFromDir(base, dir, false)
}

func (r Repo) treeFromDir(base, dir string, withoutOutputs bool) (string, error) {
	gitDir, err := r.git(nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	idx, cleanup, err := r.tempIndex(false)
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := r.git(env, "read-tree", base); err != nil {
		return "", err
	}
	ls, err := r.git(nil, "ls-tree", "-r", "-z", base)
	if err != nil {
		return "", err
	}
	type link struct{ mode, sha, path string }
	var links []link
	for _, l := range strings.Split(strings.TrimRight(ls, "\x00"), "\x00") {
		meta, path, ok := strings.Cut(l, "\t")
		// .a2dlc/run: Export does not write it to /work (the builder does not see it), but if the base commits it the
		// candidate's commit must keep it, not delete it or make CB-04 count it as touched by the builder.
		if c := strings.Fields(meta); ok && len(c) == 3 && (c[0] == "120000" || strings.HasPrefix(path, ".a2dlc/run/")) {
			links = append(links, link{c[0], c[2], path})
		}
	}
	envWork := append(env, "GIT_DIR="+strings.TrimSpace(gitDir), "GIT_WORK_TREE="+abs)
	if _, err := r.gitIn(abs, envWork, "-c", "core.filemode=false", "add", "-A"); err != nil {
		return "", err
	}
	for _, e := range links {
		if _, err := os.Lstat(filepath.Join(abs, filepath.FromSlash(e.path))); errors.Is(err, os.ErrNotExist) {
			if _, err := r.git(env, "update-index", "--add", "--cacheinfo", e.mode+","+e.sha+","+e.path); err != nil {
				return "", err
			}
		}
	}
	if withoutOutputs {
		return r.writeTree(env)
	}
	out, err := r.git(env, "write-tree")
	return strings.TrimSpace(out), err
}

// CommitOn creates a commit with tree over parent, signed by a2dlc and with the given date (the sha is reproducible),
// without touching branches or the working tree.
func (r Repo) CommitOn(tree, parent, msg string, when time.Time) (string, error) {
	date := fmt.Sprintf("@%d +0000", when.Unix())
	env := []string{"GIT_AUTHOR_NAME=a2dlc", "GIT_AUTHOR_EMAIL=a2dlc@localhost", "GIT_AUTHOR_DATE=" + date,
		"GIT_COMMITTER_NAME=a2dlc", "GIT_COMMITTER_EMAIL=a2dlc@localhost", "GIT_COMMITTER_DATE=" + date}
	out, err := r.git(env, "commit-tree", "--no-gpg-sign", tree, "-p", parent, "-m", msg)
	return strings.TrimSpace(out), err
}

// UpdateRef points ref at sha (e.g. refs/a2dlc/run/<slice>/builder_primary): the commit does not stay dangling for gc
// and the person has something to review.
func (r Repo) UpdateRef(ref, sha string) error {
	_, err := r.git(nil, "update-ref", ref, sha)
	return err
}
