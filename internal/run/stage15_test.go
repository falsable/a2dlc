package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

func findings(l contract.ContractLint, check, sev string) []contract.LintFinding {
	var out []contract.LintFinding
	for _, f := range l.Findings {
		if f.Check == check && f.Severity == sev {
			out = append(out, f)
		}
	}
	return out
}

func TestLintMissingFunctionIsError(t *testing.T) {
	s := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
	s.Functions[0].Name = "src/calc.py::median"
	s.Probes[0].Input = "median([2.5])"
	idx := map[string]map[string]bool{"src/calc.py": {"mean": true}}
	l := Lint(s, "sha256:x", idx, "", false, 0)
	if l.OK || len(findings(l, "function_exists", contract.SeverityError)) != 1 {
		t.Fatalf("%+v", l)
	}
	// with a builder configured it may still not exist: Stage 3 writes it
	if l := Lint(s, "sha256:x", idx, "", true, 0); !l.OK || len(findings(l, "function_exists", contract.SeverityWarning)) != 1 {
		t.Fatalf("%+v", l)
	}
	// a name without a file may live in a file that the diff does not touch: a warning, not an error
	s.Functions[0].Name, s.Probes[0].Input = "other", "other(1)"
	if l := Lint(s, "sha256:x", idx, "", false, 0); !l.OK || len(findings(l, "function_exists", contract.SeverityWarning)) != 1 {
		t.Fatalf("%+v", l)
	}
	// without signatures it is not checked, and it says so
	if l := Lint(s, "sha256:x", nil, "no engine signatures", false, 0); !l.OK || !strings.Contains(findings(l, "function_exists", contract.SeverityWarning)[0].Message, "no engine signatures") {
		t.Fatalf("%+v", l)
	}
	// repeated names, blank clause, probe that names nobody, critical open assumption
	s = contractOf(contract.SourceConsensus, "raises ValueError")
	s.Functions = append(s.Functions, s.Functions[0])
	s.Functions[1].Post = []string{"  "}
	s.Probes = append(s.Probes, contract.Probe{ID: "BP-02", Description: "nothing", Input: "x", Expected: "y", Kind: "example"})
	s.Assumptions = append(s.Assumptions, contract.Assumption{ID: "nan", Statement: "NaN?", Source: "specifier", Status: "open", Criticality: "critical"})
	l = Lint(s, "sha256:x", nil, "", false, 0)
	for _, c := range []string{"duplicate_function", "empty_clause", "probe_function", "open_assumption"} {
		if len(findings(l, c, contract.SeverityError)) != 1 {
			t.Errorf("%s: %+v", c, l.Findings)
		}
	}
	// π_S relaxed without relaxations
	s = contractOf(contract.SourceConsensus, "raises ValueError")
	s.PiS.Equivalence = contract.EquivalenceRelaxed
	if l := Lint(s, "sha256:x", nil, "", false, 0); l.OK || len(findings(l, "pi_s", contract.SeverityError)) != 1 {
		t.Fatalf("%+v", l)
	}
}

func TestCriticalityComesFromIntent(t *testing.T) {
	// the criticality comes from the intent already validated in m.intent; without intent it is 0
	if n := (&Machine{}).criticalityIntent(); n != 0 {
		t.Fatalf("without intent: %d", n)
	}
	for _, c := range []string{"low", "high", "critical"} {
		m := &Machine{intent: &contract.Intent{Criticality: c}}
		if n := m.criticalityIntent(); n != contract.CriticalityLevel(c) || n == 0 {
			t.Fatalf("%s: %d", c, n)
		}
	}
}

