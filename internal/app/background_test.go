package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// spawned counts the background checks the Stop hook started; no test of this package starts a real one.
var spawned int

func init() {
	spawnBackground = func(string, string) error { spawned++; return nil }
}

func stopHook(t *testing.T, a App, session string) map[string]string {
	t.Helper()
	var out, errb bytes.Buffer
	in := `{"session_id":"` + session + `","hook_event_name":"Stop"}`
	if code := a.Hook(context.Background(), "claude", strings.NewReader(in), &out, &errb); code != 0 {
		t.Fatalf("stop hook exit %d: %s", code, errb.String())
	}
	var m map[string]string
	if out.Len() > 0 {
		if err := json.Unmarshal(out.Bytes(), &m); err != nil {
			t.Fatalf("stop hook output %q: %v", out.String(), err)
		}
	}
	return m
}

// runBackground does what the detached `a2dlc check --background` would: a level 1 check into background.json.
func runBackground(t *testing.T, a App) contract.Verdict {
	t.Helper()
	v, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{ReportPath: BackgroundReport()})
	a.BackgroundDone()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// The first stop on a tree starts the background check and lets the agent stop; the receipt verify reads is untouched.
func TestStopStartsTheBackgroundCheckOnce(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	spawned = 0
	if m := stopHook(t, a, "s1"); m["decision"] == "block" {
		t.Fatalf("the first stop blocked: %v", m)
	}
	if m := stopHook(t, a, "s1"); m["decision"] == "block" {
		t.Fatalf("a stop while the check runs blocked: %v", m)
	}
	if spawned != 1 {
		t.Fatalf("background checks started: %d, want 1 (the second stop found it running)", spawned)
	}
	if _, err := os.Stat(a.path(pathVerdict)); err == nil {
		t.Fatal("the hook wrote .a2dlc/verdict.json")
	}
}

// Once the background answer for the current tree exists and is not PASS, the next stop hands the agent what it can fix,
// once per tree.
func TestStopHandsBackTheFindingsOfTheCurrentTree(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	stopHook(t, a, "s2")
	if v := runBackground(t, a); v.Verdict != contract.FAIL {
		t.Fatalf("background verdict %s", v.Verdict)
	}
	if _, err := os.Stat(a.path(pathVerdict)); err == nil {
		t.Fatal("the background check wrote .a2dlc/verdict.json")
	}
	m := stopHook(t, a, "s2")
	if m["decision"] != "block" || !strings.Contains(m["reason"], "src/lib/f.ts::f") || !strings.Contains(m["reason"], "behavior changed") {
		t.Fatalf("the findings were not handed back: %v", m)
	}
	if m := stopHook(t, a, "s2"); m["decision"] == "block" {
		t.Fatalf("the same tree was handed back twice: %v", m)
	}
}

// An answer about an older tree is not shown: the agent has changed the code since, and a new check starts.
func TestStopIgnoresAnAnswerForAnotherTree(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	runBackground(t, a)
	write(t, dir, "src/lib/f.ts", "export const f = 3\n")
	spawned = 0
	if m := stopHook(t, a, "s3"); m["decision"] == "block" {
		t.Fatalf("an answer about another tree was handed back: %v", m)
	}
	if spawned != 1 {
		t.Fatalf("no new background check for the new tree: %d", spawned)
	}
}

// agent_hook.max_blocks bounds how often one session is sent back, whatever the agent does.
func TestStopBlocksAtMostMaxBlocksPerSession(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"critical_code":["src/lib/**"],"agent_hook":{"max_blocks":1}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "max_blocks 1")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	runBackground(t, a)
	if m := stopHook(t, a, "s4"); m["decision"] != "block" {
		t.Fatalf("first finding not handed back: %v", m)
	}
	write(t, dir, "src/lib/f.ts", "export const f = 3\n")
	runBackground(t, a)
	if m := stopHook(t, a, "s4"); m["decision"] == "block" {
		t.Fatalf("blocked past max_blocks: %v", m)
	}
	if m := stopHook(t, a, "s5"); m["decision"] != "block" {
		t.Fatalf("a new session starts its own count: %v", m)
	}
}

// A PASS, a finding only a person can resolve, or A2DLC_BACKGROUND=off never block.
func TestStopDoesNotBlockWithoutWorkForTheAgent(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "README.md", "changed\n")
	runBackground(t, a)
	if m := stopHook(t, a, "s6"); m["decision"] == "block" {
		t.Fatalf("a PASS blocked: %v", m)
	}
	pending := contract.Verdict{Verdict: contract.INCONCLUSIVE, PendingWaivers: []string{"src/lib/f.ts::f"},
		Targets: []contract.VerdictTarget{{TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: contract.FAIL,
			Counterexample: &contract.Counterexample{Args: "[]", Old: "1", New: "2"}}, Waived: true}}}
	if items := feedbackItems(pending); len(items) != 0 {
		t.Fatalf("a waived target waiting for a person's signature was handed to the agent: %v", items)
	}
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	runBackground(t, a)
	t.Setenv("A2DLC_BACKGROUND", "off")
	if m := stopHook(t, a, "s7"); m["decision"] == "block" {
		t.Fatalf("A2DLC_BACKGROUND=off still blocked: %v", m)
	}
}

func TestFeedbackItemsNameUncoveredLines(t *testing.T) {
	v := contract.Verdict{Verdict: contract.INCONCLUSIVE, Coverage: &contract.Coverage{Status: contract.CoverageRan,
		Files: []contract.CoverageFile{{File: "src/a.py", Uncovered: []int{3, 4, 5, 9}}}}}
	items := feedbackItems(v)
	if len(items) != 1 || !strings.Contains(items[0], "src/a.py") || !strings.Contains(items[0], "3-5") {
		t.Fatalf("items=%v", items)
	}
}
