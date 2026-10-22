package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// Complexity of the fake engine of a2dlc run: cyclomatic is 1 plus each "if " in the candidate's file; ast_nodes, its
// length; the added lines, the lines of the file.
func (f *engineRun) Complexity(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.ComplexityResponse, error) {
	r := contract.ComplexityResponse{SchemaVersion: 1, Files: []contract.ComplexityFile{}}
	for _, file := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candidateDir, file))
		c, n := 1+strings.Count(string(b), "if "), len(b)
		r.Files = append(r.Files, contract.ComplexityFile{File: file, LocAdded: strings.Count(string(b), "\n"), Cyclomatic: &c, ASTNodes: &n})
	}
	return r, engine.ValidateComplexity(req, r)
}

// routeN2 is a valid route.json (contracts/route.schema.json) that asks for N=2.
func routeN2() contract.Route {
	return contract.Route{SchemaVersion: contract.SchemaVersion, Track: contract.TrackN2, CriticalityEffective: contract.CriticalityHigh,
		Reasons: []string{"test"}, TouchedCritical: []string{}}
}

// withRouterN2 replaces Stage 2 with one that writes an N=2 path for the duration of the test, for the N=2 tests that do
// not start from an intent; TestN2FromIntentWithRealRouter uses the real Router.
func withRouterN2(t *testing.T) {
	withStage(t, "2", func(_ context.Context, m *Machine) (string, string, string, error) {
		artifact, err := m.writeArtifact(ArtRoute, routeN2())
		return contract.StageDone, "", artifact, err
	})
}

const rolesN2 = `{"schema_version":1,"roles":{
 "builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0},
 "builder_orthogonal":{"family":"gemini","image":"img:2","argv":["sh","y"],"retries":0}}}`

// launchRecord is what the fake launcher saw of each builder: role, /work and the content of src/f.py on entry.
type launchRecord struct {
	role, work, fOnEntry string
}

// buildersN2 writes in /work what content says for each role and iteration, and records each launchRecord.
func buildersN2(t *testing.T, seen *[]launchRecord, content func(role string, pl contract.BuilderRequest) map[string]string) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		var req contract.RoleRequest
		b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
		if err := contract.DecodeStrictBytes(b, &req); err != nil || s.WorkDir == "" {
			t.Fatalf("%v %q", err, s.WorkDir)
		}
		var pl contract.BuilderRequest
		if err := contract.DecodeStrictBytes(req.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		f, _ := os.ReadFile(filepath.Join(s.WorkDir, "src", "f.py"))
		*seen = append(*seen, launchRecord{s.Role, s.WorkDir, string(f)})
		var touched []string
		for rel, c := range content(s.Role, pl) {
			p := filepath.Join(s.WorkDir, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(c), 0o644)
			touched = append(touched, rel)
		}
		if touched == nil {
			return &contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
				Status: contract.RoleRefused, Reason: "I don't know how to do it", Payload: json.RawMessage(`{}`)}, 0, false
		}
		out, _ := json.Marshal(contract.BuilderResponse{Summary: "done", FilesChanged: touched})
		return &contract.RoleResponse{SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
			Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.1}, Payload: out}, 0, false
	}
}

