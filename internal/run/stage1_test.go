package run

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

const rolesSpec = `{"schema_version":1,"roles":{
 "specifier_alpha":{"family":"claude","image":"a:1","argv":["x"],"retries":0},
 "specifier_beta":{"family":"gemini","image":"b:1","argv":["x"],"retries":0},
 "inquisitor":{"family":"openai","image":"i:1","argv":["x"],"retries":0}}}`

func contractOf(source, post string) contract.SliceContract {
	signature := "def mean(xs: list[float]) -> float"
	return contract.SliceContract{SchemaVersion: 1, SliceID: "calc.media.v1", Source: source,
		Functions: []contract.SpecFunction{{Name: "mean", Signature: &signature, Pre: []string{"finite xs"}, Post: []string{post},
			Invariants: []string{}, Errors: []contract.SpecError{}}},
		Probes:      []contract.Probe{{ID: "BP-01", Description: "one", Input: "mean([2.5])", Expected: "2.5", Kind: "example", Assumption: ptr("listas-vacias")}},
		Assumptions: []contract.Assumption{{ID: "listas-vacias", Statement: "empty is an error", Source: "catalogue", Status: "accepted"}},
		PiS:         contract.PiS{Equivalence: "strict", Relaxations: []string{}}}
}

func ptr(s string) *string { return &s }

func ok(t *testing.T, s LaunchSpec, payload any) *contract.RoleResponse {
	t.Helper()
	pl, _ := json.Marshal(payload)
	return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
		Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.01}, Payload: pl}
}

// payloadOf reads the payload of the request.json that the core mounted in /in.
func payloadOf(t *testing.T, s LaunchSpec, v any) {
	t.Helper()
	var req contract.RoleRequest
	b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	if err := contract.DecodeStrictBytes(b, &req); err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(req.Payload, v); err != nil {
		t.Fatalf("%s: %v", s.Role, err)
	}
}

// scriptSpec: α and β differ in a post; the Inquisitor resolves the discrepancy at round resolveIn (0: never).
func scriptSpec(t *testing.T, resolveIn int) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		switch s.Role {
		case contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta:
			var pl contract.SpecifierRequest
			payloadOf(t, s, &pl)
			post := "raises ValueError with empty xs"
			if s.Role == contract.RoleSpecifierBeta {
				post = "returns 0.0 with empty xs"
			}
			return ok(t, s, contract.SpecifierResponse{Contract: contractOf(s.Role, post)}), 0, false
		}
		var pl contract.InquisitorRequest
		payloadOf(t, s, &pl)
		var res *string
		if pl.Round == resolveIn {
			r := "raises ValueError: the intent says there is no mean of nothing"
			res = &r
		}
		return ok(t, s, contract.InquisitorResponse{Contract: contractOf(contract.SourceConsensus, "raises ValueError with empty xs"),
			Discrepancies:   []contract.SpecDiscrepancy{{Topic: "mean: empty list", Alpha: "raises ValueError", Beta: "returns 0.0", Resolution: res}},
			OpenAssumptions: []contract.Assumption{{ID: "nan", Statement: "does NaN propagate?", Source: "specifier", Status: "open"}}}), 0, false
	}
}

