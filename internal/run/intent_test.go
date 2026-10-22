package run

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// intentFile writes a valid intent outside the repo and returns its path.
func intentFile(t *testing.T, change func(*contract.Intent)) string {
	t.Helper()
	in := contract.Intent{SchemaVersion: 1, SliceID: "pago.v1", Mode: contract.ModeGreenfield, Goal: "settle", Invariants: []string{},
		NonGoals: []string{}, Criticality: contract.CriticalityLow, CriticalPaths: []string{}, Acceptance: []string{},
		Budget: &contract.IntentBudget{MaxInnerIterations: 5, MaxTier1AdversarialRounds: 3}}
	change(&in)
	b, _ := json.MarshalIndent(in, "", "  ")
	p := filepath.Join(t.TempDir(), "intent.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// v34Intent is a valid intent with the v3.4 fields that a test changes.
func v34Intent(change func(*contract.Intent)) contract.Intent {
	in := contract.Intent{SchemaVersion: 1, SliceID: "pago.v1", Mode: contract.ModeGreenfield, Goal: "settle", Invariants: []string{},
		NonGoals: []string{}, Criticality: contract.CriticalityLow, CriticalPaths: []string{}, Acceptance: []string{},
		Budget: &contract.IntentBudget{MaxInnerIterations: 5, MaxTier1AdversarialRounds: 3}}
	change(&in)
	return in
}

// The v3.4 fields of intent.json are validated in Go as the schema says: a malformed one fails Validate, a valid one passes.
func TestIntentV34FieldsAreValidated(t *testing.T) {
	zero, one, neg := 0, 1, -1
	cases := []struct {
		name   string
		change func(*contract.Intent)
		valid  bool
	}{
		{"change_kind feature", func(in *contract.Intent) { in.ChangeKind = "feature" }, true},
		{"change_kind unknown", func(in *contract.Intent) { in.ChangeKind = "rewrite" }, false},
		{"criticality_tags known", func(in *contract.Intent) { in.CriticalityTags = []string{"security", "auth"} }, true},
		{"criticality_tags unknown", func(in *contract.Intent) { in.CriticalityTags = []string{"performance"} }, false},
		{"sla well formed", func(in *contract.Intent) {
			in.SLAs = []contract.SLA{{Metric: "latency_p99", Op: "<", Value: 200, Unit: "ms"}}
		}, true},
		{"sla with an unknown op", func(in *contract.Intent) {
			in.SLAs = []contract.SLA{{Metric: "latency_p99", Op: "==", Value: 200, Unit: "ms"}}
		}, false},
		{"sla without unit", func(in *contract.Intent) { in.SLAs = []contract.SLA{{Metric: "latency_p99", Op: "<", Value: 200}} }, false},
		{"max_tokens 1", func(in *contract.Intent) { in.Budget.MaxTokens = &one }, true},
		{"max_tokens 0", func(in *contract.Intent) { in.Budget.MaxTokens = &zero }, false},
		{"max_tokens negative", func(in *contract.Intent) { in.Budget.MaxTokens = &neg }, false},
		{"forbidden_imports with an empty element", func(in *contract.Intent) {
			in.Capabilities = &contract.Capabilities{ForbiddenImports: []string{""}}
		}, false},
		{"read_only_paths with an empty element", func(in *contract.Intent) {
			writes := false
			in.Delivery = &contract.Delivery{Writes: &writes, ReadOnlyPaths: []string{"docs/**", ""}}
		}, false},
		{"read_only_paths well formed", func(in *contract.Intent) {
			writes := false
			in.Delivery = &contract.Delivery{Writes: &writes, ReadOnlyPaths: []string{"docs/**"}}
		}, true},
	}
	for _, c := range cases {
		err := v34Intent(c.change).Validate()
		if c.valid && err != nil {
			t.Errorf("%s: rejected: %v", c.name, err)
		}
		if !c.valid && err == nil {
			t.Errorf("%s: accepted", c.name)
		}
	}
}

// A malformed SLA is refused when a2dlc run reads the intent, not only by Validate.
func TestIntentWithMalformedSLAIsRefusedOnRun(t *testing.T) {
	a, _ := repoLite(t)
	path := intentFile(t, func(in *contract.Intent) { in.SLAs = []contract.SLA{{Metric: "x", Op: "~", Value: 1, Unit: "ms"}} })
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: path}); err == nil || !strings.Contains(err.Error(), "does not match the contract") {
		t.Fatalf("%v", err)
	}
}

