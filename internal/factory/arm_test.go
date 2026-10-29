package factory

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func controlConfig() contract.FactoryConfig {
	cfg := testConfig()
	cfg.Arm = contract.ArmControl
	return cfg
}

func TestControlCommandIsDefaultClaudeWithOnlyTheSharedBudgetCap(t *testing.T) {
	if got := ControlCommand(0.75); got != "claude --max-budget-usd 0.7500" {
		t.Fatalf("%q", got)
	}
	for _, flag := range []string{"--setting-sources", "--strict-mcp-config", "--disable-slash-commands", "--tools", "--exclude-dynamic-system-prompt-sections", "--max-turns"} {
		if strings.Contains(Command(controlConfig(), 1), flag) {
			t.Errorf("arm 0 passes %s", flag)
		}
	}
}

// A config without an arm is arm 1: its command, and so the hash of every existing config and the claude tests, do not
// change.
func TestNoArmIsArm1ByteForByte(t *testing.T) {
	cfg := testConfig()
	if cfg.Arm != "" || Command(cfg, 0.5) != ClaudeCommand(cfg, 0.5) {
		t.Fatalf("%q", Command(cfg, 0.5))
	}
	cfg.Arm = contract.ArmMinimal
	if Command(cfg, 0.5) != ClaudeCommand(cfg, 0.5) {
		t.Fatalf("%q", Command(cfg, 0.5))
	}
	cfg.Arm = "0"
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "arm") {
		t.Fatalf("%v", err)
	}
}

// Both call sites that set A2DLC_LLM_CMD follow the arm: the roles.json the lane is written with, and each launch,
// which replaces it.
func TestArm0SetsTheCommandInRolesAndInEveryLaunch(t *testing.T) {
	cfg := controlConfig()
	if got := LaneRoles(cfg).Roles[Role].Env[EnvLLMCmd]; got != "claude --max-budget-usd 1.0000" {
		t.Fatalf("roles.json: %q", got)
	}
	d := target(t)
	claude := &fakeClaude{script: func(n int, _ context.Context) (*contract.Cost, bool, error) {
		return &contract.Cost{USD: 0.25, InputTokens: 10, OutputTokens: 1, Turns: 1}, n < 2, nil
	}}
	d.Launcher = claude
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issue("issue-1")))
	if err != nil {
		t.Fatal(err)
	}
	if lanesByIssue(rep)["issue-1"].Status != contract.LaneAccepted {
		t.Fatalf("%+v", rep.Lanes)
	}
	for i, remaining := range []string{"1.0000", "0.7500", "0.5000"} {
		got := claude.seen()[i].Env[EnvLLMCmd]
		if got != "claude --max-budget-usd "+remaining || claude.seen()[i].Env["A2DLC_MODEL"] != "haiku" {
			t.Fatalf("call %d: %q", i, got)
		}
	}
}

// laneView is what a lane decided, without what it spent: the scoring path reads these and nothing about the arm.
type laneView struct {
	Status, Reason, RunOutcome string
	Stages                     []string
	Accepted                   bool
}

func viewOf(rep contract.FactoryReport) map[string]laneView {
	m := map[string]laneView{}
	for _, l := range rep.Lanes {
		v := laneView{Status: l.Status, Reason: l.Reason, Accepted: l.CandidateRef != nil}
		if l.RunOutcome != nil {
			v.RunOutcome = *l.RunOutcome
		}
		for _, s := range l.Stages {
			v.Stages = append(v.Stages, s.Stage+"="+s.Status)
		}
		m[l.IssueID] = v
	}
	return m
}

// envWithoutCommand is each launch's environment but for the one variable the arm sets, sorted so that the order lanes
// ran in does not matter.
func envWithoutCommand(calls []call) []string {
	var out []string
	for _, c := range calls {
		env := map[string]string{}
		for k, v := range c.Env {
			if k != EnvLLMCmd {
				env[k] = v
			}
		}
		b, _ := json.Marshal(struct {
			Role string
			Env  map[string]string
		}{c.Role, env})
		out = append(out, string(b))
	}
	sort.Strings(out)
	return out
}

func equalViews(a, b map[string]laneView) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

