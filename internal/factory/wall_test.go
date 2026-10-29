package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func wallConfig(seconds int) contract.FactoryConfig {
	c := testConfig()
	c.Caps.RunWallSeconds = &seconds
	return c
}

// Past caps.run_wall_seconds no new lane starts, the lane in flight is killed, the report says the run was killed by
// run wall time with the seconds that had passed, and the lane that was already accepted keeps its cost.
func TestRunWallTimeKillsTheRunAndKeepsWhatWasAccepted(t *testing.T) {
	d := target(t)
	claude := &fakeClaude{script: func(n int, ctx context.Context) (*contract.Cost, bool, error) {
		if n == 0 {
			return &contract.Cost{USD: 0.2, InputTokens: 10, OutputTokens: 1, Turns: 1}, false, nil
		}
		<-ctx.Done() // the second lane's builder never answers
		return nil, false, ctx.Err()
	}}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, wallConfig(3), batchOf(issue("issue-1"), issue("issue-2"), issue("issue-3")))
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)
	if l["issue-1"].Status != contract.LaneAccepted || l["issue-1"].Usage.USD != 0.2 || rep.Accepted != 1 || rep.Usage.USD != 0.2 {
		t.Fatalf("the accepted lane lost its cost: %+v", rep)
	}
	for _, id := range []string{"issue-2", "issue-3"} {
		if l[id].Status != contract.LaneKilled || !strings.Contains(l[id].Reason, "run wall time") {
			t.Errorf("%s: %+v", id, l[id])
		}
	}
	if len(claude.seen()) != 2 {
		t.Errorf("the third lane started: %d launches", len(claude.seen()))
	}
	k := rep.KilledBy
	if rep.Outcome != contract.RunKilled || k == nil || k.Cap != contract.KillRunWallSeconds || k.ElapsedSeconds < 3 || k.ElapsedSeconds > 30 || !strings.Contains(k.Reason, "run_wall_seconds 3") {
		t.Fatalf("outcome %s, killed_by %+v", rep.Outcome, k)
	}
	b, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "report.json"))
	var disk contract.FactoryReport
	if err != nil || contract.DecodeStrictBytes(b, &disk) != nil || disk.Validate() != nil || disk.KilledBy == nil || disk.KilledBy.Cap != contract.KillRunWallSeconds {
		t.Fatalf("%v\n%s", err, b)
	}
}

// A run inside its wall time, or with no wall cap at all, is not killed and the report has no killed_by.
func TestRunWithinItsWallTimeOrWithoutACapIsNotKilled(t *testing.T) {
	for name, cfg := range map[string]contract.FactoryConfig{"cap not reached": wallConfig(600), "no cap": testConfig()} {
		d := target(t)
		d.Launcher = &fakeClaude{script: okCost(&contract.Cost{USD: 0.1, InputTokens: 10, OutputTokens: 1, Turns: 1})}
		rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1")))
		if err != nil || rep.Outcome != contract.RunCompleted || rep.KilledBy != nil || rep.Accepted != 1 {
			t.Errorf("%s: %v %+v", name, err, rep)
		}
	}
}

// A kill by spend is recorded the same way, with its own cap.
func TestRunKilledBySpendSaysWhichCap(t *testing.T) {
	d := target(t)
	d.Launcher = &fakeClaude{script: okCost(&contract.Cost{USD: 0.6, InputTokens: 10, OutputTokens: 1, Turns: 1})}
	cfg := testConfig()
	cfg.Caps.LaneUSD, cfg.Caps.RunStopNewUSD, cfg.Caps.RunKillUSD = 0.5, 0.5, 0.5
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Outcome != contract.RunKilled || rep.KilledBy == nil || rep.KilledBy.Cap != contract.KillRunUSD {
		t.Fatalf("%s %+v", rep.Outcome, rep.KilledBy)
	}
}
