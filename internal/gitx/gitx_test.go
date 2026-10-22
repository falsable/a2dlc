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
