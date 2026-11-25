package plan

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestG3ExactGlobIntersectionAllowsDisjointPatternsInSameDir(t *testing.T) {
	if globsIntersect("src/*.go", "src/*.py") {
		t.Fatal("src/*.go and src/*.py are disjoint")
	}
	if globsIntersect("src/a_*.go", "src/b_*.go") {
		t.Fatal("src/a_*.go and src/b_*.go are disjoint")
	}
	if !globsIntersect("src/*.go", "src/foo*.go") {
		t.Fatal("src/*.go and src/foo*.go can intersect")
	}
	if !globsIntersect("src/**/*.go", "src/pkg/a.go") {
		t.Fatal("src/**/*.go and src/pkg/a.go must intersect")
	}
}

func TestG8ImportGraphGateAndExtractGoContext(t *testing.T) {
	dir := t.TempDir()
	writeF := func(rel, body string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	writeF("go.mod", "module example.com/app\n\ngo 1.23\n")
	writeF("util/u.go", "package util\n\nfunc Helper() int { return 1 }\n")
	writeF("api/h.go", "package api\n\nimport \"example.com/app/util\"\n\nfunc Handle() int { return util.Helper() }\n")

	sigs, edges := ExtractGoContext(dir, []string{"util/u.go", "api/h.go"})
	if len(sigs) != 2 || len(edges) != 1 || edges[0].From != "api/h.go" || edges[0].To != "util/u.go" {
		t.Fatalf("sigs=%+v edges=%+v", sigs, edges)
	}

	feat := contract.Intent{SchemaVersion: 1, SliceID: "feat", Mode: contract.ModeLite, Goal: "add api and util", Acceptance: []string{"works"}}
	p := contract.Plan{
		SchemaVersion: 1,
		FeatureID:     "feat",
		Slices: []contract.PlanSlice{
			{SliceID: "feat.util", Goal: "util", Acceptance: []string{"u"}, Owns: []string{"util/u.go"}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 1}, Covers: []int{0}},
			{SliceID: "feat.api", Goal: "api", Acceptance: []string{"a"}, Owns: []string{"api/h.go"}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 1}, Covers: []int{0}},
		},
	}
	// Missing depends_on: feat.api -> feat.util should fail G8.
	rep := CheckWithDeps(p, feat, []string{"util/u.go", "api/h.go"}, edges)
	if rep.Valid {
		t.Fatalf("expected G8 failure when feat.api imports feat.util without depends_on")
	}
	var g8Found bool
	for _, r := range rep.Results {
		if r.Rule == "G8" && r.Status == Fail && len(r.Findings) == 1 {
			g8Found = true
		}
	}
	if !g8Found {
		t.Fatalf("expected G8 fail result, got %+v", rep.Results)
	}

	// Adding DependsOn: ["feat.util"] makes G8 pass.
	p.Slices[1].DependsOn = []string{"feat.util"}
	rep2 := CheckWithDeps(p, feat, []string{"util/u.go", "api/h.go"}, edges)
	if !rep2.Valid {
		t.Fatalf("expected valid plan with depends_on, got %+v", rep2.Results)
	}
}

type mapExec struct {
	runs map[string]SliceRun
}

func (m *mapExec) Run(_ context.Context, sl contract.PlanSlice, _, _ string) (SliceRun, error) {
	return m.runs[sl.SliceID], nil
}

func (m *mapExec) Verifies(contract.PlanSlice, contract.PlanSliceState) bool { return false }

