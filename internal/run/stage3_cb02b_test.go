package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// rolesWithTierUp: builder_primary declares a tier_up (gemini) for CB-02b.
const rolesWithTierUp = `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0,
 "tier_up":{"family":"gemini","image":"img:2","argv":["sh","y"],"retries":0}}}}`

const fixed = "def f(x):\n    return x * 2\n"

// alwaysBroken: every round of the builder leaves a BROKEN that Tier 0 flags.
func alwaysBroken(contract.BuilderRequest) map[string]string {
	return map[string]string{"src/f.py": "ROTO\n"}
}

// Without tier_up, the rounds run out as in CB-02b and leave a builder_exhaustion escalation with the slice's target.
func TestCB02bExhaustedWithoutTierUpEscalatesWithItem(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, alwaysBroken)}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cb2b", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-02b: ") || !strings.Contains(s3.Reason, "roles.json declares no tier_up") ||
		len(requests) != 2 || len(launcherFake.seen) != 2 || st.Stages[6].Status != contract.StagePending || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %d %d %+v %v", s3, len(requests), len(launcherFake.seen), st.Stages[6], st.Outcome)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/cb2b", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 {
		t.Fatalf("%+v", escalations.Items)
	}
	it := escalations.Items[0]
	if it.Kind != KindBuilderExhaustion || it.Target != "cb2b::builder" || len(it.ResolveWith) != 0 || !strings.HasPrefix(it.ID, "esc-") ||
		!strings.Contains(it.Summary, "builder_primary: 2 rounds of Tier 0 without ok") || !strings.Contains(it.Summary, "Decomposing the slice is a person's decision") ||
		!strings.Contains(it.Evidence.Message, "invalid syntax") {
		t.Fatalf("%+v", it)
	}
	var lg contract.RunLedger
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/cb2b", "ledger.json"), &lg)
	if len(lg.Entries) != 2 {
		t.Fatalf("the ledger has %d entries, not 2 (without tier_up there is no extra round)", len(lg.Entries))
	}
}

// With a tier_up that converges, there is one extra round, the stage stays done, with the CB-02b note and one more launch record.
func TestCB02bTierUpResolvesAndContinues(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesWithTierUp)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Round < 3 {
			return alwaysBroken(pl)
		}
		return map[string]string{"src/f.py": fixed}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cb2b-ok", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || s3.Iterations != 3 || s3.Artifact == nil || s3.Artifact.Path != ArtCandidate ||
		!strings.Contains(s3.Reason, "CB-02b: builder exhausted after 2 rounds; the higher tier (gemini) resolved it") {
		t.Fatalf("%+v", s3)
	}
	// the extra round carries the last feedback, and the limit the round states: 3 of 3
	if len(requests) != 3 || requests[2].Round != 3 || requests[2].MaxRounds != 3 || requests[2].Tier0Feedback == nil {
		t.Fatalf("%+v", requests)
	}
	var lg contract.RunLedger
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/cb2b-ok", "ledger.json"), &lg)
	if len(lg.Entries) != 3 || lg.Entries[2].Family != "gemini" || lg.Entries[2].Stage != "3" || lg.Entries[2].Role != contract.RoleBuilderPrimary {
		t.Fatalf("%+v", lg.Entries)
	}
	// Stage 4 may write its own escalations.json (empty here): what cannot be there is a builder_exhaustion
	withoutExhaustion(t, filepath.Join(dir, ".a2dlc/run/cb2b-ok", ArtEscalations))
	if problems, err := VerifyRun(a, "cb2b-ok"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

// If the tier_up does not give a green Tier 0 either, the stage escalates as CB-02b and the escalation says the tier_up tried.
func TestCB02bTierUpFailsEscalates(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesWithTierUp)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, alwaysBroken)}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cb2b-no", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-02b: ") || !strings.Contains(s3.Reason, "the tier_up (gemini) spent an extra round and still did not give green Tier 0") ||
		s3.Iterations != 3 || len(requests) != 3 || st.Stages[6].Status != contract.StagePending || *st.Outcome != "escalated" {
		t.Fatalf("%+v %d %+v", s3, len(requests), st.Outcome)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/cb2b-no", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != KindBuilderExhaustion ||
		!strings.Contains(escalations.Items[0].Summary, "the tier_up (gemini) spent an extra round and still did not give green Tier 0") {
		t.Fatalf("%+v", escalations.Items)
	}
	var lg contract.RunLedger
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/cb2b-no", "ledger.json"), &lg)
	if len(lg.Entries) != 3 || lg.Entries[2].Family != "gemini" {
		t.Fatalf("%+v", lg.Entries)
	}
}

