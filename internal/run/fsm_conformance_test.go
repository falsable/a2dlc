package run

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The conformance test holds the real Run to the model: every stage is replaced by a scripted one, the same script drives
// ModelInvoke, and after each invocation the state Run left on disk must be the state the model predicts, the stages Run
// called must be the ones the model ran, in the same order, and the invariants must hold on what is on disk.
//
// What it leaves out: the budget breaker (the real spend comes from the ledger; the model checks R4 and I4, the run's budget
// tests the breaker) and stale skips (they depend on the config; the model explores them).

type scriptedRun struct {
	outcomes map[string]string // what each stage id ends in when it runs, this invocation
	called   []string
	inv      int
}

func (sr *scriptedRun) stage(id string) Stage {
	return func(ctx context.Context, m *Machine) (string, string, string, error) {
		sr.called = append(sr.called, id)
		name := "fake-" + strings.ReplaceAll(id, ".", "_") + ".json"
		art := func() string {
			p, err := m.writeArtifact(name, map[string]any{"stage": id, "invocation": sr.inv})
			if err != nil {
				panic(err)
			}
			return p
		}
		switch sr.outcomes[id] {
		case OutDone:
			return contract.StageDone, "scripted", art(), nil
		case OutSkipped:
			return contract.StageSkipped, "scripted skip", "", nil
		case OutSkippedArt:
			return contract.StageSkipped, "scripted skip", art(), nil
		case OutEscalated:
			return contract.StageEscalated, "scripted escalation", "", nil
		case OutFailed:
			return contract.StageFailed, "scripted failure", "", nil
		}
		return "", "", "", fmt.Errorf("no script for stage %s", id)
	}
}

func TestRunConformsToTheModel(t *testing.T) {
	seeds := 120
	if testing.Short() {
		seeds = 20
	}
	for seed := 0; seed < seeds; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			conformanceTrace(t, rand.New(rand.NewSource(int64(seed))))
		})
	}
}

func conformanceTrace(t *testing.T, rng *rand.Rand) {
	a, _ := repoLite(t)
	sr := &scriptedRun{}
	saved := map[string]Stage{}
	for _, id := range contract.Stages {
		saved[id] = stages[id]
		stages[id] = sr.stage(id)
	}
	t.Cleanup(func() {
		for id, f := range saved {
			stages[id] = f
		}
	})
	model := NewModel(len(contract.Stages))
	never := func(int) bool { return false }
	dir := ""
	invocations := 2 + rng.Intn(4)
	for inv := 1; inv <= invocations; inv++ {
		// R7: the world between invocations. An altered artifact is the only change made here.
		if inv > 1 && rng.Intn(2) == 0 {
			var candidates []int
			for i, st := range model.Stages {
				if i > 0 && st.Artifact && st.Intact {
					candidates = append(candidates, i)
				}
			}
			if len(candidates) > 0 {
				i := candidates[rng.Intn(len(candidates))]
				p := filepath.Join(dir, "fake-"+strings.ReplaceAll(contract.Stages[i], ".", "_")+".json")
				if err := os.WriteFile(p, []byte(`{"altered":true}`+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				model.Stages[i].Intact = false
			}
		}
		// The script of this invocation: mostly done, sometimes another outcome.
		sr.outcomes, sr.called, sr.inv = map[string]string{}, nil, inv
		for _, id := range contract.Stages {
			o := OutDone
			switch r := rng.Intn(20); {
			case r == 0:
				o = OutEscalated
			case r == 1:
				o = OutFailed
			case r < 5:
				o = OutSkipped
			case r == 5:
				o = OutSkippedArt
			}
			sr.outcomes[id] = o
		}
		// Stage 0's artifact is the intent, which open() binds to the run (R8): a scripted run has none.
		sr.outcomes["0"] = OutSkipped
		prev := model
		var step ModelStep
		model, step = ModelInvoke(model, func(i int) string { return sr.outcomes[contract.Stages[i]] }, never)

		st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "fsm.conformance"})
		if err != nil {
			t.Fatalf("invocation %d: %v", inv, err)
		}
		dir = filepath.Join(a.Repo.Dir, filepath.FromSlash(pathRun), "fsm.conformance")

		var wantCalled []string
		for _, i := range step.Ran {
			wantCalled = append(wantCalled, contract.Stages[i])
		}
		if strings.Join(sr.called, " ") != strings.Join(wantCalled, " ") {
			t.Fatalf("invocation %d: Run called %v, the model ran %v (script %v)", inv, sr.called, wantCalled, sr.outcomes)
		}
		// What is on disk, read as the model reads it.
		onDisk := ModelState{Outcome: "", Invocation: model.Invocation, Stages: make([]ModelStage, len(st.Stages))}
		if st.Outcome != nil {
			onDisk.Outcome = *st.Outcome
		}
		m := &Machine{dir: dir}
		for i, s := range st.Stages {
			if s.ID != contract.Stages[i] {
				t.Fatalf("stage %d is %s, not %s: the order changed", i, s.ID, contract.Stages[i])
			}
			onDisk.Stages[i] = ModelStage{Status: s.Status, Artifact: s.Artifact != nil, Intact: m.artifactIntact(&st.Stages[i]),
				FinishedIn: model.Stages[i].FinishedIn}
			want := model.Stages[i]
			if onDisk.Stages[i].Status != want.Status || onDisk.Stages[i].Artifact != want.Artifact || onDisk.Stages[i].Intact != want.Intact {
				t.Fatalf("invocation %d, stage %s: on disk %+v, the model predicts %+v", inv, s.ID, onDisk.Stages[i], want)
			}
		}
		if onDisk.Outcome != model.Outcome {
			t.Fatalf("invocation %d: outcome on disk %q, the model predicts %q", inv, onDisk.Outcome, model.Outcome)
		}
		if bad := append(CheckInvariants(onDisk, step), CheckStep(prev, onDisk, step, never)...); len(bad) > 0 {
			t.Fatalf("invocation %d breaks %s", inv, strings.Join(bad, "; "))
		}
	}
}
