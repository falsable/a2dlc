package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/gitx"
)

func initTestRepo(t *testing.T) gitx.Repo {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	run("init")
	run("config", "user.email", "test@falsable.dev")
	run("config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "README.md")
	run("commit", "-m", "init")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestMCPServeProtocolAndTools(t *testing.T) {
	repo := initTestRepo(t)
	// Seed a sample .a2dlc/verdict.json to exercise a2dlc_format_report
	a2dir := filepath.Join(repo.Dir, ".a2dlc")
	if err := os.MkdirAll(a2dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sampleVerdict := `{
  "schema_version": 1,
  "a2dlc_version": "0.3.0-test",
  "verdict": "FAIL",
  "base_ref": "HEAD",
  "tree_hash": "git-tree:1234567890abcdef",
  "created_at": "2026-10-21T20:00:00Z",
  "targets": [
    {
      "target": "src/pricing.py::apply_discount",
      "engine": "crosshair-diffbehavior",
      "engine_version": "0.1",
      "verdict": "FAIL",
      "reason": "behavior divergence",
      "search_budget": {},
      "generators": "default",
      "typed": true,
      "domain_guard": null,
      "equivalence": "exact",
      "counterexample": {"args": "(100, 101)", "old": "0", "new": "-1"},
      "waived": false
    }
  ],
  "out_of_scope": [],
  "pending_waivers": []
}`
	if err := os.WriteFile(filepath.Join(a2dir, "verdict.json"), []byte(sampleVerdict), 0o644); err != nil {
		t.Fatal(err)
	}

	a := app.App{
		Repo:    repo,
		Version: "0.3.0-test",
		Now:     time.Now,
	}

	input := strings.Join([]string{
		`Content-Length: 88`,
		``,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05"}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"ping"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"a2dlc_status","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"a2dlc_verify","arguments":{"commit":"HEAD"}}}`,
		`{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"a2dlc_verify_run","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"a2dlc_verify_run","arguments":{"slice":"nonexistent"}}}`,
		`{"jsonrpc":"2.0","id":8,"method":"tools/call","params":{"name":"a2dlc_intent_from_ticket","arguments":{"slice_id":"jira.pay-104","goal":"Prevent negative discount totals in pricing","criticality":"high","invariants":["total >= 0"],"acceptance":["apply_discount(100, 101) == 0"],"max_cost_usd":1.5}}}`,
		`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"a2dlc_format_report","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"unknown_tool","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":11,"method":"unknown/method"}`,
		`{not-valid-json`,
	}, "\n") + "\n"

	var out bytes.Buffer
	if err := Serve(context.Background(), a, strings.NewReader(input), &out); err != nil {
		t.Fatalf("Serve failed: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 12 {
		t.Fatalf("expected 12 JSON-RPC responses (notification skipped), got %d:\n%s", len(lines), out.String())
	}

	// 1. Verify initialize response
	var initResp rpcResponse
	if err := json.Unmarshal([]byte(lines[0]), &initResp); err != nil {
		t.Fatal(err)
	}
	if initResp.Error != nil {
		t.Fatalf("unexpected init error: %+v", initResp.Error)
	}

	// 2. Verify tools/list response contains all 6 tools
	for _, toolName := range []string{"a2dlc_status", "a2dlc_check", "a2dlc_verify", "a2dlc_verify_run", "a2dlc_intent_from_ticket", "a2dlc_format_report"} {
		if !strings.Contains(lines[2], toolName) {
			t.Fatalf("tools/list missing expected tool %s: %s", toolName, lines[2])
		}
	}

	// 3. Verify a2dlc_intent_from_ticket wrote .a2dlc/intents/jira.pay-104.intent.json
	if !strings.Contains(lines[7], `jira.pay-104.intent.json`) {
		t.Fatalf("expected intent path in response: %s", lines[7])
	}
	if _, err := os.Stat(filepath.Join(repo.Dir, ".a2dlc", "intents", "jira.pay-104.intent.json")); err != nil {
		t.Fatalf("expected intent file on disk: %v", err)
	}

	// 4. Verify a2dlc_format_report generated Markdown table with counterexample
	if !strings.Contains(lines[8], `src/pricing.py::apply_discount`) || !strings.Contains(lines[8], `(100, 101)`) {
		t.Fatalf("expected formatted report with counterexample: %s", lines[8])
	}
}
