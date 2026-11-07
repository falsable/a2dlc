package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/mcp"
)

// The test binary is its own a2dlc: with A2DLC_TEST_AS_CLI=1 it runs the CLI and exits, which is how the MCP tools (that
// launch mcp.Exe) and the reference side of each comparison reach the same code without building anything.
func TestMain(m *testing.M) {
	if os.Getenv("A2DLC_TEST_AS_CLI") == "1" {
		os.Exit(run(os.Args[1:]))
	}
	os.Exit(m.Run())
}

func agentRepo(t *testing.T) (app.App, string) {
	t.Helper()
	t.Setenv("A2DLC_TEST_AS_CLI", "1")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	old := mcp.Exe
	mcp.Exe = func() (string, error) { return exe, nil }
	t.Cleanup(func() { mcp.Exe = old })
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "t@example.com"}, {"config", "user.name", "t"},
		{"commit", "-q", "--allow-empty", "-m", "init"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return app.App{Repo: repo}, exe
}

type cliSide struct {
	ExitCode int             `json:"exit_code"`
	Stdout   string          `json:"stdout"`
	Stderr   string          `json:"stderr"`
	JSON     json.RawMessage `json:"json"`
}

// cliRun is the CLI as a person runs it: in the repository, stdin empty.
func cliRun(t *testing.T, exe, dir string, args ...string) cliSide {
	t.Helper()
	c := exec.Command(exe, args...)
	c.Dir = dir
	var out, errb bytes.Buffer
	c.Stdout, c.Stderr = &out, &errb
	res := cliSide{}
	if err := c.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		res.ExitCode = ee.ExitCode()
	}
	res.Stdout, res.Stderr = out.String(), errb.String()
	return res
}

type toolReply struct {
	IsError bool
	Text    string
}

