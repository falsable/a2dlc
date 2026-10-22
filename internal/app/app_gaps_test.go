package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// budgetEngine is the fake engine with a diff budget in its response, as an engine that ran out of time reports it.
type budgetEngine struct {
	*fakeEngine
	budget *contract.DiffBudget
}

func (b budgetEngine) Run(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error) {
	r, err := b.fakeEngine.Run(ctx, e, req, baseDir, candDir)
	r.DiffBudget = b.budget
	return r, err
}

// A protected file that was deleted has no blob to approve: it stays INCONCLUSIVE, with its escalation.
func TestProtectedDeletedFileIsInconclusive(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"protected_paths":["tests/human/**"]}`)
	write(t, dir, "tests/human/test_calc.py", "def test_add():\n    assert 1 + 1 == 2\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "protect tests/human")
	os.Remove(filepath.Join(dir, "tests/human/test_calc.py"))
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("deleted protected file: v=%s err=%v", v.Verdict, err)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil {
		t.Fatal(err)
	}
	for _, e := range esc.Items {
		if e.Kind == "protected_path" && e.Target == "tests/human/test_calc.py::*" {
			return
		}
	}
	t.Fatalf("no protected_path escalation for the deleted file: %+v", esc.Items)
}

// The hook's message names the protected paths that have no valid approval, read from the last check's escalations.
func TestHookMessageNamesUnapprovedProtectedPaths(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"protected_paths":["tests/human/**"]}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "protect tests/human")
	// Written after the commit: a protected file that is committed is not a change, and there is nothing to escalate.
	write(t, dir, "tests/human/test_calc.py", "def test_add():\n    assert 1 + 1 == 2\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	notes := a.protectedNotes()
	if !strings.Contains(notes, "tests/human/test_calc.py::*") {
		t.Fatalf("protected notes: %q", notes)
	}
	msg := message(v, nil, notes)
	if !strings.Contains(msg, "Protected paths without a valid approval") || !strings.Contains(msg, "tests/human/test_calc.py::*") {
		t.Fatalf("hook message does not name the protected path:\n%s", msg)
	}
	if strings.Contains(message(v, nil, ""), "Protected paths") {
		t.Fatal("the protected section appears without notes")
	}
}

// A two-target response with the relaxation marker turns every target INCONCLUSIVE, the PASS one too: the relaxation
// is diff-wide.
func TestEquivalenceUnsupportedCoversEveryTargetOfTheResponse(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2 // UNSUPPORTED\n")
	write(t, dir, "src/lib/g.ts", "IMPL added\n")
	relax := "compare receipts without trace_id"
	v, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{EquivalenceRelaxations: []string{relax}})
	if err != nil || len(v.Targets) != 2 || v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	for _, tg := range v.Targets {
		if tg.Verdict != contract.INCONCLUSIVE || !strings.Contains(tg.Reason, relax) {
			t.Fatalf("target not covered by the relaxation: %+v", tg)
		}
	}
}

// The file-level waiver of a protected path sets no intent and covers no target; an engine-target waiver of
// enforce_new_behavior sets intent; a test decision on an engine target covers nothing at engine level.
func TestWaiverFlags(t *testing.T) {
	waived, intent := waiverFlags(map[string]contract.Waiver{
		"src/lib/f.ts::*": {Target: "src/lib/f.ts::*", Decision: contract.EnforceNewBehavior},
	})
	if len(waived) != 0 || intent {
		t.Fatalf("a file-level waiver must set nothing: waived=%v intent=%v", waived, intent)
	}
	waived, intent = waiverFlags(map[string]contract.Waiver{
		"src/lib/f.ts::f": {Target: "src/lib/f.ts::f", Decision: contract.EnforceNewBehavior},
	})
	if !waived["src/lib/f.ts::f"] || !intent {
		t.Fatalf("an enforce_new_behavior waiver on a function: waived=%v intent=%v", waived, intent)
	}
	waived, intent = waiverFlags(map[string]contract.Waiver{
		"src/lib/g.ts::g": {Target: "src/lib/g.ts::g", Decision: contract.CharacterizationTest},
	})
	if waived["src/lib/g.ts::g"] || intent {
		t.Fatalf("a test decision covers no engine target: waived=%v intent=%v", waived, intent)
	}
}

// An engine's diff budget reaches the verdict: a cut function the engine answered is INCONCLUSIVE, and a cut function
// nobody answered keeps the verdict from PASS and is named in the message.
func TestDiffBudgetCutsAreNeverSilent(t *testing.T) {
	a, m, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nIMPL added\n") // PASS for f

	a.Runner = budgetEngine{m, &contract.DiffBudget{TotalS: 30, UsedS: 30, Cut: []contract.DiffCut{{Target: "src/lib/f.ts::f", Reason: "over budget"}}}}
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE || len(v.Targets) != 1 || v.Targets[0].Verdict != contract.INCONCLUSIVE ||
		!strings.Contains(v.Targets[0].Reason, "over budget") {
		t.Fatalf("answered cut: v=%+v err=%v", v, err)
	}
	if v.DiffBudget == nil || v.DiffBudget.TotalS != 30 || len(v.DiffBudget.Cut) != 1 {
		t.Fatalf("budget not copied into the verdict: %+v", v.DiffBudget)
	}

	a.Runner = budgetEngine{m, &contract.DiffBudget{TotalS: 30, UsedS: 30, Cut: []contract.DiffCut{{Target: "src/lib/g.ts::g", Reason: "over budget"}}}}
	v, err = a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE || len(v.DiffBudget.Cut) != 1 {
		t.Fatalf("unanswered cut must not PASS: v=%s err=%v", v.Verdict, err)
	}
	if msg := message(v, nil, ""); !strings.Contains(msg, "src/lib/g.ts::g: not judged") {
		t.Fatalf("the unanswered cut is not named:\n%s", msg)
	}

	a.Runner = m
	if v, err = a.Check(context.Background(), "HEAD"); err != nil || v.DiffBudget != nil {
		t.Fatalf("an engine without budget must leave it absent: %+v %v", v.DiffBudget, err)
	}
}

// Complexity judges with the base's config, as check does: a working tree that drops the glob cannot take the file
// out of the measure.
func TestComplexityJudgesWithTheBasesConfig(t *testing.T) {
	a, _, dir := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "if a\n"})
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["other/**"],"budget":{}}]}`)
	r, err := a.Complexity(context.Background(), "HEAD", c)
	if err != nil || len(r.Files) != 1 || r.Files[0].File != "src/lib/f.ts" || len(r.Unmeasured) != 0 {
		t.Fatalf("the working tree's config took the file out of the measure: %v %+v", err, r)
	}
}

// SaveVerdict writes the diff budget's cut as an array, as CheckWith does.
func TestSaveVerdictWritesCutAsArray(t *testing.T) {
	a, _, dir := newRepo(t)
	v := contract.Verdict{SchemaVersion: contract.SchemaVersion, Verdict: contract.PASS, DiffBudget: &contract.DiffBudget{TotalS: 60, UsedS: 3}}
	if err := a.SaveVerdict(v); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, pathVerdict))
	if err != nil || !strings.Contains(string(b), `"cut": []`) {
		t.Fatalf("cut is not an array: %s %v", b, err)
	}
}