// prepareGreenfield: repoLite with the catalogue committed to the base, Stage 1 roles and intent.json for the run.
// It returns the App, the launcher, the options and the run folder.
func prepareGreenfield(t *testing.T, slice string) (app.App, *fakeLauncher, Options, string) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/catalogue.json", `{"schema_version":1,"domain":"numeric","entries":[
 {"id":"listas-vacias","category":"empty lists","question":"What happens with empty xs?"},
 {"id":"rounding","category":"rounding mode","question":"Does it round?"}]}`)
	git(t, dir, "add", ".a2dlc/catalogue.json")
	git(t, dir, "commit", "-qm", "catalogue")
	write(t, dir, ".a2dlc/roles.json", rolesSpec)
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal, in.Invariants = slice, contract.ModeGreenfield, "arithmetic mean", []string{"there is no mean of nothing"}
	})
	return a, &fakeLauncher{}, Options{Slice: slice, Mode: contract.ModeGreenfield, Intent: path}, filepath.Join(dir, ".a2dlc/run", slice)
}

// dump copies real artifacts of a run to $A2DLC_VOLCADO with name <schema>__<testCase>.json, to validate them
// with contracts/tests/validate.py (third argument): it is the only thing that checks the Go output against the schemas.
func dump(t *testing.T, run string, names map[string]string) {
	t.Helper()
	dest := os.Getenv("A2DLC_VOLCADO")
	if dest == "" {
		return
	}
	os.MkdirAll(dest, 0o755)
	for de, a := range names {
		b, err := os.ReadFile(filepath.Join(run, filepath.FromSlash(de)))
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dest, a), b, 0o644)
	}
}

func TestStage1DualSpecDivergesInRound1(t *testing.T) {
	a, launcherFake, opts, dir := prepareGreenfield(t, "calc.media.v1")
	run := func() string { return dir }
	launcherFake.script = scriptSpec(t, 1)
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	s1 := st.Stages[2]
	if s1.ID != "1" || s1.Status != contract.StageDone || s1.Artifact == nil || s1.Artifact.Path != ArtContract || s1.Iterations != 1 {
		t.Fatalf("%+v", s1)
	}
	// the open assumption stays in S* and the reason says so; the "rounding" entry from the catalogue was not instantiated either
	if !strings.Contains(s1.Reason, "1 open assumptions") || !strings.Contains(s1.Reason, "rounding") {
		t.Fatalf("reason %q", s1.Reason)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v", st.Outcome)
	}
	// §3 policy: α and β see intent and request; never the other's contractJSON. The Inquisitor, the two and the catalogue.
	want := []string{"intent.json,request.json", "intent.json,request.json", "catalogue.json,contract_alpha.json,contract_beta.json,request.json"}
	if len(launcherFake.seen) != 3 {
		t.Fatalf("launches: %v", launcherFake.seen)
	}
	for i, q := range want {
		if strings.Join(launcherFake.seen[i], ",") != q {
			t.Fatalf("launch %d saw %v", i, launcherFake.seen[i])
		}
	}
	var s contract.SliceContract
	b, _ := os.ReadFile(filepath.Join(run(), ArtContract))
	if err := contract.DecodeStrictBytes(b, &s); err != nil || s.Validate() != nil || s.Source != contract.SourceConsensus || len(s.Assumptions) != 2 || s.Assumptions[1].ID != "nan" {
		t.Fatalf("%v %+v", err, s)
	}
	var p contract.Probes
	b, _ = os.ReadFile(filepath.Join(run(), ArtProbes))
	h, _ := HashFile(filepath.Join(run(), ArtContract))
	if err := contract.DecodeStrictBytes(b, &p); err != nil || p.ContractHash != h || h != s1.Artifact.SHA256 || len(p.Probes) != 1 {
		t.Fatalf("%v %+v", err, p)
	}
	for _, artifact := range []string{ArtContractAlpha, ArtContractBeta, ArtCatalogue} {
		if _, err := os.Stat(filepath.Join(run(), artifact)); err != nil {
			t.Fatal(err)
		}
	}
	var lg contract.RunLedger
	b, _ = os.ReadFile(filepath.Join(run(), "ledger.json"))
	if err := contract.DecodeStrictBytes(b, &lg); err != nil || len(lg.Entries) != 3 || lg.Entries[2].Role != contract.RoleInquisitor || st.Budget.SpentUSD < 0.029 {
		t.Fatalf("%v %+v", err, lg.Entries)
	}
	for _, in := range lg.Entries[0].Inputs {
		if strings.Contains(in.Path, "contract_beta") {
			t.Fatal("the ledger says α saw contract_beta")
		}
	}
	if problems, err := VerifyRun(a, "calc.media.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	dump(t, run(), map[string]string{ArtContract: "slice-contract__run-consensus.json", ArtContractBeta: "slice-contract__run-beta.json",
		ArtProbes: "probes__run.json", ArtCatalogue: "assumption-catalogue__run.json",
		"launches/001-1-specifier_alpha-1/in/request.json": "role-request__run-alpha.json",
		"launches/003-1-inquisitor-1/in/request.json":      "role-request__run-inquisitor.json"})
}

func TestStage1PersistentDivergenceEscalates(t *testing.T) {
	a, launcherFake, opts, dir := prepareGreenfield(t, "calc.media.v1")
	run := func() string { return dir }
	launcherFake.script = scriptSpec(t, 0)
	opts.MaxSpecRounds = 2
	var second contract.SpecifierRequest
	script := launcherFake.script
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleSpecifierAlpha && len(launcherFake.seen) == 4 {
			payloadOf(t, s, &second)
		}
		return script(s)
	}
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	s1 := st.Stages[2]
	if s1.Status != contract.StageEscalated || s1.Iterations != 2 || !strings.Contains(s1.Reason, "CB-01") || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", s1, st.Outcome)
	}
	if st.Stages[3].Status != contract.StagePending || st.Stages[6].Status != contract.StagePending {
		t.Fatalf("the run went on after escalating: %+v", st.Stages[6])
	}
	// round 2: α receives the unresolved discrepancy and still does not see β's contractJSON
	if second.Round != 2 || len(second.Discrepancies) != 1 || second.Discrepancies[0].Topic != "mean: empty list" ||
		strings.Join(launcherFake.seen[3], ",") != "intent.json,request.json" || len(launcherFake.seen) != 6 {
		t.Fatalf("%+v %v", second, launcherFake.seen)
	}
	var escalations contract.Escalations
	b, _ := os.ReadFile(filepath.Join(run(), ArtEscalations))
	if err := contract.DecodeStrictBytes(b, &escalations); err != nil || len(escalations.Items) != 1 || escalations.Items[0].Kind != "spec_divergence" ||
		escalations.Items[0].Target != "calc.media.v1::mean: empty list" || !strings.Contains(escalations.Items[0].Evidence.Message, "β: returns 0.0") ||
		escalations.Items[0].ClusterID != contract.ClusterID("spec_divergence", escalations.Items[0].Evidence.Message) {
		t.Fatalf("%v %+v", err, escalations)
	}
	for _, artifact := range []string{ArtContract, ArtProbes} {
		if _, err := os.Stat(filepath.Join(run(), artifact)); err == nil {
			t.Fatalf("%s written without consensus", artifact)
		}
	}
	var lg contract.RunLedger
	b, _ = os.ReadFile(filepath.Join(run(), "ledger.json"))
	if err := contract.DecodeStrictBytes(b, &lg); err != nil || len(lg.Entries) != 6 {
		t.Fatalf("%v %d", err, len(lg.Entries))
	}
	dump(t, run(), map[string]string{ArtEscalations: "escalation__run-spec.json",
		"launches/004-1-specifier_alpha-1/in/request.json": "role-request__run-alpha-ronda2.json"})
}

func TestStage1SkipsOrFailsWithReason(t *testing.T) {
	// greenfield without intent: the specifiers have nothing to translate
	a, _ := repoLite(t)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "s1", Mode: contract.ModeGreenfield})
	if err != nil || st.Stages[2].Status != contract.StageSkipped || !strings.Contains(st.Stages[2].Reason, "without intent.json") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// without the inquisitor there is no dual-spec, and nothing is launched
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"specifier_alpha":{"family":"c","image":"a","argv":["x"]},"specifier_beta":{"family":"g","image":"b","argv":["x"]}}}`)
	path := intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "s2", contract.ModeBrownfield, "x" })
	launcherFake := &fakeLauncher{}
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "s2", Mode: contract.ModeBrownfield, Intent: path})
	if err != nil || st.Stages[2].Status != contract.StageSkipped || !strings.Contains(st.Stages[2].Reason, "missing inquisitor") || len(launcherFake.seen) != 0 {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// lite has no S*
	a, _ = repoLite(t)
	if st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "s3"}); err != nil || !strings.Contains(st.Stages[2].Reason, "lite mode") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// β refuses: escalated with its reason
	a, launcherFake, opts, _ := prepareGreenfield(t, "calc.media.v1")
	script := scriptSpec(t, 1)
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleSpecifierBeta {
			r := ok(t, s, map[string]any{})
			r.Status, r.Reason = contract.RoleRefused, "the intent does not say what to do with empty lists"
			return r, 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageEscalated || !strings.Contains(st.Stages[2].Reason, "specifier_beta refused") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// the inquisitor cites an entry that is not in the catalogue: the catalogue governs, the stage fails and is INCONCLUSIVE
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			c := contractOf(contract.SourceConsensus, "raises ValueError")
			c.Assumptions = append(c.Assumptions, contract.Assumption{ID: "time-zone", Statement: "UTC", Source: "catalogue", Status: "accepted"})
			return ok(t, s, contract.InquisitorResponse{Contract: c, Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, "time-zone") || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// a catalogue assumption accepted without consensus probe: agreement is not correctness, the stage fails (§5 Stage 1 step 4)
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			c := contractOf(contract.SourceConsensus, "raises ValueError")
			c.Probes[0].Assumption = nil
			return ok(t, s, contract.InquisitorResponse{Contract: c, Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, "missing consensus probe: the catalogue assumption listas-vacias") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// two accepted catalogue assumptions without a probe: the rejection names both, not only the first
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			c := contractOf(contract.SourceConsensus, "raises ValueError")
			c.Probes[0].Assumption = nil
			c.Assumptions = append(c.Assumptions, contract.Assumption{ID: "rounding", Statement: "no rounding", Source: "catalogue", Status: "accepted"})
			return ok(t, s, contract.InquisitorResponse{Contract: c, Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, "the catalogue assumptions listas-vacias, rounding are accepted") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// the same omission fixed on the retry: the second request carries the rejection reason and the stage closes S*
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	var retried contract.InquisitorRequest
	inquisitorCalls := 0
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			inquisitorCalls++
			c := contractOf(contract.SourceConsensus, "raises ValueError")
			if inquisitorCalls == 1 {
				c.Probes[0].Assumption = nil
			} else {
				payloadOf(t, s, &retried)
			}
			return ok(t, s, contract.InquisitorResponse{Contract: c, Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageDone || inquisitorCalls != 2 ||
		!strings.Contains(retried.Rejection, "missing consensus probe: the catalogue assumption listas-vacias") || retried.Round != 1 {
		t.Fatalf("%v %+v calls=%d rejection=%q", err, st.Stages[2], inquisitorCalls, retried.Rejection)
	}
	// a specifier that signs its contract as the other one is retried with the reason; a second wrong answer fails the stage
	for _, fixed := range []bool{true, false} {
		a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
		var retriedSpec contract.SpecifierRequest
		alphaCalls := 0
		launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
			if s.Role == contract.RoleSpecifierAlpha {
				alphaCalls++
				if alphaCalls == 2 {
					payloadOf(t, s, &retriedSpec)
				}
				if alphaCalls == 1 || !fixed {
					return ok(t, s, contract.SpecifierResponse{Contract: contractOf(contract.RoleSpecifierBeta, "raises ValueError")}), 0, false
				}
			}
			return script(s)
		}
		st, err = Run(context.Background(), a, launcherFake, opts)
		want := contract.StageDone
		if !fixed {
			want = contract.StageFailed
		}
		if err != nil || st.Stages[2].Status != want || alphaCalls != 2 || !strings.Contains(retriedSpec.Rejection, `says source "specifier_beta"`) || retriedSpec.Round != 1 ||
			(!fixed && !strings.Contains(st.Stages[2].Reason, "after 2 attempts")) {
			t.Fatalf("fixed=%v %v %+v calls=%d rejection=%q", fixed, err, st.Stages[2], alphaCalls, retriedSpec.Rejection)
		}
	}
	// a probe that cites an assumption that does not exist in S*: outside contractJSON (Validate)
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			c := contractOf(contract.SourceConsensus, "raises ValueError")
			c.Probes[0].Assumption = ptr("phantom")
			return ok(t, s, contract.InquisitorResponse{Contract: c, Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, "phantom") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// α and β of the same declared family: the stage runs and notes it (§5 asks for different families)
	a, launcherFake, opts, dir = prepareGreenfield(t, "calc.media.v1")
	// the same family needs the written exception of roles.json (family_exception): the reason goes into the stage reason
	root := filepath.Join(dir, "..", "..", "..")
	sameFamily := strings.Replace(rolesSpec, `"family":"gemini"`, `"family":"claude"`, 1)
	write(t, root, ".a2dlc/roles.json", strings.Replace(sameFamily, "{", `{"family_exception":{"reason":"test: one licence"},`, 1))
	launcherFake.script = scriptSpec(t, 1)
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageDone ||
		!strings.Contains(st.Stages[2].Reason, "same declared family (claude)") || !strings.Contains(st.Stages[2].Reason, "test: one licence") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	// the specifiers and the inquisitor are told the engine language of the repo (absent means python)
	a, launcherFake, opts, dir = prepareGreenfield(t, "calc.media.v1")
	write(t, filepath.Join(dir, "..", "..", ".."), ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"x","paths":["src/**"],"budget":{}}]}`)
	var langs []string
	base := scriptSpec(t, 1)
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		switch s.Role {
		case contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta:
			var pl contract.SpecifierRequest
			payloadOf(t, s, &pl)
			langs = append(langs, pl.EngineLanguage)
		case contract.RoleInquisitor:
			var pl contract.InquisitorRequest
			payloadOf(t, s, &pl)
			langs = append(langs, pl.EngineLanguage)
		}
		return base(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageDone {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
	if strings.Join(langs, ",") != "typescript,typescript,typescript" {
		t.Fatalf("engine_language sent to the roles: %v", langs)
	}
	// α returns a candidate with β's source: outside contractJSON, failed
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleSpecifierAlpha {
			return ok(t, s, contract.SpecifierResponse{Contract: contractOf(contract.SourceBeta, "x")}), 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, "source") {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}
}

// engineSignatures is an engine.Runner that only gives signatures: mean from src/calc.py.
type engineSignatures struct{ requests int }

func (f *engineSignatures) Run(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Response, error) {
	return contract.Response{}, nil
}

func (f *engineSignatures) Stub(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.StubResponse, map[string][]byte, error) {
	return contract.StubResponse{}, nil, nil
}

// Traces answers the trace harness green with full branch coverage, so the refactor capture has no reason to stop.
func (f *engineSignatures) Traces(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.TracesResponse, error) {
	zero, full := 0, 1.0
	return contract.TracesResponse{TestExit: &zero, Traces: []contract.CaptureTrace{}, BranchCoverage: &full}, nil
}

func (f *engineSignatures) Signatures(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.SignaturesResponse, error) {
	f.requests++
	files := []contract.SignatureFile{}
	for _, c := range req.ChangedFiles {
		files = append(files, contract.SignatureFile{File: c, Symbols: []contract.Signature{{Name: "mean", Signature: "def mean(xs: list[float]) -> float", Lines: []int{1, 2}}}})
	}
	return contract.SignaturesResponse{SchemaVersion: 1, Files: files}, nil
}

func (f *engineSignatures) Tier0(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Tier0Response, error) {
	// an engine that reviews and finds nothing: Tier 0 counts as executed (an ErrWithoutTier0 would escalate Stage 3)
	return contract.Tier0Response{SchemaVersion: contract.SchemaVersion, OK: true, Findings: []contract.Tier0Finding{}}, nil
}

func (f *engineSignatures) Mutate(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.MutateResponse, map[string][]byte, error) {
	return contract.MutateResponse{}, nil, errors.New("mutate operation not supported")
}

func (f *engineSignatures) Complexity(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.ComplexityResponse, error) {
	return contract.ComplexityResponse{}, engine.ErrWithoutComplexity
}

var _ engine.Runner = (*engineSignatures)(nil)

func TestStage1RefactorCaptureWithoutLLM(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],
 "tests":{"image":"x","paths":["tests/**"],"command":"run"}}`)
	write(t, dir, "src/calc.py", "def mean(xs):\n    return sum(xs) / len(xs)\n")
	write(t, dir, "tests/test_calc.t", "from src.calc import mean\n")
	write(t, dir, "tests/test_other.t", "calculate passed\n") // 'calculate' is not the word 'calc'
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "refactor base")
	write(t, dir, "src/calc.py", "def mean(xs):\n    return sum(xs) / float(len(xs))\n")
	write(t, dir, "tests/test_new.t", "calc\n")   // it is not in the base: it is not part of the capture
	write(t, dir, ".a2dlc/roles.json", rolesSpec) // even if they are there, refactor does not launch specifiers
	engine := &engineSignatures{}
	a.Runner = engine
	launcherFake := &fakeLauncher{}
	m, err := open(a, launcherFake, Options{Slice: "calc.refactor", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	st, reason, artifact, err := stageSpec(context.Background(), m)
	if err != nil || st != contract.StageDone || artifact != ArtContract || reason != "" || len(launcherFake.seen) != 0 || engine.requests != 1 {
		t.Fatalf("%v %s %q %s seen=%v", err, st, reason, artifact, launcherFake.seen)
	}
	var c contract.Capture
	b, _ := os.ReadFile(filepath.Join(m.dir, ArtCapture))
	if err := contract.DecodeStrictBytes(b, &c); err != nil || strings.Join(c.Tests, ",") != "tests/test_calc.t" || strings.Join(c.ChangedFiles, ",") != "src/calc.py" ||
		len(c.Signatures) != 1 || c.Selection != selectionCapture || c.BaseSha != m.baseSha {
		t.Fatalf("%v %+v", err, c)
	}
	var s contract.SliceContract
	b, _ = os.ReadFile(filepath.Join(m.dir, ArtContract))
	if err := contract.DecodeStrictBytes(b, &s); err != nil || s.Validate() != nil || len(s.Functions) != 1 || s.Functions[0].Name != "src/calc.py::mean" ||
		s.PiS.Equivalence != "strict" || s.Source != contract.SourceConsensus || len(s.Probes) != 0 {
		t.Fatalf("%v %+v", err, s)
	}
	var p contract.Probes
	b, _ = os.ReadFile(filepath.Join(m.dir, ArtProbes))
	if h, _ := HashFile(filepath.Join(m.dir, ArtContract)); contract.DecodeStrictBytes(b, &p) != nil || p.ContractHash != h || p.Probes == nil {
		t.Fatalf("%+v", p)
	}
	dump(t, m.dir, map[string]string{ArtCapture: "capture__run.json", ArtContract: "slice-contract__run-refactor.json"})
}

// TestBrownfieldStage1MatchesTheRoute: the reason that Stage 2 leaves in route.json about brownfield_facade's Stage 1
// must describe what Stage 1 did: it launched the specifiers (dual-spec) or wrote capture.json (capture).
func TestBrownfieldStage1MatchesTheRoute(t *testing.T) {
	a, launcherFake, opts, run := prepareGreenfield(t, "calc.media.v1")
	opts.Mode = contract.ModeBrownfield
	opts.Intent = intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal, in.Invariants = "calc.media.v1", contract.ModeBrownfield, "arithmetic mean", []string{"there is no mean of nothing"}
		in.Criticality = contract.CriticalityHigh
	})
	// with high criticality Stage 1.5 stops at an open assumption or a post without probe, before the router: the
	// Inquisitor leaves no open assumptions and its probe names the function and shares a word with the post
	script := scriptSpec(t, 1)
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			c := contractOf(contract.SourceConsensus, "returns the average")
			c.Probes[0].Description = "mean returns the average of the list"
			return ok(t, s, contract.InquisitorResponse{Contract: c,
				Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
		}
		return script(s)
	}
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	if s1 := st.Stages[2]; s1.ID != "1" || s1.Status != contract.StageDone {
		t.Fatalf("%+v", s1)
	}
	launchedSpecifiers := len(launcherFake.seen) >= 2
	_, errCapture := os.Stat(filepath.Join(run, ArtCapture))
	hasCapture := errCapture == nil
	var r contract.Route
	b, err := os.ReadFile(filepath.Join(run, ArtRoute))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &r); err != nil {
		t.Fatal(err)
	}
	reasons := strings.Join(r.Reasons, "|")
	saysByCapture := strings.Contains(reasons, "behavior capture")
	saysDualSpec := strings.Contains(reasons, "runs dual-spec")
	if saysByCapture != hasCapture || saysDualSpec != launchedSpecifiers || saysByCapture == saysDualSpec {
		t.Fatalf("route.json says capture=%v dual-spec=%v; Stage 1 launched specifiers=%v and wrote capture.json=%v: %s", saysByCapture, saysDualSpec, launchedSpecifiers, hasCapture, reasons)
	}
	if r.DualSpecRequired {
		t.Fatalf("dual-spec is not mandatory in brownfield_facade: %+v", r)
	}
}