func TestPlanRunnerAggregateMaxCostUSDAndPostMergeRepair(t *testing.T) {
	// Test aggregate budget cap & post-merge repair using Runner.finish and Runner.Run.
	repo, base := newRepo(t)
	feat := contract.Intent{SchemaVersion: 1, SliceID: "feat-cap", Mode: contract.ModeLite, Goal: "two slices", Acceptance: []string{"ok"}}
	p := contract.Plan{
		SchemaVersion: 1,
		FeatureID:     "feat-cap",
		Slices: []contract.PlanSlice{
			{SliceID: "feat-cap.s1", Goal: "one", Acceptance: []string{"a"}, Owns: []string{"a.txt"}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 1}, Covers: []int{0}},
			{SliceID: "feat-cap.s2", Goal: "two", Acceptance: []string{"b"}, Owns: []string{"b.txt"}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 1}, Covers: []int{0}, DependsOn: []string{"feat-cap.s1"}},
		},
	}

	c1 := commitFile(t, repo.Dir, base, "a.txt", "a\n")
	execFake := &mapExec{
		runs: map[string]SliceRun{
			"feat-cap.s1": {Status: contract.PlanPassed, Candidate: c1, CostUSD: 0.60},
			"feat-cap.s2": {Status: contract.PlanPassed, Candidate: c1, CostUSD: 0.60},
		},
	}
	runner := Runner{
		Repo:  repo,
		Exec:  execFake,
		Final: func(context.Context, string, string) (bool, string, error) { return true, "", nil },
		Accept: func(contract.Plan, string, string, string) (contract.PlanAcceptance, error) {
			return contract.PlanAcceptance{Kind: "test", At: "2026-11-25T15:00:00Z"}, nil
		},
		MaxCostUSD: 0.50,
	}
	pBytes, _ := json.Marshal(p)
	st, _, err := runner.Run(context.Background(), pBytes, feat, base)
	if err != nil {
		t.Fatal(err)
	}
	if st.Slices[1].Status != contract.PlanEscalated || !strings.Contains(st.Slices[1].Reason, "max_cost_usd exceeded") {
		t.Fatalf("expected s2 escalated by max_cost_usd, got %+v", st.Slices[1])
	}

	// Test post-merge repair loop: first Final fails, RepairFunc produces fixed commit, second Final passes.
	c2 := commitFile(t, repo.Dir, base, "b.txt", "b\n")
	cFixed := commitFile(t, repo.Dir, c1, "b.txt", "fixed\n")
	finalCalls := 0
	feat2 := contract.Intent{SchemaVersion: 1, SliceID: "feat-repair", Mode: contract.ModeLite, Goal: "two slices", Acceptance: []string{"ok"}}
	p2 := contract.Plan{
		SchemaVersion: 1,
		FeatureID:     "feat-repair",
		Slices: []contract.PlanSlice{
			{SliceID: "feat-repair.s1", Goal: "one", Acceptance: []string{"a"}, Owns: []string{"a.txt"}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 1}, Covers: []int{0}},
			{SliceID: "feat-repair.s2", Goal: "two", Acceptance: []string{"b"}, Owns: []string{"b.txt"}, Criticality: "low", Budget: contract.PlanBudget{MaxCostUSD: 1}, Covers: []int{0}, DependsOn: []string{"feat-repair.s1"}},
		},
	}
	runner2 := Runner{
		Repo: repo,
		Exec: &mapExec{
			runs: map[string]SliceRun{
				"feat-repair.s1": {Status: contract.PlanPassed, Candidate: c1},
				"feat-repair.s2": {Status: contract.PlanPassed, Candidate: c2},
			},
		},
		Final: func(_ context.Context, _, merged string) (bool, string, error) {
			finalCalls++
			if merged == cFixed {
				return true, "", nil
			}
			return false, "integration test failed", nil
		},
		RepairFunc: func(_ context.Context, _, _, _ string) (string, bool, error) {
			return cFixed, true, nil
		},
		Accept: func(contract.Plan, string, string, string) (contract.PlanAcceptance, error) {
			return contract.PlanAcceptance{Kind: "test", At: "2026-11-25T15:00:00Z"}, nil
		},
	}
	pBytes2, _ := json.Marshal(p2)
	st2, _, err := runner2.Run(context.Background(), pBytes2, feat2, base)
	if err != nil {
		t.Fatal(err)
	}
	if st2.Outcome == nil || *st2.Outcome != contract.PASS || finalCalls != 2 {
		t.Fatalf("expected PASS after repair (finalCalls=%d), got outcome=%v reason=%q", finalCalls, st2.Outcome, st2.Reason)
	}
}
