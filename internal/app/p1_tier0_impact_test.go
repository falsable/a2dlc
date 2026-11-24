package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestTier0SelectsAffectedTestsFromImpactMapAndFallsBackSafely(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{
		"schema_version":1,
		"engines":[],
		"tests":{"image":"tests:1","paths":["tests/**"],"command":"nop"}
	}`)
	write(t, dir, ".tests-impact.json", `{"src/a.py":["tests/a.t"],"src/b.py":["tests/b.t"]}`)
	write(t, dir, "src/a.py", "IMPL a\n")
	write(t, dir, "src/b.py", "IMPL b\n")
	write(t, dir, "src/unmapped.py", "IMPL c\n")
	write(t, dir, "tests/a.t", "ta uses:a\n")
	write(t, dir, "tests/b.t", "tb uses:b\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")

	candMapped := candidateOf(t, a, map[string]string{"src/a.py": "IMPL a\n# changed\n"})
	ft := &fakeTests{}
	a.Tests = ft
	r, _, err := a.Tier0With(context.Background(), "HEAD", candMapped, nil, Tier0Extra{Tests: true})
	if err != nil || !r.Reviewed {
		t.Fatalf("Tier0With mapped failed: r=%+v err=%v", r, err)
	}
	if len(ft.selected) != 1 || ft.selected[0] != "tests/a.t" {
		t.Fatalf("expected A2DLC_TESTS=tests/a.t, got selected=%+v runs=%+v", ft.selected, ft.runs)
	}

	// Changing an unmapped source falls back cleanly to empty A2DLC_TESTS (full suite selection signal).
	candUnmapped := candidateOf(t, a, map[string]string{"src/unmapped.py": "IMPL c\n# changed\n"})
	cfg, _ := a.Config()
	baseSha, _ := a.Repo.ResolveCommit("HEAD")
	candTree, _ := a.Repo.CommitTree(candUnmapped)
	if sel := a.selectTier0Tests(context.Background(), cfg, baseSha, candTree, []string{"src/unmapped.py"}, func() error { return nil }, nil, nil); sel != nil {
		t.Fatalf("expected nil fallback for unmapped source, got %v", sel)
	}
}

func TestTier0SelectsAffectedTestsFromDepsGraph(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{
		"schema_version":1,
		"engines":[{"language":"python","image":"eng-py:1","paths":["src/**"],"budget":{}}],
		"tests":{"image":"tests:1","paths":["tests/**"],"command":"nop"}
	}`)
	write(t, dir, "src/a.py", "IMPL a\n")
	write(t, dir, "src/b.py", "IMPL b\n")
	write(t, dir, "tests/test_a.t", "ta uses:a\n")
	write(t, dir, "tests/test_b.t", "tb uses:b\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "init")

	dr := &depsFake{fakeEngine: &fakeEngine{}, edges: []contract.DepsEdge{
		{From: "tests/test_a.t", To: "src/a.py"},
		{From: "tests/test_b.t", To: "src/b.py"},
	}}
	a.Runner = dr
	ft := &fakeTests{}
	a.Tests = ft

	cand := candidateOf(t, a, map[string]string{"src/a.py": "IMPL a\n# changed\n"})
	r, _, err := a.Tier0With(context.Background(), "HEAD", cand, nil, Tier0Extra{Tests: true})
	if err != nil || !r.Reviewed {
		t.Fatalf("Tier0With deps_graph failed: r=%+v err=%v", r, err)
	}
	if len(ft.selected) != 1 || strings.TrimSpace(ft.selected[0]) != "tests/test_a.t" {
		t.Fatalf("expected A2DLC_TESTS=tests/test_a.t from deps_graph, got selected=%+v", ft.selected)
	}
}

type depsFake struct {
	*fakeEngine
	edges []contract.DepsEdge
}

func (d *depsFake) Deps(context.Context, contract.EngineConfig, contract.Request, string, string) (contract.DepsResponse, error) {
	return contract.DepsResponse{SchemaVersion: contract.SchemaVersion, Edges: d.edges}, nil
}