func callTool(t *testing.T, a app.App, name string, args map[string]any) toolReply {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
	var out bytes.Buffer
	if err := mcp.Serve(context.Background(), a, strings.NewReader(string(b)+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil || len(resp.Result.Content) != 1 {
		t.Fatalf("mcp answer for %s: %v %s", name, err, out.String())
	}
	return toolReply{IsError: resp.Result.IsError, Text: resp.Result.Content[0].Text}
}

// sameAsCLI calls the tool and the CLI with the same arguments on the same repository: the tool's stdout, stderr and exit
// code are the CLI's, and isError says the exit code is not 0.
func sameAsCLI(t *testing.T, a app.App, exe, tool string, args map[string]any, wantExit int, cli ...string) cliSide {
	t.Helper()
	want := cliRun(t, exe, a.Repo.Dir, cli...)
	if want.ExitCode != wantExit {
		t.Fatalf("%s: the CLI exited %d, the case expects %d; stderr %q", tool, want.ExitCode, wantExit, want.Stderr)
	}
	r := callTool(t, a, tool, args)
	var got cliSide
	if err := json.Unmarshal([]byte(r.Text), &got); err != nil {
		t.Fatalf("%s: the result is not the tool's document: %v\n%s", tool, err, r.Text)
	}
	if got.ExitCode != want.ExitCode || got.Stdout != want.Stdout || got.Stderr != want.Stderr {
		t.Errorf("%s differs from the CLI:\ntool: %+v\ncli:  %+v", tool, got, want)
	}
	if r.IsError != (want.ExitCode != 0) {
		t.Errorf("%s: isError %v with exit code %d", tool, r.IsError, want.ExitCode)
	}
	if strings.HasPrefix(strings.TrimSpace(want.Stdout), "{") && len(got.JSON) == 0 {
		t.Errorf("%s: stdout is a JSON document and the result carries no json", tool)
	}
	return got
}

func TestAgentToolsAgreeWithTheCLI(t *testing.T) {
	a, exe := agentRepo(t)

	// run and plan run: a repository with no config, intent or plan fails the same way for the tool and for the CLI.
	run := sameAsCLI(t, a, exe, "a2dlc_run", map[string]any{"slice": "s1", "max_cost_usd": 2}, 2,
		"run", "--slice=s1", "--max-cost-usd=2", "--json")
	if run.Stderr == "" {
		t.Error("a failed run says why on stderr")
	}
	sameAsCLI(t, a, exe, "a2dlc_plan_run", map[string]any{"plan": "no-plan.json", "intent": "no-intent.json", "max_cost_usd": 1.5}, 64,
		"plan", "run", "--plan=no-plan.json", "--intent=no-intent.json", "--max-cost-usd=1.5", "--json")

	// approve --pending lists, signs nothing, asks nothing
	sameAsCLI(t, a, exe, "a2dlc_approve_pending", map[string]any{}, 1, "approve", "--pending", "--json")

	// answer: bad usage reaches the CLI's check (the tool needs every argument first), then a run that cannot take it,
	// then the answer written
	full := map[string]any{"slice": "pago.v1", "probe": "esc-1", "choice": "alpha", "reason": "the spec is strict", "by": "aldemar"}
	cli := []string{"answer", "--slice=pago.v1", "--probe=esc-1", "--choice=alpha", "--reason=the spec is strict", "--by=aldemar"}
	sameAsCLI(t, a, exe, "a2dlc_answer", full, 2, cli...)

	runDir := filepath.Join(a.Repo.Dir, ".a2dlc", "run", "pago.v1")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile("../../contracts/examples/valid/run-state__stage4.json")
	if err != nil {
		t.Fatal(err)
	}
	state = bytes.ReplaceAll(bytes.ReplaceAll(state, []byte("run-3f9a1c2b7d4e"), []byte("run-000000000000")), []byte("core.relational.as_set.v1"), []byte("pago.v1"))
	item := `{"id":"esc-1","kind":"test_without_teeth","target":"tests/test_calc.py::tests.test_calc::test_media_vacia","summary":"s","evidence":{"base":"passed","candidate":"passed"},"resolve_with":["characterization_test"]}`
	for name, b := range map[string][]byte{"state.json": state, "escalations.json": []byte(`{"schema_version":1,"tree_hash":"git-tree:4b825dc642cb6eb9a060e54bf8d69288fbee4904","created_at":"2026-10-16T12:00:00Z","items":[` + item + `]}`)} {
		if err := os.WriteFile(filepath.Join(runDir, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got := sameAsCLI(t, a, exe, "a2dlc_answer", full, 0, cli...)
	if !strings.Contains(got.Stdout, "recorded in") {
		t.Errorf("a written answer says where: %q", got.Stdout)
	}
	if _, err := os.Stat(filepath.Join(runDir, "answers.json")); err != nil {
		t.Fatalf("answers.json not written: %v", err)
	}
}

// A tool never runs without a budget, never takes a question it cannot ask, and says why in a machine-readable error.
func TestAgentToolsRefuseWhatTheyCannotDo(t *testing.T) {
	a, _ := agentRepo(t)
	cases := []struct {
		tool string
		args map[string]any
		code string
	}{
		{"a2dlc_run", map[string]any{"slice": "s1"}, "max_cost_usd_required"},
		{"a2dlc_plan_run", map[string]any{"plan": "p.json", "intent": "i.json"}, "max_cost_usd_required"},
		{"a2dlc_run", map[string]any{"slice": "s1", "max_cost_usd": 0}, "max_cost_usd_invalid"},
		{"a2dlc_run", map[string]any{"slice": "s1", "max_cost_usd": -3}, "max_cost_usd_invalid"},
		{"a2dlc_plan_run", map[string]any{"plan": "p.json", "intent": "i.json", "max_cost_usd": "5"}, "max_cost_usd_invalid"},
		{"a2dlc_run", map[string]any{"max_cost_usd": 1}, "missing_argument"},
		{"a2dlc_run", map[string]any{"slice": "s1", "max_cost_usd": 1, "max_rounds": 1.5}, "invalid_argument"},
		{"a2dlc_run", map[string]any{"slice": "s1", "max_cost_usd": 1, "max_cost": 1}, "unknown_argument"},
		{"a2dlc_answer", map[string]any{"slice": "s", "probe": "p", "choice": "c", "reason": "r"}, "missing_argument"},
		{"a2dlc_approve_pending", map[string]any{"key": "~/.ssh/id_ed25519"}, "signing_requires_a_person"},
		{"a2dlc_approve_pending", map[string]any{"yes": true}, "signing_requires_a_person"},
	}
	for _, c := range cases {
		r := callTool(t, a, c.tool, c.args)
		var doc struct {
			Error struct {
				Code     string `json:"code"`
				Message  string `json:"message"`
				ExitCode int    `json:"exit_code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(r.Text), &doc); err != nil {
			t.Fatalf("%s %v: %v\n%s", c.tool, c.args, err, r.Text)
		}
		if !r.IsError || doc.Error.Code != c.code || doc.Error.ExitCode != 64 || doc.Error.Message == "" {
			t.Errorf("%s %v: isError %v, error %+v, want code %s", c.tool, c.args, r.IsError, doc.Error, c.code)
		}
	}
}
