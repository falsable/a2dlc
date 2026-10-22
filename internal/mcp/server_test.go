package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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
	for _, toolName := range []string{"a2dlc_status", "a2dlc_check", "a2dlc_verify", "a2dlc_verify_run", "a2dlc_intent_from_ticket", "a2dlc_intent_from_signal", "a2dlc_format_report"} {
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

// The Stage 7 → Stage 0 loop: a sentinel alert becomes a bugfix intent that inherits what the alert must not
// decide (budget cap, invariants, boundary, delivery) from the alerted slice's intent, and is refused when
// nothing tells the threshold.
func TestIntentFromSignalInheritsBudgetInvariantsAndThreshold(t *testing.T) {
	repo := initTestRepo(t)
	a := app.App{Repo: repo}
	intents := filepath.Join(repo.Dir, ".a2dlc", "intents")
	if err := os.MkdirAll(intents, 0o755); err != nil {
		t.Fatal(err)
	}
	source := `{"schema_version": 1, "slice_id": "billing.ledger.v1", "mode": "greenfield",
  "goal": "Settle the ledger in integer cents", "invariants": ["sum(credits) == sum(debits)"],
  "non_goals": ["Reporting"], "criticality": "high",
  "budget": {"max_cost_usd": 1.5, "max_inner_iterations": 5, "max_tier1_adversarial_rounds": 3, "max_mutants": 30},
  "critical_paths": ["src/billing/**"], "acceptance": ["settle([]) == 0"],
  "boundary": {"forbidden_paths": ["src/auth/**"], "do_not_refactor": ["idempotency_key"]},
  "delivery": {"writes": false, "sentinel": {"error_rate_delta_max": 0.01, "latency_p99_ms_max": 250}}}`
	if err := os.WriteFile(filepath.Join(intents, "billing.ledger.v1.intent.json"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	call := func(id int, args string) string {
		input := strings.Join([]string{
			`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`,
			fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"a2dlc_intent_from_signal","arguments":%s}}`, id, args),
		}, "\n") + "\n"
		var out bytes.Buffer
		if err := Serve(context.Background(), a, strings.NewReader(input), &out); err != nil {
			t.Fatalf("Serve failed: %v", err)
		}
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		return lines[len(lines)-1]
	}

	// 1. Latency breach on ring 10 %: the threshold comes from the inherited sentinel, nothing else is decided by the alert.
	resp := call(2, `{"from_slice":"billing.ledger.v1","metric":"latency_p99_ms","observed":310,"ring":10}`)
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("unexpected error: %s", resp)
	}
	path := filepath.Join(intents, "billing.ledger.v1.signal-latency-p99-ms.intent.json")
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("intent not written: %v", err)
	}
	var got struct {
		ChangeKind  string   `json:"change_kind"`
		Criticality string   `json:"criticality"`
		Goal        string   `json:"goal"`
		Invariants  []string `json:"invariants"`
		Acceptance  []string `json:"acceptance"`
		Budget      struct {
			MaxCostUSD float64 `json:"max_cost_usd"`
		} `json:"budget"`
		Boundary struct {
			ForbiddenPaths []string `json:"forbidden_paths"`
		} `json:"boundary"`
		Delivery struct {
			Sentinel struct {
				LatencyP99MsMax int `json:"latency_p99_ms_max"`
			} `json:"sentinel"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	switch {
	case got.ChangeKind != "bugfix":
		t.Fatalf("change_kind = %q, want bugfix", got.ChangeKind)
	case got.Criticality != "high":
		t.Fatalf("criticality = %q, want the alerted slice's", got.Criticality)
	case got.Budget.MaxCostUSD != 1.5:
		t.Fatalf("budget cap = %v, want the inherited 1.5", got.Budget.MaxCostUSD)
	case !strings.Contains(got.Goal, "threshold 250"):
		t.Fatalf("goal does not carry the sentinel threshold: %s", got.Goal)
	case len(got.Invariants) != 1 || got.Invariants[0] != "sum(credits) == sum(debits)":
		t.Fatalf("invariants = %v, want the inherited one", got.Invariants)
	case len(got.Acceptance) != 2 || got.Acceptance[0] != "latency_p99_ms <= 250 on ring 10% after the fix":
		t.Fatalf("acceptance = %v", got.Acceptance)
	case len(got.Boundary.ForbiddenPaths) != 1 || got.Boundary.ForbiddenPaths[0] != "src/auth/**":
		t.Fatalf("boundary not inherited: %v", got.Boundary.ForbiddenPaths)
	case got.Delivery.Sentinel.LatencyP99MsMax != 250:
		t.Fatalf("delivery not inherited: %+v", got.Delivery)
	}
	if !strings.Contains(resp, "budget cap 1.50 USD") || !strings.Contains(resp, "a2dlc run --slice billing.ledger.v1.signal-latency-p99-ms") {
		t.Fatalf("response lacks the cap or the run command: %s", resp)
	}

	// 2. A violated runtime invariant is added to the inherited ones, and max_cost_usd can only be set explicitly.
	resp = call(3, `{"from_slice":"billing.ledger.v1","slice_id":"billing.ledger.v1.signal-inv","metric":"invariant","observed":3,"invariant":"balance >= 0","max_cost_usd":0.5}`)
	if strings.Contains(resp, `"isError":true`) {
		t.Fatalf("unexpected error: %s", resp)
	}
	b, err = os.ReadFile(filepath.Join(intents, "billing.ledger.v1.signal-inv.intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Invariants) != 2 || got.Invariants[1] != "balance >= 0" || got.Budget.MaxCostUSD != 0.5 {
		t.Fatalf("invariant not added or cap not applied: %v %v", got.Invariants, got.Budget.MaxCostUSD)
	}

	// 3. Without an intent to inherit from and without a threshold, the alert cannot start a run by itself.
	resp = call(4, `{"from_slice":"orphan.slice","metric":"error_rate_delta","observed":0.2}`)
	if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "threshold is required") {
		t.Fatalf("expected a threshold error: %s", resp)
	}
	if _, err := os.Stat(filepath.Join(intents, "orphan.slice.signal-error-rate-delta.intent.json")); err == nil {
		t.Fatal("a refused alert must not write an intent")
	}

	// 4. With the threshold given, an orphan alert gets the default cap and high criticality.
	resp = call(5, `{"from_slice":"orphan.slice","metric":"error_rate_delta","observed":0.2,"threshold":0.05}`)
	if strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "budget cap 2.00 USD") || !strings.Contains(resp, `inherited_from\": null`) {
		t.Fatalf("orphan alert: %s", resp)
	}

	// 5. What the alert cannot decide: raise the inherited cap, loosen the declared sentinel threshold, or overwrite an intent.
	resp = call(6, `{"from_slice":"billing.ledger.v1","slice_id":"billing.ledger.v1.signal-raise","metric":"latency_p99_ms","observed":310,"max_cost_usd":9}`)
	if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "cannot raise the budget") {
		t.Fatalf("expected the raise to be refused: %s", resp)
	}
	if _, err := os.Stat(filepath.Join(intents, "billing.ledger.v1.signal-raise.intent.json")); err == nil {
		t.Fatal("a refused raise must not write an intent")
	}
	resp = call(7, `{"from_slice":"billing.ledger.v1","slice_id":"billing.ledger.v1.signal-loose","metric":"latency_p99_ms","observed":310,"threshold":900}`)
	if strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, `payload_threshold_ignored\": true`) {
		t.Fatalf("the declared sentinel must win over the payload threshold: %s", resp)
	}
	b, err = os.ReadFile(filepath.Join(intents, "billing.ledger.v1.signal-loose.intent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Acceptance) == 0 || got.Acceptance[0] != "latency_p99_ms <= 250 on ring 1% after the fix" {
		t.Fatalf("acceptance must carry the declared 250, not the payload's 900: %v", got.Acceptance)
	}
	resp = call(8, `{"from_slice":"billing.ledger.v1","slice_id":"billing.ledger.v1.signal-inv","metric":"latency_p99_ms","observed":310}`)
	if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "never overwrites") {
		t.Fatalf("expected the overwrite to be refused: %s", resp)
	}
	resp = call(9, `{"from_slice":"billing.ledger.v1","slice_id":"billing.ledger.v1","metric":"latency_p99_ms","observed":310}`)
	if !strings.Contains(resp, `"isError":true`) || !strings.Contains(resp, "must differ from from_slice") {
		t.Fatalf("expected the self-overwrite to be refused: %s", resp)
	}
}
