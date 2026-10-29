package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
)

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// repoLite: a repo with a config without engines or tests, one base commit and one change in the work tree.
func repoLite(t *testing.T) (app.App, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[]}`)
	write(t, dir, "src/f.py", "def f(x):\n    return x\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	write(t, dir, "src/f.py", "def f(x):\n    return x + 1\n")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tick := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	a := app.App{Repo: repo, Version: "test", Now: func() time.Time { tick = tick.Add(time.Second); return tick }}
	return a, dir
}

// fakeLauncher simulates a role's command: it writes in /out what script says for that attempt and records what it saw.
type fakeLauncher struct {
	script func(s LaunchSpec) (resp *contract.RoleResponse, exit int, timeout bool)
	seen   [][]string
}

func (l *fakeLauncher) Launch(_ context.Context, r contract.Role, s LaunchSpec) (LaunchResult, error) {
	var vio []string
	filepath.WalkDir(s.InDir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			vio = append(vio, filepath.Base(p))
		}
		return nil
	})
	l.seen = append(l.seen, vio)
	resp, exit, to := l.script(s)
	if resp != nil {
		b, _ := json.Marshal(resp)
		os.WriteFile(filepath.Join(s.OutDir, "response.json"), b, 0o644)
	}
	return LaunchResult{Exit: exit, TimedOut: to, Argv: []string{"docker", "run", r.Image}, EnvKeys: []string{"X_KEY"}, Network: networkOf(r)}, nil
}

func TestRunLiteRunsOracleAndResumes(t *testing.T) {
	a, dir := repoLite(t)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "demo.v1"})
	if err != nil {
		t.Fatal(err)
	}
	byStage := map[string]contract.StageRun{}
	for _, s := range st.Stages {
		byStage[s.ID] = s
	}
	if byStage["4"].Status != contract.StageDone || byStage["4"].Artifact == nil || byStage["4"].Artifact.Path != ArtVerdict {
		t.Fatalf("%+v", byStage["4"])
	}
	if byStage["2"].Status != contract.StageDone || byStage["3"].Status != contract.StageSkipped || !strings.Contains(byStage["3"].Reason, "builder_primary") {
		t.Fatalf("%+v %+v", byStage["2"], byStage["3"])
	}
	// without engines or tests the scope is empty: everything stays out_of_scope and the oracle gives PASS (the repo
	// declared that nothing is the gate's concern). The run's outcome is the Stage 4 verdict as it is.
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("outcome %v", st.Outcome)
	}
	for _, f := range []string{"state.json", ArtVerdict, ArtRoute} {
		if _, err := os.Stat(filepath.Join(dir, ".a2dlc/run/demo.v1", f)); err != nil {
			t.Fatal(err)
		}
	}
	// resuming: stages done with an intact artifact are not run again (same artifact, same hash)
	before := byStage["4"].Artifact.SHA256
	st2, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "demo.v1"})
	if err != nil || st2.RunID != st.RunID || st2.Stages[6].Artifact.SHA256 != before || st2.Stages[6].StartedAt == nil || *st2.Stages[6].StartedAt != *byStage["4"].StartedAt {
		t.Fatalf("%v %+v", err, st2.Stages[6])
	}
	// another base over the same folder is rejected: a run is for one base
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "other")
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "demo.v1"}); err == nil || !strings.Contains(err.Error(), "another base") {
		t.Fatalf("%v", err)
	}
	if problems, err := VerifyRun(a, "demo.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
	if _, err := Run(context.Background(), a, nil, Options{Slice: "Bad Slice"}); err == nil {
		t.Fatal("invalid slice accepted")
	}
}

func TestInvokeMountsOnlyVisibleAndRecordsEachAttempt(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"saboteur":{"family":"gemini","image":"img:1","argv":["sh","x"],"retries":1}}}`)
	m, err := open(a, &fakeLauncher{}, Options{Slice: "s.v1", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	// the run's artifacts: the saboteur can only see contract, signatures, escalations and intent; tier0 never
	for _, artifact := range []string{ArtContract, ArtSignatures, ArtIntent, ArtTier0} {
		m.writeArtifact(artifact, map[string]string{"a": artifact})
	}
	launcherFake := &fakeLauncher{}
	intent := 0
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		intent++
		if intent == 1 {
			return nil, 0, true // timeout: it is retried
		}
		return &contract.RoleResponse{SchemaVersion: 1, RunID: m.state.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
			Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.05}, Payload: json.RawMessage(`{"ok":true}`)}, 0, false
	}
	m.launcher = launcherFake
	invocation, err := m.Invoke(context.Background(), contract.RoleSaboteur, "4", map[string]any{"uncovered": []int{3}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if invocation.Response == nil || invocation.Response.Status != contract.RoleOK || len(invocation.Entries) != 2 {
		t.Fatalf("%+v", invocation)
	}
	for _, vio := range launcherFake.seen {
		if strings.Join(vio, ",") != "contract.json,intent.json,request.json,signatures.json" {
			t.Fatalf("the saboteur saw %v", vio)
		}
	}
	if invocation.Entries[0].Status != contract.LedgerTimeout || invocation.Entries[1].Status != contract.LedgerOK || invocation.Entries[1].PrevHash == nil ||
		*invocation.Entries[1].PrevHash != invocation.Entries[0].Hash || invocation.Entries[1].Cost.USD != 0.05 || len(invocation.Entries[1].Inputs) != 4 ||
		invocation.Entries[1].Inputs[0].Path != "/in/request.json" || invocation.Entries[1].EnvKeys[0] != "X_KEY" {
		t.Fatalf("%+v\n%+v", invocation.Entries[0], invocation.Entries[1])
	}
	if m.state.Budget.SpentUSD != 0.05 || m.state.Budget.UnreportedRoles != 1 {
		t.Fatalf("%+v", m.state.Budget)
	}
	// the ledger on disk verifies, and an altered entry does not
	var lg contract.RunLedger
	b, _ := os.ReadFile(filepath.Join(m.dir, "ledger.json"))
	if err := contract.DecodeStrictBytes(b, &lg); err != nil || Verify(lg) != nil {
		t.Fatalf("%v %v", err, Verify(lg))
	}
	lg.Entries[0].EnvKeys = []string{}
	if Verify(lg) == nil {
		t.Fatal("an altered entry passed verification")
	}
	// a role with a workdir that the policy does not give it, and an unconfigured role, are rejected before launch
	if _, err := m.Invoke(context.Background(), contract.RoleSaboteur, "4", nil, dir); err == nil {
		t.Fatal("saboteur with workdir accepted")
	}
	if _, err := m.Invoke(context.Background(), contract.RoleBuilderPrimary, "3", nil, dir); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("%v", err)
	}
}