// TestStage1BaseCatalogueAndRolesThatDoNotRespond: the catalogue comes from the base and not from the working tree, an
// Inquisitor that refuses escalates, a role without response after its retries fails (INCONCLUSIVE), and a base
// catalogue outside contractJSON fails without launching anyone.
func TestStage1BaseCatalogueAndRolesThatDoNotRespond(t *testing.T) {
	// the working tree loses "rounding"; the base keeps it and the Inquisitor receives it
	a, launcherFake, opts, run := prepareGreenfield(t, "calc.media.v1")
	repo := filepath.Dir(filepath.Dir(filepath.Dir(run)))
	write(t, repo, catalogPath, `{"schema_version":1,"domain":"numeric","entries":[
 {"id":"listas-vacias","category":"empty lists","question":"What happens with empty xs?"}]}`)
	script := scriptSpec(t, 1)
	var received []string
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			var pl contract.InquisitorRequest
			payloadOf(t, s, &pl)
			if pl.Catalogue == nil {
				t.Fatal("the Inquisitor did not receive the catalogue")
			}
			for _, e := range pl.Catalogue.Entries {
				received = append(received, e.ID)
			}
		}
		return script(s)
	}
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	var inRun contract.AssumptionCatalogue
	b, _ := os.ReadFile(filepath.Join(run, ArtCatalogue))
	if err := contract.DecodeStrictBytes(b, &inRun); err != nil || len(inRun.Entries) != 2 || len(received) != 2 || received[1] != "rounding" {
		t.Fatalf("%v catalogue.json %+v, the Inquisitor received %v", err, inRun, received)
	}
	if s1 := st.Stages[2]; s1.Status != contract.StageDone || !strings.Contains(s1.Reason, "did not instantiate these catalogue entries: rounding") {
		t.Fatalf("%+v", s1)
	}

	// the Inquisitor refuses: escalated
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	script = scriptSpec(t, 1)
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleInquisitor {
			r := ok(t, s, map[string]any{})
			r.Status, r.Reason = contract.RoleRefused, "the two candidates cannot be compared"
			return r, 0, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageEscalated || !strings.Contains(st.Stages[2].Reason, "inquisitor refused") || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}

	// α does not respond (exits with 1) and has no retries: failed and INCONCLUSIVE
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	script = scriptSpec(t, 1)
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleSpecifierAlpha {
			return nil, 1, false
		}
		return script(s)
	}
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, "specifier_alpha did not give a valid answer") || st.Outcome == nil || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v %+v", err, st.Stages[2])
	}

	// the catalogue committed to the base does not meet its contractJSON: failed before launching anyone
	a, launcherFake, opts, run = prepareGreenfield(t, "calc.media.v1")
	repo = filepath.Dir(filepath.Dir(filepath.Dir(run)))
	write(t, repo, catalogPath, `{"schema_version":1}`)
	git(t, repo, "add", catalogPath)
	git(t, repo, "commit", "-qm", "broken catalogue")
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[2].Status != contract.StageFailed || !strings.Contains(st.Stages[2].Reason, catalogPath+" of the base out of contract") || len(launcherFake.seen) != 0 {
		t.Fatalf("%v %+v %v", err, st.Stages[2], launcherFake.seen)
	}
}
