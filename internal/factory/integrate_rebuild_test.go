package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// rebuildRun builds two lanes that rewrite the same line of src/f.py (the fake builder writes x + <launch number>), so
// the second does not apply on the first, and integrates them with the rebuild on.
func rebuildRun(t *testing.T, d Deps, claude *fakeClaude, cfg contract.FactoryConfig, hold *fakeHoldout) (contract.FactoryReport, contract.FactoryIntegration) {
	t.Helper()
	d.Launcher = claude
	batch := batchOf(issue("issue-1"), issue("issue-2"))
	rep, err := RunBatch(context.Background(), d, cfg, batch)
	if err != nil || rep.Accepted != 2 {
		t.Fatalf("%v %+v", err, rep.Lanes)
	}
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	deps := integrateDeps(d, hold)
	deps.Rebuild = &RebuildDeps{Config: cfg, Batch: batch}
	rec, err := Integrate(context.Background(), deps, holdoutSpec(dir, seal, "issue-1", "issue-2"), rep)
	if err != nil {
		t.Fatal(err)
	}
	return rep, rec
}

func cheapClaude() *fakeClaude {
	return &fakeClaude{script: okCost(&contract.Cost{USD: 0.3, InputTokens: 1000, CacheReadInputTokens: 600, OutputTokens: 50, Turns: 4})}
}

