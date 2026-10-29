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

// fakeDelivery answers ok with the HashedFile entries it is given; it writes to /out/tests only the write entries.
func fakeDelivery(t *testing.T, files []contract.HashedFile, write map[string]string) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		for rel, c := range write {
			p := filepath.Join(s.OutDir, "tests", filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(c), 0o644)
		}
		pl, _ := json.Marshal(contract.SaboteurResponse{TestFiles: files, Targets: []string{"src/f.py::f"}})
		return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
			Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.01}, Payload: pl}, 0, false
	}
}

func hashedFile(path, content string) contract.HashedFile {
	return contract.HashedFile{Path: path, SHA256: hashBytes([]byte(content))}
}

func TestStage4SaboteurRejectsInvalidDeliveries(t *testing.T) {
	const c = "t passed\ncubre src/f.py:1-2\n"
	cases := []struct {
		name, reason string
		script       func(t *testing.T) func(s LaunchSpec) (*contract.RoleResponse, int, bool)
	}{
		{"path outside the repo", "it is not a clean relative path", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("../src/f.py", c)}, map[string]string{"../src/f.py": c})
		}},
		{"absolute path", "it is not a clean relative path", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("/tests/t.t", c)}, nil)
		}},
		{"path with a backslash", "it is not a clean relative path", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile(`tests\t.t`, c)}, nil)
		}},
		{"NTFS stream over a repo test", "it is not a clean relative path", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("tests/test_old.t::$DATA", c)}, map[string]string{"tests/test_old.t::$DATA": c})
		}},
		{"name ending in a dot", "it is not a clean relative path", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("tests/test_old.t.", c)}, map[string]string{"tests/test_old.t.": c})
		}},
		{"same repo test with different case", "already exists in the repo", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("tests/TEST_OLD.t", c)}, map[string]string{"tests/TEST_OLD.t": c})
		}},
		{"hash that does not match", "does not match", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{{Path: "tests/test_h.t", SHA256: strings.Repeat("0", 64)}}, map[string]string{"tests/test_h.t": c})
		}},
		{"declared file not written", "it is not in /out/tests", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("tests/test_a.t", c)}, nil)
		}},
		{"the saboteur refuses", "the saboteur refused", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
				return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
					Status: contract.RoleRefused, Reason: "I don't know", Payload: json.RawMessage(`{}`)}, 0, false
			}
		}},
		{"payload out of contract", "out of contract", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
				return &contract.RoleResponse{SchemaVersion: 1, RunID: readRunID(t, s), Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
					Status: contract.RoleOK, Payload: json.RawMessage(`{"extra":1}`)}, 0, false
			}
		}},
		{"no tests", "delivered no tests", func(t *testing.T) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
			return fakeDelivery(t, []contract.HashedFile{}, nil)
		}},
	}
	for _, k := range cases {
		t.Run(k.name, func(t *testing.T) {
			a, dir, _ := repoWithTests(t)
			launcherFake := &fakeLauncher{script: k.script(t)}
			st, err := Run(context.Background(), a, launcherFake, Options{Slice: "inv"})
			if err != nil {
				t.Fatal(err)
			}
			s4 := st.Stages[6]
			if !strings.Contains(s4.Reason, k.reason) || st.Outcome == nil || *st.Outcome != contract.INCONCLUSIVE {
				t.Fatalf("%+v outcome=%v", s4, st.Outcome)
			}
			if previous, _ := relativePathsUnder(filepath.Join(dir, ".a2dlc/run/inv", dirOverlay)); len(previous) != 0 {
				t.Fatalf("the overlay was not supposed to receive anything: %v", previous)
			}
		})
	}
}

func TestStage4SaboteurDoesNotDeliverTheSameTestTwiceEvenIfCaseChanges(t *testing.T) {
	a, _, _ := repoWithTests(t)
	const c = "t passed\ncubre src/f.py:1-2\n"
	launcherFake := &fakeLauncher{script: fakeDelivery(t, []contract.HashedFile{hashedFile("tests/test_a.t", c), hashedFile("tests/TEST_A.t", c)},
		map[string]string{"tests/test_a.t": c, "tests/TEST_A.t": c})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "dup"})
	if err != nil || !strings.Contains(st.Stages[6].Reason, "which it already delivered") {
		t.Fatalf("%v %+v", err, st.Stages[6])
	}
}