// A tier_up that refuses is also a failure of the tier: the builder's reason ends up in the CB-02b reason.
func TestCB02bTierUpRefusalEscalates(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesWithTierUp)
	var requests []contract.BuilderRequest
	base := fakeBuilder(t, &requests, alwaysBroken)
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if len(requests) == 2 { // the third call is the tier_up's round
			return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
				Status: contract.RoleRefused, Reason: "I do not know how to do it", Payload: json.RawMessage(`{}`)}, 0, false
		}
		return base(s)
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cb2b-refuses", MaxInner: 2})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-02b: ") || !strings.Contains(s3.Reason, "did not meet it: the builder refused: I do not know how to do it") {
		t.Fatalf("%+v", s3)
	}
}

// Two builders that run out (each with its own tier_up) at N=2: a single builder_exhaustion escalation that names both.
func TestCB02bN2WithoutCandidateEscalatesWithItem(t *testing.T) {
	withRouterN2(t)
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{
 "builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0,
  "tier_up":{"family":"gemini","image":"img:3","argv":["sh","z"],"retries":0}},
 "builder_orthogonal":{"family":"gemini","image":"img:2","argv":["sh","y"],"retries":0,
  "tier_up":{"family":"claude","image":"img:4","argv":["sh","w"],"retries":0}}}}`)
	var seen []launchRecord
	broken := func(string, contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "ROTO\n"}
	}
	st, err := Run(context.Background(), a, &fakeLauncher{script: buildersN2(t, &seen, broken)}, Options{Slice: "n2cb", MaxInner: 1})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-02b: N=2 without a candidate: ") || len(seen) != 4 ||
		st.Stages[6].Status != contract.StagePending || *st.Outcome != "escalated" {
		t.Fatalf("%+v %d %v", s3, len(seen), st.Outcome)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/n2cb", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != KindBuilderExhaustion || escalations.Items[0].Target != "n2cb::builder" ||
		!strings.Contains(escalations.Items[0].Summary, "builder_primary: 1 rounds") || !strings.Contains(escalations.Items[0].Summary, "builder_orthogonal: 1 rounds") {
		t.Fatalf("%+v", escalations.Items)
	}
}

// A retry that converges does not inherit the builder_exhaustion escalation left by the previous run of the stage.
func TestCB02bRetryRemovesOldEscalation(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, alwaysBroken)}
	if st, err := Run(context.Background(), a, launcherFake, Options{Slice: "retry", MaxInner: 1}); err != nil || st.Stages[5].Status != contract.StageEscalated {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
	path := filepath.Join(dir, ".a2dlc/run/retry", ArtEscalations)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the first run was supposed to leave the escalation: %v", err)
	}
	launcherFake = &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string { return map[string]string{"src/f.py": fixed} })}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "retry", MaxInner: 1})
	if err != nil || st.Stages[5].Status != contract.StageDone {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
	withoutExhaustion(t, path)
}

// The removal only applies to the CB-02b escalation: an item of another kind in the same file stays, and with no items left the file is deleted.
func TestCB02bRemovesOnlyItsEscalation(t *testing.T) {
	dir := t.TempDir()
	m := &Machine{dir: dir}
	write(t, dir, ArtEscalations, `{"schema_version":1,"tree_hash":"git-tree:`+strings.Repeat("0", 40)+`","created_at":"2026-10-17T12:00:00Z","items":[
 {"id":"esc-000000000001","kind":"builder_exhaustion","target":"s::builder","summary":"x","evidence":{},"resolve_with":[]},
 {"id":"esc-000000000002","kind":"spec_divergence","target":"s::tema","summary":"y","evidence":{},"resolve_with":[]}]}`)
	if err := m.removePriorExhaustion(); err != nil {
		t.Fatal(err)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "spec_divergence" {
		t.Fatalf("%+v", escalations.Items)
	}
	write(t, dir, ArtEscalations, `{"schema_version":1,"tree_hash":"git-tree:`+strings.Repeat("0", 40)+`","created_at":"2026-10-17T12:00:00Z","items":[
 {"id":"esc-000000000001","kind":"builder_exhaustion","target":"s::builder","summary":"x","evidence":{},"resolve_with":[]}]}`)
	if err := m.removePriorExhaustion(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ArtEscalations)); err == nil {
		t.Fatal("without items the file must not remain")
	}
}

// withoutExhaustion fails if the escalations.json at the path has a builder_exhaustion; if it does not exist, it passes.
func withoutExhaustion(t *testing.T, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	var escalations contract.Escalations
	if err := contract.DecodeStrictBytes(b, &escalations); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	for _, e := range escalations.Items {
		if e.Kind == KindBuilderExhaustion {
			t.Fatalf("a builder_exhaustion remains in %s: %s", path, b)
		}
	}
}