func TestTraceCoverageDetectsPostWithoutProbe(t *testing.T) {
	s := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
	s.Functions[0].Post = append(s.Functions[0].Post, "does not modify xs")
	s.Functions[0].Invariants = []string{"min(xs) <= mean(xs)"}
	s.Probes = []contract.Probe{{ID: "BP-01", Description: "naïve empty list is rejected", Input: "mean([])", Expected: "raises ValueError", Kind: "negative"},
		{ID: "BP-02", Description: "the minimum bounds it", Input: "mean([1, 3])", Expected: "2 >= min", Kind: "example"}}
	l := Lint(s, "sha256:x", nil, "", false, 0)
	// 'naïve' matches with its accent (the rule does not use regexp's \b); 'modify' does not appear in any probe of mean
	if !l.OK || len(l.TraceCoverage.Uncovered) != 1 || l.TraceCoverage.Uncovered[0].Clause != "does not modify xs" ||
		l.TraceCoverage.Uncovered[0].Kind != "post" || l.TraceCoverage.Severity != contract.SeverityWarning || l.TraceCoverage.Rule != traceRule {
		t.Fatalf("%+v", l.TraceCoverage)
	}
	if len(findings(l, "trace_coverage", contract.SeverityWarning)) != 1 {
		t.Fatalf("%+v", l.Findings)
	}
	// a probe that does not name the function does not cover it, even though it shares the word
	s.Probes = append(s.Probes, contract.Probe{ID: "BP-03", Description: "nobody does modify anything", Input: "other()", Expected: "x", Kind: "example"})
	s.Functions = append(s.Functions, contract.SpecFunction{Name: "other", Pre: []string{}, Post: []string{"x"}, Invariants: []string{}, Errors: []contract.SpecError{}})
	if l := Lint(s, "sha256:x", nil, "", false, 0); len(l.TraceCoverage.Uncovered) != 1 {
		t.Fatalf("%+v", l.TraceCoverage)
	}
	// with an intent of criticality high the post without probe is an error and the lint does not pass
	if l := Lint(s, "sha256:x", nil, "", false, 3); l.OK || l.TraceCoverage.Severity != contract.SeverityError || len(findings(l, "trace_coverage", contract.SeverityError)) != 1 {
		t.Fatalf("%+v", l)
	}
}

// writeContract leaves S* in the run as Stage 1 would.
func writeContract(t *testing.T, m *Machine, s contract.SliceContract) {
	t.Helper()
	if _, err := m.writeArtifact(ArtContract, s); err != nil {
		t.Fatal(err)
	}
}

func readLint(t *testing.T, m *Machine) contract.ContractLint {
	t.Helper()
	var l contract.ContractLint
	b, _ := os.ReadFile(filepath.Join(m.dir, ArtLint))
	if err := contract.DecodeStrictBytes(b, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestStage15LintEscalatesBeforeSpending(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	write(t, dir, ".a2dlc/roles.json", strings.Replace(rolesSpec, `"inquisitor"`, `"back_translator":{"family":"mistral","image":"bt:1","argv":["x"],"retries":0},"inquisitor"`, 1))
	a.Runner = &engineSignatures{}
	launcherFake := &fakeLauncher{}
	m, err := open(a, launcherFake, Options{Slice: "calc.media.v1", Mode: contract.ModeGreenfield})
	if err != nil {
		t.Fatal(err)
	}
	// without contract.json there is nothing to validate
	if st, reason, _, err := stageContractLint(context.Background(), m); err != nil || st != contract.StageSkipped || !strings.Contains(reason, "no contract.json") {
		t.Fatalf("%v %s %q", err, st, reason)
	}
	write(t, dir, ".a2dlc/run/calc.media.v1/intent.json", `{"goal":"mean"}`)
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	s.Functions[0].Name = "src/f.py::median" // engineSignatures gives mean from src/f.py, the changed file
	s.Probes[0].Input = "median([2.5])"
	writeContract(t, m, s)
	st, reason, artifact, err := stageContractLint(context.Background(), m)
	if err != nil || st != contract.StageEscalated || artifact != ArtLint || !strings.Contains(reason, "a2dlc-lint: 1 errors") || !strings.Contains(reason, "fix the code") || strings.Contains(reason, "editing contract.json does not help") || len(launcherFake.seen) != 0 {
		t.Fatalf("%v %s %q %s %v", err, st, reason, artifact, launcherFake.seen)
	}
	l := readLint(t, m)
	h, _ := HashFile(filepath.Join(m.dir, ArtContract))
	if l.OK || l.ContractHash != h || findings(l, "function_exists", contract.SeverityError)[0].Where != "src/f.py::median" {
		t.Fatalf("%+v", l)
	}
	dump(t, m.dir, map[string]string{ArtLint: "contract-lint__run-error.json"})
	// a contract.json that does not decode is also a lint error, not a machine one
	write(t, dir, ".a2dlc/run/calc.media.v1/contract.json", `{"schema_version":1}`)
	// and that error is not fixed by hand: the message does not promise a correction that Stage 1 would overwrite
	if st, reason, _, err := stageContractLint(context.Background(), m); err != nil || st != contract.StageEscalated || len(findings(readLint(t, m), "contract", contract.SeverityError)) != 1 ||
		!strings.Contains(reason, "editing contract.json does not help") || !strings.Contains(reason, "deleting .a2dlc/run/calc.media.v1") {
		t.Fatalf("%v %s %q", err, st, reason)
	}
}

// scriptBT: Stage 1 as scriptSpec (consensus in round 1) and in 1.5 the Back-translator and the Inquisitor that compares;
// matches sets what the Inquisitor answers. It stores the 1.5 Inquisitor's payload in bt.
func scriptBT(t *testing.T, matches bool, bt *contract.InquisitorBTRequest) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	stage1 := scriptSpec(t, 1)
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Stage != "1.5" {
			return stage1(s)
		}
		if s.Role == contract.RoleBackTranslator {
			var pl contract.BackTranslatorRequest
			payloadOf(t, s, &pl)
			return ok(t, s, contract.BTText{Prose: "mean returns the average; with the empty list raises ValueError", Examples: []string{"mean([2.5]) returns 2.5"}}), 0, false
		}
		payloadOf(t, s, bt)
		r := contract.InquisitorBTResponse{Matches: true, Discrepancies: []contract.BTDiscrepancy{}}
		if !matches {
			r = contract.InquisitorBTResponse{Matches: false, Discrepancies: []contract.BTDiscrepancy{{Clause: "NaN in the list",
				IntentSays: "a NaN is an error of the caller", ContractImplies: "S* says nothing about NaN"}}}
		}
		return ok(t, s, r), 0, false
	}
}

