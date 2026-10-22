package run

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// engineMutant is a fake engine for Stage 5: diff leaves every file out of scope (the coverage comes from the tests),
// without stubs or signatures, and mutate always returns two mutants on line 2 of src/f.py, marked MUT_A and MUT_B,
// without respecting max_mutants (so only the core's cap is tested).
type engineMutant struct{ requests []contract.MutateBudget }

func (m *engineMutant) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.Response, error) {
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
	for _, f := range req.ChangedFiles {
		r.OutOfScope = append(r.OutOfScope, contract.OutOfScope{File: f, Reason: "not_exported"})
	}
	return r, nil
}

func (m *engineMutant) Stub(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.StubResponse, map[string][]byte, error) {
	return contract.StubResponse{SchemaVersion: 1, Stubbed: []contract.StubFile{}, Unstubbed: []string{}}, nil, nil
}

func (m *engineMutant) Signatures(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.SignaturesResponse, error) {
	return contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}, nil
}

func (m *engineMutant) Mutate(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.MutateResponse, map[string][]byte, error) {
	var b contract.MutateBudget
	if err := contract.DecodeStrictBytes(req.Budget, &b); err != nil {
		return contract.MutateResponse{}, nil, err
	}
	m.requests = append(m.requests, b)
	return contract.MutateResponse{SchemaVersion: 1, NotGenerated: []contract.FileReason{}, Pruned: []contract.PrunedMutant{{File: "src/f.py", Line: 2, Reason: contract.PruneAssert}}, Mutants: []contract.Mutant{
			{ID: "m001", File: "src/f.py", Line: 2, Operator: contract.OpArithmetic, Description: "arithmetic operator + changed to -"},
			{ID: "m002", File: "src/f.py", Line: 2, Operator: contract.OpConstant, Description: "numeric constant changed: +1"}}},
		map[string][]byte{"m001": []byte("def f(x):\n    return x - 2  # MUT_A\n"), "m002": []byte("def f(x):\n    return x + 3  # MUT_B\n")}, nil
}

// runnerMutant is the fake runner with one more state: 'detecta:<marca>' fails if any file under src/ of the tree has
// the mark (the test detects that mutant) and passes if not.
type runnerMutant struct{ runs int }

func (f *runnerMutant) RunTests(_ context.Context, _ contract.TestsConfig, _ string, env map[string]string, tree string) (engine.TestRun, error) {
	f.runs++
	src := ""
	filepath.WalkDir(filepath.Join(tree, "src"), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			b, _ := os.ReadFile(p)
			src += string(b)
		}
		return nil
	})
	var x, cov strings.Builder
	x.WriteString("<testsuites><testsuite>")
	exit := 0
	for _, a := range strings.Fields(env["A2DLC_TESTS"]) {
		b, err := os.ReadFile(filepath.Join(tree, filepath.FromSlash(a)))
		if err != nil {
			continue
		}
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			c := strings.Fields(l)
			if len(c) != 2 {
				continue
			}
			if c[0] == "cubre" {
				i := strings.LastIndex(c[1], ":")
				var d, h int
				fmt.Sscanf(c[1][i+1:], "%d-%d", &d, &h)
				fmt.Fprintf(&cov, "SF:/work/%s\n", c[1][:i])
				for n := 1; n < 100; n++ {
					hit := 0
					if n >= d && n <= h {
						hit = 1
					}
					fmt.Fprintf(&cov, "DA:%d,%d\n", n, hit)
				}
				cov.WriteString("end_of_record\n")
				continue
			}
			state := c[1]
			if marca, ok := strings.CutPrefix(state, "detecta:"); ok {
				state = "passed"
				if strings.Contains(src, marca) {
					state = "failed"
				}
			}
			fmt.Fprintf(&x, `<testcase classname="c" name="%s" file="%s">`, c[0], a)
			if state != "passed" {
				exit = 1
				x.WriteString(`<failure message="assert"/>`)
			}
			x.WriteString("</testcase>")
		}
	}
	x.WriteString("</testsuite></testsuites>")
	r := engine.TestRun{JUnit: []byte(x.String()), Exit: exit}
	if cov.Len() > 0 {
		r.Coverage = []byte(cov.String())
	}
	return r, nil
}

