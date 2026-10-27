package main

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// a2dlc-31w: check and verify warn (stderr, no change in the exit code) when the pre-push hook is pinned to another build.
func TestCheckAndVerifyWarnWhenPrePushNamesAnotherBinary(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	hook := "#!/bin/sh\n# --- BEGIN A2DLC ---\n# note\n\"C:/old/dist/a2dlc.exe\" verify --commit HEAD || exit 1\n# --- END A2DLC ---\n"
	os.MkdirAll(filepath.Join(dir, ".git", "hooks"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, ".git", "hooks", "pre-push"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(old)
	for _, cmd := range []string{"verify", "check"} {
		stderr := captureStderr(t, func() { run([]string{cmd}) })
		if !strings.Contains(stderr, "warning") || !strings.Contains(stderr, "C:/old/dist/a2dlc.exe") {
			t.Fatalf("%s: no warning about the hook's binary:\n%s", cmd, stderr)
		}
	}
	if stderr := captureStderr(t, func() { run([]string{"version"}) }); strings.Contains(stderr, "warning") {
		t.Fatalf("version must not warn: %s", stderr)
	}
}

func captureStderr(t *testing.T, f func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string)
	go func() { b, _ := io.ReadAll(r); done <- string(b) }()
	f()
	os.Stderr = old
	w.Close()
	return <-done
}