func TestStage15BackTranslationMismatchEscalates(t *testing.T) {
	a, launcherFake, opts, dir := prepareGreenfield(t, "calc.media.v1")
	// the back_translator is of α's family: it runs anyway and the reason says so
	write(t, a.Repo.Dir, ".a2dlc/roles.json", strings.Replace(rolesSpec, `"inquisitor"`, `"back_translator":{"family":"claude","image":"bt:1","argv":["x"],"retries":0},"inquisitor"`, 1))
	var bt contract.InquisitorBTRequest
	launcherFake.script = scriptBT(t, false, &bt)
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	s15 := st.Stages[3]
	if s15.ID != "1.5" || s15.Status != contract.StageEscalated || s15.Artifact == nil || s15.Artifact.Path != ArtEscalations ||
		!strings.Contains(s15.Reason, "in 1 points") || !strings.Contains(s15.Reason, "of family claude") || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", s15, st.Outcome)
	}
	if st.Stages[4].Status != contract.StagePending {
		t.Fatalf("the run went on after escalating: %+v", st.Stages[4])
	}
	// §3: the Back-translator sees only S*; the 1.5 Inquisitor does not receive intent.json through /in, only in the payload
	if len(launcherFake.seen) != 5 || strings.Join(launcherFake.seen[3], ",") != "contract.json,request.json" ||
		strings.Join(launcherFake.seen[4], ",") != "catalogue.json,contract_alpha.json,contract_beta.json,request.json" {
		t.Fatalf("%v", launcherFake.seen)
	}
	var intent map[string]any
	if json.Unmarshal(bt.Intent, &intent) != nil || intent["goal"] != "arithmetic mean" || bt.Backtranslation.Prose == "" {
		t.Fatalf("%+v", bt)
	}
	var escalations contract.Escalations
	b, _ := os.ReadFile(filepath.Join(dir, ArtEscalations))
	if err := contract.DecodeStrictBytes(b, &escalations); err != nil || len(escalations.Items) != 1 || escalations.Items[0].Kind != "backtranslation_mismatch" ||
		escalations.Items[0].Target != "calc.media.v1::NaN in the list" || !strings.Contains(escalations.Items[0].Evidence.Message, "S* says nothing about NaN") ||
		escalations.Items[0].ClusterID != contract.ClusterID("backtranslation_mismatch", escalations.Items[0].Evidence.Message) {
		t.Fatalf("%v %+v", err, escalations)
	}
	h, _ := HashFile(filepath.Join(dir, ArtContract))
	var tr contract.Backtranslation
	var rev contract.BacktranslationReview
	b, _ = os.ReadFile(filepath.Join(dir, ArtBacktranslation))
	if err := contract.DecodeStrictBytes(b, &tr); err != nil || tr.ContractHash != h {
		t.Fatalf("%v %+v", err, tr)
	}
	b, _ = os.ReadFile(filepath.Join(dir, ArtBTReview))
	if err := contract.DecodeStrictBytes(b, &rev); err != nil || rev.Matches || rev.ContractHash != h {
		t.Fatalf("%v %+v", err, rev)
	}
	dump(t, dir, map[string]string{ArtLint: "contract-lint__run-bt.json", ArtBacktranslation: "backtranslation__run.json",
		ArtBTReview: "backtranslation-review__run.json", ArtEscalations: "escalation__run-bt.json",
		"launches/004-1.5-back_translator-1/in/request.json":   "role-request__run-bt.json",
		"launches/004-1.5-back_translator-1/out/response.json": "role-response__run-bt.json",
		"launches/005-1.5-inquisitor-1/in/request.json":        "role-request__run-inquisitor-bt.json",
		"launches/005-1.5-inquisitor-1/out/response.json":      "role-response__run-inquisitor-bt.json"})

	// if it matches, the stage ends done and the run goes on to the oracle
	a, launcherFake, opts, _ = prepareGreenfield(t, "calc.media.v1")
	write(t, a.Repo.Dir, ".a2dlc/roles.json", strings.Replace(rolesSpec, `"inquisitor"`, `"back_translator":{"family":"mistral","image":"bt:1","argv":["x"],"retries":0},"inquisitor"`, 1))
	launcherFake.script = scriptBT(t, true, &bt)
	if st, err = Run(context.Background(), a, launcherFake, opts); err != nil || st.Stages[3].Status != contract.StageDone || st.Stages[3].Artifact.Path != ArtLint ||
		!strings.Contains(st.Stages[3].Reason, "matches") || strings.Contains(st.Stages[3].Reason, "family") || *st.Outcome != contract.PASS {
		t.Fatalf("%v %+v", err, st.Stages[3])
	}
}

