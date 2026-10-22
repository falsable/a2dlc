// Package gitx is everything the core asks of git: the candidate's tree with a temporary index, the diff against the
// base, exporting trees to folders and reading blobs from a commit.
package gitx

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Excluded are the gate outputs and the signatures: they do not enter the tree_hash (ADR meta/0004). .a2dlc/run is the
// state of `a2dlc run` (state, route, ledger, Saboteur overlay): it is not code either, and if it got in, the verdict
// would go stale for the code commit.
var Excluded = []string{".a2dlc/verdict.json", ".a2dlc/reports", ".a2dlc/approvals.json", ".a2dlc/escalations.json", ".a2dlc/run"}

type Repo struct{ Dir string }

func Open(dir string) (Repo, error) {
	out, err := Repo{Dir: dir}.git(nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repo{}, fmt.Errorf("not a git repo: %w", err)
	}
	return Repo{Dir: filepath.FromSlash(strings.TrimSpace(out))}, nil
}

func (r Repo) git(env []string, args ...string) (string, error) {
	var out, errb bytes.Buffer
	c := exec.Command("git", args...)
	c.Dir = r.Dir
	c.Env = append(os.Environ(), env...)
	c.Stdout, c.Stderr = &out, &errb
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(errb.String()))
	}
	return out.String(), nil
}

func (r Repo) abs(p string) string {
	p = strings.TrimSpace(p)
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(r.Dir, p)
}

func (r Repo) tempIndex(copyCurrent bool) (string, func(), error) {
	f, err := os.CreateTemp("", "a2dlc-index-*")
	if err != nil {
		return "", nil, err
	}
	name := f.Name()
	f.Close()
	cleanup := func() { os.Remove(name) }
	if copyCurrent {
		// Starting from the real index avoids rehashing every file in a large repo.
		p, err := r.git(nil, "rev-parse", "--git-path", "index")
		if err == nil {
			if b, err := os.ReadFile(r.abs(p)); err == nil {
				if err := os.WriteFile(name, b, 0o600); err != nil {
					cleanup()
					return "", nil, err
				}
				// The copy keeps the index's mtime: git uses it to detect "racy" files (edited in the same second and
				// with the same size). With the current mtime, git would count them as clean and check would not see
				// the change (found by TestOverMaxFilesDoesNotRunAndSaysSo).
				if st, err := os.Stat(r.abs(p)); err == nil {
					os.Chtimes(name, st.ModTime(), st.ModTime())
				}
				return name, cleanup, nil
			}
		}
	}
	os.Remove(name) // git creates the index if it does not exist; an empty 0-byte one looks corrupt to it
	return name, cleanup, nil
}

func (r Repo) writeTree(env []string) (string, error) {
	args := append([]string{"rm", "--cached", "-r", "-q", "--ignore-unmatch", "--"}, Excluded...)
	if _, err := r.git(env, args...); err != nil {
		return "", err
	}
	out, err := r.git(env, "write-tree")
	return strings.TrimSpace(out), err
}

// WorktreeTree is the git tree of the working tree as it is (with the uncommitted and untracked files, without the
// ignored ones), without the gate outputs. It does not touch the real index.
func (r Repo) WorktreeTree() (string, error) {
	idx, cleanup, err := r.tempIndex(true)
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := r.git(env, "add", "-A"); err != nil {
		return "", err
	}
	return r.writeTree(env)
}

// CommitTree is the tree of a commit without the gate outputs: what verify compares with the receipt.
func (r Repo) CommitTree(commit string) (string, error) {
	idx, cleanup, err := r.tempIndex(false)
	if err != nil {
		return "", err
	}
	defer cleanup()
	env := []string{"GIT_INDEX_FILE=" + idx}
	if _, err := r.git(env, "read-tree", commit); err != nil {
		return "", err
	}
	return r.writeTree(env)
}

// Upstream is the commit of the remote branch that HEAD follows, or "" if there is none.
func (r Repo) Upstream() string {
	sha, err := r.ResolveCommit("@{upstream}")
	if err != nil {
		return ""
	}
	return sha
}