// budget.max_tokens stops the run like max_cost_usd, and the reason names the cap that tripped; null is no cap.
func TestTokenCapTripsAndIsNamed(t *testing.T) {
	max, used := 100, 101
	m := &Machine{intent: &contract.Intent{Budget: &contract.IntentBudget{MaxTokens: &max}}}
	m.state.Budget.SpentTokens = &used
	if got := m.exceeded(); got != "CB-03: 101 tokens exceed budget.max_tokens 100" {
		t.Fatalf("%q", got)
	}
	at := 100
	m.state.Budget.SpentTokens = &at
	if got := m.exceeded(); got != "" {
		t.Fatalf("at the cap is not over it: %q", got)
	}
	usd, spent := 1.0, 2.0
	m.state.Budget.MaxCostUSD, m.state.Budget.SpentUSD, m.state.Budget.SpentTokens = &usd, spent, &used
	if got := m.exceeded(); !strings.HasPrefix(got, "CB-03: cost ") {
		t.Fatalf("cost is named first: %q", got)
	}
	m.intent.Budget.MaxTokens = nil
	m.state.Budget.MaxCostUSD = nil
	if got := m.exceeded(); got != "" {
		t.Fatalf("null is no cap: %q", got)
	}
}

func byID(st contract.RunState) map[string]contract.StageRun {
	found := map[string]contract.StageRun{}
	for _, s := range st.Stages {
		found[s.ID] = s
	}
	return found
}

