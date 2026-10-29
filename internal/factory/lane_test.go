package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func okCost(cost *contract.Cost) func(int, context.Context) (*contract.Cost, bool, error) {
	return func(int, context.Context) (*contract.Cost, bool, error) { return cost, false, nil }
}

func TestAcceptedLaneReportsCostPerStageWithTheFourCounters(t *testing.T) {
	d := target(t)
	cost := &contract.Cost{USD: 0.3, InputTokens: 1000, CacheReadInputTokens: 600, CacheCreationInputTokens: 200, OutputTokens: 50, Turns: 4}
	claude := &fakeClaude{script: okCost(cost)}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-1"]
	if l.Status != contract.LaneAccepted || l.RunOutcome == nil || *l.RunOutcome != contract.PASS || rep.Accepted != 1 || rep.Outcome != contract.RunCompleted {
		t.Fatalf("%+v", rep)
	}
	if rep.CostPerAcceptedUSD == nil || *rep.CostPerAcceptedUSD != 0.3 {
		t.Fatalf("cost per accepted %v", rep.CostPerAcceptedUSD)
	}
	want := contract.Counters{USD: 0.3, Calls: 1, Turns: 4, InputTokens: 1000, CacheReadInputTokens: 600, CacheCreationInputTokens: 200, OutputTokens: 50}
	if l.Usage != want || rep.Usage != want {
		t.Fatalf("lane %+v run %+v", l.Usage, rep.Usage)
	}
	// the cost sits on the stage that spent it (the builder is Stage 3) and on no other
	for _, s := range l.Stages {
		if (s.Stage == "3") != (s.Counters == want) || (s.Stage != "3" && s.Counters != (contract.Counters{})) {
			t.Fatalf("stage %s: %+v", s.Stage, s)
		}
	}
	if l.Stages[0].Stage != "-1" || len(l.Stages) != 10 {
		t.Fatalf("stages %+v", l.Stages)
	}
	// only the builder runs: the saboteur is absent from roles.json, so it is never launched
	for _, c := range claude.seen() {
		if c.Role != Role {
			t.Fatalf("launched %q", c.Role)
		}
	}
	// the report on disk is the one returned, strictly decodable, and what a later run counts as prior spend
	b, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk contract.FactoryReport
	if err := contract.DecodeStrictBytes(b, &disk); err != nil || disk.Validate() != nil || disk.Usage != rep.Usage {
		t.Fatalf("%v %v", err, disk.Validate())
	}
	if prior, err := PriorSpend(d.Out); err != nil || prior != 0.3 {
		t.Fatalf("prior %v %v", prior, err)
	}
	// the lane's ledger outlives its worktree
	if _, err := os.Stat(filepath.Join(d.Out, rep.RunID, "ledgers", "issue-1.json")); err != nil {
		t.Fatal(err)
	}
}

func TestEveryLaunchCarriesTheMinimalClaudeFlagsAndTheRemainingBudget(t *testing.T) {
	d := target(t)
	claude := &fakeClaude{script: func(n int, _ context.Context) (*contract.Cost, bool, error) {
		return &contract.Cost{USD: 0.25, InputTokens: 10, OutputTokens: 1, Turns: 1}, n < 2, nil // two failing rounds, then a good one
	}}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	if lanesByIssue(rep)["issue-1"].Status != contract.LaneAccepted {
		t.Fatalf("%+v", rep.Lanes)
	}
	calls := claude.seen()
	if len(calls) != 3 {
		t.Fatalf("%d calls", len(calls))
	}
	for i, remaining := range []string{"1.0000", "0.7500", "0.5000"} {
		got := calls[i].Env[EnvLLMCmd]
		want := "claude --setting-sources '' --strict-mcp-config --disable-slash-commands --tools Read,Edit --exclude-dynamic-system-prompt-sections --max-budget-usd " + remaining
		if got != want || calls[i].Env["A2DLC_MODEL"] != "haiku" || strings.Contains(got, "--max-turns") {
			t.Fatalf("call %d: %q", i, got)
		}
	}
}

func TestIssueWithoutDoneCommandIsSkippedAndSpendsNothing(t *testing.T) {
	d := target(t)
	claude := &fakeClaude{script: okCost(&contract.Cost{USD: 0.3})}
	d.Launcher = claude
	empty := issue("issue-2")
	empty.DoneCommand = "  "
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(empty))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-2"]
	if l.Status != contract.LaneSkipped || l.Reason != "no done_command: not a ticket" || l.SliceID != nil || len(claude.seen()) != 0 || rep.Usage.USD != 0 {
		t.Fatalf("%+v %d", l, len(claude.seen()))
	}
	if rep.CostPerAcceptedUSD != nil || rep.Accepted != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestLaneBranchAndSliceNamesAreStable(t *testing.T) {
	if got := sliceID("issue-3", "run-0a1b2c3d4e5f"); got != "issue-3.0a1b2c" {
		t.Fatal(got)
	}
	if got := Precheck(issue("x")); got != "" {
		t.Fatal(got)
	}
}
