package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

func gitRun(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t"), env...)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func newRepo(t *testing.T) (gitx.Repo, string) {
	dir := t.TempDir()
	gitRun(t, dir, nil, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("base\n"), 0o644)
	gitRun(t, dir, nil, "add", "-A")
	gitRun(t, dir, nil, "commit", "-qm", "base")
	return gitx.Repo{Dir: dir}, gitRun(t, dir, nil, "rev-parse", "HEAD")
}

// commitFile is a commit on parent that writes one file, without touching the working tree.
func commitFile(t *testing.T, dir, parent, path, content string) string {
	idx := filepath.Join(t.TempDir(), "index")
	env := []string{"GIT_INDEX_FILE=" + idx}
	gitRun(t, dir, env, "read-tree", parent)
	blobFile := filepath.Join(t.TempDir(), "blob")
	os.WriteFile(blobFile, []byte(content), 0o644)
	blob := gitRun(t, dir, nil, "hash-object", "-w", blobFile)
	gitRun(t, dir, env, "update-index", "--add", "--cacheinfo", "100644,"+blob+","+path)
	tree := gitRun(t, dir, env, "write-tree")
	return gitRun(t, dir, nil, "commit-tree", tree, "-p", parent, "-m", path)
}

// graphPlan is a valid plan over deps: slice i owns s<i>.txt and covers criterion i.
func graphPlan(deps [][]int) (contract.Plan, contract.Intent) {
	p := contract.Plan{SchemaVersion: 1, FeatureID: "f", Shared: []string{}}
	var acc []string
	for i, ds := range deps {
		s := contract.PlanSlice{SliceID: fmt.Sprintf("f.s%d", i), Goal: fmt.Sprintf("slice %d", i), Owns: []string{fmt.Sprintf("s%d.txt", i)},
			DependsOn: []string{}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 0.1}, Acceptance: []string{}, Invariants: []string{}, Covers: []int{i}}
		for _, d := range ds {
			s.DependsOn = append(s.DependsOn, fmt.Sprintf("f.s%d", d))
		}
		p.Slices = append(p.Slices, s)
		acc = append(acc, fmt.Sprintf("criterion %d", i))
	}
	cap := 1.0
	f := contract.Intent{SchemaVersion: 1, SliceID: "f", Mode: contract.ModeGreenfield, Goal: "a feature", Invariants: []string{}, NonGoals: []string{},
		Criticality: "low", CriticalPaths: []string{}, Acceptance: acc,
		Budget: &contract.IntentBudget{MaxCostUSD: &cap, MaxInnerIterations: 5, MaxTier1AdversarialRounds: 3}}
	return p, f
}

// scripted runs a slice as the script says; a passed slice commits its own file on the base it was given.
type scripted struct {
	t        *testing.T
	dir      string
	outcome  map[string]string // slice id → outcome of its run in this invocation
	stale    map[string]bool   // slice id → a passed run that no longer verifies
	n        int
	sawBases map[string]string // slice id → base of its last run
	mu       sync.Mutex
	inflight int
	peak     int           // most runs at once
	hold     time.Duration // how long a run takes, so runs that may overlap do
}

func (s *scripted) Run(_ context.Context, sl contract.PlanSlice, intentPath, base string) (SliceRun, error) {
	s.mu.Lock()
	s.n++
	n := s.n
	s.inflight++
	if s.inflight > s.peak {
		s.peak = s.inflight
	}
	s.sawBases[sl.SliceID] = base
	o := s.outcome[sl.SliceID]
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.inflight--; s.mu.Unlock() }()
	time.Sleep(s.hold)
	var in contract.Intent
	b, err := os.ReadFile(intentPath)
	if err != nil {
		return SliceRun{}, err
	}
	if err := contract.DecodeStrictBytes(b, &in); err != nil || in.SliceID != sl.SliceID || in.Boundary == nil {
		s.t.Errorf("the intent of %s: %v %+v", sl.SliceID, err, in)
	}
	if o != OutPassed {
		return SliceRun{Status: o, Reason: "scripted " + o}, nil
	}
	c := commitFile(s.t, s.dir, base, sl.Owns[0], fmt.Sprintf("%s run %d\n", sl.SliceID, n))
	return SliceRun{Status: contract.PlanPassed, Candidate: c, Reason: "PASS"}, nil
}

func (s *scripted) Verifies(sl contract.PlanSlice, _ contract.PlanSliceState) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.stale[sl.SliceID]
}