func readRouteFile(t *testing.T, dir, slice string) contract.Route {
	t.Helper()
	var r contract.Route
	b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run", slice, ArtRoute))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// withDualSpec leaves in the repo what Stage 1 needs to run dual-spec (committed catalogue and the three roles) and the
// launcher's script; without them, a greenfield intent of medium or higher escalates at Stage 1 (mandatory dual-spec).
func withDualSpec(t *testing.T, dir string, launcherFake *fakeLauncher) {
	t.Helper()
	write(t, dir, ".a2dlc/catalogue.json", `{"schema_version":1,"domain":"numeric","entries":[
 {"id":"listas-vacias","category":"empty lists","question":"What happens with an empty xs?"}]}`)
	git(t, dir, "add", ".a2dlc/catalogue.json")
	git(t, dir, "commit", "-qm", "catalogue")
	write(t, dir, ".a2dlc/roles.json", rolesSpec)
	script := scriptSpec(t, 1)
	launcherFake.script = func(spec LaunchSpec) (*contract.RoleResponse, int, bool) {
		r, output, deadline := script(spec)
		if r != nil { // the Stage 1 script talks about calc.media.v1, leaves an open assumption and a probe that does not cover the clause
			r.Payload = bytes.ReplaceAll(r.Payload, []byte("calc.media.v1"), []byte("pago.v1"))
			// the high lint requires that some probe covers each clause
			r.Payload = bytes.ReplaceAll(r.Payload, []byte(`"description":"one"`), []byte(`"description":"mean with empty xs"`))
			if spec.Role == contract.RoleInquisitor {
				var obj map[string]any
				if err := json.Unmarshal(r.Payload, &obj); err != nil {
					t.Fatal(err)
				}
				obj["open_assumptions"] = []any{}
				r.Payload, _ = json.Marshal(obj)
			}
		}
		return r, output, deadline
	}
}

func TestIntentHighGivesN2AndDualSpec(t *testing.T) {
	a, dir := repoLite(t)
	launcherFake := &fakeLauncher{}
	withDualSpec(t, dir, launcherFake)
	path := intentFile(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityHigh })
	// before Stage 2, readRoute computes the same decision that Stage 2 writes afterwards (Stage 1 needs it)
	m, err := open(a, launcherFake, Options{Slice: "pago.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	before, err := m.readRoute()
	if err != nil {
		t.Fatal(err)
	}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	found := byID(st)
	// without --mode, the run takes the intent's mode
	if st.Mode != contract.ModeGreenfield || found["0"].Status != contract.StageDone || found["0"].Artifact == nil || found["0"].Artifact.Path != ArtIntent {
		t.Fatalf("%s %+v", st.Mode, found["0"])
	}
	orig, _ := os.ReadFile(path)
	copied, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/pago.v1", ArtIntent))
	if string(orig) != string(copied) || found["0"].Artifact.SHA256 != hashBytes(orig) {
		t.Fatal("intent.json is not the exact copy of --intent")
	}
	r := readRouteFile(t, dir, "pago.v1")
	if found["2"].Status != contract.StageDone || r.Track != contract.TrackN2 || !r.DualSpecRequired || r.CriticalityEffective != contract.CriticalityHigh || len(r.TouchedCritical) != 0 {
		t.Fatalf("%+v %+v", found["2"], r)
	}
	// S* metrics are born with Stage 1's contract.json: before Stage 2 they are null; the rest of the decision
	// (what Stage 1 reads) must be equal
	if before.RuleCount != nil || before.StateMutationDepth != nil || before.MetricsReason == nil {
		t.Fatalf("readRoute before Stage 2 with metrics %+v: want null and a reason", before)
	}
	before.MetricsReason = nil
	r.RuleCount, r.StateMutationDepth, r.MetricsReason = nil, nil, nil
	if !reflect.DeepEqual(before, r) {
		t.Fatalf("readRoute before Stage 2 %+v, route.json %+v", before, r)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v", st.Outcome)
	}
	// resuming with the same intent changes nothing; without --intent it uses the saved one
	for _, o := range []Options{{Slice: "pago.v1", Intent: path}, {Slice: "pago.v1"}} {
		st2, err := Run(context.Background(), a, &fakeLauncher{}, o)
		if err != nil || st2.RunID != st.RunID || *byID(st2)["0"].StartedAt != *found["0"].StartedAt {
			t.Fatalf("%v %+v", err, byID(st2)["0"])
		}
	}
	if problems, err := VerifyRun(a, "pago.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

// Mandatory dual-spec (Router §5) is not skipped silently: without Stage 1's roles the run escalates at Stage 1 and does
// not build without S*. With low criticality it is not mandatory, and Stage 1 is still skipped with a reason.
func TestStage1DualSpecRequiredWithoutRolesEscalates(t *testing.T) {
	a, _ := repoLite(t)
	path := intentFile(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityMedium })
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s1 := byID(st)["1"]
	if s1.Status != contract.StageEscalated || !strings.Contains(s1.Reason, "dual-spec mandatory") || !strings.Contains(s1.Reason, contract.RoleSpecifierAlpha) {
		t.Fatalf("%+v", s1)
	}
	if st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("outcome %v", st.Outcome)
	}
	if byID(st)["3"].Status == contract.StageDone {
		t.Fatalf("the builder ran without S*: %+v", byID(st)["3"])
	}
	// low criticality: not mandatory, Stage 1 is skipped with a reason and the run goes on
	a, _ = repoLite(t)
	path = intentFile(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityLow })
	st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s1 = byID(st)["1"]; s1.Status != contract.StageSkipped || !strings.Contains(s1.Reason, "no dual-spec") || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", s1, st.Outcome)
	}
}

