// @a2dlc:slice_id = "beads-a2dlc-05f.cfa14d"
// @a2dlc:mode = "lite"
// @a2dlc:rationale = "help is answered before the repo is opened, so usage never depends on a git repository"
// @a2dlc:rejected_alternatives = []
// @a2dlc:protected_invariants = []
// @a2dlc:do_not_refactor = []
package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func runCapturingStdout(t *testing.T, args []string) (int, string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run(args)
	os.Stdout = old
	w.Close()
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func TestHelpOutsideAGitRepo(t *testing.T) {
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	for _, args := range [][]string{{"--help"}, {"help"}} {
		code, out := runCapturingStdout(t, args)
		if code != 0 || !strings.Contains(out, "usage: a2dlc") {
			t.Fatalf("%v: exit %d, stdout %q", args, code, out)
		}
	}
	if code, _ := runCapturingStdout(t, nil); code != 64 {
		t.Fatalf("no arguments: exit %d, want 64", code)
	}
}
