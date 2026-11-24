package run

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

func TestRunEgressPoolReusesProxyAndNetworkAcrossLaunches(t *testing.T) {
	bin, logPath := dockerLog(t, "")
	ctx, pool := WithRunEgressPool(context.Background())
	defer func() {
		if err := pool.Close(); err != nil {
			t.Fatal(err)
		}
		calls := dockerCalls(t, logPath)
		netCreates := 0
		proxyStarts := 0
		for _, c := range calls {
			if strings.HasPrefix(c, "network create --internal ") {
				netCreates++
			}
			if strings.HasPrefix(c, "run -d ") {
				proxyStarts++
			}
		}
		if netCreates != 1 || proxyStarts != 1 {
			t.Fatalf("expected 1 shared proxy/net across 3 launches, got netCreates=%d proxyStarts=%d calls=%q", netCreates, proxyStarts, calls)
		}
	}()

	r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: "provider", EgressAllow: []string{"openrouter.ai"}}
	for i := 1; i <= 3; i++ {
		if _, err := (DockerLauncher{Bin: bin}).Launch(ctx, r, LaunchSpec{
			Role: "saboteur", Stage: "4", Attempt: i, InDir: t.TempDir(), OutDir: t.TempDir(),
			Name: "a2dlc-run-shared-" + string(rune('0'+i)),
		}); err != nil {
			t.Fatalf("launch %d: %v", i, err)
		}
	}
}

func TestStage5MutantsExecuteConcurrently(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"tests:1","paths":["tests/**"],"command":"all"}}`)
	write(t, dir, "tests/t1.t", "t1 passed\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "with-tests")
	var mu sync.Mutex
	inFlight, peak := 0, 0
	release := make(chan struct{})
	var once sync.Once
	a.Tests = &concurrentTestsFake{
		onRun: func(isMutant bool) {
			if !isMutant {
				return
			}
			mu.Lock()
			inFlight++
			if inFlight > peak {
				peak = inFlight
			}
			if inFlight >= 2 {
				once.Do(func() { close(release) })
			}
			mu.Unlock()
			select {
			case <-release:
			case <-time.After(2 * time.Second):
			}
			mu.Lock()
			inFlight--
			mu.Unlock()
		},
	}

	m := &Machine{app: a, dir: t.TempDir()}
	cand, err := a.Repo.ResolveCommit("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	tree, err := a.Repo.CommitTree(cand)
	if err != nil {
		t.Fatal(err)
	}
	diff := app.DiffMutable{
		Tree:    tree,
		Tests:   []string{"tests/t1.t"},
		Sources: []string{"src/f.py"},
		Mutants: []app.Mutant{
			{Mutant: contract.Mutant{ID: "m1", File: "src/f.py", Line: 1, Operator: "op"}, Content: []byte("MUTANT 1")},
			{Mutant: contract.Mutant{ID: "m2", File: "src/f.py", Line: 1, Operator: "op"}, Content: []byte("MUTANT 2")},
			{Mutant: contract.Mutant{ID: "m3", File: "src/f.py", Line: 1, Operator: "op"}, Content: []byte("MUTANT 3")},
		},
	}
	rep, err := m.measureMutants(context.Background(), diff, "", 0.8, 10)
	if err != nil {
		t.Fatalf("measureMutants failed: %v", err)
	}
	if peak < 2 {
		t.Fatalf("expected concurrent mutant runs (peak >= 2), got peak=%d", peak)
	}
	if len(rep.Mutants) != 3 || rep.KillRate == nil || *rep.KillRate != 1.0 || rep.Verdict != contract.PASS {
		t.Fatalf("unexpected mutation report: %+v", rep)
	}
}

func TestN2BuildersExecuteConcurrently(t *testing.T) {
	withRouterN2(t)
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	var mu sync.Mutex
	inFlight, peak := 0, 0
	release := make(chan struct{})
	var once sync.Once
	launcher := &fakeLauncher{script: func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		mu.Lock()
		inFlight++
		if inFlight > peak {
			peak = inFlight
		}
		if inFlight >= 2 {
			once.Do(func() { close(release) })
		}
		mu.Unlock()
		select {
		case <-release:
		case <-time.After(2 * time.Second):
		}
		mu.Lock()
		inFlight--
		mu.Unlock()
		var req contract.RoleRequest
		raw, err := json.Marshal(contract.BuilderResponse{Summary: "done", FilesChanged: []string{"src/f.py"}})
		if err != nil {
			t.Fatal(err)
		}
		b, _ := readFileBytes(s.InDir + "/request.json")
		_ = json.Unmarshal(b, &req)
		write(t, s.WorkDir, "src/f.py", "def f(x):\n    return x + 1\n")
		return &contract.RoleResponse{
			SchemaVersion: 1, RunID: req.RunID, Role: s.Role, Stage: s.Stage, Attempt: s.Attempt,
			Status: contract.RoleOK, Cost: &contract.Cost{USD: 0.05}, Payload: raw,
		}, 0, false
	}}
	m, err := open(a, launcher, Options{Slice: "n2par", Mode: "greenfield"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.writeArtifact(ArtRoute, routeN2()); err != nil {
		t.Fatal(err)
	}
	st, reason, _, err := stageTier0(context.Background(), m)
	if err != nil || st != contract.StageDone {
		t.Fatalf("concurrent N=2 stageTier0 failed: st=%s reason=%q err=%v", st, reason, err)
	}
	if peak < 2 {
		t.Fatalf("expected concurrent N=2 builders (peak >= 2), got peak=%d", peak)
	}
}

func readFileBytes(path string) ([]byte, error) {
	return os.ReadFile(path)
}

type concurrentTestsFake struct {
	onRun func(isMutant bool)
}

func (f *concurrentTestsFake) RunTests(_ context.Context, _ contract.TestsConfig, _ string, _ map[string]string, tree string) (engine.TestRun, error) {
	b, _ := os.ReadFile(filepath.Join(tree, "src", "f.py"))
	isMutant := strings.HasPrefix(string(b), "MUTANT")
	if f.onRun != nil {
		f.onRun(isMutant)
	}
	status := ""
	exit := 0
	if isMutant {
		status = `<failure message="killed"/>`
		exit = 1
	}
	return engine.TestRun{
		Exit:  exit,
		JUnit: []byte(`<testsuites><testsuite><testcase classname="c" name="t1">` + status + `</testcase></testsuite></testsuites>`),
	}, nil
}
