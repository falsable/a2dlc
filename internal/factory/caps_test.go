package factory

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// rottenCost is a builder whose file never passes Tier 0, so the lane keeps asking for a new round until a cap stops it.
func rottenCost(c contract.Cost) func(int, context.Context) (*contract.Cost, bool, error) {
	return func(int, context.Context) (*contract.Cost, bool, error) { return &c, true, nil }
}

func TestLaneUSDCapStopsTheLaneAtTheCap(t *testing.T) {
	d := target(t)
	claude := &fakeClaude{script: rottenCost(contract.Cost{USD: 0.5, InputTokens: 10, OutputTokens: 1, Turns: 1})}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-1"]
	// 0.5 + 0.5 reaches the cap of 1: a cap exactly reached is spent, so there is no third call
	if l.Status != contract.LaneCapped || !strings.Contains(l.Reason, "lane usd") || len(claude.seen()) != 2 || l.Usage.USD != 1 || rep.Accepted != 0 {
		t.Fatalf("%+v calls=%d", l, len(claude.seen()))
	}
	if rep.Outcome != contract.RunCompleted || rep.CostPerAcceptedUSD != nil {
		t.Fatalf("%+v", rep)
	}
}

func TestLaneTurnsCapStopsTheLane(t *testing.T) {
	d := target(t)
	claude := &fakeClaude{script: rottenCost(contract.Cost{USD: 0.01, InputTokens: 10, OutputTokens: 1, Turns: 2})}
	d.Launcher = claude
	cfg := testConfig()
	cfg.Caps.LaneTurns = 3
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-1"]
	if l.Status != contract.LaneCapped || !strings.Contains(l.Reason, "lane turns") || len(claude.seen()) != 2 || l.Usage.Turns != 4 {
		t.Fatalf("%+v calls=%d", l, len(claude.seen()))
	}
}

func TestLaneWallCapStopsTheLane(t *testing.T) {
	d := target(t)
	d.Launcher = &fakeClaude{script: func(_ int, ctx context.Context) (*contract.Cost, bool, error) {
		<-ctx.Done()
		return nil, false, ctx.Err()
	}}
	cfg := testConfig()
	cfg.Caps.LaneSeconds = 1
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)["issue-1"]
	if l.Status != contract.LaneCapped || l.Reason != "lane wall time: 1 s" || rep.Outcome != contract.RunCompleted {
		t.Fatalf("%+v", l)
	}
}

func TestRunSoftStopLaunchesNoNewLanes(t *testing.T) {
	d := target(t)
	claude := &fakeClaude{script: okCost(&contract.Cost{USD: 0.6, InputTokens: 10, OutputTokens: 1, Turns: 1})}
	d.Launcher = claude
	cfg := testConfig()
	cfg.Caps.RunStopNewUSD = 0.5
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1"), issue("issue-2"), issue("issue-3")))
	if err != nil {
		t.Fatal(err)
	}
	by := lanesByIssue(rep)
	if by["issue-1"].Status != contract.LaneAccepted || rep.Outcome != contract.RunStoppedNewLanes || len(claude.seen()) != 1 {
		t.Fatalf("%+v calls=%d", rep, len(claude.seen()))
	}
	for _, id := range []string{"issue-2", "issue-3"} {
		if by[id].Status != contract.LaneSkipped || !strings.Contains(by[id].Reason, "run_stop_new_usd") || by[id].SliceID != nil {
			t.Fatalf("%s: %+v", id, by[id])
		}
	}
	if rep.Accepted != 1 || rep.CostPerAcceptedUSD == nil || *rep.CostPerAcceptedUSD != 0.6 {
		t.Fatalf("%+v", rep)
	}
}

func TestRunKillStopsEveryLaneInFlight(t *testing.T) {
	d := target(t)
	var inFlight sync.WaitGroup
	inFlight.Add(2)
	claude := &fakeClaude{script: func(n int, ctx context.Context) (*contract.Cost, bool, error) {
		inFlight.Done()
		inFlight.Wait() // both lanes have a call in flight before either answers
		if n == 0 {
			return &contract.Cost{USD: 0.9, InputTokens: 10, OutputTokens: 1, Turns: 1}, false, nil
		}
		<-ctx.Done()
		return nil, false, ctx.Err()
	}}
	d.Launcher = claude
	cfg := testConfig()
	cfg.Concurrency = 2
	cfg.Caps.LaneUSD, cfg.Caps.RunKillUSD, cfg.Caps.RunStopNewUSD = 0.9, 0.9, 0.9
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1"), issue("issue-2")))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outcome != contract.RunKilled || rep.Accepted != 0 {
		t.Fatalf("%+v", rep)
	}
	for _, l := range rep.Lanes {
		if l.Status != contract.LaneKilled || !strings.Contains(l.Reason, "run_kill_usd") {
			t.Fatalf("%+v", l)
		}
	}
}

func TestTotalCapCountsWhatEarlierRunsSpent(t *testing.T) {
	d := target(t)
	old := notRun("old-1", contract.LaneCapped, "lane usd")
	old.Usage = contract.Counters{USD: 24.9, Calls: 1}
	prior := contract.FactoryReport{SchemaVersion: 1, RunID: "run-aaaaaaaaaaaa", BatchID: "old.1", Lanes: []contract.FactoryLane{old},
		Usage: old.Usage, StartedAt: "2026-10-28T10:00:00Z", FinishedAt: "2026-10-28T11:00:00Z", Outcome: contract.RunCompleted,
		BaseSha: strings.Repeat("a", 40), ConfigSHA256: "sha256:" + strings.Repeat("a", 64)}
	if err := writeJSON(d.Out+"/run-aaaaaaaaaaaa/report.json", prior); err != nil {
		t.Fatal(err)
	}
	claude := &fakeClaude{script: okCost(&contract.Cost{USD: 0.2, InputTokens: 10, OutputTokens: 1, Turns: 1})}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	calls := claude.seen()
	// the call gets what is left under the total (25 - 24.9), not the lane's own 1
	if len(calls) != 1 || !strings.HasSuffix(calls[0].Env[EnvLLMCmd], "--max-budget-usd 0.1000") {
		t.Fatalf("%+v", calls)
	}
	if rep.PriorSpendUSD != 24.9 || rep.Outcome != contract.RunKilled || lanesByIssue(rep)["issue-1"].Status != contract.LaneKilled ||
		!strings.Contains(lanesByIssue(rep)["issue-1"].Reason, "total_usd") {
		t.Fatalf("%+v", rep)
	}
	// and a run that starts when the total is already spent launches nothing
	d2 := target(t)
	d2.Out = d.Out
	c2 := &fakeClaude{script: okCost(&contract.Cost{USD: 0.2})}
	d2.Launcher = c2
	rep2, err := RunBatch(context.Background(), d2, testConfig(), batchOf(issue("issue-1")))
	if err != nil || len(c2.seen()) != 0 || rep2.Outcome != contract.RunStoppedNewLanes || lanesByIssue(rep2)["issue-1"].Status != contract.LaneSkipped {
		t.Fatalf("%v %+v", err, rep2)
	}
}

func TestPriorSpendRefusesAReportItCannotRead(t *testing.T) {
	d := target(t)
	if err := writeJSON(d.Out+"/run-bbbbbbbbbbbb/report.json", map[string]any{"run_id": "x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := PriorSpend(d.Out); err == nil {
		t.Fatal("an unreadable report counted as zero spend")
	}
	d.Launcher = &fakeClaude{script: okCost(&contract.Cost{USD: 0.1})}
	if _, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1"))); err == nil {
		t.Fatal("the run started without knowing what was already spent")
	}
}
