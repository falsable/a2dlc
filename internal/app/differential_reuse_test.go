package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// RFC-0005: with config.run_differential "stage3", Check takes the diff answer Divergences got on the same tree instead of
// asking the engine again. Without the field, on another tree or with another request, it asks.

// commitRunDifferential commits a config with the engine over src/lib/** and run_differential set to value ("" leaves it out).
func commitRunDifferential(t *testing.T, dir, value string) {
	t.Helper()
	cfg := `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]`
	if value != "" {
		cfg += `,"run_differential":"` + value + `"`
	}
	write(t, dir, ".a2dlc/config.json", cfg+"}")
	git(t, dir, "commit", "-qam", "config", "--allow-empty")
}

func engineOp(v contract.Verdict, name string) bool {
	if v.Timings == nil {
		return false
	}
	for _, op := range v.Timings.EngineOps {
		if op.Name == name {
			return true
		}
	}
	return false
}

func TestCheckTakesStage3sDiffAnswerOnTheSameTree(t *testing.T) {
	a, m, dir := newRepo(t)
	commitRunDifferential(t, dir, contract.RunDifferentialStage3)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	found, _, answers, err := a.Divergences(context.Background(), "HEAD", "")
	if err != nil || len(found) != 1 || m.calls != 1 {
		t.Fatalf("found=%v calls=%d err=%v", found, m.calls, err)
	}
	v, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{Differential: answers})
	if err != nil || m.calls != 1 {
		t.Fatalf("the engine ran again: calls=%d err=%v", m.calls, err)
	}
	if len(v.Targets) != 1 || v.Targets[0].Verdict != contract.FAIL || v.Targets[0].Counterexample == nil || v.Targets[0].Counterexample.New != "export const f = 2\n" {
		t.Fatalf("the verdict must judge Stage 3's answer: %+v", v.Targets)
	}
	if !engineOp(v, "diff_from_stage3") || engineOp(v, "diff") {
		t.Fatalf("the receipt must say where the diff came from: %+v", v.Timings)
	}
	// A Saboteur round judges the same tree again: the answer is still there, and the first judgment did not change it.
	if v2, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{Differential: answers}); err != nil || m.calls != 1 || v2.Targets[0].Verdict != contract.FAIL {
		t.Fatalf("second check: calls=%d err=%v %+v", m.calls, err, v2.Targets)
	}
}

func TestCheckRunsTheDiffWhenStage3sAnswerDoesNotApply(t *testing.T) {
	cases := map[string]struct {
		value string
		opts  func(CheckOptions) CheckOptions
		edit  string // a change to the working tree after Stage 3 ("" none)
	}{
		"no field":               {value: "", opts: func(o CheckOptions) CheckOptions { return o }},
		"fresh":                  {value: contract.RunDifferentialFresh, opts: func(o CheckOptions) CheckOptions { return o }},
		"another tree":           {value: contract.RunDifferentialStage3, opts: func(o CheckOptions) CheckOptions { return o }, edit: "export const f = 3\n"},
		"relaxations in Stage 4": {value: contract.RunDifferentialStage3, opts: func(o CheckOptions) CheckOptions { o.EquivalenceRelaxations = []string{"r"}; return o }},
	}
	for name, c := range cases {
		a, m, dir := newRepo(t)
		commitRunDifferential(t, dir, c.value)
		write(t, dir, "src/lib/f.ts", "export const f = 2\n")
		_, _, answers, err := a.Divergences(context.Background(), "HEAD", "")
		if err != nil {
			t.Fatal(err)
		}
		if c.edit != "" {
			write(t, dir, "src/lib/f.ts", c.edit)
		}
		v, err := a.CheckWith(context.Background(), "HEAD", c.opts(CheckOptions{Differential: answers}))
		if err != nil || m.calls != 2 || !engineOp(v, "diff") || engineOp(v, "diff_from_stage3") {
			t.Fatalf("%s: calls=%d err=%v timings=%+v", name, m.calls, err, v.Timings)
		}
		if c.edit != "" && v.Targets[0].Counterexample.New != c.edit {
			t.Fatalf("%s: judged the old tree: %+v", name, v.Targets[0])
		}
	}
}

func TestRunDifferentialUnknownValueIsRefused(t *testing.T) {
	a, _, dir := newRepo(t)
	commitRunDifferential(t, dir, "always")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	if _, err := a.Check(context.Background(), "HEAD"); err == nil || !strings.Contains(err.Error(), `run_differential "always"`) {
		t.Fatalf("err=%v", err)
	}
}
