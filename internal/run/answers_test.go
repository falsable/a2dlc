package run

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// liteRun runs a lite slice to the end (no S*, no escalations) and returns the app and the run's folder.
func liteRun(t *testing.T) (app.App, string) {
	t.Helper()
	a, _ := repoLite(t)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1"}); err != nil {
		t.Fatal(err)
	}
	return a, filepath.Join(a.Repo.Dir, ".a2dlc", "run", "pago.v1")
}

// stateRunID reads the run id of a slice's state.json.
func stateRunID(t *testing.T, runDir string) string {
	t.Helper()
	var st contract.RunState
	b, err := os.ReadFile(filepath.Join(runDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &st); err != nil {
		t.Fatal(err)
	}
	return st.RunID
}

// An answer needs a probe, a choice, a reason and a person, and the run must have asked a probe (S* or the escalations).
func TestAnswerNeedsItsFieldsAndAProbe(t *testing.T) {
	ans := contract.Answer{ProbeID: "esc-1", Choice: "alpha", By: "aldemar", Reason: "the spec is strict"}
	missing := ans
	missing.Reason = " "
	if _, err := WriteAnswer(app.App{}, "pago.v1", missing); err == nil || !strings.Contains(err.Error(), "--reason is missing") {
		t.Fatalf("reason: %v", err)
	}
	a, _ := liteRun(t) // lite: no S* and no escalations
	if _, err := WriteAnswer(a, "pago.v1", ans); err == nil || !strings.Contains(err.Error(), "is not asked by this run") {
		t.Fatalf("a run that asked nothing: %v", err)
	}
}

// The file is bound to the run id and to the escalations the probe was asked about; a second answer for the same probe
// replaces the first; an answer whose artifact changed is not applied.
func TestAnswerIsBoundToRunAndArtifact(t *testing.T) {
	a, runDir := liteRun(t)
	escalations := filepath.Join(runDir, ArtEscalations)
	if err := os.WriteFile(escalations, []byte(`{"schema_version":1,"items":[{"id":"esc-1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	runID := stateRunID(t, runDir)
	// an answer to a probe the escalations do not ask is refused, whatever the file holds
	if _, err := WriteAnswer(a, "pago.v1", contract.Answer{ProbeID: "esc-9", Choice: "alpha", By: "aldemar", Reason: "x"}); err == nil || !strings.Contains(err.Error(), "esc-9") {
		t.Fatalf("a probe that was not asked: %v", err)
	}

	path, err := WriteAnswer(a, "pago.v1", contract.Answer{ProbeID: "esc-1", Choice: "alpha", By: "aldemar", Reason: "first"})
	if err != nil || filepath.Base(path) != ArtAnswers {
		t.Fatalf("%v %s", err, path)
	}
	if _, err := WriteAnswer(a, "pago.v1", contract.Answer{ProbeID: "esc-1", Choice: "beta", By: "aldemar", Reason: "second"}); err != nil {
		t.Fatal(err)
	}
	file, present, err := readAnswers(runDir, runID)
	if err != nil || !present || file.RunID != runID || len(file.Answers) != 1 || file.Answers[0].Choice != "beta" || file.Answers[0].At == "" {
		t.Fatalf("%v %+v", err, file)
	}
	want, err := HashFile(escalations)
	if err != nil || file.ContractHash != want {
		t.Fatalf("contract_hash %q, want the escalations' %q (%v)", file.ContractHash, want, err)
	}
	m := &Machine{dir: runDir, state: contract.RunState{RunID: runID}}
	if got, ok, reason, err := m.answerFor("esc-1", ArtEscalations); err != nil || !ok || got.Choice != "beta" || reason != "" {
		t.Fatalf("applies to its artifact: %v %v %+v %q", err, ok, got, reason)
	}
	if err := os.WriteFile(escalations, []byte(`{"schema_version":1,"items":[{"id":"esc-1"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(escalations, []byte(`{"schema_version":1,"items":[{"id":"esc-1","summary":"changed after the answer"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok, reason, _ := m.answerFor("esc-1", ArtEscalations); ok || !strings.Contains(reason, "escalations.json") {
		t.Fatalf("a changed escalation still applies: %v %q", ok, reason)
	}
}

// The file keeps one contract_hash, so answers bound to escalations.json and to contract.json cannot sit together: a write
// for the other artifact is refused and names the live answer it would drop. Two answers of the same artifact both stay. Once
// the escalations change, their answers are stale: the probe's answer is written and they are dropped without a refusal.
func TestAnswerRefusesToDropALiveAnswerOfAnotherArtifact(t *testing.T) {
	a, runDir := liteRun(t)
	runID := stateRunID(t, runDir)
	put := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(runDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put(ArtEscalations, `{"schema_version":1,"items":[{"id":"esc-1"},{"id":"esc-2"}]}`)
	put(ArtProbes, `{"schema_version":1,"probes":[{"id":"prb-1"}]}`)
	put(ArtContract, `{"schema_version":1}`)
	for _, probe := range []string{"esc-1", "esc-2"} {
		if _, err := WriteAnswer(a, "pago.v1", contract.Answer{ProbeID: probe, Choice: "alpha", By: "aldemar", Reason: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := WriteAnswer(a, "pago.v1", contract.Answer{ProbeID: "prb-1", Choice: "beta", By: "aldemar", Reason: "y"}); err == nil || !strings.Contains(err.Error(), "esc-1") {
		t.Fatalf("a live answer was dropped without a refusal: %v", err)
	}
	file, present, err := readAnswers(runDir, runID)
	if err != nil || !present || len(file.Answers) != 2 {
		t.Fatalf("the refused write changed the file: %v %+v", err, file)
	}
	put(ArtEscalations, `{"schema_version":1,"items":[{"id":"esc-1","summary":"changed after the answers"}]}`)
	if _, err := WriteAnswer(a, "pago.v1", contract.Answer{ProbeID: "prb-1", Choice: "beta", By: "aldemar", Reason: "y"}); err != nil {
		t.Fatalf("stale answers must not block the write: %v", err)
	}
	file, _, err = readAnswers(runDir, runID)
	if err != nil || len(file.Answers) != 1 || file.Answers[0].ProbeID != "prb-1" {
		t.Fatalf("%v %+v", err, file)
	}
}

// An answers.json of another run is refused on resume, not skipped: a2dlc run stops with the reason.
func TestAnswersOfAnotherRunAreRefusedOnResume(t *testing.T) {
	a, runDir := liteRun(t)
	other := `{"schema_version":1,"run_id":"run-000000000000","contract_hash":"sha256:` + strings.Repeat("a", 64) + `","answers":[]}`
	if err := os.WriteFile(filepath.Join(runDir, ArtAnswers), []byte(other), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkAnswersRun(runDir, stateRunID(t, runDir)); err == nil || !strings.Contains(err.Error(), "belongs to run run-000000000000") {
		t.Fatalf("%v", err)
	}
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1"}); err == nil || !strings.Contains(err.Error(), "belongs to run") {
		t.Fatalf("resume with another run's answers: %v", err)
	}
}

// Only the CLI writes answers.json: no role can see it, so none can write it either (CB-04 keeps .a2dlc out of /work).
func TestRolesNeverSeeAnswers(t *testing.T) {
	roles := []string{contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta, contract.RoleInquisitor, contract.RoleBackTranslator,
		contract.RoleBuilderPrimary, contract.RoleBuilderOrthogonal, contract.RoleSaboteur}
	for _, r := range roles {
		if slices.Contains(Visible(r), ArtAnswers) {
			t.Errorf("%s sees %s", r, ArtAnswers)
		}
	}
}
