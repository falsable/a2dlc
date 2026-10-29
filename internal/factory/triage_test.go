package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/run"
)

// triageFake answers the triage calls (the ones whose slice starts with triage-) with the word its table says and hands
// every other launch to the lane fake, so one launcher serves a whole run the way the real one does.
type triageFake struct {
	lanes   *fakeClaude
	answers map[string]string // issue id -> what the model says; missing = build
	cost    *contract.Cost
	mu      sync.Mutex
	calls   []call
	workdir [][]string // what the triage call's /work held
}

func (f *triageFake) Launch(ctx context.Context, r contract.Role, s run.LaunchSpec) (run.LaunchResult, error) {
	var req contract.RoleRequest
	b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	if err := contract.DecodeStrictBytes(b, &req); err != nil {
		return run.LaunchResult{}, err
	}
	if !strings.HasPrefix(req.SliceID, "triage-") {
		return f.lanes.Launch(ctx, r, s)
	}
	issue, _, _ := strings.Cut(strings.TrimPrefix(req.SliceID, "triage-"), ".")
	answer, ok := f.answers[issue]
	if !ok {
		answer = "build"
	}
	entries, _ := os.ReadDir(s.WorkDir)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	f.mu.Lock()
	f.calls = append(f.calls, call{Role: s.Role, Env: r.Env})
	f.workdir = append(f.workdir, names)
	f.mu.Unlock()
	out, _ := json.Marshal(contract.BuilderResponse{Summary: answer, FilesChanged: []string{}})
	resp, _ := json.Marshal(contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
		Status: contract.RoleOK, Cost: f.cost, Payload: out})
	os.WriteFile(filepath.Join(s.OutDir, "response.json"), resp, 0o644)
	return run.LaunchResult{Argv: []string{"docker", "run", r.Image}, Network: run.NetworkNone}, nil
}

func triageSetup(t *testing.T, answers map[string]string) (Deps, *triageFake) {
	d := target(t)
	f := &triageFake{answers: answers, cost: &contract.Cost{USD: 0.01, InputTokens: 1200, OutputTokens: 4, Turns: 1},
		lanes: &fakeClaude{script: okCost(&contract.Cost{USD: 0.3, InputTokens: 1000, CacheReadInputTokens: 600, OutputTokens: 50, Turns: 4})}}
	d.Launcher = f
	return d, f
}

func triageBatch() contract.FactoryBatch {
	clear1, vague, clear3 := issue("issue-1"), issue("issue-2"), issue("issue-3")
	vague.Title, vague.Body = "Make it better", "It should be nicer somehow."
	return batchOf(clear1, vague, clear3)
}

// Arm 1: each issue gets one cheap call with the pinned model and no tools, a vague issue is triaged out at its recorded
// cost and gets no lane, and the report's usage and cost per accepted change count the triage.
func TestTriageSkipsAVagueIssueAndCountsItsCost(t *testing.T) {
	d, f := triageSetup(t, map[string]string{"issue-2": "triaged_out"})
	cfg := testConfig()
	cfg.Triage = &contract.FactoryTriage{Model: "haiku-pinned"}
	rep, err := RunBatch(context.Background(), d, cfg, triageBatch())
	if err != nil {
		t.Fatal(err)
	}
	l := lanesByIssue(rep)
	if l["issue-2"].Status != contract.LaneSkipped || !strings.Contains(l["issue-2"].Reason, "triaged out") || l["issue-2"].Usage.Calls != 0 {
		t.Fatalf("the vague issue: %+v", l["issue-2"])
	}
	if l["issue-1"].Status != contract.LaneAccepted || l["issue-3"].Status != contract.LaneAccepted || len(f.lanes.seen()) != 2 {
		t.Fatalf("lanes: %+v, lane launches %d", rep.Lanes, len(f.lanes.seen()))
	}
	tr := rep.Triage
	if tr == nil || tr.Model != "haiku-pinned" || len(tr.Issues) != 3 || tr.Usage.Calls != 3 || tr.Usage.InputTokens != 3600 || tr.Usage.OutputTokens != 12 || tr.Usage.Turns != 3 {
		t.Fatalf("triage: %+v", tr)
	}
	for _, it := range tr.Issues {
		want := contract.TriageBuild
		if it.IssueID == "issue-2" {
			want = contract.TriageTriagedOut
		}
		if it.Choice != want || it.Usage.USD != 0.01 || it.Usage.Calls != 1 {
			t.Errorf("%+v", it)
		}
	}
	// the run total is the lanes plus the triage, and the cost per accepted change is that total over the accepted
	if rep.Usage.USD < 0.629 || rep.Usage.USD > 0.631 || rep.Usage.Calls != 5 || rep.Accepted != 2 || rep.CostPerAcceptedUSD == nil || *rep.CostPerAcceptedUSD < 0.3149 || *rep.CostPerAcceptedUSD > 0.3151 {
		t.Fatalf("usage %+v, cost per accepted %v", rep.Usage, rep.CostPerAcceptedUSD)
	}
	// the call is the minimal arm's: the pinned model, no tools, and the issue text is all it has (an empty workdir)
	for i, c := range f.calls {
		cmd := c.Env[EnvLLMCmd]
		if c.Env["A2DLC_MODEL"] != "haiku-pinned" || !strings.Contains(cmd, "--tools ''") || strings.Contains(cmd, "Read") || !strings.Contains(cmd, "--max-budget-usd") {
			t.Errorf("call %d env: %v", i, c.Env)
		}
		if len(f.workdir[i]) != 0 {
			t.Errorf("call %d had files to read: %v", i, f.workdir[i])
		}
	}
	b, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "report.json"))
	var disk contract.FactoryReport
	if err != nil || contract.DecodeStrictBytes(b, &disk) != nil || disk.Validate() != nil || disk.Triage == nil {
		t.Fatalf("%v\n%s", err, b)
	}
}

