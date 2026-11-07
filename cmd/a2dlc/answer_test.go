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
	// state.json must satisfy its schema: the valid example of the contract, with this test's run and slice
	state, err := os.ReadFile("../../contracts/examples/valid/run-state__stage4.json")
	if err != nil {
		t.Fatal(err)
	}
	state = bytes.ReplaceAll(bytes.ReplaceAll(state, []byte("run-3f9a1c2b7d4e"), []byte("run-000000000000")), []byte("core.relational.as_set.v1"), []byte("pago.v1"))
	if err := os.WriteFile(filepath.Join(runDir, "state.json"), state, 0o644); err != nil {
		t.Fatal(err)
	}
	item := `{"id":"esc-1","kind":"test_without_teeth","target":"tests/test_calc.py::tests.test_calc::test_media_vacia","summary":"s","evidence":{"base":"passed","candidate":"passed"},"resolve_with":["characterization_test"]}`
	if err := os.WriteFile(filepath.Join(runDir, "escalations.json"), []byte(`{"schema_version":1,"tree_hash":"git-tree:4b825dc642cb6eb9a060e54bf8d69288fbee4904","created_at":"2026-10-16T12:00:00Z","items":[`+item+`]}`), 0o644); err != nil {
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
