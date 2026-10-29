package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func integrateDeps(d Deps, h HoldoutRunner) IntegrateDeps {
	return IntegrateDeps{Deps: d, Holdout: h, MainRef: "main"}
}

func laneByIssue(r contract.FactoryIntegration) map[string]contract.FactoryIntegrationLane {
	m := map[string]contract.FactoryIntegrationLane{}
	for _, l := range r.Lanes {
		m[l.IssueID] = l
	}
	return m
}

// The acceptance of bead 3fu.4: lanes are queued and each is rebased on the branch as it stands; a conflicting lane
// does not land; a lane that fails the holdout never lands and the lanes after it still do; main does not move.
func TestIntegrateQueuesRebasesAndNeverLandsAConflictOrAHoldoutFailure(t *testing.T) {
	d := target(t)
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A", "CANARY": "c"})
	rejected := contract.FactoryLane{IssueID: "i-rejected", Status: contract.LaneRejected, Reason: "tests_touched", Stages: []contract.StageUsage{}}
	rep := reportOf(d,
		acceptedLane("i1", candidate(t, d, base, "i1", map[string]string{"src/a.py": "A = 1\n"})),
		acceptedLane("i2", candidate(t, d, base, "i2", map[string]string{"src/b.py": "B = 2\n"})),
		acceptedLane("i3", candidate(t, d, base, "i3", map[string]string{"src/f.py": "def f(x):\n    return x + 1\n"})),
		acceptedLane("i4", candidate(t, d, base, "i4", map[string]string{"src/f.py": "def f(x):\n    return x + 2\n"})),
		acceptedLane("i5", candidate(t, d, base, "i5", map[string]string{"src/bad.py": "BAD = 1\n"})),
		acceptedLane("i6", candidate(t, d, base, "i6", map[string]string{"src/c.py": "C = 3\n"})),
		acceptedLane("i7", candidate(t, d, base, "i7", map[string]string{"src/d.py": "D = 4\n"})),
		rejected)
	hold := &fakeHoldout{exit: failWhenPresent("src/bad.py")}
	rec, err := Integrate(context.Background(), integrateDeps(d, hold), holdoutSpec(dir, seal, "i1", "i2", "i3", "i4", "i5", "i6"), rep)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, l := range rec.Lanes {
		got[l.IssueID] = l.Status
	}
	want := map[string]string{"i1": "integrated", "i2": "integrated", "i3": "integrated", "i4": "conflict", "i5": "holdout_failed",
		"i6": "integrated", "i7": "no_holdout", "i-rejected": "not_queued"}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s is %q, want %q (%+v)", id, got[id], w, laneByIssue(rec)[id])
		}
	}
	if rec.Integrated != 4 {
		t.Fatalf("integrated = %d", rec.Integrated)
	}
	lanes := laneByIssue(rec)
	// the first lane starts from the base; the second is rebased on the first, and so is every later one
	if lanes["i1"].Rebased || !lanes["i2"].Rebased || !lanes["i3"].Rebased {
		t.Errorf("rebased: %v %v %v", lanes["i1"].Rebased, lanes["i2"].Rebased, lanes["i3"].Rebased)
	}
	if c := lanes["i4"].Conflicts; len(c) != 1 || c[0] != "src/f.py" || lanes["i4"].Commit != nil {
		t.Errorf("conflict lane: %+v", lanes["i4"])
	}
	if lanes["i5"].Commit != nil || lanes["i5"].HoldoutExit == nil || *lanes["i5"].HoldoutExit != 1 {
		t.Errorf("holdout lane: %+v", lanes["i5"])
	}
	if lanes["i-rejected"].QueuePosition != nil || lanes["i7"].QueuePosition == nil || *lanes["i6"].QueuePosition != 5 {
		t.Errorf("queue positions: %+v", rec.Lanes)
	}
	// the holdout saw, for each lane, its own files and those of the lanes integrated before it, and never the failed lane's
	if len(hold.runs) != 5 {
		t.Fatalf("holdout runs: %v", hold.runs)
	}
	for i, w := range []string{"i1.test", "i1.test i2.test", "i1.test i2.test i3.test", "i1.test i2.test i3.test i5.test", "i1.test i2.test i3.test i6.test"} {
		if g := strings.Join(hold.runs[i], " "); g != w {
			t.Errorf("run %d: %q, want %q", i, g, w)
		}
	}
	// the branch holds exactly the three integrated lanes' files plus i6's, and nothing of the failed or conflicting ones
	head := rec.HeadSha
	if git(t, d.Repo.Dir, "rev-list", "--count", base+".."+head) != "4" {
		t.Errorf("commits on the integration branch: %s", git(t, d.Repo.Dir, "log", "--oneline", base+".."+head))
	}
	tree := git(t, d.Repo.Dir, "ls-tree", "-r", "--name-only", head)
	for _, f := range []string{"src/a.py", "src/b.py", "src/c.py"} {
		if !strings.Contains(tree, f) {
			t.Errorf("%s missing from the integration branch", f)
		}
	}
	for _, f := range []string{"src/bad.py", "src/d.py"} {
		if strings.Contains(tree, f) {
			t.Errorf("%s landed on the integration branch", f)
		}
	}
	if f := git(t, d.Repo.Dir, "show", head+":src/f.py"); !strings.Contains(f, "x + 1") {
		t.Errorf("f.py: %q", f)
	}
	// main did not move, and the record says so
	if m := git(t, d.Repo.Dir, "rev-parse", "main"); m != base || rec.MainShaBefore != base || rec.MainShaAfter != base {
		t.Errorf("main %s, record %s -> %s, base %s", m, rec.MainShaBefore, rec.MainShaAfter, base)
	}
	if rec.IntegrationBranch != "factory/"+rep.RunID+"/integration" || git(t, d.Repo.Dir, "rev-parse", rec.IntegrationBranch) != head {
		t.Errorf("branch %s", rec.IntegrationBranch)
	}
	// the record is on disk and strictly decodable; the holdout's output is in its own folder, not in the record
	b, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "integration.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk contract.FactoryIntegration
	if err := contract.DecodeStrictBytes(b, &disk); err != nil || disk.Validate() != nil || strings.Contains(string(b), "CANARY") {
		t.Fatalf("%v %v", err, disk.Validate())
	}
	if _, err := os.Stat(filepath.Join(d.Out, rep.RunID, "holdout-logs", "i5.log")); err != nil {
		t.Error(err)
	}
}