// repoMutation: Python engine over src/, config.tests and a candidate that changes line 2 of src/f.py, with a test
// that runs it and detects MUT_A (not MUT_B). Stage 4 gives PASS without the Saboteur.
func repoMutation(t *testing.T, mutation string) (app.App, string, *engineMutant, *runnerMutant) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],`+
		`"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}`+mutation+`}`)
	write(t, dir, "tests/test_old.t", "old passed\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "with tests")
	write(t, dir, "src/f.py", "def f(x):\n    return x + 2\n")
	write(t, dir, "tests/test_f.t", "t_f detecta:MUT_A\ncubre src/f.py:1-2\n")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"saboteur":{"family":"script","image":"img:1","argv":["sh","x"],"retries":0}}}`)
	m, r := &engineMutant{}, &runnerMutant{}
	a.Runner, a.Tests = m, r
	return a, dir, m, r
}

func readMutation(t *testing.T, dir, slice string) contract.MutationReport {
	t.Helper()
	var rep contract.MutationReport
	b, err := os.ReadFile(filepath.Join(dir, ".a2dlc/run", slice, ArtMutation))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &rep); err != nil {
		t.Fatal(err)
	}
	return rep
}

func mutantsByID(rep contract.MutationReport) map[string]contract.MutantResult {
	out := map[string]contract.MutantResult{}
	for _, r := range rep.Mutants {
		out[r.ID] = r
	}
	return out
}

func TestStage5KillsSurvivorsWithTheSaboteur(t *testing.T) {
	a, dir, engine, _ := repoMutation(t, "")
	launcherFake := &fakeLauncher{}
	var request contract.SaboteurRequest
	var escalations contract.Escalations
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		var req contract.RoleRequest
		b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
		if err := contract.DecodeStrictBytes(b, &req); err != nil || req.Stage != "5" {
			t.Fatalf("%v %+v", err, req)
		}
		if err := contract.DecodeStrictBytes(req.Payload, &request); err != nil {
			t.Fatal(err)
		}
		b, _ = os.ReadFile(filepath.Join(s.InDir, ArtEscalations))
		if err := contract.DecodeStrictBytes(b, &escalations); err != nil {
			t.Fatal(err)
		}
		resp, e, to := fakeSaboteur("tests/test_sab.t", "t_sab detecta:MUT_B\n")(s)
		resp.RunID = req.RunID
		return resp, e, to
	}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "mut.v1", MaxRounds: 2})
	if err != nil {
		t.Fatal(err)
	}
	s4, s5 := st.Stages[6], st.Stages[7]
	if s4.Status != contract.StageDone || s4.Iterations != 0 || s5.Status != contract.StageDone || s5.Iterations != 1 || s5.Reason != "1 equivalent mutants pruned (see pruned_equivalent)" ||
		s5.Artifact == nil || s5.Artifact.Path != ArtMutation || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %+v outcome=%v", s4, s5, st.Outcome)
	}
	// the engine got only the changed line and the default cap
	if len(engine.requests) != 1 || engine.requests[0].MaxMutants != MaxMutantsDefault || fmt.Sprint(engine.requests[0].ChangedLines) != "map[src/f.py:[2]]" {
		t.Fatalf("%+v", engine.requests)
	}
	// the Saboteur got the survivors without code, without uncovered lines, and saw the escalation
	if len(request.Uncovered) != 0 || len(request.Survivors) != 1 || request.Survivors[0] != (contract.SaboteurSurvivor{File: "src/f.py", Line: 2,
		Operator: contract.OpConstant, Description: "numeric constant changed: +1"}) || request.Round != 1 {
		t.Fatalf("%+v", request)
	}
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "mutant_survived" || escalations.Items[0].Target != "src/f.py::mutant:m002" ||
		escalations.Items[0].Needs == nil || escalations.Items[0].Needs.Role != contract.RoleSaboteur || escalations.Items[0].Evidence.Mutant.Line != 2 {
		t.Fatalf("%+v", escalations.Items)
	}
	if len(launcherFake.seen) != 1 || strings.Join(launcherFake.seen[0], ",") != "escalations.json,request.json,signatures.json" {
		t.Fatalf("the Saboteur saw %v", launcherFake.seen)
	}
	rep := readMutation(t, dir, "mut.v1")
	ms := mutantsByID(rep)
	if rep.Verdict != contract.PASS || rep.KillRate == nil || *rep.KillRate != 1 || rep.Round != 1 || rep.Threshold != KillRateDefault ||
		strings.Join(rep.Tests, ",") != "tests/test_f.t,tests/test_sab.t" || ms["m001"].Status != contract.MutantKilled ||
		strings.Join(ms["m001"].KilledBy, ",") != "tests/test_f.t::c::t_f" || strings.Join(ms["m002"].KilledBy, ",") != "tests/test_sab.t::c::t_sab" {
		t.Fatalf("%+v", rep)
	}
	// the pruned ones go to their list, with their reason, and are not mutants: the kill rate comes from m001 and m002 alone
	if want := []contract.PrunedMutant{{File: "src/f.py", Line: 2, Reason: contract.PruneAssert}}; !reflect.DeepEqual(rep.PrunedEquivalent, want) || len(rep.Mutants) != 2 {
		t.Fatalf("pruned_equivalent %+v, mutants %d", rep.PrunedEquivalent, len(rep.Mutants))
	}
	// with the gate passed, no mutant_survived escalation remains
	var finished contract.Escalations
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/mut.v1", ArtEscalations))
	if err := contract.DecodeStrictBytes(b, &finished); err != nil || len(finished.Items) != 0 {
		t.Fatalf("%v %+v", err, finished.Items)
	}
	if problems, err := VerifyRun(a, "mut.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

// The usual path: the candidate brings no tests, the Stage 4 Saboteur covers the line and its test, with the overlay, it is
// all of T at Stage 5; there it kills one mutant and the Stage 5 Saboteur brings the one that kills the other.
func TestStage5InheritsTheStage4Overlay(t *testing.T) {
	a, dir, _, _ := repoMutation(t, "")
	os.Remove(filepath.Join(dir, "tests/test_f.t"))
	requests := map[string]contract.SaboteurRequest{}
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		var req contract.RoleRequest
		b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
		if err := contract.DecodeStrictBytes(b, &req); err != nil {
			t.Fatal(err)
		}
		var pl contract.SaboteurRequest
		if err := contract.DecodeStrictBytes(req.Payload, &pl); err != nil {
			t.Fatal(err)
		}
		requests[req.Stage] = pl
		path, content := "tests/test_s4.t", "t_s4 detecta:MUT_A\ncubre src/f.py:1-2\n"
		if req.Stage == "5" {
			path, content = "tests/test_s5.t", "t_s5 detecta:MUT_B\n"
		}
		resp, e, to := fakeSaboteur(path, content)(s)
		resp.RunID = req.RunID
		return resp, e, to
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "inherits"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Stages[6].Iterations != 1 || st.Stages[7].Iterations != 1 || st.Stages[7].Status != contract.StageDone || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %+v %v", st.Stages[6], st.Stages[7], *st.Outcome)
	}
	p4, p5 := requests["4"], requests["5"]
	if len(p4.Uncovered) != 1 || p4.Survivors != nil || len(p5.Uncovered) != 0 || len(p5.Survivors) != 1 ||
		strings.Join(p5.PreviousTests, ",") != "tests/test_s4.t" {
		t.Fatalf("%+v\n%+v", p4, p5)
	}
	rep := readMutation(t, dir, "inherits")
	ms := mutantsByID(rep)
	if strings.Join(rep.Tests, ",") != "tests/test_s4.t,tests/test_s5.t" || strings.Join(ms["m001"].KilledBy, ",") != "tests/test_s4.t::c::t_s4" ||
		strings.Join(ms["m002"].KilledBy, ",") != "tests/test_s5.t::c::t_s5" {
		t.Fatalf("%+v", rep)
	}
	var lg contract.RunLedger
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/inherits/ledger.json"))
	if err := contract.DecodeStrictBytes(b, &lg); err != nil || len(lg.Entries) != 2 || lg.Entries[0].Stage != "4" || lg.Entries[1].Stage != "5" {
		t.Fatalf("%v %+v", err, lg.Entries)
	}
	if problems, err := VerifyRun(a, "inherits"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

func TestStage5WithoutImprovementIsInconclusiveAfterRounds(t *testing.T) {
	a, dir, _, runner := repoMutation(t, "")
	n := 0
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		n++
		resp, e, to := fakeSaboteur(fmt.Sprintf("tests/test_%d.t", n), fmt.Sprintf("t_%d passed\n", n))(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "no-improvement", MaxRounds: 2})
	if err != nil {
		t.Fatal(err)
	}
	s5 := st.Stages[7]
	if s5.Status != contract.StageDone || s5.Iterations != 2 || !strings.Contains(s5.Reason, "kill_rate 0.50 < 0.80") ||
		!strings.Contains(s5.Reason, "after 2 saboteur rounds") || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%+v %v", s5, *st.Outcome)
	}
	rep := readMutation(t, dir, "no-improvement")
	if rep.Verdict != contract.INCONCLUSIVE || *rep.KillRate != 0.5 || mutantsByID(rep)["m002"].Status != contract.MutantSurvived || rep.Round != 2 ||
		len(rep.Tests) != 3 {
		t.Fatalf("%+v", rep)
	}
	// three measured rounds (0, 1, 2): one clean run and two mutant runs each, after the 2 runs of Stage 4
	if runner.runs != 2+3*3 {
		t.Fatalf("runs %d", runner.runs)
	}
	// a red Saboteur test on the candidate without mutating kills no one: the round is not measured and the loop stops
	a, dir, _, _ = repoMutation(t, "")
	launcherFake = &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		resp, e, to := fakeSaboteur("tests/test_red.t", "t_red failed\n")(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "red", MaxRounds: 3}); err != nil {
		t.Fatal(err)
	}
	rep = readMutation(t, dir, "red")
	if st.Stages[7].Iterations != 1 || rep.Verdict != contract.INCONCLUSIVE || rep.KillRate != nil ||
		strings.Join(rep.RedOnCandidate, ",") != "tests/test_red.t::c::t_red" || mutantsByID(rep)["m001"].Status != contract.MutantNotRun {
		t.Fatalf("%+v %+v", st.Stages[7], rep)
	}
}

func TestStage5CapBelowListLeavesItNotRunAndNotPass(t *testing.T) {
	a, dir, engine, _ := repoMutation(t, `,"mutation":{"max_mutants":1}`)
	launcherFake := &fakeLauncher{}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "cap"})
	if err != nil {
		t.Fatal(err)
	}
	rep := readMutation(t, dir, "cap")
	ms := mutantsByID(rep)
	if engine.requests[0].MaxMutants != 1 || rep.MaxMutants != 1 || ms["m001"].Status != contract.MutantKilled ||
		ms["m002"].Status != contract.MutantNotRun || ms["m002"].Reason != "max_mutants cap=1" || *rep.KillRate != 1 ||
		rep.Verdict != contract.INCONCLUSIVE || !strings.Contains(rep.Reason, "1 mutants not run") {
		t.Fatalf("%+v", rep)
	}
	// the Saboteur does not fix a cap: it is not called
	if len(launcherFake.seen) != 0 || *st.Outcome != contract.INCONCLUSIVE || st.Stages[7].Iterations != 0 {
		t.Fatalf("%v %+v", launcherFake.seen, st.Stages[7])
	}
}

// A mutant that could not be judged (timeout, without JUnit, red without an identifiable test) blocks the PASS just like one that is not
// run: the contract JSON says 'PASS only if nothing was left unmeasured' (mutation.schema.json).
func TestJudgingAnInconclusiveMutantDoesNotPass(t *testing.T) {
	rep := contract.MutationReport{Mutants: []contract.MutantResult{
		{Mutant: contract.Mutant{ID: "m001"}, Status: contract.MutantKilled, KilledBy: []string{"t"}}, {Mutant: contract.Mutant{ID: "m002"}, Status: contract.MutantKilled, KilledBy: []string{"t"}},
		{Mutant: contract.Mutant{ID: "m003"}, Status: contract.MutantInconclusive}}}
	judge(&rep, 0.8)
	if rep.Verdict != contract.INCONCLUSIVE || *rep.KillRate != 1 || !strings.Contains(rep.Reason, "1 not judged") {
		t.Fatalf("%+v", rep)
	}
	rep.Mutants[2].Status = contract.MutantKilled
	if judge(&rep, 0.8); rep.Verdict != contract.PASS {
		t.Fatalf("%+v", rep)
	}
}

// onlyPruned is an engine that only prunes: no changed line gives mutants, so the stage is skipped and the reason
// counts what was pruned.
type onlyPruned struct{ *engineMutant }

func (onlyPruned) Mutate(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.MutateResponse, map[string][]byte, error) {
	return contract.MutateResponse{SchemaVersion: 1, Mutants: []contract.Mutant{}, NotGenerated: []contract.FileReason{},
		Pruned: []contract.PrunedMutant{{File: "src/f.py", Line: 2, Reason: contract.PruneLogging}}}, map[string][]byte{}, nil
}

func TestStage5PruneWithoutMutantsSkipsWithReason(t *testing.T) {
	a, _, _, _ := repoMutation(t, "")
	a.Runner = onlyPruned{a.Runner.(*engineMutant)}
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "only-pruned"})
	if err != nil || st.Stages[7].Status != contract.StageSkipped || !strings.Contains(st.Stages[7].Reason, "1 equivalent mutants pruned") {
		t.Fatalf("%v %+v", err, st.Stages[7])
	}
}

func TestStage5SkipsWithoutPassOrTests(t *testing.T) {
	// Stage 4 INCONCLUSIVE (line without coverage and no Saboteur): nothing is mutated
	a, dir, _ := repoWithTests(t)
	os.Remove(filepath.Join(dir, ".a2dlc/roles.json"))
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "no-pass"})
	if err != nil || st.Stages[7].Status != contract.StageSkipped || !strings.Contains(st.Stages[7].Reason, "Stage 4 was not PASS") {
		t.Fatalf("%v %+v", err, st.Stages[7])
	}
	// without config.tests there is nothing to run T with, and the outcome is still Stage 4's
	a, _ = repoLite(t)
	st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lite"})
	if err != nil || st.Stages[7].Status != contract.StageSkipped || !strings.Contains(st.Stages[7].Reason, "no config.tests") || *st.Outcome != contract.PASS {
		t.Fatalf("%v %+v", err, st.Stages[7])
	}
	// an engine that does not mutate leaves the file listed and, with no mutant at all, the stage is INCONCLUSIVE: the file in
	// scope was not measured, so it cannot be a PASS nor a skip that the outcome ignores
	a, dir, _, _ = repoMutation(t, "")
	a.Runner = withoutMutate{a.Runner.(*engineMutant)}
	st, err = Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "no-engine"})
	if err != nil || st.Stages[7].Status != contract.StageDone || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v %+v", err, st.Stages[7])
	}
	rep := readMutation(t, dir, "no-engine")
	if rep.Verdict != contract.INCONCLUSIVE || !reflect.DeepEqual(rep.UnsupportedFiles, []string{"src/f.py"}) ||
		!strings.Contains(rep.Reason, "1 in-scope files not mutated") || len(rep.Unsupported) != 1 || !strings.Contains(rep.Unsupported[0].Reason, "the python engine does not mutate") {
		t.Fatalf("%+v", rep)
	}
}

// A changed file in scope that no engine mutates (src/g.rb has no engine: the config's engine covers src/f.py only) is listed
// in unsupported_files and blocks PASS even when every mutant of the other file is killed.
func TestStage5MixedUnsupportedFileBlocksPass(t *testing.T) {
	a, dir, _, _ := repoMutation(t, "")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/f.py"],"budget":{}}],`+
		`"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	// the base's config is the one the mutants judge (as check does): commit it, so src/g.rb is in scope and no engine covers it
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "mixed config")
	write(t, dir, "tests/test_f.t", "t_f detecta:MUT_A\nt_g detecta:MUT_B\ncubre src/f.py:1-2\ncubre src/g.rb:1-2\n")
	write(t, dir, "src/g.rb", "puts 1\n")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	rep := readMutation(t, dir, "mixed")
	if st.Stages[7].Status != contract.StageDone || *st.Outcome != contract.INCONCLUSIVE || rep.Verdict != contract.INCONCLUSIVE ||
		rep.KillRate == nil || *rep.KillRate != 1 || !reflect.DeepEqual(rep.UnsupportedFiles, []string{"src/g.rb"}) ||
		!strings.Contains(rep.Reason, "1 in-scope files not mutated") {
		t.Fatalf("%+v %+v", st.Stages[7], rep)
	}
}

// Judging: unsupported files block PASS whatever the kill rate, and they are counted with the other reasons.
func TestJudgeUnsupportedFilesBlockPass(t *testing.T) {
	killed := []contract.MutantResult{{Mutant: contract.Mutant{ID: "m001"}, Status: contract.MutantKilled, KilledBy: []string{"t"}}}
	rep := contract.MutationReport{Mutants: killed, UnsupportedFiles: []string{"src/a.rb", "src/b.rb"}}
	judge(&rep, 0.8)
	if rep.Verdict != contract.INCONCLUSIVE || !strings.Contains(rep.Reason, "2 in-scope files not mutated") || *rep.KillRate != 1 {
		t.Fatalf("%+v", rep)
	}
	clean := contract.MutationReport{Mutants: killed}
	if judge(&clean, 0.8); clean.Verdict != contract.PASS {
		t.Fatalf("%+v", clean)
	}
	// below the threshold the reason keeps the kill-gate message, and the count is added in front
	low := contract.MutationReport{Mutants: []contract.MutantResult{{Mutant: contract.Mutant{ID: "m001"}, Status: contract.MutantSurvived}},
		UnsupportedFiles: []string{"src/a.rb"}}
	judge(&low, 0.8)
	if low.Verdict != contract.INCONCLUSIVE || !strings.HasPrefix(low.Reason, "1 in-scope files not mutated") || !strings.Contains(low.Reason, "kill gate") {
		t.Fatalf("%+v", low)
	}
}

func (m *engineMutant) Tier0(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.Tier0Response, error) {
	// an engine that reviews and finds nothing: Tier 0 counts as run (an ErrWithoutTier0 would escalate Stage 3)
	return contract.Tier0Response{SchemaVersion: contract.SchemaVersion, OK: true, Findings: []contract.Tier0Finding{}}, nil
}

func (m *engineMutant) Complexity(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.ComplexityResponse, error) {
	return contract.ComplexityResponse{}, engine.ErrWithoutComplexity
}

type withoutMutate struct{ *engineMutant }

func (withoutMutate) Mutate(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.MutateResponse, map[string][]byte, error) {
	return contract.MutateResponse{}, nil, fmt.Errorf("mutate operation not supported")
}

// The intent's mutant cap (budget.max_mutants) wins over config.mutation.max_mutants, which wins over the
// default; null leaves the config's or the default.
func TestStage5MutantCapFromIntent(t *testing.T) {
	four, one := 4, 1
	for _, c := range []struct {
		name     string
		mutation string
		intent   *int
		want     int
	}{
		{"intent without config", "", &one, 1},
		{"intent over config", `,"mutation":{"max_mutants":2}`, &one, 1},
		{"intent greater than config", `,"mutation":{"max_mutants":1}`, &four, 4},
		{"null with config", `,"mutation":{"max_mutants":1}`, nil, 1},
		{"null without config", "", nil, MaxMutantsDefault},
	} {
		a, dir, engine, _ := repoMutation(t, c.mutation)
		write(t, dir, "tests/test_f.t", "t_f detecta:MUT_A\nt_g detecta:MUT_B\ncubre src/f.py:1-2\n") // kills both: the Saboteur does not come in
		path := intentFile(t, func(in *contract.Intent) {
			in.SliceID, in.Mode, in.Budget.MaxMutants = "cap-intent", contract.ModeLite, c.intent
		})
		if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "cap-intent", Intent: path}); err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		rep := readMutation(t, dir, "cap-intent")
		if len(engine.requests) != 1 || engine.requests[0].MaxMutants != c.want || rep.MaxMutants != c.want {
			t.Fatalf("%s: requests %+v, mutation.json max_mutants %d, wanted %d", c.name, engine.requests, rep.MaxMutants, c.want)
		}
		// the fake engine returns two mutants without respecting the cap: the core's cap leaves the extra one not run
		if c.want == 1 && mutantsByID(rep)["m002"].Status != contract.MutantNotRun {
			t.Fatalf("%s: %+v", c.name, rep.Mutants)
		}
	}
}

// engineMutantN2 is engineMutant with Complexity (the winner is the candidate whose src/f.py says ORTO) and a Mutate that notes the
// src/f.py of the candidate it receives.
type engineMutantN2 struct {
	*engineMutant
	runner    *runnerTreeMutant
	candidate string // src/f.py of the tree that Mutate received
	before    int    // T runs that had already happened when Mutate arrived
}

func (m *engineMutantN2) Mutate(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candidateDir string) (contract.MutateResponse, map[string][]byte, error) {
	b, _ := os.ReadFile(filepath.Join(candidateDir, "src", "f.py"))
	m.candidate, m.before = string(b), len(m.runner.sources)
	return m.engineMutant.Mutate(ctx, e, req, baseDir, candidateDir)
}

func (m *engineMutantN2) Complexity(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.ComplexityResponse, error) {
	r := contract.ComplexityResponse{SchemaVersion: 1, Files: []contract.ComplexityFile{}}
	for _, file := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candidateDir, file))
		c, n := 2, len(b)
		if strings.Contains(string(b), "ORTO") {
			c = 1
		}
		r.Files = append(r.Files, contract.ComplexityFile{File: file, LocAdded: strings.Count(string(b), "\n"), Cyclomatic: &c, ASTNodes: &n})
	}
	return r, engine.ValidateComplexity(req, r)
}

// runnerTreeMutant is the runnerMutant that notes the src/f.py of the tree on each T run.
type runnerTreeMutant struct {
	runnerMutant
	sources []string
}

func (f *runnerTreeMutant) RunTests(ctx context.Context, tc contract.TestsConfig, image string, env map[string]string, tree string) (engine.TestRun, error) {
	b, _ := os.ReadFile(filepath.Join(tree, "src", "f.py"))
	f.sources = append(f.sources, string(b))
	return f.runnerMutant.RunTests(ctx, tc, image, env, tree)
}

// With builders, mutation works on the commit of the candidate that Occam chose (the orthogonal one), not on the working tree
// nor on the primary: Mutate gets its tree and T runs on it.
func TestStage5MutatesTheChosenCandidate(t *testing.T) {
	withRouterN2(t)
	a, dir, _, _ := repoMutation(t, "")
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	os.Remove(filepath.Join(dir, "src/f.py"))
	os.Remove(filepath.Join(dir, "tests/test_f.t"))
	git(t, dir, "checkout", "--", "src/f.py") // the working tree goes back to the base: only the builders' output is new
	runner := &runnerTreeMutant{}
	engine := &engineMutantN2{engineMutant: &engineMutant{}, runner: runner}
	a.Runner, a.Tests = engine, runner
	tests := "t_a detecta:MUT_A\nt_b detecta:MUT_B\ncubre src/f.py:1-2\n"
	launcherFake := &fakeLauncher{script: buildersN2(t, new([]launchRecord), func(role string, _ contract.BuilderRequest) map[string]string {
		marca := "PRIM"
		if role == contract.RoleBuilderOrthogonal {
			marca = "ORTO"
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x + 2  # " + marca + "\n", "tests/test_f.t": tests}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "n2.mut"})
	if err != nil {
		t.Fatal(err)
	}
	var selection contract.Selection
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/n2.mut", ArtSelection), &selection)
	if selection.Selected == nil || *selection.Selected != contract.RoleBuilderOrthogonal {
		t.Fatalf("Occam had to choose the orthogonal one: %+v", selection)
	}
	s5 := st.Stages[7]
	if s5.Status != contract.StageDone || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", s5, st.Outcome)
	}
	if !strings.Contains(engine.candidate, "ORTO") || strings.Contains(engine.candidate, "PRIM") {
		t.Fatalf("Mutate got the wrong tree:\n%s", engine.candidate)
	}
	// the first T run after Mutate is the clean run of Stage 5, on the chosen candidate's tree
	if engine.before >= len(runner.sources) || !strings.Contains(runner.sources[engine.before], "ORTO") || strings.Contains(runner.sources[engine.before], "PRIM") {
		t.Fatalf("T runs %q, Mutate arrived after %d", runner.sources, engine.before)
	}
	rep := readMutation(t, dir, "n2.mut")
	if rep.Verdict != contract.PASS || strings.Join(rep.Tests, ",") != "tests/test_f.t" || len(rep.Mutants) != 2 {
		t.Fatalf("%+v", rep)
	}
}
