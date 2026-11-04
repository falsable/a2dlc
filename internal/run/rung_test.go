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

// config.rung "critical_code": when no changed file matches critical_code and the route's criticality is low, Stage 5 is
// skipped with RungCriticalCodeNoMutation and the verdict records test_gates.mutation as not_applicable with that reason.
func TestRungCriticalCodeOutsideSkipsMutationAndSaysSo(t *testing.T) {
	a, dir, engine, _ := repoMutation(t, `,"rung":"critical_code","critical_code":["lib/**"]`)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "rung-cc-out"})
	if err != nil {
		t.Fatal(err)
	}
	s5 := st.Stages[7]
	if s5.Status != contract.StageSkipped || s5.Reason != contract.RungCriticalCodeNoMutation || s5.Artifact != nil {
		t.Fatalf("stage 5: %+v", s5)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v, want Stage 4's PASS", st.Outcome)
	}
	if len(engine.requests) != 0 {
		t.Fatalf("the engine was asked to mutate outside critical_code: %+v", engine.requests)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/rung-cc-out", ArtMutation)); !os.IsNotExist(err) {
		t.Fatalf("mutation.json exists outside critical_code: %v", err)
	}
	var v contract.Verdict
	b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/rung-cc-out", ArtVerdict))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &v); err != nil {
		t.Fatal(err)
	}
	g := v.TestGates
	if g == nil || g.Mutation == nil || g.Mutation.Status != contract.GateNotApplicable || g.Mutation.Reason != contract.RungCriticalCodeNoMutation {
		t.Fatalf("test_gates.mutation: %+v", g)
	}
}

// config.rung "critical_code": when a changed file matches critical_code, or when the Stage 2 route has criticality_effective
// above low, Stage 5 runs mutation and the verdict carries no not_applicable mutation gate.
func TestRungCriticalCodeInsideOrMediumRouteRunsMutation(t *testing.T) {
	t.Run("inside critical_code", func(t *testing.T) {
		a, dir, _, _ := repoMutation(t, `,"rung":"critical_code","critical_code":["src/**"]`)
		os.Remove(filepath.Join(dir, ".a2dlc/roles.json"))
		st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "rung-cc-in"})
		if err != nil {
			t.Fatal(err)
		}
		if st.Stages[7].Status == contract.StageSkipped {
			t.Fatalf("Stage 5 skipped inside critical_code: %+v", st.Stages[7])
		}
		if rep := readMutation(t, dir, "rung-cc-in"); len(rep.Mutants) == 0 {
			t.Fatalf("no mutants measured: %+v", rep)
		}
		b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/rung-cc-in", ArtVerdict))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"mutation": {"status"`) || strings.Contains(string(b), "not part of this rung") {
			t.Fatalf("the verdict records a mutation gate: %s", b)
		}
	})
	t.Run("outside critical_code with medium route criticality", func(t *testing.T) {
		a, dir, _, _ := repoMutation(t, `,"rung":"critical_code","critical_code":["lib/**"]`)
		os.Remove(filepath.Join(dir, ".a2dlc/roles.json"))
		intent := intentFile(t, func(in *contract.Intent) {
			in.SliceID, in.Mode, in.Criticality = "rung-cc-route", contract.ModeLite, contract.CriticalityMedium
		})
		st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "rung-cc-route", Mode: contract.ModeLite, Intent: intent})
		if err != nil {
			t.Fatal(err)
		}
		if st.Stages[7].Status == contract.StageSkipped {
			t.Fatalf("Stage 5 skipped on medium route: %+v", st.Stages[7])
		}
		if rep := readMutation(t, dir, "rung-cc-route"); len(rep.Mutants) == 0 {
			t.Fatalf("no mutants measured: %+v", rep)
		}
		b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run/rung-cc-route", ArtVerdict))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `"mutation": {"status"`) || strings.Contains(string(b), "not part of this rung") {
			t.Fatalf("the verdict records a mutation gate: %s", b)
		}
	})
}