// A lane that does not apply is built once more from its issue, on the integration head, through its arm's own launcher,
// and lands; its spend is in the record and in cost per accepted change, and main does not move. Both arms do the same.
func TestConflictedLaneIsRebuiltOnTheIntegrationHeadAndLands(t *testing.T) {
	for name, arm := range map[string]string{"arm 1 minimal": contract.ArmMinimal, "arm 0 control": contract.ArmControl} {
		d := target(t)
		mainBefore := git(t, d.Repo.Dir, "rev-parse", "main")
		cfg := testConfig()
		cfg.Arm = arm
		claude := cheapClaude()
		rep, rec := rebuildRun(t, d, claude, cfg, &fakeHoldout{})
		l := laneByIssue(rec)["issue-2"]
		if l.Status != contract.IntegrationIntegrated || l.Rebuild == nil || l.Rebuild.Outcome != contract.LaneAccepted || l.Rebuild.CandidateRef == nil || rec.Integrated != 2 {
			t.Fatalf("%s: %+v", name, rec.Lanes)
		}
		if len(l.Conflicts) != 1 || l.Conflicts[0] != "src/f.py" || laneByIssue(rec)["issue-1"].Rebuild != nil {
			t.Errorf("%s: %+v", name, rec.Lanes)
		}
		want := contract.Counters{USD: 0.3, Calls: 1, Turns: 4, InputTokens: 1000, CacheReadInputTokens: 600, OutputTokens: 50}
		if l.Rebuild.Usage != want || rec.Usage == nil || *rec.Usage != want || l.Rebuild.WallSeconds <= 0 {
			t.Errorf("%s: rebuild usage %+v, record usage %+v", name, l.Rebuild.Usage, rec.Usage)
		}
		// the rebuild is a third launch, built on the head that has lane 1's edit (x + 1): the fake writes x + 3
		calls := claude.seen()
		if len(calls) != 3 {
			t.Fatalf("%s: %d launches", name, len(calls))
		}
		cmd := calls[2].Env[EnvLLMCmd]
		if (arm == contract.ArmControl) != (cmd == ControlCommand(cfg.Caps.LaneUSD) || strings.HasPrefix(cmd, "claude --disallowedTools")) || (arm == contract.ArmMinimal) != strings.Contains(cmd, "--tools") {
			t.Errorf("%s: the rebuild did not go through the arm's launcher: %q", name, cmd)
		}
		if f := git(t, d.Repo.Dir, "show", rec.HeadSha+":src/f.py"); !strings.Contains(f, "x + 3") {
			t.Errorf("%s: f.py %q", name, f)
		}
		// 0.6 for the lanes and 0.3 for the rebuild over the 2 accepted
		if c := CostPerAccepted(rep, rec); c == nil || *c < 0.4499 || *c > 0.4501 {
			t.Errorf("%s: cost per accepted %v", name, c)
		}
		if git(t, d.Repo.Dir, "rev-parse", "main") != mainBefore || rec.MainShaAfter != mainBefore {
			t.Errorf("%s: main moved", name)
		}
		if _, err := os.Stat(filepath.Join(d.Out, rep.RunID, "ledgers", "issue-2-rb.json")); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The rebuilt lane goes through the same sealed holdout. When it fails there it is dropped, not rebuilt again, and the
// rebuild's spend is still counted.
func TestRebuiltLaneThatFailsTheHoldoutIsDroppedAfterOneRebuild(t *testing.T) {
	d := target(t)
	claude := cheapClaude()
	hold := &fakeHoldout{exit: func(tree string, _ []string) int {
		if b, _ := os.ReadFile(filepath.Join(tree, "src", "f.py")); strings.Contains(string(b), "x + 3") {
			return 1
		}
		return 0
	}}
	rep, rec := rebuildRun(t, d, claude, testConfig(), hold)
	l := laneByIssue(rec)["issue-2"]
	if l.Status != contract.IntegrationHoldoutFailed || l.Commit != nil || l.Rebuild == nil || rec.Integrated != 1 || len(claude.seen()) != 3 {
		t.Fatalf("%+v launches %d", rec.Lanes, len(claude.seen()))
	}
	if rec.Usage == nil || rec.Usage.USD != 0.3 || !strings.Contains(l.Reason, "rebuild") {
		t.Errorf("usage %+v reason %q", rec.Usage, l.Reason)
	}
	// one lane accepted in the end: the whole spend over it
	if c := CostPerAccepted(rep, rec); c == nil || *c < 0.8999 || *c > 0.9001 {
		t.Errorf("cost per accepted %v", c)
	}
}

// The rebuild's test guard takes the integration head as its base: a test file that a landed lane added is a base file
// to it, so a rebuild that edits it is rejected, with the file named and the spend counted.
func TestRebuildCannotEditATestFileThatALandedLaneAdded(t *testing.T) {
	d := targetWithTests(t)
	// the integration head: the base plus a test file a landed lane added
	write(t, d.Repo.Dir, "tests/test_added.py", "def test_added():\n    assert f(1) == 2\n")
	git(t, d.Repo.Dir, "add", "-A")
	git(t, d.Repo.Dir, "commit", "-qm", "a landed lane's test")
	head := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	claude := cheapClaude()
	claude.edit = func(work string) { // the rebuild weakens the landed lane's test
		os.WriteFile(filepath.Join(work, "tests", "test_added.py"), []byte("def test_added():\n    assert True\n"), 0o644)
	}
	d.Launcher = claude
	cfg := testConfig()
	batch := batchOf(issue("issue-2"))
	rb, err := newRebuilder(&RebuildDeps{Config: cfg, Batch: batch}, d, contract.FactoryReport{RunID: "run-0123456789ab"}, &worktrees{dir: d.Repo.Dir}, func() {})
	if err != nil {
		t.Fatal(err)
	}
	rec, ref := rb.build(context.Background(), head, "issue-2")
	if ref != "" || rec.Outcome != contract.LaneRejected || !strings.Contains(rec.Reason, "tests/test_added.py (modified)") || rec.CandidateRef != nil {
		t.Fatalf("%+v %q", rec, ref)
	}
	if rec.Usage.USD != 0.3 || rec.Usage.Calls != 1 {
		t.Errorf("the rebuild's spend was not counted: %+v", rec.Usage)
	}
	// an issue the batch does not have is not rebuilt, and no model is called
	if rec, ref := rb.build(context.Background(), head, "issue-9"); ref != "" || rec.Outcome != contract.LaneSkipped || len(claude.seen()) != 1 {
		t.Errorf("%+v %q", rec, ref)
	}
}

// A rebuild is a new lane: it stops at the run's spend caps like any other, and a run that already spent past
// run_stop_new_usd does not start it. The lane is dropped, the skipped rebuild is in the record at zero cost.
func TestRebuildIsNotStartedPastTheRunsStopNewCap(t *testing.T) {
	d := target(t)
	cfg := testConfig()
	cfg.Caps.RunStopNewUSD = 0.5 // the two lanes spent 0.6
	claude := cheapClaude()
	_, rec := rebuildRun(t, d, claude, cfg, &fakeHoldout{})
	l := laneByIssue(rec)["issue-2"]
	if l.Status != contract.IntegrationConflict || l.Rebuild == nil || l.Rebuild.Outcome != contract.LaneSkipped || len(claude.seen()) != 2 {
		t.Fatalf("%+v launches %d", rec.Lanes, len(claude.seen()))
	}
	if rec.Usage == nil || rec.Usage.USD != 0 || rec.Integrated != 1 {
		t.Errorf("%+v", rec.Usage)
	}
}

// Without RebuildDeps a lane that does not apply is dropped as before, and the record has no usage.
func TestConflictedLaneIsNotRebuiltWithoutRebuildDeps(t *testing.T) {
	d := target(t)
	d.Launcher = cheapClaude()
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1"), issue("issue-2")))
	if err != nil {
		t.Fatal(err)
	}
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	rec, err := Integrate(context.Background(), integrateDeps(d, &fakeHoldout{}), holdoutSpec(dir, seal, "issue-1", "issue-2"), rep)
	if err != nil {
		t.Fatal(err)
	}
	if l := laneByIssue(rec)["issue-2"]; l.Status != contract.IntegrationConflict || l.Rebuild != nil || rec.Usage != nil {
		t.Fatalf("%+v", rec)
	}
}