// Arm 0 triages through its own launcher: default claude -p with the config's model, none of arm 1's flags.
func TestTriageOfTheControlArmUsesItsOwnLauncherAndModel(t *testing.T) {
	d, f := triageSetup(t, map[string]string{"issue-2": "triaged_out"})
	cfg := testConfig()
	cfg.Arm, cfg.Model, cfg.Triage = contract.ArmControl, "reference-model", &contract.FactoryTriage{}
	rep, err := RunBatch(context.Background(), d, cfg, triageBatch())
	if err != nil {
		t.Fatal(err)
	}
	if rep.Triage == nil || rep.Triage.Model != "reference-model" || len(f.calls) != 3 || len(f.lanes.seen()) != 2 {
		t.Fatalf("triage %+v, triage calls %d", rep.Triage, len(f.calls))
	}
	for i, c := range f.calls {
		cmd := c.Env[EnvLLMCmd]
		if c.Env["A2DLC_MODEL"] != "reference-model" || cmd != ControlCommand(cfg.Caps.LaneUSD) || strings.Contains(cmd, "--tools") || strings.Contains(cmd, "--setting-sources") {
			t.Errorf("call %d: %v", i, c.Env)
		}
	}
}

// An issue the triage cannot decide is built, with the reason; an issue without a done_command is not triaged and costs
// nothing; a run without triage in its config makes no triage call and has no triage in its report.
func TestTriageNeverHidesAnIssueAndSpendsNothingOnOneWithoutDoneCommand(t *testing.T) {
	d, f := triageSetup(t, map[string]string{"issue-1": "I think this one is probably fine, build it maybe"})
	cfg := testConfig()
	cfg.Triage = &contract.FactoryTriage{Model: "haiku-pinned"}
	noCmd := issue("issue-9")
	noCmd.DoneCommand = ""
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1"), noCmd))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 1 || len(rep.Triage.Issues) != 1 {
		t.Fatalf("triage calls %d: %+v", len(f.calls), rep.Triage)
	}
	if it := rep.Triage.Issues[0]; it.Choice != contract.TriageBuild || !strings.Contains(it.Reason, "unusable") {
		t.Errorf("%+v", it)
	}
	if lanesByIssue(rep)["issue-1"].Status != contract.LaneAccepted || lanesByIssue(rep)["issue-9"].Status != contract.LaneSkipped {
		t.Errorf("%+v", rep.Lanes)
	}
	d2, f2 := triageSetup(t, nil)
	rep2, err := RunBatch(context.Background(), d2, testConfig(), batchOf(issue("issue-1")))
	if err != nil || len(f2.calls) != 0 || rep2.Triage != nil {
		t.Fatalf("%v %d %+v", err, len(f2.calls), rep2.Triage)
	}
}

// Cost per accepted change over the whole pipeline: the report's usage already holds the lanes and the triage, and the
// integration's own calls are added on top.
func TestCostPerAcceptedSumsLanesTriageAndIntegration(t *testing.T) {
	rep := contract.FactoryReport{Usage: contract.Counters{USD: 0.63}, Triage: &contract.TriageReport{Usage: contract.Counters{USD: 0.03}}}
	rec := contract.FactoryIntegration{Integrated: 2, Usage: &contract.Counters{USD: 0.27}}
	if c := CostPerAccepted(rep, rec); c == nil || *c < 0.4499 || *c > 0.4501 {
		t.Fatalf("%v", c)
	}
}
