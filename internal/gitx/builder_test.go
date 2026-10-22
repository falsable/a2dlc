package gitx

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Export of the base followed by TreeFromDir without touching anything gives back the base's tree: the execute bit and
// the symlinks Export does not write come back as they were (without that, a builder that did not touch a script would change its mode).
func TestTreeFromDirRoundTrip(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "f.py"), []byte("def f(x):\n    return x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho hola\n"), 0o644)
	// the signatures are committed: the hashed tree does not carry them, the builder's commit does
	os.MkdirAll(filepath.Join(dir, ".a2dlc"), 0o755)
	os.WriteFile(filepath.Join(dir, ".a2dlc", "approvals.json"), []byte(`{"schema_version":1,"approvals":[]}`), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "update-index", "--chmod=+x", "run.sh")
	symlink := runtime.GOOS != "windows"
	if symlink {
		if err := os.Symlink("src/f.py", filepath.Join(dir, "atajo.py")); err != nil {
			t.Fatal(err)
		}
		git(t, dir, "add", "atajo.py")
	}
	git(t, dir, "commit", "-qm", "base")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := r.ResolveCommit("HEAD")
	baseTree, err := r.CommitTree(base)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := r.Export(base, work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(work, "atajo.py")); symlink && err == nil {
		t.Fatal("Export already writes symlinks: TreeFromDir's re-entry is redundant")
	}
	tree, err := r.TreeFromDir(base, work)
	if err != nil || tree != baseTree {
		t.Fatalf("round trip without changes: %s != %s (%v)", tree, baseTree, err)
	}
	complete, err := r.TreeFromDirComplete(base, work)
	if raw, _ := r.git(nil, "rev-parse", base+"^{tree}"); err != nil || complete != strings.TrimSpace(raw) {
		t.Fatalf("complete round trip: %s != %s (%v)", complete, raw, err)
	}
	// one change and one gate output: the change goes into both; the output only into the complete one
	os.WriteFile(filepath.Join(work, "src", "f.py"), []byte("def f(x):\n    return x + 1\n"), 0o644)
	os.WriteFile(filepath.Join(work, ".a2dlc", "verdict.json"), []byte("{}"), 0o644)
	if tree, err = r.TreeFromDir(base, work); err != nil {
		t.Fatal(err)
	}
	if diffs, _ := r.DiffNames(baseTree, tree); strings.Join(diffs, ",") != "src/f.py" {
		t.Fatalf("changes: %v", diffs)
	}
	if complete, err = r.TreeFromDirComplete(base, work); err != nil {
		t.Fatal(err)
	}
	if diffs, _ := r.DiffNames(base, complete); strings.Join(diffs, ",") != ".a2dlc/verdict.json,src/f.py" {
		t.Fatalf("changes of the complete: %v", diffs)
	}
	mode := exec.Command("git", "ls-tree", tree, "run.sh")
	mode.Dir = dir
	if out, _ := mode.Output(); !strings.HasPrefix(string(out), "100755") {
		t.Fatalf("run.sh lost the execute bit: %s", out)
	}
	// the commit hangs off the base, is reproducible and the real one did not move
	when := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	c1, err := r.CommitOn(complete, base, "a2dlc builder_primary run-x", when)
	if err != nil {
		t.Fatal(err)
	}
	c2, _ := r.CommitOn(complete, base, "a2dlc builder_primary run-x", when)
	if c1 != c2 || !r.IsAncestor(base, c1) {
		t.Fatalf("%s %s", c1, c2)
	}
	if _, err := r.Blob(c1, ".a2dlc/approvals.json"); err != nil {
		t.Fatal("the builder's commit deleted the base's signatures")
	}
	if h, _ := r.CommitTree(c1); h != tree {
		t.Fatalf("the hashed tree of commit %s is not that of TreeFromDir %s", h, tree)
	}
	if head, _ := r.ResolveCommit("HEAD"); head != base {
		t.Fatal("CommitOn moved HEAD")
	}
	if err := r.UpdateRef("refs/a2dlc/run/s/builder_primary", c1); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.ResolveCommit("refs/a2dlc/run/s/builder_primary"); got != c1 {
		t.Fatal(got)
	}
}

// If the base commits .a2dlc/run, the builder does not see it in /work (the Saboteur's overlay lives there), the
// candidate's commit keeps it as is and it is not listed as touched by the builder (CB-04 would look at those paths).
func TestCommittedRunKeptInCommitNotSeenByBuilder(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	os.MkdirAll(filepath.Join(dir, "src"), 0o755)
	os.WriteFile(filepath.Join(dir, "src", "f.py"), []byte("def f(x):\n    return x\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".a2dlc", "run", "demo.v1", "overlay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".a2dlc", "run", "demo.v1", "overlay", "test_f.py"), []byte("def test_f(): pass\n"), 0o644)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := r.ResolveCommit("HEAD")
	work := t.TempDir()
	if err := r.Export(base, work); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(work, "src", "f.py")); err != nil {
		t.Fatalf("Export did not write the code: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, ".a2dlc", "run")); err == nil {
		t.Fatal("Export carried .a2dlc/run to /work")
	}
	os.WriteFile(filepath.Join(work, "src", "f.py"), []byte("def f(x):\n    return x + 1\n"), 0o644)
	complete, err := r.TreeFromDirComplete(base, work)
	if err != nil {
		t.Fatal(err)
	}
	if diffs, _ := r.DiffNames(base, complete); strings.Join(diffs, ",") != "src/f.py" {
		t.Fatalf("changes of the complete: %v", diffs)
	}
	if out, _ := exec.Command("git", "-C", dir, "ls-tree", "-r", "--name-only", complete).Output(); !strings.Contains(string(out), ".a2dlc/run/demo.v1/overlay/test_f.py") {
		t.Fatalf("the commit lost .a2dlc/run: %s", out)
	}
}
