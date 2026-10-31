package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// config.rung "gates" stops a2dlc run after Stage 4: Stage 5 is skipped with the rung as its reason, no mutation.json exists
// (so nothing reports a kill rate or a PASS for mutation), the outcome is Stage 4's, and the verdict says that mutation was not
// part of what was measured.
func TestRungGatesSkipsMutationAndSaysSo(t *testing.T) {
	a, dir, engine, _ := repoMutation(t, `,"rung":"gates"`)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "rung-gates"})
	if err != nil {
		t.Fatal(err)
	}
	s5 := st.Stages[7]
	if s5.Status != contract.StageSkipped || s5.Reason != contract.RungGatesNoMutation || s5.Artifact != nil {
		t.Fatalf("stage 5: %+v", s5)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v, want Stage 4's PASS", st.Outcome)
	}
	if len(engine.requests) != 0 {
		t.Fatalf("the engine was asked to mutate at the gates rung: %+v", engine.requests)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/rung-gates", ArtMutation)); !os.IsNotExist(err) {
		t.Fatalf("mutation.json exists at the gates rung: %v", err)
	}
	var v contract.Verdict
	b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/rung-gates", ArtVerdict))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &v); err != nil {
		t.Fatal(err)
	}
	g := v.TestGates
	if g == nil || g.Mutation == nil || g.Mutation.Status != contract.GateNotApplicable || g.Mutation.Reason != contract.RungGatesNoMutation {
		t.Fatalf("test_gates.mutation: %+v", g)
	}
}

// The default rung is today's behavior: an absent rung and "mutation" run Stage 5 and the verdict carries no mutation gate.
func TestRungMutationIsTheDefault(t *testing.T) {
	for name, extra := range map[string]string{"absent": "", "explicit": `,"rung":"mutation"`} {
		a, dir, _, _ := repoMutation(t, extra)
		os.Remove(filepath.Join(dir, ".a2dlc/roles.json")) // no Saboteur: the live mutants end the stage INCONCLUSIVE, which is enough to show it ran
		st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "rung-" + name})
		if err != nil {
			t.Fatal(err)
		}
		if st.Stages[7].Status == contract.StageSkipped {
			t.Fatalf("%s: Stage 5 skipped: %+v", name, st.Stages[7])
		}
		rep := readMutation(t, dir, "rung-"+name)
		if len(rep.Mutants) == 0 {
			t.Fatalf("%s: no mutants measured: %+v", name, rep)
		}
		b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/rung-"+name, ArtVerdict))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"mutation": {"status"`) || strings.Contains(string(b), "not part of this rung") {
			t.Fatalf("%s: the verdict records a mutation gate: %s", name, b)
		}
	}
}

// An unknown rung is refused when the config is read, never treated as the default.
func TestRungUnknownIsRefused(t *testing.T) {
	a, _, _, _ := repoMutation(t, `,"rung":"sandbox"`)
	_, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "rung-unknown"})
	if err == nil || !strings.Contains(err.Error(), `rung "sandbox"`) {
		t.Fatalf("err = %v", err)
	}
}