func readJSONTest(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, v); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func TestN2TwoBuildersOrthogonalWinsByOccam(t *testing.T) {
	withRouterN2(t)
	a, dir, engine := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, pl contract.BuilderRequest) map[string]string {
		switch {
		case role == contract.RoleBuilderOrthogonal:
			return map[string]string{"src/f.py": "def f(x):\n    return x + x\n"}
		case pl.Tier0Feedback == nil:
			return map[string]string{"src/f.py": "def f(x):\n    return x +\nROTO\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    if x:\n        return x * 2\n    return 0\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "n2.v1"})
	if err != nil {
		t.Fatal(err)
	}
	s3, s4 := st.Stages[5], st.Stages[6]
	if s3.Status != contract.StageDone || s3.Iterations != 3 || !strings.Contains(s3.Reason, "@a2dlc header missing") || s4.Status != contract.StageDone {
		t.Fatalf("%+v\n%+v", s3, s4)
	}
	run := filepath.Join(dir, ".a2dlc/run/n2.v1")
	var c contract.Candidate
	readJSONTest(t, filepath.Join(run, ArtCandidate), &c)
	if len(c.Candidates) != 2 || c.Builder != contract.RoleBuilderPrimary || c.Commit != c.Candidates[0].Commit ||
		c.Candidates[0].Iterations != 2 || c.Candidates[1].Builder != contract.RoleBuilderOrthogonal || c.Candidates[1].Iterations != 1 || len(c.Dropped) != 0 {
		t.Fatalf("%+v", c)
	}
	p, o := c.Candidates[0], c.Candidates[1]
	if git(t, dir, "rev-parse", "refs/a2dlc/run/n2.v1/builder_orthogonal") != o.Commit || git(t, dir, "rev-parse", o.Commit+"^") != st.BaseSha ||
		git(t, dir, "show", o.Commit+":src/f.py") != "def f(x):\n    return x + x" {
		t.Fatalf("orthogonal commit built wrongly: %s", o.Commit)
	}
	// policy: the orthogonal starts from the base with its own /work and sees only its request (without the primary's
	// feedback, which already existed when they launched it)
	if len(seen) != 3 || seen[2].role != contract.RoleBuilderOrthogonal || seen[2].fOnEntry != baseFile || seen[2].work == seen[0].work ||
		seen[1].fOnEntry == baseFile || strings.Join(launcherFake.seen[2], ",") != "request.json" || strings.Join(launcherFake.seen[1], ",") != "request.json,tier0_feedback.json" {
		t.Fatalf("%+v %v", seen, launcherFake.seen)
	}
	if _, err := os.Stat(filepath.Join(run, ArtTier0)); err != nil {
		t.Fatal("the primary's feedback had to exist when launching the orthogonal")
	}
	// Stage 4: one verdict per candidate, each from its own tree, and the selection picks the simpler one
	var verdictP, verdictO, v contract.Verdict
	readJSONTest(t, filepath.Join(run, ArtVerdictPrimary), &verdictP)
	readJSONTest(t, filepath.Join(run, ArtVerdictOrthogonal), &verdictO)
	readJSONTest(t, filepath.Join(run, ArtVerdict), &v)
	if verdictP.TreeHash != "git-tree:"+p.TreeHash || verdictO.TreeHash != "git-tree:"+o.TreeHash || v.TreeHash != verdictO.TreeHash || v.Stage != "4" ||
		verdictP.Verdict != contract.PASS || verdictO.Verdict != contract.PASS || v.ContractHash != "" {
		t.Fatalf("%s %s %s %+v", verdictP.TreeHash, verdictO.TreeHash, v.TreeHash, v)
	}
	var selection contract.Selection
	readJSONTest(t, filepath.Join(run, ArtSelection), &selection)
	if selection.Selected == nil || *selection.Selected != contract.RoleBuilderOrthogonal || len(selection.Candidates) != 2 || selection.Candidates[0].Complexity == nil ||
		*selection.Candidates[0].Complexity.Cyclomatic != 2 || *selection.Candidates[1].Complexity.Cyclomatic != 1 || !strings.Contains(selection.Reason, "cyclomatic 1 versus 2") ||
		!strings.Contains(s4.Reason, "builder_orthogonal") {
		t.Fatalf("%+v %q", selection, s4.Reason)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS || len(engine.sawCand) != 2 {
		t.Fatalf("%v %v", st.Outcome, engine.sawCand)
	}
	// the repo verdict is the selected one's, even though check wrote each candidate's
	var repoVerdict contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/verdict.json"), &repoVerdict)
	if repoVerdict.TreeHash != verdictO.TreeHash {
		t.Fatalf("repo verdict is %s", repoVerdict.TreeHash)
	}
	// what follows (the Stage 5 mutation) works on the chosen one
	m, err := open(a, launcherFake, Options{Slice: "n2.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if candidate, err := m.candidate(); err != nil || candidate != o.Commit {
		t.Fatalf("%v %s", err, candidate)
	}
	var lg contract.RunLedger
	readJSONTest(t, filepath.Join(run, "ledger.json"), &lg)
	if len(lg.Entries) != 3 || lg.Entries[2].Role != contract.RoleBuilderOrthogonal || lg.Entries[2].Family != "gemini" || len(lg.Entries[2].Inputs) != 1 {
		t.Fatalf("%+v", lg.Entries)
	}
	if problems, err := VerifyRun(a, "n2.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

// runnerTree runs the files in $A2DLC_TESTS: a line "name if <file> <text>" is a test that passes only if that file of
// the tree contains the text, so the same test gives a different result on each tree (base, candidate, peer).
type runnerTree struct{}

func (runnerTree) RunTests(_ context.Context, _ contract.TestsConfig, _ string, env map[string]string, tree string) (engine.TestRun, error) {
	var x strings.Builder
	x.WriteString("<testsuites><testsuite>")
	exit := 0
	for _, a := range strings.Fields(env["A2DLC_TESTS"]) {
		b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(a)))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			c := strings.Fields(l)
			if len(c) != 4 || c[1] != "if" {
				continue
			}
			source, _ := os.ReadFile(filepath.Join(tree, filepath.FromSlash(c[2])))
			fmt.Fprintf(&x, `<testcase classname="c" name="%s" file="%s">`, c[0], a)
			if !strings.Contains(string(source), c[3]) {
				exit = 1
				x.WriteString(`<failure message="assert f(2) == 4"/>`)
			}
			x.WriteString("</testcase>")
		}
	}
	x.WriteString("</testsuite></testsuites>")
	return engine.TestRun{JUnit: []byte(x.String()), Exit: exit}, nil
}

// The real Router (Stage 2) writes route.json from a high greenfield intent, and Stage 3 launches the two builders from
// that file: no helper replaces Stage 2. That intent requires dual-spec (Router §5), which is not what this test
// measures, so Stages 1 and 1.5 are replaced with a finished S*.
func TestN2FromIntentWithRealRouter(t *testing.T) {
	withStage(t, "1", func(_ context.Context, m *Machine) (string, string, string, error) {
		artifact, err := m.writeArtifact(ArtContract, contractOf(contract.SourceConsensus, "doubles"))
		return contract.StageDone, "", artifact, err
	})
	withStage(t, "1.5", func(context.Context, *Machine) (string, string, string, error) {
		return contract.StageDone, "", "", nil
	})
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	path := intentFile(t, func(in *contract.Intent) { in.Criticality = contract.CriticalityHigh })
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, _ contract.BuilderRequest) map[string]string {
		if role == contract.RoleBuilderOrthogonal {
			return map[string]string{"src/f.py": "def f(x):\n    return x + x\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "pago.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if r := byID(st)["2"]; r.Status != contract.StageDone {
		t.Fatalf("the real Stage 2 did not run: %+v %+v", r, st.Stages)
	}
	run := filepath.Join(dir, ".a2dlc/run/pago.v1")
	var r contract.Route
	readJSONTest(t, filepath.Join(run, ArtRoute), &r)
	if r.Track != contract.TrackN2 || r.CriticalityEffective != contract.CriticalityHigh {
		t.Fatalf("%+v", r)
	}
	if len(seen) != 2 || seen[0].role != contract.RoleBuilderPrimary || seen[1].role != contract.RoleBuilderOrthogonal {
		t.Fatalf("builders launched: %+v", seen)
	}
	var c contract.Candidate
	readJSONTest(t, filepath.Join(run, ArtCandidate), &c)
	if len(c.Candidates) != 2 || len(c.Dropped) != 0 {
		t.Fatalf("%+v", c)
	}
}

// A route.json that does not match the contract is read neither as N=1 nor as N=2: Stage 3 fails without launching any builder.
func TestN2RouteOutOfContractFailsStage3(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	var seen []launchRecord
	only := func(string, contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	}
	m, err := open(a, &fakeLauncher{script: buildersN2(t, &seen, only)}, Options{Slice: "suelto"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.writeArtifact(ArtRoute, map[string]any{"track": "N=2", "reason": "test"}); err != nil {
		t.Fatal(err)
	}
	st, reason, _, err := stageTier0(context.Background(), m)
	if err != nil || st != contract.StageFailed || !strings.Contains(reason, ArtRoute+" does not match the contract") || len(seen) != 0 {
		t.Fatalf("%v %s %q %+v", err, st, reason, seen)
	}
}

func TestN2EachFailsOtherBuildersTestIsCB01(t *testing.T) {
	withRouterN2(t)
	a, dir := repoLite(t)
	// an engine for Python over src/: without an engine that runs tier0, Stage 3 would escalate ("Tier 0 ran nothing")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],"tests":{"image":"x","paths":["tests/**"],"command":"run"}}`)
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "tests")
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	a.Tests = runnerTree{}
	a.Runner = &engineRun{}
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, _ contract.BuilderRequest) map[string]string {
		if role == contract.RoleBuilderPrimary {
			return map[string]string{"src/f.py": "def f(x):\n    return x*2\n", "tests/test_p.t": "tp if src/f.py x*2\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x+x\n", "tests/test_o.t": "to if src/f.py x+x\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cb01"})
	if err != nil {
		t.Fatal(err)
	}
	s4 := st.Stages[6]
	if s4.Status != contract.StageEscalated || !strings.Contains(s4.Reason, "CB-01: divergence between builders") || s4.Artifact == nil ||
		s4.Artifact.Path != ArtSelection || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", s4, st.Outcome)
	}
	run := filepath.Join(dir, ".a2dlc/run/cb01")
	var selection contract.Selection
	readJSONTest(t, filepath.Join(run, ArtSelection), &selection)
	if selection.Selected != nil || selection.Candidates[0].Verdict != contract.FAIL || selection.Candidates[1].Verdict != contract.FAIL ||
		!strings.Contains(selection.Reason, "tests/test_o.t") || !strings.Contains(selection.Reason, "tests/test_p.t") {
		t.Fatalf("%+v", selection)
	}
	// escalations.json joins those of both, each with its builder and its cause
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(run, ArtEscalations), &escalations)
	byBuilder := map[string]string{}
	for _, e := range escalations.Items {
		if e.Kind == "behavior_divergence" {
			byBuilder[e.Builder] = e.Target
		}
		if e.ClusterID == "" {
			t.Fatalf("escalated without cluster_id: %+v", e)
		}
	}
	if !strings.Contains(byBuilder[contract.RoleBuilderPrimary], "test_o.t") || !strings.Contains(byBuilder[contract.RoleBuilderOrthogonal], "test_p.t") {
		t.Fatalf("%+v", escalations.Items)
	}
	if _, err := os.Stat(filepath.Join(run, ArtVerdict)); err == nil {
		t.Fatal("with CB-01 there is no chosen verdict")
	}
}

func TestN2WithoutOrthogonalOrOneNotConverging(t *testing.T) {
	// the router asks for N=2 and roles.json has no orthogonal: it runs N=1 and the state says so
	withRouterN2(t)
	a, _, _ := repoBuilder(t)
	var seen []launchRecord
	only := func(string, contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	}
	st, err := Run(context.Background(), a, &fakeLauncher{script: buildersN2(t, &seen, only)}, Options{Slice: "only"})
	if err != nil || st.Stages[5].Status != contract.StageDone || !strings.Contains(st.Stages[5].Reason, "has no builder_orthogonal") || len(seen) != 1 {
		t.Fatalf("%v %+v", err, st.Stages[5])
	}
	// the orthogonal refuses: the primary goes on alone, as N=1, and candidate.json records it as dropped
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	seen = nil
	refuses := func(role string, pl contract.BuilderRequest) map[string]string {
		if role == contract.RoleBuilderOrthogonal {
			return nil
		}
		return only(role, pl)
	}
	st, err = Run(context.Background(), a, &fakeLauncher{script: buildersN2(t, &seen, refuses)}, Options{Slice: "niega"})
	if err != nil || st.Stages[5].Status != contract.StageDone || !strings.Contains(st.Stages[5].Reason, "builder_orthogonal without a candidate") ||
		st.Stages[6].Status != contract.StageDone {
		t.Fatalf("%v %+v %+v", err, st.Stages[5], st.Stages[6])
	}
	var c contract.Candidate
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/niega", ArtCandidate), &c)
	if len(c.Candidates) != 1 || len(c.Dropped) != 1 || c.Dropped[0].Builder != contract.RoleBuilderOrthogonal || !strings.Contains(c.Dropped[0].Reason, "I don't know how to do it") {
		t.Fatalf("%+v", c)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/niega", ArtSelection)); err == nil {
		t.Fatal("with a single candidate there is no selection")
	}
}

func TestWithEquivalenceRecordsTheGateAndKeepsUnmatchedPassesOut(t *testing.T) {
	v := contract.Verdict{Verdict: contract.PASS}
	withEquivalence(&v, app.Equivalence{Gate: contract.Gate{Status: contract.GateRan, Reason: "1 functions compared"},
		Inconclusive: []contract.TargetResult{{Target: "a::g"}}})
	if v.TestGates == nil || v.TestGates.EquivalenceS == nil || v.TestGates.EquivalenceS.Status != contract.GateRan ||
		v.TestGates.NOPOracle.Status != contract.GateNotConfigured || v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("%+v", v)
	}
	w := contract.Verdict{Verdict: contract.PASS}
	withEquivalence(&w, app.Equivalence{Gate: contract.Gate{Status: contract.GateNotApplicable}})
	if w.Verdict != contract.PASS || w.TestGates.EquivalenceS.Status != contract.GateNotApplicable {
		t.Fatalf("a not_applicable gate changes nothing: %+v", w)
	}
	if got := divergentTargets(app.Equivalence{Divergences: []contract.TargetResult{{Target: "a::f"}, {Target: "b::g"}}}); got != "a::f, b::g" {
		t.Fatalf("divergent targets %q", got)
	}
}

func TestOccamLexicographic(t *testing.T) {
	n := func(x int) *int { return &x }
	complexities := func(c, a *int, loc int) *contract.Complexity {
		return &contract.Complexity{Cyclomatic: c, ASTNodes: a, LocAdded: loc}
	}
	cases := []struct {
		a, b   *contract.Complexity
		winner int
		reason string
	}{
		{complexities(n(3), n(10), 5), complexities(n(2), n(90), 9), 1, "cyclomatic 2 versus 3"},
		{complexities(n(2), n(10), 5), complexities(n(2), n(90), 1), 0, "ast_nodes 10 versus 90"},
		{complexities(nil, n(50), 5), complexities(n(1), n(40), 9), 1, "ast_nodes 40 versus 50"}, // without cyclomatic on one side, it is not compared
		{complexities(n(2), n(10), 7), complexities(n(2), n(10), 3), 1, "diff lines 3 versus 7"},
		{complexities(n(2), n(10), 3), complexities(n(2), n(10), 3), 0, "tie"},
		{nil, complexities(n(1), n(1), 1), 0, "could not be measured"},
	}
	for _, c := range cases {
		if onDisk, m := occam([2]*contract.Complexity{c.a, c.b}); onDisk != c.winner || !strings.Contains(m, c.reason) {
			t.Errorf("%d %q, wanted %d %q", onDisk, m, c.winner, c.reason)
		}
	}
}

// engineVerdicts is engineRun with the differential verdict according to the candidate's file: "FAILS" gives FAIL with a
// counterexample, "INCON" gives INCONCLUSIVE and anything else PASS.
type engineVerdicts struct{ engineRun }

func (f *engineVerdicts) Run(ctx context.Context, cfg contract.EngineConfig, req contract.Request, baseDir, candidateDir string) (contract.Response, error) {
	r, err := f.engineRun.Run(ctx, cfg, req, baseDir, candidateDir)
	for i := range r.Targets {
		b, _ := os.ReadFile(filepath.Join(candidateDir, strings.Split(r.Targets[i].Target, "::")[0]))
		switch {
		case strings.Contains(string(b), "FAILS"):
			r.Targets[i].Verdict = contract.FAIL
			r.Targets[i].Counterexample = &contract.Counterexample{Args: "x=2", Old: "2", New: "4"}
		case strings.Contains(string(b), "INCON"):
			r.Targets[i].Verdict, r.Targets[i].Reason = contract.INCONCLUSIVE, "budget exhausted"
		}
	}
	return r, err
}

// n2With runs N=2 with an engineVerdicts: each role writes the src/f.py that source says.
func n2With(t *testing.T, slice string, source map[string]string) (contract.RunState, string) {
	t.Helper()
	withRouterN2(t)
	a, dir, _ := repoBuilder(t)
	a.Runner = &engineVerdicts{}
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, _ contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": source[role]}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: slice})
	if err != nil {
		t.Fatal(err)
	}
	return st, filepath.Join(dir, ".a2dlc")
}

func TestN2OnlyOnePassesIsChosen(t *testing.T) {
	st, repo := n2With(t, "uno", map[string]string{
		contract.RoleBuilderPrimary:    "def f(x):\n    return x * 2\n",
		contract.RoleBuilderOrthogonal: "def f(x):\n    return x + x  # FAILS\n"})
	s4 := st.Stages[6]
	var selection contract.Selection
	readJSONTest(t, filepath.Join(repo, "run/uno", ArtSelection), &selection)
	if s4.Status != contract.StageDone || selection.Selected == nil || *selection.Selected != contract.RoleBuilderPrimary || selection.Candidates[1].Verdict != contract.FAIL ||
		!strings.Contains(selection.Reason, "only builder_primary gave PASS") || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %+v %v", s4, selection, st.Outcome)
	}
	var verdictP, repoVerdict contract.Verdict
	readJSONTest(t, filepath.Join(repo, "run/uno", ArtVerdictPrimary), &verdictP)
	readJSONTest(t, filepath.Join(repo, "verdict.json"), &repoVerdict)
	if verdictP.Verdict != contract.PASS || repoVerdict.TreeHash != verdictP.TreeHash || repoVerdict.Verdict != contract.PASS {
		t.Fatalf("the repo verdict is not the primary's: %s %s %s", repoVerdict.Verdict, repoVerdict.TreeHash, verdictP.TreeHash)
	}
}

func TestN2NonePassesWorstRemains(t *testing.T) {
	// FAIL weighs more than INCONCLUSIVE whichever builder gives it: the swap leaves the worst one's verdict
	cases := []struct{ name, primary, orthogonal, worst string }{
		{"pfail", "return x  # FAILS", "return x  # INCON", contract.RoleBuilderPrimary},
		{"ofail", "return x  # INCON", "return x  # FAILS", contract.RoleBuilderOrthogonal},
	}
	for _, c := range cases {
		st, repo := n2With(t, c.name, map[string]string{
			contract.RoleBuilderPrimary:    "def f(x):\n    " + c.primary + "\n",
			contract.RoleBuilderOrthogonal: "def f(x):\n    " + c.orthogonal + "\n"})
		var selection contract.Selection
		readJSONTest(t, filepath.Join(repo, "run", c.name, ArtSelection), &selection)
		if selection.Selected != nil || !strings.Contains(selection.Reason, "neither gave PASS") || !strings.Contains(selection.Reason, "the one from "+c.worst) ||
			st.Stages[6].Status != contract.StageDone || st.Outcome == nil || *st.Outcome != contract.FAIL {
			t.Fatalf("%s: %+v %v %+v", c.name, selection, st.Outcome, st.Stages[6])
		}
		var repoVerdict, worstVerdict contract.Verdict
		artifact := ArtVerdictPrimary
		if c.worst == contract.RoleBuilderOrthogonal {
			artifact = ArtVerdictOrthogonal
		}
		readJSONTest(t, filepath.Join(repo, "verdict.json"), &repoVerdict)
		readJSONTest(t, filepath.Join(repo, "run", c.name, artifact), &worstVerdict)
		if worstVerdict.Verdict != contract.FAIL || repoVerdict.TreeHash != worstVerdict.TreeHash || repoVerdict.Verdict != contract.FAIL {
			t.Fatalf("%s: the repo verdict is not the worst one: %s %s", c.name, repoVerdict.TreeHash, worstVerdict.TreeHash)
		}
		var escalations contract.Escalations
		readJSONTest(t, filepath.Join(repo, "run", c.name, ArtEscalations), &escalations)
		if escalations.TreeHash != worstVerdict.TreeHash {
			t.Fatalf("%s: escalations.json is from another tree: %s", c.name, escalations.TreeHash)
		}
	}
}

func TestN2NoCandidateEscalatesOrFails(t *testing.T) {
	withRouterN2(t)
	// both builders exhaust Tier 0: the stage escalates and says what each one left outside
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	var seen []launchRecord
	broken := func(string, contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "ROTO\n"}
	}
	st, err := Run(context.Background(), a, &fakeLauncher{script: buildersN2(t, &seen, broken)}, Options{Slice: "agotan", MaxInner: 1})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.Contains(s3.Reason, "N=2 without a candidate: builder_primary: ") || !strings.Contains(s3.Reason, "builder_orthogonal: ") ||
		strings.Count(s3.Reason, "Tier 0 does not converge") != 2 || len(seen) != 2 || st.Stages[6].Status != contract.StagePending || st.Outcome == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v %v", s3, seen, st.Outcome)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/agotan", ArtCandidate)); err == nil {
		t.Fatal("without a candidate there is no candidate.json")
	}
	// a tool that goes down is not a builders' problem: failed and INCONCLUSIVE
	a, _, _ = repoBuilder(t)
	write(t, a.Repo.Dir, ".a2dlc/roles.json", rolesN2)
	falls := &fakeLauncher{script: func(LaunchSpec) (*contract.RoleResponse, int, bool) { return nil, 1, false }}
	if st, err = Run(context.Background(), a, falls, Options{Slice: "caen"}); err != nil || st.Stages[5].Status != contract.StageFailed ||
		!strings.Contains(st.Stages[5].Reason, "N=2 without a candidate") || st.Outcome == nil || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v %+v %v", err, st.Stages[5], st.Outcome)
	}
}

