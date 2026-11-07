package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/gitx"
)

// `a2dlc finding label` is usable without a terminal: flags in, an exit code per result (0 written, 1 no such finding,
// 2 unreadable record, 64 bad usage), and --json out. Report and list read the record the label wrote to.
func TestFindingCommandExitCodesAndJSON(t *testing.T) {
	dir := t.TempDir()
	a := app.App{Repo: gitx.Repo{Dir: dir}, Now: time.Now}
	run := func(args ...string) (int, string, string) {
		var out, errb bytes.Buffer
		return findingCommand(a, args, &out, &errb), out.String(), errb.String()
	}
	label := []string{"label", "--id", "fnd-3a7c91d02b4e", "--as", "false", "--by", "ana", "--reason", "r", "--json"}

	if code, _, _ := run(); code != 64 {
		t.Fatalf("no subcommand: %d", code)
	}
	if code, _, _ := run("label", "--id", "fnd-3a7c91d02b4e", "--as", "false"); code != 64 {
		t.Fatalf("no --by: %d", code)
	}
	if code, _, _ := run("label", "--id", "fnd-3a7c91d02b4e", "--as", "fixed", "--by", "ana"); code != 64 {
		t.Fatalf("outcome fixed: %d", code)
	}
	if code, _, msg := run(label...); code != 1 || !strings.Contains(msg, "no such finding") {
		t.Fatalf("empty record: %d %q", code, msg)
	}

	rec := filepath.Join(dir, ".a2dlc", "reports", "findings.jsonl")
	os.MkdirAll(filepath.Dir(rec), 0o755)
	raised := `{"schema_version":1,"event":"raised","at":"2026-11-07T10:00:00Z","id":"fnd-3a7c91d02b4e","base_ref":"abc","tree_hash":"git-tree:1","kind":"check_finding","source":"check:lint","target":"README.md::lint","state":"parked"}` + "\n"
	os.WriteFile(rec, []byte(raised), 0o644)
	code, out, _ := run(label...)
	var e map[string]any
	if code != 0 || json.Unmarshal([]byte(out), &e) != nil || e["outcome"] != "false" || e["by"] != "ana" || e["via"] != "cli" {
		t.Fatalf("label: %d %s", code, out)
	}
	code, out, _ = run("report")
	if code != 0 || !strings.Contains(out, "check:lint") || !strings.Contains(out, "irrelevant") {
		t.Fatalf("report: %d %s", code, out)
	}
	code, out, _ = run("report", "--json")
	var rep struct{ Total struct{ Raised, False int } }
	if code != 0 || json.Unmarshal([]byte(out), &rep) != nil || rep.Total.Raised != 1 || rep.Total.False != 1 {
		t.Fatalf("report --json: %d %s", code, out)
	}
	if code, out, _ = run("list", "--open"); code != 0 || strings.TrimSpace(out) != "" {
		t.Fatalf("a labeled finding is not open: %d %q", code, out)
	}
	os.WriteFile(rec, []byte("{not json}\n"), 0o644)
	if code, _, _ := run("report"); code != 2 {
		t.Fatalf("corrupt record: %d", code)
	}
}
