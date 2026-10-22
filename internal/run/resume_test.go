package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// launcherThatFails delegates to another launcher and returns a launch error (like a docker that is not there) on call
// number fails.
type launcherThatFails struct {
	Launcher
	calls, fails int
}

func (l *launcherThatFails) Launch(ctx context.Context, r contract.Role, s LaunchSpec) (LaunchResult, error) {
	l.calls++
	if l.calls == l.fails {
		return LaunchResult{}, errors.New("docker: executable file not found")
	}
	return l.Launcher.Launch(ctx, r, s)
}

func jsonIndent(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", " ")
	return append(b, '\n'), err
}

func converge(contract.BuilderRequest) map[string]string {
	return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
}

func broken(contract.BuilderRequest) map[string]string {
	return map[string]string{"src/f.py": "ROTO\n"}
}

func ledgerOf(t *testing.T, run string) contract.RunLedger {
	t.Helper()
	var lg contract.RunLedger
	readJSONTest(t, filepath.Join(run, "ledger.json"), &lg)
	return lg
}

func sumLedger(lg contract.RunLedger) float64 {
	var s float64
	for _, e := range lg.Entries {
		if e.Cost != nil {
			s += e.Cost.USD
		}
	}
	return s
}

// An error in the middle of a stage cannot delete the spend already recorded: the ledger has it and state.json was not saved.
func TestResumeAfterErrorKeepsSpend(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	base := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	opts := Options{Slice: "g.v1", MaxCostUSD: 0.15, MaxInner: 3}
	if _, err := Run(context.Background(), a, &launcherThatFails{Launcher: base, fails: 2}, opts); err == nil || !strings.Contains(err.Error(), "launch builder_primary") {
		t.Fatalf("the first Run had to fail at launch: %v", err)
	}
	run := filepath.Join(dir, ".a2dlc/run/g.v1")
	if n := len(ledgerOf(t, run).Entries); n != 1 {
		t.Fatalf("the ledger had to have 1 entry: %d", n)
	}
	// resuming with the same limit: the spend of the recorded launch counts, so one more launch fits and not two
	second := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	st, err := Run(context.Background(), a, second, opts)
	s3 := byID(st)["3"]
	if err != nil || s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-03") || len(second.seen) != 1 {
		t.Fatalf("%v %+v launches=%d", err, s3, len(second.seen))
	}
	lg := ledgerOf(t, run)
	if st.Budget.SpentUSD != sumLedger(lg) || st.Budget.SpentUSD != 0.2 || st.Budget.ByRole[contract.RoleBuilderPrimary] != 0.2 {
		t.Fatalf("state %+v versus ledger %v", st.Budget, sumLedger(lg))
	}
	// and over the limit nothing is launched: the lost spend can no longer hide the excess
	a, dir, _ = repoBuilder(t)
	requests = nil
	base = &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	if _, err := Run(context.Background(), a, &launcherThatFails{Launcher: base, fails: 2}, Options{Slice: "g.v2", MaxCostUSD: 0.15, MaxInner: 3}); err == nil {
		t.Fatal("the first Run had to fail")
	}
	third := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	st, err = Run(context.Background(), a, third, Options{Slice: "g.v2", MaxCostUSD: 0.05})
	if s3 = byID(st)["3"]; err != nil || s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-03") || len(third.seen) != 0 {
		t.Fatalf("%v %+v launches=%d", err, s3, len(third.seen))
	}
	_ = dir
}

// If the ledger has less spend than state.json (deleted or truncated), resuming cannot reset the budget.
func TestResumeWithTruncatedLedgerIsRejected(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.v1", MaxInner: 2}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, ".a2dlc/run/t.v1/ledger.json")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "t.v1", MaxInner: 2}); err == nil || !strings.Contains(err.Error(), "ledger") {
		t.Fatalf("a deleted ledger with spend in state.json had to be rejected: %v", err)
	}
}

// runThatConverges leaves a run with Stages 3 and 4 done (2 entries, 0.2 USD).
func runThatConverges(t *testing.T, slice string) (app.App, string) {
	t.Helper()
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback == nil {
			return map[string]string{"src/f.py": "def f(x):\n    return x +\nROTO\n"}
		}
		return converge(pl)
	})}
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: slice}); err != nil {
		t.Fatal(err)
	}
	if problems, err := VerifyRun(a, slice); err != nil || len(problems) != 0 {
		t.Fatalf("the healthy run had to verify: %v %v", problems, err)
	}
	return a, filepath.Join(dir, ".a2dlc/run", slice)
}