func TestN2InRefactorBrownfieldSecondBuilderIsLegacy(t *testing.T) {
	// the Router writes N=2 for these modes with "the second builder is the legacy one": Stage 3 launches only one
	// builder, with or without builder_orthogonal in roles.json, and does not note that it ran N=1 (no role is missing)
	for _, mode := range []string{contract.ModeRefactor, contract.ModeBrownfield} {
		for _, withOrthogonal := range []bool{true, false} {
			a, dir, _ := repoBuilder(t)
			if withOrthogonal {
				write(t, dir, ".a2dlc/roles.json", rolesN2)
			}
			var seen []launchRecord
			only := func(string, contract.BuilderRequest) map[string]string {
				return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
			}
			m, err := open(a, &fakeLauncher{script: buildersN2(t, &seen, only)}, Options{Slice: "legacy", Mode: mode})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := m.writeArtifact(ArtRoute, routeN2()); err != nil {
				t.Fatal(err)
			}
			st, reason, _, err := stageTier0(context.Background(), m)
			if err != nil || st != contract.StageDone || !strings.Contains(reason, "@a2dlc header missing") || len(seen) != 1 || seen[0].role != contract.RoleBuilderPrimary {
				t.Fatalf("%s orthogonal=%v: %v %s %q %+v", mode, withOrthogonal, err, st, reason, seen)
			}
			var c contract.Candidate
			readJSONTest(t, filepath.Join(m.dir, ArtCandidate), &c)
			if len(c.Dropped) != 0 || len(c.Candidates) > 1 {
				t.Fatalf("%s: %+v", mode, c)
			}
		}
	}
}