// config.rung "critical_code": in Stage 4, uncovered_change outside critical_code with a low route does not launch the
// Saboteur; inside critical_code (or on a medium+ route) it launches the Saboteur only for the critical uncovered files.
func TestRungCriticalCodeStage4SaboteurRespectsCriticality(t *testing.T) {
	t.Run("outside critical_code and low route skips saboteur", func(t *testing.T) {
		a, dir, _ := repoWithTests(t)
		write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"rung":"critical_code","critical_code":["lib/**"],"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
		git(t, dir, "add", ".a2dlc/config.json")
		git(t, dir, "commit", "-qm", "rung critical_code outside")
		write(t, dir, "src/f.py", "def f(x):\n    return x + 2\n")
		launcher := &fakeLauncher{}
		st, err := Run(context.Background(), a, launcher, Options{Slice: "sab-cc-out", MaxRounds: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(launcher.seen) != 0 {
			t.Fatalf("saboteur launched outside critical_code: %+v", launcher.seen)
		}
		s4 := st.Stages[6]
		if !strings.Contains(s4.Reason, contract.RungCriticalCodeNoSaboteur) || st.Outcome == nil || *st.Outcome != contract.INCONCLUSIVE {
			t.Fatalf("stage 4: %+v outcome=%v", s4, st.Outcome)
		}
	})
	t.Run("inside critical_code launches saboteur", func(t *testing.T) {
		a, dir, _ := repoWithTests(t)
		write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"rung":"critical_code","critical_code":["src/**"],"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
		git(t, dir, "add", ".a2dlc/config.json")
		git(t, dir, "commit", "-qm", "rung critical_code inside")
		write(t, dir, "src/f.py", "def f(x):\n    return x + 2\n")
		launcher := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
			resp, exit, to := fakeSaboteur("tests/test_sab.t", "t_sab passed\ncubre src/f.py:1-2\n")(s)
			resp.RunID = readRunID(t, s)
			return resp, exit, to
		}}
		st, err := Run(context.Background(), a, launcher, Options{Slice: "sab-cc-in", MaxRounds: 2})
		if err != nil {
			t.Fatal(err)
		}
		if len(launcher.seen) != 1 || st.Outcome == nil || *st.Outcome != contract.PASS {
			t.Fatalf("seen=%d outcome=%v stage4=%+v", len(launcher.seen), st.Outcome, st.Stages[6])
		}
	})
}

// config.rung "critical_code": when dual_spec_required is false and the route's criticality is low (track N=1), Stage 1
// skips launching specifier_alpha, specifier_beta, and inquisitor (0 LLM spec calls) even when roles.json defines them.
func TestRungCriticalCodeStage1SkipsDualSpecOnLowRoute(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"rung":"critical_code","critical_code":["lib/**"],"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{
		"specifier_alpha":{"family":"claude","image":"img:1","argv":["sh","a"]},
		"specifier_beta":{"family":"gemini","image":"img:1","argv":["sh","b"]},
		"inquisitor":{"family":"claude","image":"img:1","argv":["sh","i"]}
	}}`)
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "rung critical_code stage1")
	intent := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Criticality = "s1-cc-low", contract.ModeBrownfield, contract.CriticalityLow
	})
	launcher := &fakeLauncher{}
	st, err := Run(context.Background(), a, launcher, Options{Slice: "s1-cc-low", Mode: contract.ModeBrownfield, Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	if len(launcher.seen) != 0 {
		t.Fatalf("expected 0 LLM specifier calls on low route under rung critical_code, got %d: %+v", len(launcher.seen), launcher.seen)
	}
	s1 := st.Stages[2]
	if s1.Status != contract.StageSkipped || s1.Reason != contract.RungCriticalCodeNoDualSpec {
		t.Fatalf("expected Stage 1 skipped with RungCriticalCodeNoDualSpec, got: %+v", s1)
	}
}