// TestRunnerConformsToTheModel drives the real runner, with real git merges, over every graph on 4 slices and three
// invocations with random slice outcomes, final checks, passed slices that stop verifying and 1 to 3 slices at once.
// After each invocation the slices it ran, their statuses, their invocation and the outcome must be what the model
// predicts; no slice may start before a slice it depends on that also ran; no more than the slots may run at once; and
// every passed slice must have run on a base that contains the candidate of each slice it depends on (P2). The model is
// sequential: with slices side by side only the order they start in may differ from it.
func TestRunnerConformsToTheModel(t *testing.T) {
	var overlapped int32
	outs := []string{OutPassed, OutPassed, OutEscalated, OutFailed} // passing more often reaches the deeper states
	for gi, deps := range allGraphs(modelSlices) {
		gi, deps := gi, deps
		t.Run(fmt.Sprint(gi), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewSource(int64(gi)))
			repo, base := newRepo(t)
			p, f := graphPlan(deps)
			planBytes, _ := json.Marshal(p)
			ex := &scripted{t: t, dir: repo.Dir, sawBases: map[string]string{}, hold: 15 * time.Millisecond}
			finalOK := true
			jobs := 1 + rng.Intn(3)
			r := Runner{Repo: repo, Exec: ex, Final: func(_ context.Context, b, merged string) (bool, string, error) {
				if b != base {
					t.Errorf("final check against %s, not the feature base", b)
				}
				return finalOK, "scripted", nil
			}, Slots: func(width int) (int, *contract.PlanScheduler) {
				return jobs, &contract.PlanScheduler{Jobs: jobs, Why: "scripted", Demand: contract.PlanDemand{}, Host: contract.PlanPlace{Name: "test"}}
			}}
			var order []int
			for _, id := range Order(p) {
				var i int
				fmt.Sscanf(id, "f.s%d", &i)
				order = append(order, i)
			}
			model := NewModelState(deps, order)
			for inv := 1; inv <= 3; inv++ {
				ex.outcome, ex.stale = map[string]string{}, map[string]bool{}
				o := make([]string, modelSlices)
				for i := range o {
					o[i] = outs[rng.Intn(len(outs))]
					ex.outcome[fmt.Sprintf("f.s%d", i)] = o[i]
					if model.Slices[i].Status == contract.PlanPassed && rng.Intn(4) == 0 {
						model.Slices[i].Verifies = false
						ex.stale[fmt.Sprintf("f.s%d", i)] = true
					}
				}
				finalOK = rng.Intn(3) != 0
				var step ModelStep
				model, step = ModelInvoke(model, func(i int) string { return o[i] }, finalOK)
				st, ran, err := r.Run(context.Background(), planBytes, f, base)
				if err != nil {
					t.Fatal(err)
				}
				var want []string
				for _, i := range step.Ran {
					want = append(want, fmt.Sprintf("f.s%d", i))
				}
				if a, b := append([]string{}, ran...), append([]string{}, want...); !sameSet(a, b) {
					t.Fatalf("invocation %d ran %v, the model %v", inv, ran, want)
				}
				pos := map[string]int{}
				for k, id := range ran {
					pos[id] = k
				}
				for i, ds := range deps {
					id := fmt.Sprintf("f.s%d", i)
					for _, d := range ds {
						dp, dRan := pos[fmt.Sprintf("f.s%d", d)]
						if ip, iRan := pos[id]; iRan && dRan && dp > ip {
							t.Fatalf("invocation %d started %s before its dependency f.s%d: %v", inv, id, d, ran)
						}
					}
				}
				if ex.peak > jobs {
					t.Fatalf("%d runs at once with %d slot(s)", ex.peak, jobs)
				}
				for i, ms := range model.Slices {
					rs := st.Slices[i]
					if rs.Status != ms.Status || rs.FinishedIn != ms.FinishedIn {
						t.Fatalf("invocation %d, slice %d: runner %s/%d, model %s/%d", inv, i, rs.Status, rs.FinishedIn, ms.Status, ms.FinishedIn)
					}
					if rs.Status == contract.PlanPassed {
						for _, d := range deps[i] {
							if !repo.IsAncestor(*st.Slices[d].Candidate, *rs.Base) {
								t.Fatalf("slice %d ran on a base without the candidate of %d", i, d)
							}
						}
					}
				}
				got := ""
				if st.Outcome != nil {
					got = *st.Outcome
				}
				if got != model.Outcome {
					t.Fatalf("invocation %d: outcome %s, the model %s", inv, got, model.Outcome)
				}
				if got == contract.PASS {
					for _, s := range st.Slices {
						if !repo.IsAncestor(*s.Candidate, *st.Merged) {
							t.Fatalf("the merged tree lacks %s", s.SliceID)
						}
					}
				}
				b, err := os.ReadFile(filepath.Join(StateDir(repo.Dir, "f"), "state.json"))
				if err != nil {
					t.Fatal(err)
				}
				var onDisk contract.PlanState
				if err := contract.DecodeStrictBytes(b, &onDisk); err != nil {
					t.Fatalf("state.json out of contract: %v", err)
				}
			}
			if ex.peak > 1 {
				atomic.AddInt32(&overlapped, 1)
			}
		})
	}
	t.Cleanup(func() {
		if atomic.LoadInt32(&overlapped) == 0 {
			t.Error("no graph ran two slices at once: the concurrent schedule was not exercised")
		}
	})
}