func rewrite(t *testing.T, path string, f func([]byte) []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, f(b), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyRunDetectsTamperedLedgerAndArtifacts(t *testing.T) {
	cases := []struct {
		name   string
		altera func(t *testing.T, run string)
		want   string
	}{
		{"ledger deleted", func(t *testing.T, run string) {
			if err := os.Remove(filepath.Join(run, "ledger.json")); err != nil {
				t.Fatal(err)
			}
		}, "ledger.json is missing"},
		{"ledger truncated", func(t *testing.T, run string) {
			lg := ledgerOf(t, run)
			lg.Entries = lg.Entries[:1]
			b, _ := jsonIndent(lg)
			if err := os.WriteFile(filepath.Join(run, "ledger.json"), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}, "fewer"},
		{"artifact of a done stage", func(t *testing.T, run string) {
			rewrite(t, filepath.Join(run, ArtVerdict), func(b []byte) []byte { return append(b, ' ') })
		}, "stage 4"},
		{"ledger from another run", func(t *testing.T, run string) {
			rewrite(t, filepath.Join(run, "ledger.json"), func(b []byte) []byte {
				return []byte(strings.Replace(string(b), ledgerOf(t, run).RunID, "run-foreign", 1))
			})
		}, "another run"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, run := runThatConverges(t, "v.v1")
			c.altera(t, run)
			problems, err := VerifyRun(a, "v.v1")
			if err != nil || len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), c.want) {
				t.Fatalf("%v %q", err, problems)
			}
		})
	}
	// the artifact of an escalated stage is also attested
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "e.v1", MaxInner: 2}); err != nil {
		t.Fatal(err)
	}
	if problems, err := VerifyRun(a, "e.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	rewrite(t, filepath.Join(dir, ".a2dlc/run/e.v1", ArtTier0), func(b []byte) []byte { return append(b, ' ') })
	if problems, err := VerifyRun(a, "e.v1"); err != nil || len(problems) != 1 || !strings.Contains(problems[0], "stage 3") {
		t.Fatalf("%v %q", err, problems)
	}
}

// A stage that runs again leaves the later ones stale, even if they are done and intact (README: a new candidate
// requires another verdict).
func TestResumeRedoesWhatFollowsTheTouchedStage(t *testing.T) {
	a, dir, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, converge)}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "r.v1"})
	if err != nil || len(requests) != 1 || len(engine.sawCand) != 1 || st.Stages[6].Status != contract.StageDone {
		t.Fatalf("%v %d %d", err, len(requests), len(engine.sawCand))
	}
	start4 := *st.Stages[6].StartedAt
	// touch candidate.json: Stage 3 runs again (the builder is launched again) and 4, done and intact, too
	rewrite(t, filepath.Join(dir, ".a2dlc/run/r.v1", ArtCandidate), func(b []byte) []byte { return append(b, ' ') })
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "r.v1"})
	if err != nil || len(requests) != 2 || len(engine.sawCand) != 2 || *st.Stages[6].StartedAt == start4 {
		t.Fatalf("%v requests=%d judged=%d start4=%s->%s", err, len(requests), len(engine.sawCand), start4, *st.Stages[6].StartedAt)
	}
	// a stage left running in state.json runs, and so do the ones after it; the earlier ones do not
	path := filepath.Join(dir, ".a2dlc/run/r.v1/state.json")
	var onDisk contract.RunState
	readJSONTest(t, path, &onDisk)
	start3 := *onDisk.Stages[5].StartedAt
	onDisk.Stages[6].Status = contract.StageRunning
	b, _ := jsonIndent(onDisk)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
	prev4 := *onDisk.Stages[6].StartedAt
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "r.v1"})
	if err != nil || len(requests) != 2 || len(engine.sawCand) != 3 || *st.Stages[5].StartedAt != start3 || *st.Stages[6].StartedAt == prev4 {
		t.Fatalf("%v requests=%d judged=%d", err, len(requests), len(engine.sawCand))
	}
	for _, s := range st.Stages[7:] {
		if s.StartedAt == nil || s.Status == contract.StagePending {
			t.Fatalf("stage %s was not redone: %+v", s.ID, s)
		}
	}
}

