package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// config with the engine over src/lib/** and the given critical_code ("" leaves the field out), committed so that the base has it.
func commitConfig(t *testing.T, dir, critical string) {
	t.Helper()
	cfg := `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]`
	if critical != "" {
		cfg += `,"critical_code":` + critical
	}
	write(t, dir, ".a2dlc/config.json", cfg+"}")
	git(t, dir, "commit", "-qam", "config", "--allow-empty")
}

// 1. Without the field, a divergence is a note on its target with the counterexample, and the verdict is not FAIL for it.
func TestDivergenceIsANoteWhenNothingIsDeclaredCritical(t *testing.T) {
	a, _, dir := newRepo(t)
	commitConfig(t, dir, "")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.Targets) != 1 {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	got := v.Targets[0]
	if got.Verdict != contract.FAIL || got.Note != contract.NoteBehaviorDivergence || got.Counterexample == nil || got.Waived {
		t.Fatalf("the target must keep its FAIL and counterexample and carry the note: %+v", got)
	}
	if len(v.PendingWaivers) != 0 {
		t.Fatalf("a note asks for no waiver: %v", v.PendingWaivers)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil || len(esc.Items) != 0 {
		t.Fatalf("a note asks nothing of a person: %+v %v", esc.Items, err)
	}
	// The note is its own category: nothing lands in the coverage notes.
	for _, f := range v.Coverage.Files {
		if len(f.Noted) != 0 || len(f.Uncovered) != 0 {
			t.Fatalf("coverage: %+v", f)
		}
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify accepts a note on code that is not critical: %v %v", probs, err)
	}
}

// 2. The same divergence on declared critical code (by file, by glob or by file::symbol) is FAIL, as before.
func TestDivergenceOnCriticalCodeStaysFail(t *testing.T) {
	for _, critical := range []string{`["src/lib/f.ts"]`, `["src/lib/**"]`, `["src/lib/f.ts::f"]`, `["other/**","src/*/f.ts::*"]`} {
		a, _, dir := newRepo(t)
		commitConfig(t, dir, critical)
		write(t, dir, "src/lib/f.ts", "export const f = 2\n")
		v, err := a.Check(context.Background(), "HEAD")
		if err != nil || v.Verdict != contract.FAIL || v.Targets[0].Note != "" {
			t.Fatalf("critical_code %s: v=%s note=%q err=%v", critical, v.Verdict, v.Targets[0].Note, err)
		}
	}
}

// Critical code declared for another file or another symbol leaves this divergence a note.
func TestDivergenceOutsideTheCriticalGlobsIsANote(t *testing.T) {
	for _, critical := range []string{`["src/lib/g.ts"]`, `["src/lib/f.ts::g"]`, `["src/other/**"]`} {
		a, _, dir := newRepo(t)
		commitConfig(t, dir, critical)
		write(t, dir, "src/lib/f.ts", "export const f = 2\n")
		v, err := a.Check(context.Background(), "HEAD")
		if err != nil || v.Verdict != contract.PASS || v.Targets[0].Note != contract.NoteBehaviorDivergence {
			t.Fatalf("critical_code %s: v=%s note=%q err=%v", critical, v.Verdict, v.Targets[0].Note, err)
		}
	}
}

// The base's config decides: a diff cannot drop its own critical code in the same change.
func TestDiffCannotDropItsOwnCriticalCode(t *testing.T) {
	a, _, dir := newRepo(t)
	commitConfig(t, dir, `["src/lib/**"]`)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.FAIL {
		t.Fatalf("v=%s err=%v", v.Verdict, err)
	}
}

// 3. Critical code with a signed waiver behaves as today: PASS, and verify accepts it only with the signature.
func TestCriticalDivergenceWithSignedWaiverIsAsBefore(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a, _, dir := newRepo(t)
	commitConfig(t, dir, `["src/lib/**"]`)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)

	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"on purpose"}]}`)
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 1 || !v.Targets[0].Waived || v.Targets[0].Note != "" {
		t.Fatalf("unsigned: %+v %v", v, err)
	}
	if err := a.Approve("src/lib/f.ts::f", key, "me@a2dlc"); err != nil {
		t.Fatal(err)
	}
	if v, err = a.Check(context.Background(), "HEAD"); err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 0 {
		t.Fatalf("signed: %+v %v", v, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "signed")
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify signed: %v %v", probs, err)
	}
}

// 4. An INCONCLUSIVE target is the same with or without critical code.
func TestInconclusiveTargetIsNotChangedByCriticalCode(t *testing.T) {
	for _, critical := range []string{"", `["src/lib/**"]`} {
		a, _, dir := newRepo(t)
		commitConfig(t, dir, critical)
		write(t, dir, "src/lib/f.ts", "INCONCLUSIVE\n")
		v, err := a.Check(context.Background(), "HEAD")
		if err != nil || v.Verdict != contract.INCONCLUSIVE || v.Targets[0].Note != "" {
			t.Fatalf("critical_code %q: v=%s note=%q err=%v", critical, v.Verdict, v.Targets[0].Note, err)
		}
	}
}

// A divergence note does not hide an INCONCLUSIVE next to it, and a FAIL on critical code still wins over a note.
func TestNoteDoesNotMaskOtherTargets(t *testing.T) {
	a, _, dir := newRepo(t)
	commitConfig(t, dir, `["src/lib/g.ts"]`)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, "src/lib/g.ts", "export const g = 1\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.FAIL || len(v.Targets) != 2 {
		t.Fatalf("v=%s err=%v", v.Verdict, err)
	}
	notes := 0
	for _, tg := range v.Targets {
		if tg.Note != "" {
			notes++
			if tg.Target != "src/lib/f.ts::f" {
				t.Fatalf("the note is on %s", tg.Target)
			}
		}
	}
	if notes != 1 {
		t.Fatalf("notes=%d", notes)
	}
}

// verify does not take a note on critical code at its word: the receipt is a file the agent can write, and the base's
// config says what is critical.
func TestVerifyRefusesANoteOnCriticalCode(t *testing.T) {
	a, _, dir := newRepo(t)
	commitConfig(t, dir, `["src/lib/**"]`)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.FAIL {
		t.Fatalf("v=%s err=%v", v.Verdict, err)
	}
	// A forged receipt: PASS, with the critical divergence relabeled as a note.
	v.Verdict, v.Targets[0].Note = contract.PASS, contract.NoteBehaviorDivergence
	if err := a.write(pathVerdict, v); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")
	probs, err := a.Verify("HEAD")
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "critical code src/lib/f.ts::f") {
		t.Fatalf("verify: %v %v", probs, err)
	}
}

// A note that is not what Aggregate would write (no counterexample, a waiver, another category) is refused.
func TestVerifyRefusesAMalformedNote(t *testing.T) {
	a, _, _ := newRepo(t)
	cx := &contract.Counterexample{Args: "[]", Old: "1", New: "2"}
	for name, tg := range map[string]contract.VerdictTarget{
		"no counterexample": {TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: contract.FAIL}, Note: contract.NoteBehaviorDivergence},
		"waived":            {TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: contract.FAIL, Counterexample: cx}, Waived: true, Note: contract.NoteBehaviorDivergence},
		"not a FAIL":        {TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: contract.PASS, Counterexample: cx}, Note: contract.NoteBehaviorDivergence},
		"other category":    {TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: contract.FAIL, Counterexample: cx}, Note: "defensive_handler"},
	} {
		probs := a.noteProblems(contract.Verdict{BaseRef: "HEAD", Targets: []contract.VerdictTarget{tg}})
		if len(probs) != 1 || !strings.Contains(probs[0], "is not a behavior divergence") {
			t.Errorf("%s: %v", name, probs)
		}
	}
}