// RemoteDefaultBase is where HEAD forks from origin's default branch (refs/remotes/origin/HEAD), or "" if there is no
// such branch. It is the base of a branch that was never pushed, such as an agent's worktree branch.
func (r Repo) RemoteDefaultBase() string {
	out, err := r.git(nil, "merge-base", "HEAD", "refs/remotes/origin/HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// IsAncestor says whether a is an ancestor of b or equal to it.
func (r Repo) IsAncestor(a, b string) bool {
	_, err := r.git(nil, "merge-base", "--is-ancestor", a, b)
	return err == nil
}

func (r Repo) ResolveCommit(ref string) (string, error) {
	out, err := r.git(nil, "rev-parse", "--verify", ref+"^{commit}")
	return strings.TrimSpace(out), err
}

// Changed lists the files that change from base to tree (the deleted ones apart), without anything under .a2dlc/:
// the config and the waivers are not code.
func (r Repo) Changed(base, tree string) (changed, deleted []string, err error) {
	out, err := r.git(nil, "diff", "--name-status", "--no-renames", "-z", base, tree)
	if err != nil {
		return nil, nil, err
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	changed, deleted = []string{}, []string{}
	for i := 0; i+1 < len(fields); i += 2 {
		state, path := fields[i], fields[i+1]
		if strings.HasPrefix(path, ".a2dlc/") {
			continue
		}
		if state == "D" {
			deleted = append(deleted, path)
		} else {
			changed = append(changed, path)
		}
	}
	return changed, deleted, nil
}

// ChangedLines are the lines of path in tree that are not in base (numbered in tree): those of each "+" hunk of a diff
// without context. A new file gives all its lines; a pure deletion, none.
func (r Repo) ChangedLines(base, tree, path string) ([]int, error) {
	out, err := r.git(nil, "diff", "--unified=0", "--no-renames", "--no-color", base, tree, "--", path)
	if err != nil {
		return nil, err
	}
	lines := []int{}
	for _, l := range strings.Split(out, "\n") {
		if !strings.HasPrefix(l, "@@ ") {
			continue
		}
		// @@ -a[,b] +c[,d] @@
		fields := strings.Fields(l)
		if len(fields) < 3 || !strings.HasPrefix(fields[2], "+") {
			continue
		}
		c, d := 0, 1
		if i := strings.IndexByte(fields[2], ','); i >= 0 {
			fmt.Sscan(fields[2][1:i], &c)
			fmt.Sscan(fields[2][i+1:], &d)
		} else {
			fmt.Sscan(fields[2][1:], &c)
		}
		for k := 0; k < d; k++ {
			lines = append(lines, c+k)
		}
	}
	return lines, nil
}

// DiffNames lists the paths that differ between two trees (to say what made a receipt stale).
func (r Repo) DiffNames(a, b string) ([]string, error) {
	out, err := r.git(nil, "diff", "--name-only", "-z", a, b)
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(out, "\x00")
	if s == "" {
		return []string{}, nil
	}
	return strings.Split(s, "\x00"), nil
}

// Blob is the blob id of a path in a tree or commit.
func (r Repo) Blob(treeish, path string) (string, error) {
	out, err := r.git(nil, "rev-parse", treeish+":"+path)
	return strings.TrimSpace(out), err
}

// Files lists all the file paths of a tree or commit (with / and without quotes).
func (r Repo) Files(treeish string) ([]string, error) {
	out, err := r.git(nil, "ls-tree", "-r", "--name-only", "-z", treeish)
	if err != nil {
		return nil, err
	}
	s := strings.TrimRight(out, "\x00")
	if s == "" {
		return []string{}, nil
	}
	return strings.Split(s, "\x00"), nil
}

// LastBlob is the blob id of path in the newest commit of commit's history that has it (deleted later included), or ""
// if it never existed. A shallow clone cannot see the whole history, so there it is an error rather than a guess.
func (r Repo) LastBlob(commit, path string) (string, error) {
	shallow, err := r.git(nil, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(shallow) == "true" {
		return "", fmt.Errorf("the clone is shallow: its history cannot tell whether %s ever existed", path)
	}
	out, err := r.git(nil, "log", "--full-history", "--format=%H", commit, "--", path)
	if err != nil {
		return "", err
	}
	for _, sha := range strings.Fields(out) {
		if b, err := r.Blob(sha, path); err == nil {
			return b, nil
		}
	}
	return "", nil
}

// WorktreeBlob is the blob id that path in the working tree would have if it were committed.
func (r Repo) WorktreeBlob(path string) (string, error) {
	out, err := r.git(nil, "hash-object", "--", path)
	return strings.TrimSpace(out), err
}

// Show reads a file from a tree or commit; if it does not exist it returns os.ErrNotExist.
func (r Repo) Show(treeish, path string) ([]byte, error) {
	if _, err := r.Blob(treeish, path); err != nil {
		return nil, os.ErrNotExist
	}
	out, err := r.git(nil, "cat-file", "blob", treeish+":"+path)
	return []byte(out), err
}

// HooksDir is the real hooks folder (respects core.hooksPath, like .beads/hooks).
func (r Repo) HooksDir() (string, error) {
	out, err := r.git(nil, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return "", err
	}
	return r.abs(out), nil
}

func (r Repo) Config(key string) string {
	out, _ := r.git(nil, "config", "--get", key)
	return strings.TrimSpace(out)
}

// Export writes the contents of a tree or commit into dir (which must exist). Symlinks are skipped: on Windows creating
// them requires permissions, and an engine that needs them will say so as INCONCLUSIVE.
func (r Repo) Export(treeish, dir string) error {
	// without .a2dlc/run: if the base has it committed, the builder would see the Saboteur's overlay and launches
	c := exec.Command("git", "archive", "--format=tar", treeish, "--", ":(exclude).a2dlc/run")
	c.Dir = r.Dir
	var errb bytes.Buffer
	c.Stderr = &errb
	pipe, err := c.StdoutPipe()
	if err != nil {
		return err
	}
	if err := c.Start(); err != nil {
		return err
	}
	errTar := extract(pipe, dir)
	io.Copy(io.Discard, pipe)
	if err := c.Wait(); err != nil {
		return fmt.Errorf("git archive %s: %v: %s", treeish, err, strings.TrimSpace(errb.String()))
	}
	return errTar
}

func extract(rd io.Reader, dir string) error {
	tr := tar.NewReader(rd)
	root, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		dst := filepath.Join(root, filepath.FromSlash(h.Name))
		if dst != root && !strings.HasPrefix(dst, root+string(filepath.Separator)) {
			return fmt.Errorf("path outside the destination in the tar: %q", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dst, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
			if err != nil {
				return err
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return err
			}
		}
	}
}
