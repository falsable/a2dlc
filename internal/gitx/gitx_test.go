package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
}

func TestChangedLines(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("l1\nl2\nl3\nl4\nl5\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	// l2 changes, l4 is deleted, two new lines at the end, and b.py is a new file
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("l1\nL2\nl3\nl5\nl6\nl7\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "b.py"), []byte("x\ny\n"), 0o644)
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := r.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.ChangedLines("HEAD", tree, "a.py")
	if err != nil || !reflect.DeepEqual(got, []int{2, 5, 6}) {
		t.Fatalf("a.py: %v %v", got, err)
	}
	if got, _ = r.ChangedLines("HEAD", tree, "b.py"); !reflect.DeepEqual(got, []int{1, 2}) {
		t.Fatalf("b.py: %v", got)
	}
}

// A nested git worktree that nobody tracked (a Claude Code session's .claude/worktrees/<name>) is an embedded repository
// to git: `add -A` would stage it as a gitlink the commit never has, so the tree of a clean HEAD would differ from the
// tree of the working tree (a2dlc-3hd). It is not part of the tree, and neither is a nested clone.
func TestWorktreeTreeIgnoresUntrackedNestedRepositories(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("l1\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := r.CommitTree("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "worktree", "add", "-q", "-b", "nested", filepath.Join(dir, ".claude", "worktrees", "nested"))
	clone := filepath.Join(dir, "vendor", "clone")
	os.MkdirAll(filepath.Dir(clone), 0o755)
	git(t, dir, "clone", "-q", dir, clone)
	got, err := r.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("an untracked nested repository changed the tree: %s, the commit's is %s", got, want)
	}
	// regular untracked files still count
	os.WriteFile(filepath.Join(dir, "new.py"), []byte("x\n"), 0o644)
	if got, _ = r.WorktreeTree(); got == want {
		t.Fatal("an untracked regular file must change the tree")
	}
}

// A repository that the commit does track as a gitlink (a submodule) stays in the tree, so a clean HEAD has the same tree
// as its working tree.
func TestWorktreeTreeKeepsTrackedGitlinks(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	sub := filepath.Join(dir, "sub")
	os.MkdirAll(sub, 0o755)
	git(t, sub, "init", "-q")
	os.WriteFile(filepath.Join(sub, "s.py"), []byte("s\n"), 0o644)
	git(t, sub, "add", "-A")
	git(t, sub, "commit", "-qm", "sub")
	os.WriteFile(filepath.Join(dir, "a.py"), []byte("l1\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base with a gitlink")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want, err := r.CommitTree("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := r.WorktreeTree(); err != nil || got != want {
		t.Fatalf("%v: tracked gitlink dropped from the tree: %s vs %s", err, got, want)
	}
}
