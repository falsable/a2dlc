package gitx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitT(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func writeT(t *testing.T, dir, rel, s string) {
	t.Helper()
	p := filepath.Join(dir, rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Two branches that change different files merge into one commit with both changes, and the working tree is untouched.
func TestMergeCommitsDisjointAndConflict(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeT(t, dir, "a.txt", "a\n")
	writeT(t, dir, "b.txt", "b\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")
	base := gitT(t, dir, "rev-parse", "HEAD")
	branch := func(name, file, content string) string {
		gitT(t, dir, "checkout", "-q", "-b", name, base)
		writeT(t, dir, file, content)
		gitT(t, dir, "commit", "-qam", name)
		return gitT(t, dir, "rev-parse", "HEAD")
	}
	x, y, z := branch("x", "a.txt", "a from x\n"), branch("y", "b.txt", "b from y\n"), branch("z", "a.txt", "a from z\n")
	gitT(t, dir, "checkout", "-q", "main")
	r := Repo{Dir: dir}
	merged, err := r.MergeCommits(base, []string{x, y}, "plan merge")
	if err != nil {
		t.Fatal(err)
	}
	a, _ := r.Show(merged, "a.txt")
	b, _ := r.Show(merged, "b.txt")
	if string(a) != "a from x\n" || string(b) != "b from y\n" {
		t.Fatalf("merged a=%q b=%q", a, b)
	}
	if onDisk, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(onDisk) != "a\n" {
		t.Fatal("the merge touched the working tree")
	}
	_, err = r.MergeCommits(base, []string{x, z}, "plan merge")
	if !errors.Is(err, ErrMergeConflict) || !strings.Contains(err.Error(), "a.txt") {
		t.Fatalf("a conflict on a.txt was not reported: %v", err)
	}
	if same, err := r.MergeCommits(base, nil, "m"); err != nil || same != base {
		t.Fatalf("no commits must give the base: %s %v", same, err)
	}
}

// The same merge twice is the same commit, and a commit that contains the other is a fast-forward.
func TestMergeCommitsDeterministicAndFastForward(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeT(t, dir, "a.txt", "a\n")
	writeT(t, dir, "b.txt", "b\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "base")
	base := gitT(t, dir, "rev-parse", "HEAD")
	gitT(t, dir, "checkout", "-q", "-b", "x", base)
	writeT(t, dir, "a.txt", "a from x\n")
	gitT(t, dir, "commit", "-qam", "x")
	x := gitT(t, dir, "rev-parse", "HEAD")
	gitT(t, dir, "checkout", "-q", "-b", "y", base)
	writeT(t, dir, "b.txt", "b from y\n")
	gitT(t, dir, "commit", "-qam", "y")
	y := gitT(t, dir, "rev-parse", "HEAD")
	r := Repo{Dir: dir}
	m1, err1 := r.MergeCommits(base, []string{x, y}, "m")
	m2, err2 := r.MergeCommits(base, []string{x, y}, "m")
	if err1 != nil || err2 != nil || m1 != m2 {
		t.Fatalf("two merges of the same commits: %s %v, %s %v", m1, err1, m2, err2)
	}
	if ff, err := r.MergeCommits(base, []string{x}, "m"); err != nil || ff != x {
		t.Fatalf("a single descendant is not a fast-forward: %s %v", ff, err)
	}
	if same, err := r.MergeCommits(m1, []string{x, y}, "m"); err != nil || same != m1 {
		t.Fatalf("commits already in the result changed it: %s %v", same, err)
	}
}

// A worktree is added once at a commit and moved to another later, keeping its untracked files.
func TestWorktreeAtAddsAndMoves(t *testing.T) {
	dir := t.TempDir()
	gitT(t, dir, "init", "-q", "-b", "main")
	writeT(t, dir, "a.txt", "1\n")
	gitT(t, dir, "add", "-A")
	gitT(t, dir, "commit", "-qm", "one")
	one := gitT(t, dir, "rev-parse", "HEAD")
	writeT(t, dir, "a.txt", "2\n")
	gitT(t, dir, "commit", "-qam", "two")
	two := gitT(t, dir, "rev-parse", "HEAD")
	r := Repo{Dir: dir}
	wt := filepath.Join(t.TempDir(), "wt", "slice")
	if err := r.WorktreeAt(wt, one); err != nil {
		t.Fatal(err)
	}
	writeT(t, wt, ".a2dlc/run/s/state.json", "{}")
	if err := r.WorktreeAt(wt, two); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(filepath.Join(wt, "a.txt"))
	if string(a) != "2\n" || gitT(t, wt, "rev-parse", "HEAD") != two {
		t.Fatalf("the worktree is at %q", a)
	}
	if _, err := os.Stat(filepath.Join(wt, ".a2dlc/run/s/state.json")); err != nil {
		t.Fatal("the run folder did not survive the checkout")
	}
}