func TestCriticalPathsRaiseOneLevelAndLiteIgnoresThem(t *testing.T) {
	a, dir := repoLite(t) // changes src/f.py
	launcherFake := &fakeLauncher{}
	withDualSpec(t, dir, launcherFake)
	path := intentFile(t, func(in *contract.Intent) {
		in.Criticality, in.CriticalPaths = contract.CriticalityMedium, []string{"db/**", "src/**"}
	})
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", Intent: path}); err != nil {
		t.Fatal(err)
	}
	r := readRouteFile(t, dir, "pago.v1")
	if r.CriticalityEffective != contract.CriticalityHigh || r.Track != contract.TrackN2 || !r.DualSpecRequired ||
		strings.Join(r.TouchedCritical, ",") != "src/f.py" || !strings.Contains(strings.Join(r.Reasons, "|"), "goes up to high") {
		t.Fatalf("%+v", r)
	}
	// critical_paths that do not match: stays medium, N=1, and greenfield medium requires dual-spec all the same
	a, dir = repoLite(t)
	launcherFake = &fakeLauncher{}
	withDualSpec(t, dir, launcherFake)
	path = intentFile(t, func(in *contract.Intent) {
		in.Criticality, in.CriticalPaths = contract.CriticalityMedium, []string{"db/**"}
	})
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", Intent: path}); err != nil {
		t.Fatal(err)
	}
	if r = readRouteFile(t, dir, "pago.v1"); r.CriticalityEffective != contract.CriticalityMedium || r.Track != contract.TrackN1 || !r.DualSpecRequired || len(r.TouchedCritical) != 0 {
		t.Fatalf("%+v", r)
	}
	// lite: N=1 without dual-spec even if it is critical and touches critical_paths
	a, dir = repoLite(t)
	path = intentFile(t, func(in *contract.Intent) {
		in.Mode, in.Criticality, in.CriticalPaths = contract.ModeLite, contract.CriticalityCritical, []string{"src/**"}
	})
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: path}); err != nil {
		t.Fatal(err)
	}
	if r = readRouteFile(t, dir, "pago.v1"); r.Track != contract.TrackN1 || r.DualSpecRequired || !strings.Contains(strings.Join(r.Reasons, "|"), "lite mode") {
		t.Fatalf("%+v", r)
	}
	// without intent: Stage 0 skipped with a reason and the Router routes low, N=1
	a, dir = repoLite(t)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1"})
	if err != nil || byID(st)["0"].Status != contract.StageSkipped || !strings.Contains(byID(st)["0"].Reason, "no --intent") {
		t.Fatalf("%v %+v", err, byID(st)["0"])
	}
	if r = readRouteFile(t, dir, "pago.v1"); r.Track != contract.TrackN1 || r.CriticalityEffective != contract.CriticalityLow {
		t.Fatalf("%+v", r)
	}
}