func TestStage4SaboteurWithoutConfigTestsLaunchesNothing(t *testing.T) {
	// the repo has lines without cover and a saboteur role, but there is no command to run its tests with
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[]}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "no tests")
	m := &Machine{app: a, dir: t.TempDir(), maxRounds: 1}
	finished, err := m.roundSaboteur(context.Background(), "4", nil, nil, filepath.Join(m.dir, dirOverlay), 1, "", nil)
	if err != nil || !strings.Contains(finished, "without config.tests") {
		t.Fatalf("%v %q", err, finished)
	}
}

func TestStage3RedoingRemovesTheOverlayOfThePreviousCandidate(t *testing.T) {
	a, _, _ := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	old := stages["3"]
	var overlayGone bool
	withStage(t, "3", func(ctx context.Context, m *Machine) (string, string, string, error) {
		// what the Saboteur had brought for the previous candidate
		p := filepath.Join(m.dir, dirOverlay, "tests", "test_old.t")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("old passed\n"), 0o644)
		r1, r2, r3, err := old(ctx, m)
		_, e := os.Stat(filepath.Join(m.dir, dirOverlay))
		overlayGone = os.IsNotExist(e)
		return r1, r2, r3, err
	})
	if _, err := Run(context.Background(), a, launcherFake, Options{Slice: "ov"}); err != nil {
		t.Fatal(err)
	}
	if !overlayGone {
		t.Fatal("Stage 3 left the overlay of the previous candidate")
	}
}

func TestStage5RegeneratesSignaturesForItsCandidate(t *testing.T) {
	// signatures.json left by Stage 4 may belong to another candidate (N=2 with the primary chosen after rounds of the
	// orthogonal): Stage 5 does not reuse it
	a, _, _, _ := repoMutation(t, "")
	const sentinel = `{"sentinel":true}`
	old := stages["5"]
	withStage(t, "5", func(ctx context.Context, m *Machine) (string, string, string, error) {
		if err := os.WriteFile(filepath.Join(m.dir, ArtSignatures), []byte(sentinel), 0o644); err != nil {
			t.Fatal(err)
		}
		return old(ctx, m)
	})
	var signaturesSeen string
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		b, _ := os.ReadFile(filepath.Join(s.InDir, ArtSignatures))
		signaturesSeen = string(b)
		resp, e, to := fakeSaboteur("tests/test_sab.t", "t_sab detecta:MUT_B\n")(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "firmas", MaxRounds: 2})
	if err != nil || st.Stages[7].Iterations != 1 {
		t.Fatalf("%v %+v", err, st.Stages[7])
	}
	if signaturesSeen == "" || strings.Contains(signaturesSeen, "sentinel") {
		t.Fatalf("the Stage 5 saboteur received the old signatures: %q", signaturesSeen)
	}
}