func sameSet(a, b []string) bool {
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, ",") == strings.Join(b, ",")
}

// A settled plan runs nothing (J5); a new version of the plan keeps the state of the slices it did not change (and that
// depend only on unchanged slices) and runs the rest; another base is refused rather than mixed with the old state.
func TestSettledPlanRunsNothingAndNewVersionsCarryOver(t *testing.T) {
	repo, base := newRepo(t)
	p, f := graphPlan([][]int{{}, {0}, {}})
	planBytes, _ := json.Marshal(p)
	ex := &scripted{t: t, dir: repo.Dir, sawBases: map[string]string{}, outcome: map[string]string{"f.s0": OutPassed, "f.s1": OutPassed, "f.s2": OutPassed}}
	r := Runner{Repo: repo, Exec: ex, Final: func(context.Context, string, string) (bool, string, error) { return true, "", nil }}
	st, ran, err := r.Run(context.Background(), planBytes, f, base)
	if err != nil || *st.Outcome != contract.PASS || len(ran) != 3 {
		t.Fatalf("%v %v %v", err, st.Outcome, ran)
	}
	if ex.sawBases["f.s1"] != *st.Slices[0].Candidate {
		t.Fatalf("a slice with one dependency should start from that candidate (fast-forward), got %s", ex.sawBases["f.s1"])
	}
	if _, ran, err := r.Run(context.Background(), planBytes, f, base); err != nil || len(ran) != 0 {
		t.Fatalf("a settled plan ran %v (%v)", ran, err)
	}
	// s0 changes: s0 and s1 (which depends on it) run again; s2 keeps its state
	p.Slices[0].Goal = "another goal"
	changed, _ := json.Marshal(p)
	st, ran, err = r.Run(context.Background(), changed, f, base)
	if err != nil || !sameSet(ran, []string{"f.s0", "f.s1"}) || *st.Outcome != contract.PASS {
		t.Fatalf("a new version ran %v (%v), outcome %v", ran, err, st.Outcome)
	}
	// a slice dropped and one added: the new one runs, nothing else
	p.Slices = append(p.Slices[:2], contract.PlanSlice{SliceID: "f.s9", Goal: "new", Owns: []string{"s9.txt"}, DependsOn: []string{},
		Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 0.1}, Acceptance: []string{}, Invariants: []string{}, Covers: []int{2}})
	ex.outcome["f.s9"] = OutPassed
	changed, _ = json.Marshal(p)
	if st, ran, err = r.Run(context.Background(), changed, f, base); err != nil || strings.Join(ran, ",") != "f.s9" || len(st.Slices) != 3 {
		t.Fatalf("after replacing a slice ran %v (%v), %d slices", ran, err, len(st.Slices))
	}
	other := commitFile(t, repo.Dir, base, "x.txt", "x\n")
	if _, _, err := r.Run(context.Background(), changed, f, other); err == nil || !strings.Contains(err.Error(), "is on base") {
		t.Fatalf("another base was not refused: %v", err)
	}
}

// A plan that does not pass its gates never runs a slice.
func TestInvalidPlanRunsNothing(t *testing.T) {
	repo, base := newRepo(t)
	p, f := graphPlan([][]int{{}, {}})
	p.Slices[1].Owns = []string{"s0.txt"} // two concurrent slices on one file: G3
	planBytes, _ := json.Marshal(p)
	ex := &scripted{t: t, dir: repo.Dir, sawBases: map[string]string{}}
	r := Runner{Repo: repo, Exec: ex}
	if _, _, err := r.Run(context.Background(), planBytes, f, base); err == nil || !strings.Contains(err.Error(), "does not pass its gates") || ex.n != 0 {
		t.Fatalf("err %v, %d runs", err, ex.n)
	}
}