// fakeRunner runs the files in $A2DLC_TESTS of the tree: each line "name passed|failed" is a testcase, and a line
// "cubre <file>:<a>-<b>" leaves LCOV coverage with those lines executed (1-99 as statements).
type fakeRunner struct{ runs int }

func (f *fakeRunner) RunTests(_ context.Context, _ contract.TestsConfig, _ string, env map[string]string, tree string) (engine.TestRun, error) {
	f.runs++
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
			fmt.Fprintf(&x, `<testcase classname="c" name="%s" file="%s">`, c[0], a)
			if c[1] != "passed" {
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

// repoWithTests: config.tests without engines, with src/ under the coverage precondition and a change without a test.
func repoWithTests(t *testing.T) (app.App, string, *fakeRunner) {
	t.Helper()
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	write(t, dir, "tests/test_old.t", "old passed\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "with tests")
	write(t, dir, "src/f.py", "def f(x):\n    return x + 2\n")
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"saboteur":{"family":"script","image":"img:1","argv":["sh","x"],"retries":0}}}`)
	r := &fakeRunner{}
	a.Tests = r
	return a, dir, r
}

// fakeSaboteur leaves in /out/tests the file it is told to and responds ok, declaring it with its hash.
func fakeSaboteur(path, content string) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		p := filepath.Join(s.OutDir, "tests", filepath.FromSlash(path))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), 0o644)
		pl, _ := json.Marshal(contract.SaboteurResponse{TestFiles: []contract.HashedFile{{Path: path, SHA256: hashBytes([]byte(content))}}, Targets: []string{"src/f.py::f"}})
		return &contract.RoleResponse{SchemaVersion: 1, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt, Status: contract.RoleOK,
			Cost: &contract.Cost{USD: 0.01}, Payload: pl}, 0, false
	}
}

func TestStage4ClosesTheLoopWithTheSaboteur(t *testing.T) {
	a, dir, runner := repoWithTests(t)
	launcherFake := &fakeLauncher{}
	var m *Machine
	launcherFake.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		// the saboteur only sees what the policy lets it: request, escalations, signatures (without contract or intent yet)
		var req contract.RoleRequest
		b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
		if err := contract.DecodeStrictBytes(b, &req); err != nil {
			t.Fatal(err)
		}
		var pl contract.SaboteurRequest
		if err := contract.DecodeStrictBytes(req.Payload, &pl); err != nil || pl.Round != 1 || pl.MaxRounds != 2 || len(pl.Uncovered) != 1 ||
			pl.Uncovered[0].File != "src/f.py" || strings.Join(pl.TestsPaths, ",") != "tests/**" || pl.Command != "run" || len(pl.PreviousTests) != 0 {
			t.Fatalf("%v %+v", err, pl)
		}
		resp, exit, to := fakeSaboteur("tests/test_sab.t", "t_sab passed\ncubre src/f.py:1-2\n")(s)
		resp.RunID = req.RunID
		return resp, exit, to
	}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "sab.v1", MaxRounds: 2})
	if err != nil {
		t.Fatal(err)
	}
	_ = m
	s4 := st.Stages[6]
	if s4.Status != contract.StageDone || s4.Iterations != 1 || s4.Reason != "" || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%+v outcome=%v", s4, st.Outcome)
	}
	if len(launcherFake.seen) != 1 || strings.Join(launcherFake.seen[0], ",") != "escalations.json,request.json,signatures.json" {
		t.Fatalf("the saboteur saw %v", launcherFake.seen)
	}
	// round 1 without tests (runs nothing); round 2: NOP/Oracle over the overlay (candidate and base with stubs) and
	// tests_diff over base and candidate with the overlay
	if runner.runs != 4 {
		t.Fatalf("test runs: %d", runner.runs)
	}
	run := filepath.Join(dir, ".a2dlc/run/sab.v1")
	if _, err := os.Stat(filepath.Join(run, dirOverlay, "tests", "test_sab.t")); err != nil {
		t.Fatal(err)
	}
	var v contract.Verdict
	b, _ := os.ReadFile(filepath.Join(run, ArtVerdict))
	// only line 2 changed (the return): it is the one that was without cover and the one the saboteur's test executed
	if err := contract.DecodeStrictBytes(b, &v); err != nil || v.Verdict != contract.PASS || len(v.Coverage.Files[0].Uncovered) != 0 || len(v.Coverage.Files[0].Covered) != 1 {
		t.Fatalf("%v %+v", err, v.Coverage)
	}
	var lg contract.RunLedger
	b, _ = os.ReadFile(filepath.Join(run, "ledger.json"))
	if err := contract.DecodeStrictBytes(b, &lg); err != nil || len(lg.Entries) != 1 || len(lg.Entries[0].Outputs) != 2 || st.Budget.SpentUSD != 0.01 {
		t.Fatalf("%v %+v %+v", err, lg.Entries, st.Budget)
	}
	if problems, err := VerifyRun(a, "sab.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

func TestStage4RejectsOutOfPlaceTestsAndExhaustsRounds(t *testing.T) {
	// a test outside tests.paths does not enter the overlay: the verdict stays INCONCLUSIVE and the reason says so
	a, _, _ := repoWithTests(t)
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		resp, e, to := fakeSaboteur("src/g.py", "x passed\n")(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "outside"})
	if err != nil || st.Stages[6].Iterations != 1 || !strings.Contains(st.Stages[6].Reason, "outside tests.paths") || *st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v %+v", err, st.Stages[6])
	}
	// a test that overwrites one of the repo's does not either
	a, _, _ = repoWithTests(t)
	launcherFake = &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		resp, e, to := fakeSaboteur("tests/test_old.t", "old passed\n")(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "overwrites"}); err != nil || !strings.Contains(st.Stages[6].Reason, "already exists in the repo") {
		t.Fatalf("%v %+v", err, st.Stages[6])
	}
	// tests that do not run the lines: the rounds run out and the state says how many
	a, _, runner := repoWithTests(t)
	n := 0
	launcherFake = &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		n++
		resp, e, to := fakeSaboteur(fmt.Sprintf("tests/test_%d.t", n), "t passed\ncubre src/f.py:50-60\n")(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "exhaust", MaxRounds: 2})
	// two re-checks with overlay, each NOP (2 runs) + tests_diff (2)
	if err != nil || st.Stages[6].Iterations != 2 || !strings.Contains(st.Stages[6].Reason, "after 2 saboteur rounds") || *st.Outcome != contract.INCONCLUSIVE || runner.runs != 8 {
		t.Fatalf("%v %+v runs=%d", err, st.Stages[6], runner.runs)
	}
	// and the second round's payload names the first round's test
	var req contract.RoleRequest
	b, _ := os.ReadFile(filepath.Join(a.Repo.Dir, ".a2dlc/run/exhaust/launches/002-4-saboteur-1/in/request.json"))
	var pl contract.SaboteurRequest
	if err := contract.DecodeStrictBytes(b, &req); err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(req.Payload, &pl); err != nil || pl.Round != 2 || strings.Join(pl.PreviousTests, ",") != "tests/test_1.t" {
		t.Fatalf("%v %+v", err, pl)
	}
	// without a saboteur configured, the stage says so and launches nothing
	a, dir, _ := repoWithTests(t)
	os.Remove(filepath.Join(dir, ".a2dlc/roles.json"))
	launcherFake = &fakeLauncher{}
	if st, err = Run(context.Background(), a, launcherFake, Options{Slice: "without"}); err != nil || !strings.Contains(st.Stages[6].Reason, "without a saboteur role") || len(launcherFake.seen) != 0 {
		t.Fatalf("%v %+v", err, st.Stages[6])
	}
}

func readRunID(t *testing.T, s LaunchSpec) string {
	t.Helper()
	var req contract.RoleRequest
	b, _ := os.ReadFile(filepath.Join(s.InDir, "request.json"))
	if err := contract.DecodeStrictBytes(b, &req); err != nil {
		t.Fatal(err)
	}
	return req.RunID
}

func TestUnknownRolesAreRejected(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"oracle":{"family":"x","image":"i","argv":["a"]}}}`)
	if _, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "s"}); err == nil || !strings.Contains(err.Error(), "unknown role") {
		t.Fatalf("%v", err)
	}
}