func TestRouteRules(t *testing.T) {
	in := func(crit string, paths ...string) *contract.Intent {
		return &contract.Intent{Criticality: crit, CriticalPaths: paths}
	}
	cases := []struct {
		name    string
		in      *contract.Intent
		mode    string
		touched []string
		track   string
		dual    bool
		efect   string
	}{
		{"greenfield low", in("low"), contract.ModeGreenfield, nil, contract.TrackN1, false, "low"},
		{"greenfield critical", in("critical"), contract.ModeGreenfield, nil, contract.TrackN2, true, "critical"},
		// a deletion also touches the critical file
		{"deleted critical", in("low", "auth/**"), contract.ModeGreenfield, []string{"auth/token.py"}, contract.TrackN1, true, "medium"},
		{"raises only once", in("medium", "a/**", "b/**"), contract.ModeGreenfield, []string{"a/x", "b/y"}, contract.TrackN2, true, "high"},
		{"critical does not go above critical", in("critical", "a/**"), contract.ModeGreenfield, []string{"a/x"}, contract.TrackN2, true, "critical"},
		// refactor: the second builder is the legacy one and Stage 1 is capture, without dual-spec
		{"refactor high", in("high"), contract.ModeRefactor, nil, contract.TrackN2, false, "high"},
		{"brownfield medium", in("medium"), contract.ModeBrownfield, nil, contract.TrackN1, false, "medium"},
		{"lite high", in("high", "a/**"), contract.ModeLite, []string{"a/x"}, contract.TrackN1, false, "critical"},
		{"without intent", nil, contract.ModeGreenfield, []string{"a/x"}, contract.TrackN1, false, "low"},
	}
	for _, c := range cases {
		r := route(c.in, c.mode, c.touched, false)
		if r.Track != c.track || r.DualSpecRequired != c.dual || r.CriticalityEffective != c.efect || len(r.Reasons) == 0 || r.TouchedCritical == nil {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	// the S* probes' expected footprint rises the same way, and the reason says so
	r := route(in("medium", "src/**"), contract.ModeGreenfield, []string{"src/calc.py"}, true)
	if r.Track != contract.TrackN2 || r.CriticalityEffective != "high" || !strings.Contains(strings.Join(r.Reasons, "|"), "the S* probes expect to touch critical_paths (src/calc.py)") {
		t.Fatalf("%+v", r)
	}
}

// With one builder the work tree is the base: the files that the S* probes name are the footprint that the Router compares
// with critical_paths, and the real Stage 2 launches the two builders.
func TestRouterForeseesFootprintFromProbes(t *testing.T) {
	file, expr := "src/f.py", "f(2) == 4"
	withStage(t, "1", func(_ context.Context, m *Machine) (string, string, string, error) {
		c := contractOf(contract.SourceConsensus, "doubles")
		c.Probes = []contract.Probe{{ID: "BP-01", Description: "doubles", Input: "f(2)", Expected: "4", Kind: "example", File: &file, Expr: &expr}}
		if _, err := m.writeArtifact(ArtContract, c); err != nil {
			return "", "", "", err
		}
		h, err := HashFile(filepath.Join(m.dir, ArtContract))
		if err != nil {
			return "", "", "", err
		}
		artifact, err := m.writeArtifact(ArtProbes, contract.Probes{SchemaVersion: 1, SliceID: c.SliceID, ContractHash: h, Probes: c.Probes})
		return contract.StageDone, "", artifact, err
	})
	withStage(t, "1.5", func(context.Context, *Machine) (string, string, string, error) {
		return contract.StageDone, "", "", nil
	})
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	path := intentFile(t, func(in *contract.Intent) {
		in.Criticality = contract.CriticalityMedium
		in.CriticalPaths = []string{"src/**"}
	})
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(string, contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	var r contract.Route
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/pago.v1", ArtRoute), &r)
	if r.Track != contract.TrackN2 || r.CriticalityEffective != contract.CriticalityHigh || strings.Join(r.TouchedCritical, ",") != "src/f.py" ||
		!strings.Contains(strings.Join(r.Reasons, "|"), "the S* probes expect to touch") {
		t.Fatalf("%+v", r)
	}
	if len(seen) != 2 || byID(st)["3"].Status != contract.StageDone {
		t.Fatalf("builders launched: %+v; stage 3 %+v", seen, byID(st)["3"])
	}
}

func TestIntentNotOfRunIsRejected(t *testing.T) {
	a, dir := repoLite(t)
	otherSlice := intentFile(t, func(in *contract.Intent) { in.SliceID = "other.v1" })
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: otherSlice}); err == nil || !strings.Contains(err.Error(), "for slice") {
		t.Fatalf("%v", err)
	}
	path := intentFile(t, func(*contract.Intent) {})
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Mode: contract.ModeRefactor, Intent: path}); err == nil || !strings.Contains(err.Error(), "mode") {
		t.Fatalf("%v", err)
	}
	outside := filepath.Join(t.TempDir(), "bad.json")
	os.WriteFile(outside, []byte(`{"schema_version":1,"slice_id":"pago.v1","mode":"greenfield","goal":"x","criticality":"urgent"}`), 0o644)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: outside}); err == nil || !strings.Contains(err.Error(), "does not match the contract") {
		t.Fatalf("%v", err)
	}
	// none of the rejections left a half-finished run
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/pago.v1/state.json")); err == nil {
		t.Fatal("a rejected intent left state.json")
	}
	// the run is bound to its intent: another one on resume, or the saved one being touched, is an error
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: path}); err != nil {
		t.Fatal(err)
	}
	other := intentFile(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityHigh })
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: other}); err == nil || !strings.Contains(err.Error(), "another intent") {
		t.Fatalf("%v", err)
	}
	b, _ := os.ReadFile(other)
	os.WriteFile(filepath.Join(dir, ".a2dlc/run/pago.v1", ArtIntent), b, 0o644)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1"}); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("%v", err)
	}
	// a run that started without intent does not take one midway
	a, _ = repoLite(t)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Mode: contract.ModeGreenfield}); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "pago.v1", Intent: path}); err == nil || !strings.Contains(err.Error(), "started without an intent") {
		t.Fatalf("%v", err)
	}
	// nor from the folder: an intent.json placed by hand does not enter a run that started without one
	b, _ = os.ReadFile(path)
	os.WriteFile(filepath.Join(a.Repo.Dir, ".a2dlc/run/pago.v1", ArtIntent), b, 0o644)
	for _, o := range []Options{{Slice: "pago.v1"}, {Slice: "pago.v1", Intent: path}} {
		if _, err := Run(context.Background(), a, &fakeLauncher{}, o); err == nil || !strings.Contains(err.Error(), "started without an intent") {
			t.Fatalf("%+v: %v", o, err)
		}
	}
}