func TestStage15WithoutRolesLintsAndSkipsBackTranslation(t *testing.T) {
	a, launcherFake, opts, dir := prepareGreenfield(t, "calc.media.v1")
	launcherFake.script = scriptSpec(t, 1)
	st, err := Run(context.Background(), a, launcherFake, opts)
	if err != nil {
		t.Fatal(err)
	}
	s15 := st.Stages[3]
	if s15.Status != contract.StageDone || s15.Artifact == nil || s15.Artifact.Path != ArtLint || len(launcherFake.seen) != 3 ||
		!strings.Contains(s15.Reason, "missing back_translator") || !strings.Contains(s15.Reason, "Z3 skipped") {
		t.Fatalf("%+v %v", s15, launcherFake.seen)
	}
	// the post of contractOf is not named by any probe: a trace-coverage warning, which does not stop the run
	var l contract.ContractLint
	b, _ := os.ReadFile(filepath.Join(dir, ArtLint))
	if err := contract.DecodeStrictBytes(b, &l); err != nil || !l.OK || len(l.TraceCoverage.Uncovered) != 1 {
		t.Fatalf("%v %+v", err, l)
	}
	dump(t, dir, map[string]string{ArtLint: "contract-lint__run-avisos.json"})
}

// fakeSolver answers what states says for each function, in order; err simulates the engine being down.
type fakeSolver struct {
	states  map[string]string
	err     error
	request contract.Z3Request
}

func (f *fakeSolver) Solve(_ context.Context, req contract.Z3Request) (contract.Z3Response, error) {
	f.request = req
	if f.err != nil {
		return contract.Z3Response{}, f.err
	}
	r := contract.Z3Response{SchemaVersion: 1, Z3Version: "4.13.0", Results: []contract.Z3Result{}}
	for _, fn := range req.Functions {
		x := contract.Z3Result{Function: fn.Function, Status: f.states[fn.Function]}
		if x.Status == contract.Z3Sat {
			m := "[x = 1]"
			x.Model = &m
		} else {
			why := "unsat core: #1 (> x 0); #2 (< x 0)"
			x.Reason = &why
		}
		r.Results = append(r.Results, x)
	}
	return r, nil
}