// Round 1 leaves tests/test_1.t without covering the lines; round 2 re-delivers it with other content: it is rejected and
// the overlay keeps round 1's, the one already judged (with N=2, the other candidate's).
func TestStage4DoesNotRewriteATestTheSaboteurAlreadyDelivered(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	n := 0
	launcherFake := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		n++
		resp, e, to := fakeSaboteur("tests/test_1.t", fmt.Sprintf("t%d passed\ncubre src/f.py:50-60\n", n))(s)
		resp.RunID = readRunID(t, s)
		return resp, e, to
	}}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "rewrites", MaxRounds: 3})
	if err != nil || n != 2 || st.Stages[6].Iterations != 2 || !strings.Contains(st.Stages[6].Reason, "wanted to rewrite tests/test_1.t, which it already delivered") ||
		*st.Outcome != contract.INCONCLUSIVE {
		t.Fatalf("%v n=%d %+v", err, n, st.Stages[6])
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/rewrites", dirOverlay, "tests", "test_1.t"))
	if string(b) != "t1 passed\ncubre src/f.py:50-60\n" {
		t.Fatalf("the overlay was left with %q", b)
	}
}

// response.json is written by an untrusted role: a negative cost cannot lower the spend (CB-03), a status outside the
// enum does not enter the ledger, and refused/error require a reason. Each response counts as invalid_response and adds nothing.
func TestInvokeRejectsResponsesTheSchemaDoesNotAccept(t *testing.T) {
	cases := map[string]func(r *contract.RoleResponse){
		"negative cost":   func(r *contract.RoleResponse) { r.Cost = &contract.Cost{USD: -100} },
		"negative tokens": func(r *contract.RoleResponse) { r.Cost = &contract.Cost{USD: 1, InputTokens: -1} },
		"negative cache read": func(r *contract.RoleResponse) {
			r.Cost = &contract.Cost{USD: 1, InputTokens: 10, CacheReadInputTokens: -1}
		},
		"negative cache write": func(r *contract.RoleResponse) {
			r.Cost = &contract.Cost{USD: 1, InputTokens: 10, CacheCreationInputTokens: -1}
		},
		"cache above input": func(r *contract.RoleResponse) {
			r.Cost = &contract.Cost{USD: 1, InputTokens: 10, CacheReadInputTokens: 6, CacheCreationInputTokens: 5}
		},
		"cache without input":    func(r *contract.RoleResponse) { r.Cost = &contract.Cost{USD: 1, CacheReadInputTokens: 5} },
		"unknown status":         func(r *contract.RoleResponse) { r.Status = "banana" },
		"empty status":           func(r *contract.RoleResponse) { r.Status = "" },
		"refused without reason": func(r *contract.RoleResponse) { r.Status = contract.RoleRefused },
		"error without reason":   func(r *contract.RoleResponse) { r.Status = contract.RoleError },
	}
	for name, rompe := range cases {
		t.Run(name, func(t *testing.T) {
			a, dir := repoLite(t)
			write(t, dir, ".a2dlc/roles.json", `{"schema_version":1,"roles":{"saboteur":{"family":"gemini","image":"img:1","argv":["sh","x"],"retries":0}}}`)
			m, err := open(a, &fakeLauncher{}, Options{Slice: "s.v1", Mode: contract.ModeRefactor})
			if err != nil {
				t.Fatal(err)
			}
			m.state.Budget.SpentUSD = 5
			m.launcher = &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
				r := &contract.RoleResponse{SchemaVersion: 1, RunID: m.state.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
					Status: contract.RoleOK, Payload: json.RawMessage(`{}`)}
				rompe(r)
				return r, 0, false
			}}
			invocation, err := m.Invoke(context.Background(), contract.RoleSaboteur, "4", nil, "")
			if err != nil {
				t.Fatal(err)
			}
			if invocation.Response != nil || len(invocation.Entries) != 1 || invocation.Entries[0].Status != contract.LedgerInvalidResponse || invocation.Entries[0].Cost != nil {
				t.Fatalf("%+v", invocation)
			}
			if m.state.Budget.SpentUSD != 5 || m.state.Budget.UnreportedRoles != 1 {
				t.Fatalf("the spend changed: %+v", m.state.Budget)
			}
		})
	}
}

// A run without a builder leaves state, route and verdict in .a2dlc/run/: they are not code and do not enter the
// tree_hash. The person commits only the code, and verify over HEAD cannot go stale because of the run's folder.
func TestRunDoesNotEnterVerdictTreeHash(t *testing.T) {
	a, dir := repoLite(t)
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "demo.v1"})
	if err != nil || st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("%v %v", st.Outcome, err)
	}
	git(t, dir, "add", "src/f.py")
	git(t, dir, "commit", "-qm", "code")
	if problems, err := a.Verify("HEAD"); err != nil || len(problems) != 0 {
		t.Fatalf("verify after run: err=%v probs=%q", err, problems)
	}
}
