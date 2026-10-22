package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// Decision 2: a person's answer (a2dlc answer, answers.json) resolves a Stage 1 divergence on the next attempt. The
// escalation names the probe id; after the answer the specifiers see the decision as a resolved discrepancy and S* is
// written with it.
func TestStage1PersonAnswerResolvesTheDivergenceOnResume(t *testing.T) {
	a, launcherFake, opts, dir := prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = scriptSpec(t, 0)
	opts.MaxSpecRounds = 1
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil || st.Stages[2].Status != contract.StageEscalated {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	id, target := divergenceID("calc.media.v1", "mean: empty list")
	var escalations contract.Escalations
	b, _ := os.ReadFile(filepath.Join(dir, ArtEscalations))
	if err := contract.DecodeStrictBytes(b, &escalations); err != nil || len(escalations.Items) != 1 || escalations.Items[0].ID != id ||
		escalations.Items[0].Target != target || !strings.Contains(escalations.Items[0].Summary, "--probe "+id) {
		t.Fatalf("%v %+v", err, escalations)
	}
	if _, err := WriteAnswer(a, "calc.media.v1", contract.Answer{ProbeID: id, Choice: "alpha", By: "aldemar", Reason: "there is no mean of nothing"}); err != nil {
		t.Fatal(err)
	}
	var alpha []contract.SpecifierRequest
	script := launcherFake.script
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleSpecifierAlpha {
			var pl contract.SpecifierRequest
			payloadOf(t, s, &pl)
			alpha = append(alpha, pl)
		}
		return script(s)
	}
	st, err = Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	s1 := st.Stages[2]
	if s1.Status != contract.StageDone || s1.Iterations != 2 || !strings.Contains(s1.Reason, "1 discrepancies decided by a person (answers.json: mean: empty list)") {
		t.Fatalf("%+v", s1)
	}
	// the Inquisitor never resolved it: round 1 carries nothing (the answer is applied after the Inquisitor speaks), round 2 carries the decision
	if len(alpha) != 2 || len(alpha[0].Discrepancies) != 0 || len(alpha[1].Discrepancies) != 1 || alpha[1].Discrepancies[0].Resolution == nil ||
		!strings.Contains(*alpha[1].Discrepancies[0].Resolution, "a person decided (aldemar): alpha. there is no mean of nothing") {
		t.Fatalf("%+v", alpha)
	}
	if _, err := os.Stat(filepath.Join(dir, ArtContract)); err != nil {
		t.Fatal(err)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v", st.Outcome)
	}
}

// Decision 2 in Stage 1.5: only the choice "contract" accepts S* as written over the back-translation; any other answer
// keeps the escalation and the reason says why.
func TestStage15PersonAcceptsSStarOverTheBacktranslationOnResume(t *testing.T) {
	a, launcherFake, opts, dir := prepareGreenfield(t, "calc.media.v1")
	write(t, a.Repo.Dir, ".a2dlc/roles.json", strings.Replace(rolesSpec, `"inquisitor"`, `"back_translator":{"family":"mistral","image":"bt:1","argv":["x"],"retries":0},"inquisitor"`, 1))
	var bt contract.InquisitorBTRequest
	launcherFake.script = scriptBT(t, false, &bt)
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil || st.Stages[3].Status != contract.StageEscalated {
		t.Fatalf("%v %+v", err, st.Stages[3])
	}
	var escalations contract.Escalations
	b, _ := os.ReadFile(filepath.Join(dir, ArtEscalations))
	if err := contract.DecodeStrictBytes(b, &escalations); err != nil || len(escalations.Items) != 1 || !strings.Contains(escalations.Items[0].Summary, "--choice contract") {
		t.Fatalf("%v %+v", err, escalations)
	}
	id := escalations.Items[0].ID
	if _, err := WriteAnswer(a, "calc.media.v1", contract.Answer{ProbeID: id, Choice: "intent", By: "aldemar", Reason: "S* must say what happens with NaN"}); err != nil {
		t.Fatal(err)
	}
	st, err = Run(context.Background(), a, launcherFake, opts)
	if err != nil || st.Stages[3].Status != contract.StageEscalated || !strings.Contains(st.Stages[3].Reason, "the answer intent is not 'contract'") {
		t.Fatalf("%v %+v", err, st.Stages[3])
	}
	if _, err := WriteAnswer(a, "calc.media.v1", contract.Answer{ProbeID: id, Choice: "contract", By: "aldemar", Reason: "NaN is out of this slice's scope"}); err != nil {
		t.Fatal(err)
	}
	st, err = Run(context.Background(), a, launcherFake, opts)
	if err != nil || st.Stages[3].Status != contract.StageDone || st.Stages[3].Artifact == nil || st.Stages[3].Artifact.Path != ArtLint ||
		!strings.Contains(st.Stages[3].Reason, "1 points decided by a person as S* being right (answers.json: NaN in the list (by aldemar: NaN is out of this slice's scope))") {
		t.Fatalf("%v %+v", err, st.Stages[3])
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v", st.Outcome)
	}
}
