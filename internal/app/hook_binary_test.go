package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// a2dlc-31w: install pins the pre-push to the binary that ran it. When check or verify runs from another build, the
// push is gated by a binary nobody is looking at: it must be said, as a warning (never a failure).
func TestHookBinaryWarningWhenPathDiffersFromExecutable(t *testing.T) {
	a, _, _ := newRepo(t)
	if _, err := a.prePush("C:/old/dist/a2dlc.exe"); err != nil {
		t.Fatal(err)
	}
	w := a.HookBinaryWarning("C:/new/go/bin/a2dlc.exe")
	for _, want := range []string{"C:/old/dist/a2dlc.exe", "C:/new/go/bin/a2dlc.exe", "pre-push", "install"} {
		if !strings.Contains(w, want) {
			t.Fatalf("warning must name %q: %q", want, w)
		}
	}
	if w := a.HookBinaryWarning("C:/old/dist/a2dlc.exe"); w != "" {
		t.Fatalf("same binary must not warn: %q", w)
	}
}

func TestHookBinaryWarningStaysQuietWithoutAnA2dlcBlock(t *testing.T) {
	a, _, _ := newRepo(t)
	if w := a.HookBinaryWarning("/x/a2dlc"); w != "" {
		t.Fatalf("no pre-push at all: %q", w)
	}
	dir, err := a.Repo.HooksDir()
	if err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "pre-push"), []byte("#!/bin/sh\necho other tool\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if w := a.HookBinaryWarning("/x/a2dlc"); w != "" {
		t.Fatalf("a foreign hook is not ours to judge: %q", w)
	}
}

// The block can sit after a foreign hook's shebang (beads) and the path can hold spaces.
func TestHookBinaryWarningReadsBlockInsideForeignHook(t *testing.T) {
	a, _, _ := newRepo(t)
	dir, _ := a.Repo.HooksDir()
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "pre-push"), []byte("#!/usr/bin/env sh\necho before\n"), 0o755)
	if _, err := a.prePush("C:/Program Files/a2dlc/a2dlc.exe"); err != nil {
		t.Fatal(err)
	}
	if w := a.HookBinaryWarning("C:/Program Files/a2dlc/a2dlc.exe"); w != "" {
		t.Fatalf("%q", w)
	}
	if w := a.HookBinaryWarning("C:/other/a2dlc.exe"); !strings.Contains(w, "C:/Program Files/a2dlc/a2dlc.exe") {
		t.Fatalf("%q", w)
	}
}
