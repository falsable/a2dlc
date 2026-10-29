package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// A holdout that was edited after the seal, or a spec that names another seal, stops the integration before it creates
// anything: no worktree, no branch, no holdout run.
func TestIntegrateRefusesAHoldoutThatIsNotTheOneTheSpecNames(t *testing.T) {
	d := target(t)
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	rep := reportOf(d, acceptedLane("i1", candidate(t, d, base, "i1", map[string]string{"src/a.py": "A = 1\n"})))
	hold := &fakeHoldout{}
	other := strings.Repeat("9", 64)
	write(t, dir, "tests/a.test.ts", "A, edited after the seal")
	for name, spec := range map[string]contract.FactoryHoldout{"edited": holdoutSpec(dir, seal, "i1"), "other seal": holdoutSpec(dir, other, "i1")} {
		if name == "other seal" {
			write(t, dir, "tests/a.test.ts", "A")
		}
		if _, err := Integrate(context.Background(), integrateDeps(d, hold), spec, rep); err == nil {
			t.Errorf("%s: integrated", name)
		}
	}
	if b := git(t, d.Repo.Dir, "branch", "--list", "factory/*"); b != "" || len(hold.runs) != 0 {
		t.Errorf("branches %q, holdout runs %v", b, hold.runs)
	}
}

// The seal is checked again right before each mount: a holdout edited while the queue runs fails the lane that follows,
// not the ones before it.
func TestIntegrateChecksTheSealBeforeEveryMount(t *testing.T) {
	d := target(t)
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	rep := reportOf(d,
		acceptedLane("i1", candidate(t, d, base, "i1", map[string]string{"src/a.py": "A = 1\n"})),
		acceptedLane("i2", candidate(t, d, base, "i2", map[string]string{"src/b.py": "B = 2\n"})))
	hold := &fakeHoldout{}
	hold.exit = func(string, []string) int { write(t, dir, "tests/a.test.ts", "edited between lanes"); return 0 }
	rec, err := Integrate(context.Background(), integrateDeps(d, hold), holdoutSpec(dir, seal, "i1", "i2"), rep)
	if err != nil {
		t.Fatal(err)
	}
	l := laneByIssue(rec)
	if l["i1"].Status != contract.IntegrationIntegrated || l["i2"].Status != contract.IntegrationHoldoutError || l["i2"].Commit != nil {
		t.Fatalf("%+v", rec.Lanes)
	}
	if len(hold.runs) != 1 {
		t.Errorf("the second lane reached the holdout: %v", hold.runs)
	}
}

// A lane that edits the gate's own files cannot be integrated, so no waiver or approval can arrive through a lane.
func TestIntegrateDoesNotLandALaneThatEditsTheGatesFiles(t *testing.T) {
	d := target(t)
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	rep := reportOf(d, acceptedLane("i1", candidate(t, d, base, "i1", map[string]string{".a2dlc/waivers.json": "{}\n", "src/a.py": "A = 1\n"})))
	hold := &fakeHoldout{}
	rec, err := Integrate(context.Background(), integrateDeps(d, hold), holdoutSpec(dir, seal, "i1"), rep)
	if err != nil {
		t.Fatal(err)
	}
	if l := rec.Lanes[0]; l.Status != contract.IntegrationCheckFailed || !strings.Contains(l.Reason, ".a2dlc/waivers.json") || rec.Integrated != 0 || len(hold.runs) != 0 {
		t.Fatalf("%+v", rec)
	}
	if rec.HeadSha != base {
		t.Errorf("the branch moved: %s", rec.HeadSha)
	}
}

// brokenEngine fails the differential for any file that mentions BOOM, as an engine that crashed would.
type brokenEngine struct{ fakeEngine }

func (b brokenEngine) Run(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error) {
	for _, f := range req.ChangedFiles {
		if body, _ := os.ReadFile(filepath.Join(candDir, f)); strings.Contains(string(body), "BOOM") {
			return contract.Response{}, os.ErrInvalid
		}
	}
	return b.fakeEngine.Run(ctx, e, req, baseDir, candDir)
}

// The check on the integrated tree can only lower: a lane whose check is not a PASS never reaches the holdout.
func TestIntegrateDoesNotLandALaneWhoseCheckIsNotAPass(t *testing.T) {
	d := target(t)
	d.NewApp = func(r gitx.Repo) app.App {
		return app.App{Repo: r, Runner: brokenEngine{}, Version: "test", Now: d.Now}
	}
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	rep := reportOf(d,
		acceptedLane("i1", candidate(t, d, base, "i1", map[string]string{"src/a.py": "BOOM = 1\n"})),
		acceptedLane("i2", candidate(t, d, base, "i2", map[string]string{"src/b.py": "B = 2\n"})))
	hold := &fakeHoldout{}
	rec, err := Integrate(context.Background(), integrateDeps(d, hold), holdoutSpec(dir, seal, "i1", "i2"), rep)
	if err != nil {
		t.Fatal(err)
	}
	l := laneByIssue(rec)
	if l["i1"].Status != contract.IntegrationCheckFailed || l["i1"].CheckVerdict == nil || *l["i1"].CheckVerdict == contract.PASS || len(hold.runs) != 1 {
		t.Fatalf("%+v runs %v", rec.Lanes, hold.runs)
	}
	if l["i2"].Status != contract.IntegrationIntegrated || strings.Join(hold.runs[0], " ") != "i2.test" {
		t.Errorf("%+v runs %v", l["i2"], hold.runs)
	}
}

// End to end with the fake claude: the lanes come from RunBatch, so their candidates are the real run refs. Both
// builders rewrite src/f.py, so the second lane conflicts with the first and never lands; main is untouched.
func TestIntegrateTheLanesOfARunBatchWithTheFakeClaude(t *testing.T) {
	d := target(t)
	d.Launcher = &fakeClaude{script: okCost(&contract.Cost{USD: 0.2, InputTokens: 10, OutputTokens: 1, Turns: 1})}
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1"), issue("issue-2")))
	if err != nil {
		t.Fatal(err)
	}
	if rep.Accepted != 2 {
		t.Fatalf("lanes: %+v", rep.Lanes)
	}
	mainBefore := git(t, d.Repo.Dir, "rev-parse", "main")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	hold := &fakeHoldout{}
	rec, err := Integrate(context.Background(), integrateDeps(d, hold), holdoutSpec(dir, seal, "issue-1", "issue-2"), rep)
	if err != nil {
		t.Fatal(err)
	}
	l := laneByIssue(rec)
	if l["issue-1"].Status != contract.IntegrationIntegrated || l["issue-2"].Status != contract.IntegrationConflict || rec.Integrated != 1 {
		t.Fatalf("%+v", rec.Lanes)
	}
	if !l["issue-2"].Rebased || len(l["issue-2"].Conflicts) != 1 || l["issue-2"].Conflicts[0] != "src/f.py" {
		t.Errorf("%+v", l["issue-2"])
	}
	// what landed is the builder's diff alone: src/f.py, and not the lane's own setup commit (.a2dlc/roles.json)
	if files := git(t, d.Repo.Dir, "diff", "--name-only", rec.BaseSha, rec.HeadSha); files != "src/f.py" {
		t.Errorf("landed files: %q", files)
	}
	if git(t, d.Repo.Dir, "rev-parse", "main") != mainBefore || rec.MainShaAfter != mainBefore {
		t.Error("main moved")
	}
}