// Adding the role that the skipped reason asked for makes resuming re-evaluate that stage.
func TestResumeWithNewRoleRunsSkippedStage(t *testing.T) {
	a, dir, engine := repoBuilder(t)
	roles := filepath.Join(dir, ".a2dlc/roles.json")
	withRole, err := os.ReadFile(roles)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(roles); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "src/f.py", "def f(x):\n    return x + 1\n") // without a builder, Stage 4 judges the work tree
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, converge)}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "s.v1"})
	if err != nil || st.Stages[5].Status != contract.StageSkipped || len(requests) != 0 || len(engine.sawCand) != 1 {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
	// without changes to roles.json, resuming does not judge again
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "s.v1"}); err != nil || len(engine.sawCand) != 1 || st.Stages[5].Status != contract.StageSkipped {
		t.Fatalf("%v %d", err, len(engine.sawCand))
	}
	if err := os.WriteFile(roles, withRole, 0o644); err != nil {
		t.Fatal(err)
	}
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "s.v1"})
	s3 := st.Stages[5]
	if err != nil || s3.Status != contract.StageDone || s3.Artifact == nil || s3.Artifact.Path != ArtCandidate || len(requests) != 1 ||
		len(engine.sawCand) != 2 || !strings.Contains(engine.sawCand[1], "x * 2") {
		t.Fatalf("%v %+v requests=%d judged=%v", err, s3, len(requests), engine.sawCand)
	}
}

// CB-03 cuts at each stage and at each point: after a stage that finished, in Stage 1, and in N=2's orthogonal.
func TestCB03CutsAtEachStage(t *testing.T) {
	t.Run("after a done stage", func(t *testing.T) {
		a, dir, engine := repoBuilder(t)
		var requests []contract.BuilderRequest
		launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, converge)}
		st, err := Run(context.Background(), a, launcherFake, Options{Slice: "c.v1", MaxCostUSD: 0.05})
		s3, s4 := st.Stages[5], st.Stages[6]
		// the builder costs 0.1 and converges: Stage 3 finished, its paid work stays; what is cut is Stage 4
		if err != nil || s3.Status != contract.StageDone || s3.Artifact == nil || s3.Artifact.Path != ArtCandidate ||
			s4.Status != contract.StageEscalated || !strings.HasPrefix(s4.Reason, "CB-03") || st.Outcome == nil || *st.Outcome != "escalated" ||
			len(requests) != 1 || len(engine.sawCand) != 0 {
			t.Fatalf("%v %+v %+v", err, s3, s4)
		}
		start3 := *s3.StartedAt
		// raising the budget continues from Stage 4: the other builder is not paid for and the candidate is not thrown away
		st, err = Run(context.Background(), a, launcherFake, Options{Slice: "c.v1", MaxCostUSD: 1})
		if err != nil || len(requests) != 1 || *st.Stages[5].StartedAt != start3 || st.Stages[6].Status != contract.StageDone || len(engine.sawCand) != 1 {
			t.Fatalf("%v requests=%d %+v", err, len(requests), st.Stages[6])
		}
		if problems, err := VerifyRun(a, "c.v1"); err != nil || len(problems) != 0 {
			t.Fatalf("%v %v", problems, err)
		}
		_ = dir
	})
	t.Run("in Stage 3 midway through the loop", func(t *testing.T) {
		a, _, _ := repoBuilder(t)
		var requests []contract.BuilderRequest
		launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, broken)}
		st, err := Run(context.Background(), a, launcherFake, Options{Slice: "c.v2", MaxCostUSD: 0.15, MaxInner: 5})
		if s3 := st.Stages[5]; err != nil || s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-03") || len(requests) != 2 {
			t.Fatalf("%v %+v requests=%d", err, s3, len(requests))
		}
	})
	t.Run("in Stage 1 before the Inquisitor", func(t *testing.T) {
		a, launcherFake, opts, _ := prepareGreenfield(t, "calc.media.v1")
		launcherFake.script = scriptSpec(t, 1)
		opts.MaxCostUSD = 0.015 // α and β cost 0.01 each
		st, err := Run(context.Background(), a, launcherFake, opts)
		if s1 := st.Stages[2]; err != nil || s1.ID != "1" || s1.Status != contract.StageEscalated || !strings.HasPrefix(s1.Reason, "CB-03") || len(launcherFake.seen) != 2 {
			t.Fatalf("%v %+v launches=%d", err, st.Stages[2], len(launcherFake.seen))
		}
	})
	t.Run("N=2's orthogonal is not launched", func(t *testing.T) {
		withRouterN2(t)
		a, dir, _ := repoBuilder(t)
		write(t, dir, ".a2dlc/roles.json", rolesN2)
		var seen []launchRecord
		launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(string, contract.BuilderRequest) map[string]string { return converge(contract.BuilderRequest{}) })}
		st, err := Run(context.Background(), a, launcherFake, Options{Slice: "c.v4", MaxCostUSD: 0.05})
		if s3 := st.Stages[5]; err != nil || s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-03") || len(seen) != 1 || seen[0].role != contract.RoleBuilderPrimary {
			t.Fatalf("%v %+v launches=%v", err, st.Stages[5], seen)
		}
	})
}
