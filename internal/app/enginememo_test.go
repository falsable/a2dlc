package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

func engineOps(tm *contract.Timings) map[string]int {
	ops := map[string]int{}
	if tm != nil {
		for _, op := range tm.EngineOps {
			ops[op.Name]++
		}
	}
	return ops
}

// The same candidate tree checked twice in one process (a Saboteur round re-checks it with new tests) asks the engines
// once: the second answer is the remembered one, and the receipt names it.
func TestMemoAnswersTheSameTreeOnce(t *testing.T) {
	a, m, dir := newRepo(t)
	a.Memo = NewEngineMemo()
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	first, err := a.Check(context.Background(), "HEAD")
	if err != nil || first.Verdict != contract.FAIL || m.calls != 1 {
		t.Fatalf("first check: v=%s err=%v calls=%d", first.Verdict, err, m.calls)
	}
	second, err := a.Check(context.Background(), "HEAD")
	if err != nil || m.calls != 1 {
		t.Fatalf("the second check of the same tree asked the engine again: err=%v calls=%d", err, m.calls)
	}
	if second.Verdict != first.Verdict || len(second.Targets) != len(first.Targets) ||
		second.Targets[0].Counterexample == nil || second.Targets[0].Counterexample.New != first.Targets[0].Counterexample.New {
		t.Fatalf("the remembered answer is not the same: %+v vs %+v", second.Targets, first.Targets)
	}
	if ops := engineOps(second.Timings); ops["diff_from_memo"] != 1 || ops["diff"] != 0 {
		t.Fatalf("the receipt does not say the answer was reused: %v", ops)
	}
}

// A changed candidate is a new question.
func TestMemoAsksAgainWhenTheTreeChanges(t *testing.T) {
	a, m, dir := newRepo(t)
	a.Memo = NewEngineMemo()
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "src/lib/f.ts", "export const f = 3\n")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil || m.calls != 2 {
		t.Fatalf("a changed tree reused an old answer: err=%v calls=%d", err, m.calls)
	}
}

// An INCONCLUSIVE may come from a timeout or a crowded machine: it is never remembered.
func TestMemoDoesNotKeepInconclusive(t *testing.T) {
	a, m, dir := newRepo(t)
	a.Memo = NewEngineMemo()
	write(t, dir, "src/lib/f.ts", "export const f = 2 // INCONCLUSIVE\n")
	for i := 0; i < 2; i++ {
		if _, err := a.Check(context.Background(), "HEAD"); err != nil {
			t.Fatal(err)
		}
	}
	if m.calls != 2 {
		t.Fatalf("an INCONCLUSIVE answer was reused: calls=%d", m.calls)
	}
}

// Without a memo (the zero App, every test before this one) nothing is remembered.
func TestNoMemoAsksEveryTime(t *testing.T) {
	a, m, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	for i := 0; i < 2; i++ {
		if _, err := a.Check(context.Background(), "HEAD"); err != nil {
			t.Fatal(err)
		}
	}
	if m.calls != 2 {
		t.Fatalf("calls=%d", m.calls)
	}
}

// A stored answer is a copy: marking the caller's targets does not change what the next check gets.
func TestMemoReturnsCopies(t *testing.T) {
	memo := NewEngineMemo()
	e := contract.EngineConfig{Language: "typescript", Image: "x"}
	req := contract.Request{ChangedFiles: []string{"a.ts"}, Budget: json.RawMessage("{}")}
	memo.put("b", "t", e, req, contract.Response{Targets: []contract.TargetResult{{Target: "a.ts::f", Verdict: contract.PASS}}})
	r, ok := memo.get("b", "t", e, req)
	if !ok {
		t.Fatal("no answer")
	}
	r.Targets[0].Verdict = contract.INCONCLUSIVE
	r2, _ := memo.get("b", "t", e, req)
	if r2.Targets[0].Verdict != contract.PASS {
		t.Fatal("the stored answer was changed through a returned copy")
	}
	var nilMemo *EngineMemo
	nilMemo.put("b", "t", e, req, r2)
	if _, ok := nilMemo.get("b", "t", e, req); ok {
		t.Fatal("a nil memo answered")
	}
}

// pausedEngine answers after a pause and records how many were running at once.
type pausedEngine struct {
	fakeEngine
	mu            sync.Mutex
	running, peak int
}

func (s *pausedEngine) Run(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error) {
	s.mu.Lock()
	s.running++
	if s.running > s.peak {
		s.peak = s.running
	}
	s.mu.Unlock()
	time.Sleep(150 * time.Millisecond)
	s.mu.Lock()
	s.running--
	s.mu.Unlock()
	return s.fakeEngine.Run(ctx, e, req, baseDir, candDir)
}

func twoEngineRepo(t *testing.T, eng *pausedEngine) (App, string) {
	t.Helper()
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[`+
		`{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}},`+
		`{"language":"python","image":"fake","paths":["py/**"],"budget":{}}],"critical_code":["src/lib/**","py/**"]}`)
	write(t, dir, "py/g.py", "g = 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "two engines")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, "py/g.py", "g = 2\n")
	a.Runner = eng
	return a, dir
}

// Two engines over a polyglot diff run side by side, and the receipt lists their targets in config order.
func TestEnginesRunSideBySide(t *testing.T) {
	eng := &pausedEngine{}
	a, _ := twoEngineRepo(t, eng)
	os.Unsetenv("A2DLC_ENGINE_JOBS")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || len(v.Targets) != 2 {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	if eng.peak != 2 {
		t.Fatalf("the engines ran one after another: peak=%d", eng.peak)
	}
	if v.Targets[0].Target != "src/lib/f.ts::f" || v.Targets[1].Target != filepath.ToSlash("py/g.py")+"::f" {
		t.Fatalf("targets not in config order: %s, %s", v.Targets[0].Target, v.Targets[1].Target)
	}
}

// A2DLC_ENGINE_JOBS=1 runs them one after another, for a machine that cannot hold both engines' memory caps at once.
func TestEngineJobsOneRunsThemInTurn(t *testing.T) {
	eng := &pausedEngine{}
	a, _ := twoEngineRepo(t, eng)
	t.Setenv("A2DLC_ENGINE_JOBS", "1")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	if eng.peak != 1 {
		t.Fatalf("peak=%d with A2DLC_ENGINE_JOBS=1", eng.peak)
	}
}

func TestEngineJobs(t *testing.T) {
	for _, c := range []struct {
		n    int
		env  string
		want int
	}{{3, "", 3}, {3, "1", 1}, {3, "2", 2}, {3, "9", 3}, {3, "0", 3}, {3, "x", 3}, {0, "", 1}} {
		if got := engineJobs(c.n, c.env); got != c.want {
			t.Errorf("engineJobs(%d, %q) = %d, want %d", c.n, c.env, got, c.want)
		}
	}
}