// withStage replaces a stage with f for the duration of the test.
func withStage(t *testing.T, id string, f Stage) {
	t.Helper()
	previous := stages[id]
	stages[id] = f
	t.Cleanup(func() { stages[id] = previous })
}

// engineUncovered leaves the changed lines without cover (its targets examine other lines) and records each differential request.
type engineUncovered struct {
	engineRun
	diffs []contract.Request
}

func (f *engineUncovered) Run(ctx context.Context, cfg contract.EngineConfig, req contract.Request, baseDir, candidateDir string) (contract.Response, error) {
	f.diffs = append(f.diffs, req)
	r, err := f.engineRun.Run(ctx, cfg, req, baseDir, candidateDir)
	for i := range r.Targets {
		r.Targets[i].Lines = []int{50, 60}
	}
	return r, err
}

func TestN2OrthogonalSaboteurTestsRejudgePrimaryWithRelaxations(t *testing.T) {
	withRouterN2(t)
	relaxations := []string{"ignore_order", "float_tolerance:1e-9"}
	withStage(t, "1", func(_ context.Context, m *Machine) (string, string, string, error) {
		s := contractOf(contract.SourceConsensus, "doubles")
		s.PiS = contract.PiS{Equivalence: contract.EquivalenceRelaxed, Relaxations: relaxations}
		artifact, err := m.writeArtifact(ArtContract, s)
		return contract.StageDone, "", artifact, err
	})
	withStage(t, "1.5", func(context.Context, *Machine) (string, string, string, error) {
		return contract.StageDone, "", "", nil
	})
	a, dir, _ := repoBuilder(t)
	engine := &engineUncovered{}
	a.Runner = engine
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],`+
		`"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	write(t, dir, "tests/test_old.t", "old passed\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "with tests")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{
	 "builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0},
	 "builder_orthogonal":{"family":"gemini","image":"img:2","argv":["sh","y"],"retries":0},
	 "saboteur":{"family":"script","image":"img:3","argv":["sh","z"],"retries":0}}}`)
	a.Tests = &fakeRunner{}
	var seen []launchRecord
	builders := buildersN2(t, &seen, func(role string, _ contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})
	saboteur := fakeSaboteur("tests/test_sab.t", "t_sab passed\ncubre src/f.py:1-2\n")
	saboteurCalls := 0
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role != contract.RoleSaboteur {
			return builders(s)
		}
		// the first time it refuses: the primary is left without the saboteur's tests, which the orthogonal's round brings in
		if saboteurCalls++; saboteurCalls == 1 {
			return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
				Status: contract.RoleRefused, Reason: "not yet", Payload: json.RawMessage(`{}`)}, 0, false
		}
		resp, e, to := saboteur(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "rejuzga"})
	if err != nil {
		t.Fatal(err)
	}
	s4 := st.Stages[6]
	if s4.Status != contract.StageDone || !strings.Contains(s4.Reason, "builder_primary: ") || !strings.Contains(s4.Reason, "not yet") ||
		!strings.Contains(s4.Reason, "judged again with the tests from the orthogonal's rounds") || saboteurCalls != 2 {
		t.Fatalf("%+v saboteurs=%d", s4, saboteurCalls)
	}
	// primary, orthogonal, the orthogonal's round with the overlay and the primary's re-judgment: all with the π_S relaxations
	if len(engine.diffs) != 4 {
		t.Fatalf("requests to the engine: %d", len(engine.diffs))
	}
	for i, d := range engine.diffs {
		if strings.Join(d.EquivalenceRelaxations, ",") != strings.Join(relaxations, ",") {
			t.Errorf("request %d arrived with relaxations %v", i, d.EquivalenceRelaxations)
		}
	}
	// the primary was re-judged with the overlay: its verdict no longer has lines without cover
	var verdictP contract.Verdict
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/rejuzga", ArtVerdictPrimary), &verdictP)
	if verdictP.Verdict != contract.PASS || len(hasUncovered(verdictP)) != 0 {
		t.Fatalf("%s %+v", verdictP.Verdict, verdictP.Coverage)
	}
}