func TestMaxInnerFallsBackToIntent(t *testing.T) {
	a, _ := repoLite(t)
	path := intentFile(t, func(in *contract.Intent) { in.Budget.MaxInnerIterations = 2 })
	for _, c := range []struct {
		o    Options
		want int
	}{
		{Options{Slice: "pago.v1", Intent: path}, 2},
		{Options{Slice: "pago.v1", Intent: path, MaxInner: 4}, 4},
		{Options{Slice: "pago.v1"}, MaxInnerDefault},
	} {
		m, err := open(a, &fakeLauncher{}, c.o)
		if err != nil || m.maxInner != c.want {
			t.Fatalf("%+v: %v maxInner=%d", c.o, err, m.maxInner)
		}
	}
}

// saboteurNotCovering leaves a new test per round that does not run the changed lines, at cost 0.01.
func saboteurNotCovering(t *testing.T) *fakeLauncher {
	n := 0
	return &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		n++
		resp, e, to := fakeSaboteur("tests/test_"+string(rune('a'+n))+".t", "t passed\ncubre src/f.py:50-60\n")(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
}

func TestBudgetFromIntent(t *testing.T) {
	// --max-rounds without giving it: the intent's max_tier1_adversarial_rounds wins; given, the flag wins
	for _, c := range []struct{ flag, want int }{{0, 1}, {2, 2}} {
		a, _, _ := repoWithTests(t)
		path := intentFile(t, func(in *contract.Intent) { in.Mode, in.Budget.MaxTier1AdversarialRounds = contract.ModeLite, 1 })
		st, err := Run(context.Background(), a, saboteurNotCovering(t), Options{Slice: "pago.v1", Intent: path, MaxRounds: c.flag})
		if s4 := byID(st)["4"]; err != nil || s4.Iterations != c.want || !strings.Contains(s4.Reason, "rounds") {
			t.Fatalf("flag %d: %v %+v", c.flag, err, s4)
		}
	}
	// CB-03 without --max-cost-usd: the limit comes from the intent; the per-role breakdown stays in the state
	a, dir, _ := repoWithTests(t)
	cost := 0.005
	path := intentFile(t, func(in *contract.Intent) { in.Mode, in.Budget.MaxCostUSD = contract.ModeLite, &cost })
	launcherFake := saboteurNotCovering(t)
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", Intent: path})
	s4 := byID(st)["4"]
	if err != nil || s4.Status != contract.StageEscalated || !strings.HasPrefix(s4.Reason, "CB-03") || st.Outcome == nil || *st.Outcome != "escalated" ||
		st.Budget.MaxCostUSD == nil || *st.Budget.MaxCostUSD != cost || st.Budget.ByRole[contract.RoleSaboteur] != 0.01 || len(launcherFake.seen) != 1 {
		t.Fatalf("%v %+v %+v seen=%d", err, s4, st.Budget, len(launcherFake.seen))
	}
	// the state.json on disk decodes strictly with by_role
	var onDisk contract.RunState
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/pago.v1/state.json"))
	if err := contract.DecodeStrictBytes(b, &onDisk); err != nil || onDisk.Budget.ByRole[contract.RoleSaboteur] != 0.01 {
		t.Fatalf("%v %+v", err, onDisk.Budget)
	}
	// resuming over the limit launches nothing; with a higher --max-cost-usd (widen budget) it goes on
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1"})
	if err != nil || byID(st)["4"].Status != contract.StageEscalated || len(launcherFake.seen) != 1 {
		t.Fatalf("%v %+v seen=%d", err, byID(st)["4"], len(launcherFake.seen))
	}
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", MaxCostUSD: 1})
	if err != nil || byID(st)["4"].Status != contract.StageDone || *st.Budget.MaxCostUSD != 1 || len(launcherFake.seen) < 2 {
		t.Fatalf("%v %+v %+v seen=%d", err, byID(st)["4"], st.Budget, len(launcherFake.seen))
	}
}