// armRun is one batch under one arm: what its lanes decided, the launches' environment but for the arm's command, and
// the commands themselves.
type armRun struct {
	rep   contract.FactoryReport
	views map[string]laneView
	envs  []string
	cmds  []string
}

func runUnderArm(t *testing.T, arm string, d Deps, concurrency int, edit func(string), issues ...contract.FactoryIssue) armRun {
	t.Helper()
	claude := &fakeClaude{script: spend(), edit: edit}
	d.Launcher = claude
	cfg := testConfig()
	cfg.Arm, cfg.Concurrency = arm, concurrency
	rep, err := RunBatch(context.Background(), d, cfg, batchOf(issues...))
	if err != nil {
		t.Fatal(err)
	}
	r := armRun{rep: rep, views: viewOf(rep), envs: envWithoutCommand(claude.seen())}
	for _, c := range claude.seen() {
		r.cmds = append(r.cmds, c.Env[EnvLLMCmd])
	}
	return r
}

func sameLanePath(t *testing.T, one, zero armRun, launches int) {
	t.Helper()
	if !equalViews(one.views, zero.views) {
		t.Fatalf("lanes differ between arms:\n arm 1: %+v\n arm 0: %+v", one.views, zero.views)
	}
	if strings.Join(one.envs, "\n") != strings.Join(zero.envs, "\n") || len(one.envs) != launches {
		t.Fatalf("launches differ in more than the command:\n%v\n%v", one.envs, zero.envs)
	}
	for _, c := range one.cmds {
		if !strings.Contains(c, "--tools") {
			t.Fatalf("arm 1 launched %q", c)
		}
	}
	for _, c := range zero.cmds {
		if !strings.HasPrefix(c, "claude --max-budget-usd ") || strings.Contains(c, "--tools") {
			t.Fatalf("arm 0 launched %q", c)
		}
	}
	// the report has the same shape and its cost per accepted change is computed the same way
	for _, r := range []armRun{one, zero} {
		rep := r.rep
		if rep.Validate() != nil || rep.CostPerAcceptedUSD == nil || *rep.CostPerAcceptedUSD != rep.Usage.USD/float64(rep.Accepted) {
			t.Fatalf("%+v", rep)
		}
	}
}

// The same batch under both arms goes through the same lane, status and report code: what differs between the runs is
// the claude command of each launch, and nothing the scoring reads. Two lanes at once, so -race sees concurrent
// launches.
func TestBothArmsGoThroughTheSameLanePathAndOnlyTheCommandDiffers(t *testing.T) {
	one := runUnderArm(t, contract.ArmMinimal, target(t), 2, nil, issue("issue-1"), issue("issue-2"))
	zero := runUnderArm(t, contract.ArmControl, target(t), 2, nil, issue("issue-1"), issue("issue-2"))
	for _, id := range []string{"issue-1", "issue-2"} {
		if v := zero.views[id]; v.Status != contract.LaneAccepted || v.RunOutcome != contract.PASS || !v.Accepted {
			t.Fatalf("%s: %+v", id, v)
		}
	}
	if zero.rep.Accepted != 2 {
		t.Fatalf("%+v", zero.rep)
	}
	sameLanePath(t, one, zero, 2)
}

// The base-test guard is the lane's, not the arm's: arm 0 gets no more room to edit an existing test than arm 1.
func TestBothArmsRejectALaneThatEditsABaseTest(t *testing.T) {
	edit := func(work string) {
		os.WriteFile(filepath.Join(work, "tests", "test_f.py"), []byte("def test_f():\n    assert True\n"), 0o644)
	}
	one := runUnderArm(t, contract.ArmMinimal, targetWithTests(t), 1, edit, issue("issue-1"))
	zero := runUnderArm(t, contract.ArmControl, targetWithTests(t), 1, edit, issue("issue-1"))
	if v := zero.views["issue-1"]; v.Status != contract.LaneRejected || !strings.Contains(v.Reason, "tests/test_f.py (modified)") || v.Accepted {
		t.Fatalf("%+v", v)
	}
	if !equalViews(one.views, zero.views) || zero.rep.Accepted != 0 || zero.rep.CostPerAcceptedUSD != nil || zero.rep.Usage.USD != 0.3 {
		t.Fatalf("%+v %+v", one.views, zero.rep)
	}
}
