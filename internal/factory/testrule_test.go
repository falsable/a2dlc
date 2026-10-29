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

const wantTestRule = "Existing test files matching config.tests.paths (tests/**) must not be modified or deleted; new tests go in new files."

// intentSpy hands every launch to the inner launcher and keeps the intent.json the builder was given in /in.
type intentSpy struct {
	inner run.Launcher
	mu    sync.Mutex
	seen  []contract.Intent
}

func (s *intentSpy) Launch(ctx context.Context, r contract.Role, spec run.LaunchSpec) (run.LaunchResult, error) {
	var it contract.Intent
	if b, err := os.ReadFile(filepath.Join(spec.InDir, run.ArtIntent)); err == nil {
		json.Unmarshal(b, &it)
	}
	s.mu.Lock()
	s.seen = append(s.seen, it)
	s.mu.Unlock()
	return s.inner.Launch(ctx, r, spec)
}

// The rule the guard enforces is stated to the builder, in the intent it reads, identically for both arms, and it is
// the only thing the lane's intent gains (bead a2dlc-3fu.18).
func TestTheLaneIntentStatesTheTestRuleToBothArmsAndChangesNothingElse(t *testing.T) {
	is := issue("issue-1")
	goals := map[string]string{}
	for _, arm := range []string{contract.ArmMinimal, contract.ArmControl} {
		d := targetWithTests(t)
		spy := &intentSpy{inner: &fakeClaude{script: spend()}}
		d.Launcher = spy
		cfg := testConfig()
		cfg.Arm = arm
		rep, err := RunBatch(context.Background(), d, cfg, batchOf(is))
		if err != nil || len(spy.seen) == 0 || lanesByIssue(rep)["issue-1"].SliceID == nil { // the fake has no JUnit: the lane ends INCONCLUSIVE, which is not the point here
			t.Fatalf("%s: %v %+v", arm, err, rep)
		}
		slice := *lanesByIssue(rep)["issue-1"].SliceID
		want := intentFor(is, slice)
		want.Goal += "\n\n" + wantTestRule
		for _, got := range spy.seen {
			if !strings.HasSuffix(got.Goal, "\n\n"+wantTestRule) || strings.Count(got.Goal, wantTestRule) != 1 {
				t.Fatalf("%s: the builder's goal is %q", arm, got.Goal)
			}
			if a, b := mustJSON(t, got), mustJSON(t, want); a != b {
				t.Fatalf("%s: the intent changed in more than the rule:\n got %s\nwant %s", arm, a, b)
			}
		}
		// the intent kept in the run output is the one the builder got
		raw, err := os.ReadFile(filepath.Join(d.Out, rep.RunID, "intents", "issue-1.json"))
		var kept contract.Intent
		if err != nil || json.Unmarshal(raw, &kept) != nil {
			t.Fatalf("%s: %v", arm, err)
		}
		goals[arm] = kept.Goal
	}
	if goals[contract.ArmMinimal] != goals[contract.ArmControl] {
		t.Fatalf("the arms' goals differ:\n%q\n%q", goals[contract.ArmMinimal], goals[contract.ArmControl])
	}
}

// A repo that declares no test files has no rule to state, and the intent is the issue as it always was.
func TestTheLaneIntentOfARepoWithoutTestsHasNoRule(t *testing.T) {
	d := target(t)
	spy := &intentSpy{inner: &fakeClaude{script: spend()}}
	d.Launcher = spy
	rep, err := RunBatch(context.Background(), d, testConfig(), batchOf(issue("issue-1")))
	if err != nil || len(spy.seen) == 0 {
		t.Fatalf("%v %+v", err, rep)
	}
	for _, got := range spy.seen {
		if strings.Contains(got.Goal, "config.tests.paths") || got.Goal != intentFor(issue("issue-1"), got.SliceID).Goal {
			t.Fatalf("%q", got.Goal)
		}
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
