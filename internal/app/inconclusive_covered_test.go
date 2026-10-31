package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// An engine target that could not judge a file the candidate's tests executed completely stays INCONCLUSIVE with its reason
// and carries a note, and the diff is as without that engine for the file (a2dlc-38t).
const inconclusiveFile = "export const f = 1\nIMPL old\nINCONCLUSIVE\nline4\n"

func inconclusiveTarget(t *testing.T, v contract.Verdict) contract.VerdictTarget {
	t.Helper()
	if len(v.Targets) != 1 {
		t.Fatalf("targets=%+v", v.Targets)
	}
	return v.Targets[0]
}

func TestInconclusiveTargetCoveredByTestsDoesNotMakeTheDiffInconclusive(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", inconclusiveFile)
	write(t, dir, "tests/n.t", "t_covers passed\ncovers src/lib/f.ts:1-4\n")
	v, err := a.Check(context.Background(), "")
	if err != nil || v.Verdict != contract.PASS {
		t.Fatalf("v=%s err=%v cov=%+v targets=%+v", v.Verdict, err, v.Coverage, v.Targets)
	}
	tg := inconclusiveTarget(t, v)
	if tg.Verdict != contract.INCONCLUSIVE || tg.Reason != "flagged" || tg.Note != contract.NoteInconclusiveCoveredByTests {
		t.Fatalf("target=%+v", tg)
	}
	// The same file with no engine for it is the same diff verdict.
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL old\nNO_TARGET\nline4\n")
	w, _ := a.Check(context.Background(), "")
	if w.Verdict != v.Verdict {
		t.Fatalf("with the engine silent the verdict is %s, with no target %s", v.Verdict, w.Verdict)
	}
}

func TestInconclusiveTargetKeepsTheDiffInconclusiveWhereTheTestsDidNotReach(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", inconclusiveFile)
	// One changed line (4) that no test executed.
	write(t, dir, "tests/n.t", "t_covers passed\ncovers src/lib/f.ts:1-3\n")
	v, _ := a.Check(context.Background(), "")
	if v.Verdict != contract.INCONCLUSIVE || inconclusiveTarget(t, v).Note != "" {
		t.Fatalf("%s targets=%+v cov=%+v", v.Verdict, v.Targets, v.Coverage)
	}
	// No coverage in the run: nothing was executed as far as anyone can tell.
	write(t, dir, "tests/n.t", "t_none passed\n")
	v, _ = a.Check(context.Background(), "")
	if v.Verdict != contract.INCONCLUSIVE || inconclusiveTarget(t, v).Note != "" {
		t.Fatalf("%s targets=%+v cov=%+v", v.Verdict, v.Targets, v.Coverage)
	}
}

func TestInconclusiveTargetOnCriticalCodeOrStrictRunStaysInconclusive(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, ".a2dlc/config.json", strings.Replace(gatesConfig, `"tests":`, `"critical_code":["src/lib/**"],"tests":`, 1))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "critical")
	write(t, dir, "src/lib/f.ts", inconclusiveFile)
	write(t, dir, "tests/n.t", "t_covers passed\ncovers src/lib/f.ts:1-4\n")
	v, _ := a.Check(context.Background(), "")
	if v.Verdict != contract.INCONCLUSIVE || inconclusiveTarget(t, v).Note != "" {
		t.Fatalf("critical: %s targets=%+v", v.Verdict, v.Targets)
	}
	// A strict run (everything critical) is the same without critical_code.
	write(t, dir, ".a2dlc/config.json", gatesConfig)
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "not critical")
	v, _ = a.CheckWith(context.Background(), "", CheckOptions{})
	if v.Verdict != contract.PASS {
		t.Fatalf("not critical: %s targets=%+v", v.Verdict, v.Targets)
	}
	v, _ = a.CheckWith(context.Background(), "", CheckOptions{StrictDivergence: true})
	if v.Verdict != contract.INCONCLUSIVE || inconclusiveTarget(t, v).Note != "" {
		t.Fatalf("strict: %s targets=%+v", v.Verdict, v.Targets)
	}
}

func TestInconclusiveTargetWithoutATestsBlockStaysInconclusive(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "no critical code")
	write(t, dir, "src/lib/f.ts", inconclusiveFile)
	v, _ := a.Check(context.Background(), "HEAD")
	if v.Verdict != contract.INCONCLUSIVE || inconclusiveTarget(t, v).Note != "" {
		t.Fatalf("%s targets=%+v", v.Verdict, v.Targets)
	}
}

// verify decides again from the receipt: the note is valid only where the receipt's own coverage shows every changed line
// executed, on an INCONCLUSIVE target, and a behavior_divergence note stays refused on an INCONCLUSIVE one.
func TestVerifyRefusesAnInconclusiveNoteTheCoverageDoesNotSupport(t *testing.T) {
	a, _, _, _ := gatesRepo(t)
	tgt := func(verdict, note string) contract.VerdictTarget {
		return contract.VerdictTarget{TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: verdict, Reason: "flagged"}, Note: note}
	}
	cov := func(status string, uncovered ...int) *contract.Coverage {
		return &contract.Coverage{Status: status, Files: []contract.CoverageFile{{File: "src/lib/f.ts", ByEngine: []int{}, Covered: []int{1}, Uncovered: uncovered}}}
	}
	cases := []struct {
		name string
		v    contract.Verdict
		want int
	}{
		{"supported", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.INCONCLUSIVE, contract.NoteInconclusiveCoveredByTests)}, Coverage: cov(contract.CoverageRan)}, 0},
		{"an unexamined line", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.INCONCLUSIVE, contract.NoteInconclusiveCoveredByTests)}, Coverage: cov(contract.CoverageRan, 3)}, 1},
		{"coverage not run", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.INCONCLUSIVE, contract.NoteInconclusiveCoveredByTests)}, Coverage: cov(contract.CoverageUnavailable)}, 1},
		{"no coverage in the receipt", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.INCONCLUSIVE, contract.NoteInconclusiveCoveredByTests)}}, 1},
		{"on a PASS target", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.PASS, contract.NoteInconclusiveCoveredByTests)}, Coverage: cov(contract.CoverageRan)}, 1},
		{"a divergence note on an INCONCLUSIVE target", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.INCONCLUSIVE, contract.NoteBehaviorDivergence)}, Coverage: cov(contract.CoverageRan)}, 1},
		{"an unknown note", contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tgt(contract.INCONCLUSIVE, "whatever")}, Coverage: cov(contract.CoverageRan)}, 1},
	}
	for _, c := range cases {
		if got := a.noteProblems(c.v); len(got) != c.want {
			t.Errorf("%s: problems=%v", c.name, got)
		}
	}
}
