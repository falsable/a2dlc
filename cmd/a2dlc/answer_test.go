package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/gitx"
)

// `a2dlc answer` exits 64 on bad usage, 2 when the run cannot take the answer, and 0 when it is written.
func TestAnswerCommandExitCodes(t *testing.T) {
	dir := t.TempDir()
	a := app.App{Repo: gitx.Repo{Dir: dir}}
	full := []string{"--slice", "pago.v1", "--probe", "esc-1", "--choice", "alpha", "--reason", "the spec is strict", "--by", "aldemar"}

	var stderr bytes.Buffer
	if code := answerCommand(a, full[:4], &stderr); code != 64 || !strings.Contains(stderr.String(), "needs --slice") {
		t.Fatalf("missing flags: exit %d, stderr %q", code, stderr.String())
	}

	stderr.Reset()
	if code := answerCommand(a, full, &stderr); code != 2 || !strings.Contains(stderr.String(), "has no run yet") {
		t.Fatalf("no run: exit %d, stderr %q", code, stderr.String())
	}

	runDir := filepath.Join(dir, ".a2dlc", "run", "pago.v1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), []byte(`{"run_id":"run-000000000000"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "escalations.json"), []byte(`{"schema_version":1,"items":[{"id":"esc-1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	stderr.Reset()
	if code := answerCommand(a, full, &stderr); code != 0 {
		t.Fatalf("written answer: exit %d, stderr %q", code, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(runDir, "answers.json")); err != nil {
		t.Fatalf("answers.json not written: %v", err)
	}
}