func TestStage15Z3UnsatIsCB02aAndUnknownDoesNotBlock(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"trace_coverage":{"min_branch":0.5}}}`)
	z := &fakeSolver{states: map[string]string{"mean": contract.Z3Unsat}}
	m, err := open(a, &fakeLauncher{}, Options{Slice: "calc.media.v1", Mode: contract.ModeRefactor, Z3: z})
	if err != nil {
		t.Fatal(err)
	}
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	writeContract(t, m, s)
	cov := 0.9
	writeCapture(t, m, &cov)
	// without smt in S*, Z3 has nothing to resolve and the stage says so (the trace gate passes: 0.9 reaches 0.5)
	st, reason, _, err := stageContractLint(context.Background(), m)
	if err != nil || st != contract.StageDone || !strings.Contains(reason, "Z3 skipped: no S* function") || !strings.Contains(reason, "back-translation skipped: mode refactor") ||
		!strings.Contains(reason, "trace coverage gate: branch coverage") || z.request.Functions != nil {
		t.Fatalf("%v %s %q", err, st, reason)
	}
	s.Functions[0].SMT = []string{"(declare-const x Int)", "(assert (> x 0))", "(assert (< x 0))"}
	writeContract(t, m, s)
	st, reason, artifact, err := stageContractLint(context.Background(), m)
	if err != nil || st != contract.StageEscalated || artifact != ArtEscalations || !strings.Contains(reason, "CB-02a") || !strings.Contains(reason, "there is no way out here") || len(z.request.Functions) != 1 || z.request.TimeoutMS != Z3TimeoutMSDefault {
		t.Fatalf("%v %s %q %+v", err, st, reason, z.request)
	}
	var escalations contract.Escalations
	b, _ := os.ReadFile(filepath.Join(m.dir, ArtEscalations))
	if err := contract.DecodeStrictBytes(b, &escalations); err != nil || len(escalations.Items) != 1 || escalations.Items[0].Kind != "unsat_spec" || escalations.Items[0].Target != "calc.media.v1::mean" ||
		!strings.Contains(escalations.Items[0].Evidence.Message, "unsat core") ||
		escalations.Items[0].ClusterID != contract.ClusterID("unsat_spec", escalations.Items[0].Evidence.Message) {
		t.Fatalf("%v %+v", err, escalations)
	}
	dump(t, m.dir, map[string]string{ArtZ3: "z3-response__run-unsat.json", ArtEscalations: "escalation__run-unsat.json"})
	// outside refactor the real way out is to delete the run; in refactor S* comes out the same and there is no way out
	if m.state.Mode = contract.ModeGreenfield; !strings.Contains(m.withoutChannelForS(), "deleting .a2dlc/run/calc.media.v1") {
		t.Fatal(m.withoutChannelForS())
	}
	m.state.Mode = contract.ModeRefactor
	// unknown: the function stays runtime_and_pbt, a warning in lint.json, the stage goes on
	z.states["mean"] = contract.Z3Unknown
	if st, reason, _, err = stageContractLint(context.Background(), m); err != nil || st != contract.StageDone || !strings.Contains(reason, "runtime_and_pbt: mean") ||
		len(findings(readLint(t, m), "smt", contract.SeverityWarning)) != 1 {
		t.Fatalf("%v %s %q", err, st, reason)
	}
	// the engine being down does not stop it: it is recorded as not run
	z.err = os.ErrNotExist
	if st, reason, _, err = stageContractLint(context.Background(), m); err != nil || st != contract.StageDone || !strings.Contains(reason, "Z3 did not run") {
		t.Fatalf("%v %s %q", err, st, reason)
	}
	// without engine: skipped with its reason
	m.z3 = nil
	if st, reason, _, err = stageContractLint(context.Background(), m); err != nil || st != contract.StageDone || !strings.Contains(reason, "there is no z3 engine") {
		t.Fatalf("%v %s %q", err, st, reason)
	}
}

// engineRequests stores the diff requests it receives; it answers without targets.
type engineRequests struct {
	engineSignatures
	diffs []contract.Request
}

func (f *engineRequests) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.Response, error) {
	f.diffs = append(f.diffs, req)
	return contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}, nil
}

var _ engine.Runner = (*engineRequests)(nil)

func TestStage4PassesPiSRelaxationsToTheEngine(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	// the check reads the engines from the base commit (judge passes m.baseSha), so the config has to be committed
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "engine config")
	engine := &engineRequests{}
	a.Runner = engine
	m, err := open(a, &fakeLauncher{}, Options{Slice: "calc.media.v1", Mode: contract.ModeGreenfield})
	if err != nil {
		t.Fatal(err)
	}
	// without S* the request is check's: without relaxations
	if _, _, _, err := stageTier1(context.Background(), m); err != nil || len(engine.diffs) != 1 || engine.diffs[0].EquivalenceRelaxations != nil {
		t.Fatalf("%v %+v", err, engine.diffs)
	}
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	s.PiS = contract.PiS{Equivalence: contract.EquivalenceRelaxed, Relaxations: []string{"ignore_order", "float_tolerance:1e-9"}}
	writeContract(t, m, s)
	if _, _, _, err := stageTier1(context.Background(), m); err != nil || len(engine.diffs) != 2 || strings.Join(engine.diffs[1].EquivalenceRelaxations, ",") != "ignore_order,float_tolerance:1e-9" {
		t.Fatalf("%v %+v", err, engine.diffs)
	}
}

// writeCapture leaves capture.json as Stage 1 writes it in refactor, with the branch coverage given (nil: none recorded).
func writeCapture(t *testing.T, m *Machine, coverage *float64) {
	t.Helper()
	c := contract.Capture{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID, BaseSha: m.baseSha, ChangedFiles: []string{},
		Tests: []string{}, Signatures: []contract.SignatureFile{}, EngineErrors: []contract.EngineError{}, BranchCoverage: coverage}
	if _, err := m.writeArtifact(ArtCapture, c); err != nil {
		t.Fatal(err)
	}
}

func escalationsOf(t *testing.T, m *Machine) contract.Escalations {
	t.Helper()
	var es contract.Escalations
	b, err := os.ReadFile(filepath.Join(m.dir, ArtEscalations))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &es); err != nil {
		t.Fatal(err)
	}
	return es
}

func TestTraceCoverageGateDecidesOnlyWithADeclaredThreshold(t *testing.T) {
	const (
		undeclared = `{"schema_version":1,"engines":[]}`
		declared   = `{"schema_version":1,"engines":[],"tests":{"trace_coverage":{"min_branch":0.8}}}`
	)
	c05, c09 := 0.5, 0.9
	cases := []struct {
		name, config, mode string
		coverage           *float64
		noCapture          bool
		status, reason     string
		kind, target       string
	}{
		{name: "threshold not declared", config: undeclared, mode: contract.ModeRefactor, coverage: &c09,
			status: contract.StageEscalated, reason: "INCONCLUSIVE: " + traceLimit + " is not declared", kind: "trace_coverage_low", target: "calc.media.v1::traces"},
		{name: "coverage below the threshold", config: declared, mode: contract.ModeRefactor, coverage: &c05,
			status: contract.StageEscalated, reason: "is below " + traceLimit + " 0.8", kind: "trace_coverage_low", target: "calc.media.v1::traces"},
		{name: "coverage reaches the threshold", config: declared, mode: contract.ModeRefactor, coverage: &c09,
			status: contract.StageDone, reason: "trace coverage gate: branch coverage 0.9 reaches " + traceLimit + " 0.8"},
		{name: "no branch coverage recorded", config: declared, mode: contract.ModeRefactor, coverage: nil,
			status: contract.StageEscalated, reason: "INCONCLUSIVE: capture.json has no branch_coverage", kind: "trace_coverage_low", target: "calc.media.v1::traces"},
		{name: "no capture", config: declared, mode: contract.ModeRefactor, noCapture: true,
			status: contract.StageEscalated, reason: "INCONCLUSIVE: capture.json is missing", kind: "trace_coverage_low", target: "calc.media.v1::traces"},
		{name: "greenfield has no gate", config: declared, mode: contract.ModeGreenfield, coverage: &c05,
			status: contract.StageDone, reason: "trace coverage gate not applicable: mode greenfield records no traces"},
	}
	for _, c := range cases {
		a, dir := repoLite(t)
		write(t, dir, ".a2dlc/config.json", c.config)
		m, err := open(a, &fakeLauncher{}, Options{Slice: "calc.media.v1", Mode: c.mode})
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		writeContract(t, m, contractOf(contract.SourceConsensus, "raises ValueError"))
		if !c.noCapture {
			writeCapture(t, m, c.coverage)
		}
		st, reason, artifact, err := stageContractLint(context.Background(), m)
		if err != nil || st != c.status || !strings.Contains(reason, c.reason) || (c.mode == contract.ModeGreenfield && strings.Contains(reason, "branch coverage")) {
			t.Fatalf("%s: %v %s %q", c.name, err, st, reason)
		}
		if st != contract.StageEscalated {
			continue
		}
		es := escalationsOf(t, m)
		if artifact != ArtEscalations || len(es.Items) != 1 || es.Items[0].Kind != c.kind || es.Items[0].Target != c.target {
			t.Fatalf("%s: %s %+v", c.name, artifact, es)
		}
		// the schema asks the message to name the field: the threshold, or the branch coverage it reads
		if msg := es.Items[0].Evidence.Message; !strings.Contains(msg, traceLimit) && !strings.Contains(msg, "branch_coverage") {
			t.Fatalf("%s: the message does not name the field: %q", c.name, msg)
		}
	}
	// the gate is a refactor check: only refactor captures traces; brownfield_facade and greenfield have none to gate
	if !traceGateApplies(contract.ModeRefactor) || traceGateApplies(contract.ModeGreenfield) || traceGateApplies(contract.ModeBrownfield) || traceGateApplies(contract.ModeLite) {
		t.Fatal("the trace gate applies to refactor only")
	}
}

func TestLintV34ErrorTaxonomyAndInvariantDSL(t *testing.T) {
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	s.IOContract = &contract.IOContract{InputSchemaRef: "src/f.py", OutputSchemaRef: "src/f.py", ErrorTaxonomy: []string{"ValueError"}}
	// a probe that raises a code the taxonomy does not list is an error; one that names a listed code is not
	s.Probes = append(s.Probes, contract.Probe{ID: "BP-02", Description: "negative input raises InvalidAmountError", Input: "mean([-1])", Expected: "raises InvalidAmountError", Kind: "negative"},
		contract.Probe{ID: "BP-03", Description: "an empty list raises ValueError", Input: "mean([])", Expected: "raises ValueError", Kind: "negative"})
	// a raises whose first word is not listed is a warning (raisesSeverity)
	s.Functions[0].Errors = []contract.SpecError{{Condition: "xs is missing", Raises: "KeyError when xs is missing"}}
	s.Invariants = []contract.SliceInvariant{{Text: "never negative", DSL: "always balance >= 0"}, {Text: "non-negative", DSL: "balance >= 0"}, {Text: "text only"}}
	l := Lint(s, "sha256:x", nil, "", false, 0)
	if l.OK || len(findings(l, "error_taxonomy", contract.SeverityError)) != 1 || findings(l, "error_taxonomy", contract.SeverityError)[0].Where != "BP-02" {
		t.Fatalf("%+v", l.Findings)
	}
	if got := findings(l, "error_taxonomy", raisesSeverity); len(got) != 1 || got[0].Where != "mean" || !strings.Contains(got[0].Message, "raises KeyError") {
		t.Fatalf("%+v", l.Findings)
	}
	// the invariant outside the subset is a warning that says it is runtime_and_pbt; the translatable one is silent
	if got := findings(l, "invariant_dsl", contract.SeverityWarning); len(got) != 1 || got[0].Where != "never negative" || !strings.Contains(got[0].Message, "runtime_and_pbt: temporal operator always") {
		t.Fatalf("%+v", l.Findings)
	}
	// without io_contract there is no taxonomy to check against
	s.IOContract = nil
	if l := Lint(s, "sha256:x", nil, "", false, 0); len(findings(l, "error_taxonomy", contract.SeverityError)) != 0 || len(findings(l, "error_taxonomy", raisesSeverity)) != 0 {
		t.Fatalf("%+v", l.Findings)
	}
}

// A raises is read from expected only: a description or an input that starts with "Raises" is prose, not an error code.
func TestProbeRaisesIsReadFromExpectedOnly(t *testing.T) {
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	s.IOContract = &contract.IOContract{InputSchemaRef: "src/f.py", OutputSchemaRef: "src/f.py", ErrorTaxonomy: []string{"ValueError"}}
	s.Probes = append(s.Probes, contract.Probe{ID: "BP-02", Description: "Raises Negative inputs are rejected", Input: "mean([-1])", Expected: "returns -1", Kind: "example"})
	if l := Lint(s, "sha256:x", nil, "", false, 0); len(findings(l, "error_taxonomy", contract.SeverityError)) != 0 {
		t.Fatalf("prose 'Raises' in the description gave an error: %+v", l.Findings)
	}
	// the same word in expected is the code the probe raises, and it is an error when the taxonomy does not list it
	s.Probes = append(s.Probes, contract.Probe{ID: "BP-03", Description: "negative input", Input: "mean([-1])", Expected: "Raises Negative", Kind: "negative"})
	if got := findings(Lint(s, "sha256:x", nil, "", false, 0), "error_taxonomy", contract.SeverityError); len(got) != 1 || got[0].Where != "BP-03" {
		t.Fatalf("%+v", got)
	}
}

// An empty ref names no file, and a directory is not a schema: both are errors, in the candidate tree and in the base.
func TestLintRefsRejectEmptyAndDirectory(t *testing.T) {
	a, _ := repoLite(t)
	m, err := open(a, &fakeLauncher{}, Options{Slice: "calc.media.v1", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	s.IOContract = &contract.IOContract{InputSchemaRef: "", OutputSchemaRef: "src", ErrorTaxonomy: []string{}}
	l := contract.ContractLint{}
	m.lintRefs(s, &l)
	if len(l.Findings) != 2 || l.Findings[0].Where != "io_contract.input_schema_ref" || l.Findings[1].Where != "io_contract.output_schema_ref" {
		t.Fatalf("%+v", l.Findings)
	}
	for _, f := range l.Findings {
		if f.Severity != contract.SeverityError || f.Check != "refs" {
			t.Fatalf("%+v", f)
		}
	}
}

func TestLintRefsResolveInBaseOrCandidate(t *testing.T) {
	a, dir := repoLite(t)
	m, err := open(a, &fakeLauncher{}, Options{Slice: "calc.media.v1", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	s.IOContract = &contract.IOContract{InputSchemaRef: "src/f.py", OutputSchemaRef: "schemas/missing.json", ErrorTaxonomy: []string{}}
	l := contract.ContractLint{}
	m.lintRefs(s, &l)
	if len(l.Findings) != 1 || l.Findings[0].Severity != contract.SeverityError || l.Findings[0].Where != "io_contract.output_schema_ref" {
		t.Fatalf("%+v", l.Findings)
	}
	// src/f.py is in the candidate tree; once the candidate deletes it, the base still has it and it still resolves
	if err := os.Remove(filepath.Join(dir, "src", "f.py")); err != nil {
		t.Fatal(err)
	}
	l = contract.ContractLint{}
	s.IOContract.OutputSchemaRef = "src/f.py"
	m.lintRefs(s, &l)
	if len(l.Findings) != 0 {
		t.Fatalf("base-only file did not resolve: %+v", l.Findings)
	}
	// a ref that climbs out of the repository is an error; a URI is only noted
	for ref, sev := range map[string]string{"../outside.json": contract.SeverityError, "https://example.com/schema.json": contract.SeverityWarning} {
		l = contract.ContractLint{}
		s.IOContract.InputSchemaRef = ref
		m.lintRefs(s, &l)
		if len(l.Findings) != 1 || l.Findings[0].Severity != sev || l.Findings[0].Check != "refs" {
			t.Fatalf("%s: %+v", ref, l.Findings)
		}
	}
}

func TestStage15InvariantsGoToZ3AndOutOfSubsetIsRuntimeAndPBT(t *testing.T) {
	a, _ := repoLite(t)
	z := &fakeSolver{states: map[string]string{"invariant: balance never negative": contract.Z3Unsat}}
	m, err := open(a, &fakeLauncher{}, Options{Slice: "calc.media.v1", Mode: contract.ModeGreenfield, Z3: z})
	if err != nil {
		t.Fatal(err)
	}
	s := contractOf(contract.SourceConsensus, "raises ValueError")
	// the contradiction is translated and sent as its own Z3 function; the temporal one is not sent and is a warning
	s.Invariants = []contract.SliceInvariant{{Text: "balance never negative", DSL: "balance >= 0 && balance < 0"},
		{Text: "always non-negative", DSL: "always balance >= 0"}, {Text: "no DSL"}}
	writeContract(t, m, s)
	st, reason, artifact, err := stageContractLint(context.Background(), m)
	if err != nil || st != contract.StageEscalated || artifact != ArtEscalations || !strings.Contains(reason, "CB-02a") {
		t.Fatalf("%v %s %q", err, st, reason)
	}
	if len(z.request.Functions) != 1 || z.request.Functions[0].Function != "invariant: balance never negative" || len(z.request.Functions[0].SMT) != 2 ||
		z.request.Functions[0].SMT[1] != "(assert (and (>= |balance| 0) (< |balance| 0)))" {
		t.Fatalf("%+v", z.request.Functions)
	}
	es := escalationsOf(t, m)
	if len(es.Items) != 1 || es.Items[0].Kind != "unsat_spec" || es.Items[0].Target != "calc.media.v1::invariant: balance never negative" {
		t.Fatalf("%+v", es)
	}
	if got := findings(readLint(t, m), "invariant_dsl", contract.SeverityWarning); len(got) != 1 || got[0].Where != "always non-negative" {
		t.Fatalf("%+v", got)
	}
}
