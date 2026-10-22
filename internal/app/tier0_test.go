package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// seenPolicy is the policy of the last tier0 request the fake engine received (the budget's policy, nil if absent).
var seenPolicy *contract.PolicyTier0

// Tier0 of the fake engine: a line with BROKEN is a compile finding on that line; a file with NO_TIER0 makes the engine
// unable to do tier0; a probe with expr "fake" fails.
func (m *fakeEngine) Tier0(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candDir string) (contract.Tier0Response, error) {
	r := contract.Tier0Response{SchemaVersion: 1, Findings: []contract.Tier0Finding{}}
	for _, f := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candDir, f))
		if strings.Contains(string(b), "NO_TIER0") {
			return r, fmt.Errorf("%w: old engine", engine.ErrWithoutTier0)
		}
		for i, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, "BROKEN") {
				n := i + 1
				r.Findings = append(r.Findings, contract.Tier0Finding{Kind: contract.Tier0Compile, File: f, Line: &n, Message: "invalid syntax"})
			}
		}
	}
	var b contract.Tier0Budget
	if err := contract.DecodeStrictBytes(req.Budget, &b); err != nil {
		return r, err
	}
	seenPolicy = b.Policy
	for _, p := range b.Probes {
		if *p.Expr == "fake" {
			r.Findings = append(r.Findings, contract.Tier0Finding{Kind: contract.Tier0Probe, File: *p.File, Message: p.ID + " gave false"})
		}
	}
	r.OK = len(r.Findings) == 0
	return r, engine.ValidateTier0(req, r)
}

// candidateOf leaves the given content as a commit on top of HEAD without touching the working tree, as Stage 3 does.
func candidateOf(t *testing.T, a App, files map[string]string) string {
	t.Helper()
	base, _ := a.Repo.ResolveCommit("HEAD")
	work := t.TempDir()
	if err := a.Repo.Export(base, work); err != nil {
		t.Fatal(err)
	}
	for rel, s := range files {
		write(t, work, rel, s)
	}
	tree, err := a.Repo.TreeFromDir(base, work)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.Repo.CommitOn(tree, base, "a2dlc builder_primary test", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTier0SumsFindingsAndListsUnreviewed(t *testing.T) {
	a, _, _ := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\nBROKEN\n", "README.md": "bye\n"})
	probe := func(id, file, expr string) contract.Probe {
		return contract.Probe{ID: id, Description: id, Input: "x", Expected: "y", Kind: "example", File: &file, Expr: &expr}
	}
	noExpr := contract.Probe{ID: "BP-03", Description: "property", Input: "x", Expected: "y", Kind: "property"}
	probes := []contract.Probe{probe("BP-01", "src/lib/f.ts", "fake"), probe("BP-02", "other/x.go", "true"), noExpr}
	r, without, err := a.Tier0(context.Background(), "HEAD", c, probes)
	if err != nil || r.OK || len(r.Findings) != 2 || *r.Findings[0].Line != 2 || r.Findings[1].Kind != contract.Tier0Probe {
		t.Fatalf("%v %+v", err, r)
	}
	if strings.Join(without, ",") != "README.md,probe:BP-02,probe:BP-03" {
		t.Fatalf("unreviewed: %v", without)
	}
	// an engine that does not do tier0 is not the builder's failure: its files remain unreviewed
	c = candidateOf(t, a, map[string]string{"src/lib/f.ts": "NO_TIER0\n"})
	if r, without, err = a.Tier0(context.Background(), "HEAD", c, nil); err != nil || !r.OK || strings.Join(without, ",") != "src/lib/f.ts" {
		t.Fatalf("%v %+v %v", err, r, without)
	}
}

// The intent's forbidden_imports reach the engines' policy, unioned with config.tier0 (sorted, unique), and the policy
// that nothing says is not sent at all (the engines' default).
func TestTier0WithUnionsIntentForbiddenImports(t *testing.T) {
	a, _, _ := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	seenPolicy = nil
	if _, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, Tier0Extra{ForbiddenImports: []string{"json", "os", "json"}}); err != nil {
		t.Fatal(err)
	}
	if seenPolicy == nil || strings.Join(seenPolicy.ForbiddenImports, ",") != "json,os" {
		t.Fatalf("policy the engine got: %+v", seenPolicy)
	}
	seenPolicy = nil
	if _, _, err := a.Tier0(context.Background(), "HEAD", c, nil); err != nil {
		t.Fatal(err)
	}
	if seenPolicy != nil {
		t.Fatalf("without a restriction the engine gets no policy: %+v", seenPolicy)
	}
}

// intent.boundary.forbidden_side_effects reach the engines' policy the same way, and a config policy keeps its own.
func TestTier0WithUnionsIntentForbiddenSideEffects(t *testing.T) {
	a, _, _ := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	seenPolicy = nil
	extra := Tier0Extra{ForbiddenSideEffects: []string{"network.http.outbound", "fs.write", "network.http.outbound"}}
	if _, _, err := a.Tier0With(context.Background(), "HEAD", c, nil, extra); err != nil {
		t.Fatal(err)
	}
	if seenPolicy == nil || strings.Join(seenPolicy.ForbiddenSideEffects, ",") != "fs.write,network.http.outbound" || seenPolicy.ForbiddenImports != nil {
		t.Fatalf("policy the engine got: %+v", seenPolicy)
	}
}

func TestMergePolicyKeepsConfigAndAddsIntentRestrictions(t *testing.T) {
	if p := mergePolicy(nil, Tier0Extra{}); p != nil {
		t.Fatalf("%+v", p)
	}
	if p := mergePolicy(&contract.PolicyTier0{RequireHeader: true}, Tier0Extra{}); p == nil || !p.RequireHeader || p.ForbiddenImports != nil || p.ForbiddenSideEffects != nil {
		t.Fatalf("%+v", p)
	}
	p := mergePolicy(&contract.PolicyTier0{ForbiddenImports: []string{"os"}, MaxComplexity: 9}, Tier0Extra{ForbiddenImports: []string{"json", "os"}})
	if p == nil || strings.Join(p.ForbiddenImports, ",") != "json,os" || p.MaxComplexity != 9 {
		t.Fatalf("%+v", p)
	}
	p = mergePolicy(&contract.PolicyTier0{ForbiddenSideEffects: []string{"fs.write"}}, Tier0Extra{ForbiddenSideEffects: []string{"db.raw_sql_unparameterized"}})
	if p == nil || strings.Join(p.ForbiddenSideEffects, ",") != "db.raw_sql_unparameterized,fs.write" {
		t.Fatalf("%+v", p)
	}
}

func TestCheckJudgesCandidateNotWorktree(t *testing.T) {
	a, m, dir := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	v, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{CandidateTree: c})
	if err != nil || v.Verdict != contract.FAIL || m.calls != 1 || len(v.Targets) != 1 {
		t.Fatalf("%v %+v", err, v)
	}
	tree, _ := a.Repo.CommitTree(c)
	if v.TreeHash != treePrefix+tree {
		t.Fatalf("tree_hash %s, candidate %s", v.TreeHash, tree)
	}
	// the working tree is still on the base, and check without a candidate does not see the builder's change
	if b, _ := os.ReadFile(filepath.Join(dir, "src/lib/f.ts")); string(b) != "export const f = 1\n" {
		t.Fatalf("%q", b)
	}
	if v, err = a.Check(context.Background(), "HEAD"); err != nil || v.Verdict != contract.PASS {
		t.Fatalf("%v %s", err, v.Verdict)
	}
}