func TestStage4SaboteurOnTheBuilderCommit(t *testing.T) {
	// with a builder its commit is judged: a test that only exists there counts as existing even if the base does not have it
	a, dir, _ := repoBuilder(t)
	a.Runner = &engineUncovered{} // leaves the changed lines without cover
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**","tests/**"],"budget":{}}],`+
		`"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "with tests")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"builder_primary":{"family":"claude","image":"img:1","argv":["sh","x"],"retries":0},`+
		`"saboteur":{"family":"script","image":"img:2","argv":["sh","y"],"retries":0}}}`)
	a.Tests = &fakeRunner{}
	var requests []contract.BuilderRequest
	builder := fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n", "tests/test_b.t": "b passed\n"}
	})
	const c = "t passed\ncubre src/f.py:1-2\n"
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		if s.Role == contract.RoleSaboteur {
			return fakeDelivery(t, []contract.HashedFile{hashedFile("tests/TEST_B.t", c)}, map[string]string{"tests/TEST_B.t": c})(s)
		}
		return builder(s)
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "bsab"})
	if err != nil {
		t.Fatal(err)
	}
	s4 := st.Stages[6]
	if !strings.Contains(s4.Reason, "already exists in the repo") || st.Outcome == nil || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%+v outcome=%v", s4, st.Outcome)
	}
}

// fuzzEngine answers the signatures of each changed file (f, lines 1 to 2) and, for operation fuzz, the findings it is
// given; it records the request. The embedded nil engine.Runner panics on any other operation.
type fuzzEngine struct {
	engine.Runner
	findings []contract.FuzzFinding
	err      error // what the fuzz operation returns instead of findings, if set
	request  contract.Request
}

func (f *fuzzEngine) Signatures(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.SignaturesResponse, error) {
	r := contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}
	for _, file := range req.ChangedFiles {
		r.Files = append(r.Files, contract.SignatureFile{File: file, Symbols: []contract.Signature{{Name: "f", Signature: "(x)", Lines: []int{1, 2}}}})
	}
	return r, nil
}

func (f *fuzzEngine) Fuzz(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.FuzzResponse, error) {
	f.request = req
	if f.err != nil {
		return contract.FuzzResponse{}, f.err
	}
	return contract.FuzzResponse{SchemaVersion: 1, Findings: f.findings, Runs: 100, Seconds: 10}, nil
}

// signaturesOnly is an engine runner without the fuzz operation: the round must say it did not run.
type signaturesOnly struct {
	engine.Runner
}

func (s *signaturesOnly) Signatures(ctx context.Context, e contract.EngineConfig, req contract.Request, b, c string) (contract.SignaturesResponse, error) {
	return (&fuzzEngine{}).Signatures(ctx, e, req, b, c)
}

// fuzzRepo is repoLite with an engine for python over src/; its working tree changes line 2 of src/f.py.
func fuzzRepo(t *testing.T) (app.App, string) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	sha, err := a.Repo.ResolveCommit("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return a, sha
}

func TestStage4FuzzingRoundEscalatesTheShortestFindingWithFixedBudget(t *testing.T) {
	a, base := fuzzRepo(t)
	fz := &fuzzEngine{findings: []contract.FuzzFinding{
		{Function: "f", Input: "x=1000", Error: "ZeroDivisionError", Seed: FuzzSeed},
		{Function: "f", Input: "x=0", Error: "ZeroDivisionError", Seed: FuzzSeed},
	}}
	a.Runner = fz
	m := &Machine{app: a, baseSha: base}
	esc, note, err := m.fuzzRound(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if note != "" || len(esc) != 1 || esc[0].Kind != "behavior_divergence" || esc[0].Target != "src/f.py::f" ||
		esc[0].Evidence.Counterexample == nil || esc[0].Evidence.Counterexample.Args != "x=0" {
		t.Fatalf("note=%q escalations=%+v", note, esc)
	}
	if fz.request.Operation != "fuzz" || string(fz.request.Budget) != `{"seconds":10,"seed":42}` || len(fz.request.ChangedFiles) != 1 {
		t.Fatalf("request %+v budget %s", fz.request, fz.request.Budget)
	}
}

func TestStage4FuzzingEngineFailureIsAToolingEscalationNotSilence(t *testing.T) {
	a, base := fuzzRepo(t)
	a.Runner = &fuzzEngine{err: errors.New("container down")}
	m := &Machine{app: a, baseSha: base}
	esc, note, err := m.fuzzRound(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(note, "fuzzing did not run for python: container down") || len(esc) != 1 || esc[0].Kind != "tooling" ||
		esc[0].Target != "src/f.py::*" || !strings.Contains(esc[0].Summary, "were not fuzzed") {
		t.Fatalf("a failed engine leaves its functions unfuzzed in the stage and in escalations: note=%q escalations=%+v", note, esc)
	}
}

func TestStage4FuzzingRoundSaysWhenTheEngineDoesNotFuzz(t *testing.T) {
	a, base := fuzzRepo(t)
	a.Runner = &signaturesOnly{}
	m := &Machine{app: a, baseSha: base}
	esc, note, err := m.fuzzRound(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(esc) != 0 || !strings.Contains(note, "does not do the fuzz operation") {
		t.Fatalf("the round must say it did not run: note=%q escalations=%+v", note, esc)
	}
}

func TestSpanChangedCountsOnlyLinesInTheSymbol(t *testing.T) {
	if !spanChanged([]int{3, 5}, []int{4}) || spanChanged([]int{3, 5}, []int{6, 9}) || !spanChanged(nil, []int{1}) {
		t.Fatal("a changed line inside the span changes it; a symbol without a span is fuzzed")
	}
}
