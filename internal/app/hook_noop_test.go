package app

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/gitx"
)

const pushNoVerify = `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"git push --no-verify origin main"}}`

// A repo that never installed a2dlc: a globally wired hook allows every event and leaves no file behind.
func TestHookAllowsAndWritesNothingWithoutConfig(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "x.py", "x = 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := App{Repo: repo, Runner: &fakeEngine{}, Version: "test", Now: func() time.Time { return time.Unix(0, 0) }}
	for _, in := range []string{`{"session_id":"s1"}`, pushNoVerify} {
		var out, errb bytes.Buffer
		if code := a.Hook(context.Background(), "claude", strings.NewReader(in), &out, &errb); code != 0 || out.Len()+errb.Len() != 0 {
			t.Fatalf("%s: code %d, stdout %q, stderr %q", in, code, out.String(), errb.String())
		}
	}
	var out bytes.Buffer
	if code := a.Hook(context.Background(), "gemini", strings.NewReader(`{"session_id":"g1"}`), &out, &bytes.Buffer{}); code != 0 || !strings.Contains(out.String(), `"decision":"allow"`) {
		t.Fatalf("gemini: code %d, %s", code, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc")); !os.IsNotExist(err) {
		t.Fatalf("the hook wrote .a2dlc in an uninstalled repo: %v", err)
	}
}

// Deleting the config in the working tree does not disarm the hook while HEAD still has it.
func TestHookStaysArmedWhenConfigDeletedInWorktree(t *testing.T) {
	a, _, dir := newRepo(t)
	if err := os.Remove(filepath.Join(dir, ".a2dlc/config.json")); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	if code := a.Hook(context.Background(), "claude", strings.NewReader(pushNoVerify), &bytes.Buffer{}, &errb); code != 2 {
		t.Fatalf("expected the push --no-verify block, got code %d: %s", code, errb.String())
	}
}
