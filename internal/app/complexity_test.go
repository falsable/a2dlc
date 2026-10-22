package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// Complexity of the fake engine: cyclomatic is the count of "if" in the candidate's file and ast_nodes its length; a
// file with NO_COMPLEXITY makes the engine unable to measure, and one with NO_PARSE gives null.
func (m *fakeEngine) Complexity(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candDir string) (contract.ComplexityResponse, error) {
	r := contract.ComplexityResponse{SchemaVersion: 1, Files: []contract.ComplexityFile{}}
	for _, f := range req.ChangedFiles {
		b, _ := os.ReadFile(filepath.Join(candDir, f))
		s := strings.ReplaceAll(string(b), "\r\n", "\n")
		if strings.Contains(s, "NO_COMPLEXITY") {
			return r, fmt.Errorf("%w: old engine", engine.ErrWithoutComplexity)
		}
		cf := contract.ComplexityFile{File: f, LocAdded: strings.Count(s, "\n")}
		if !strings.Contains(s, "NO_PARSE") {
			c, n := strings.Count(s, "if"), len(s)
			cf.Cyclomatic, cf.ASTNodes = &c, &n
		}
		r.Files = append(r.Files, cf)
	}
	return r, engine.ValidateComplexity(req, r)
}

func TestComplexitySumsMeasuredAndListsUnmeasured(t *testing.T) {
	a, _, _ := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "if a\nif b\n", "src/lib/h.ts": "NO_PARSE\n", "README.md": "bye\n"})
	r, err := a.Complexity(context.Background(), "HEAD", c)
	if err != nil || len(r.Files) != 2 || r.Cyclomatic == nil || *r.Cyclomatic != 2 || r.ASTNodes == nil || *r.ASTNodes != 10 || r.LocAdded != 3 {
		t.Fatalf("%v %+v", err, r)
	}
	// README.md has no engine: it stays unmeasured, never as zero complexity
	if strings.Join(r.Unmeasured, ",") != "README.md" {
		t.Fatalf("unmeasured: %v", r.Unmeasured)
	}
	// an engine that does not do complexity leaves its files unmeasured and the totals nil
	c = candidateOf(t, a, map[string]string{"src/lib/f.ts": "NO_COMPLEXITY\n"})
	if r, err = a.Complexity(context.Background(), "HEAD", c); err != nil || r.Cyclomatic != nil || len(r.Files) != 0 || strings.Join(r.Unmeasured, ",") != "src/lib/f.ts" {
		t.Fatalf("%v %+v", err, r)
	}
}

func TestEscalationsCarryClusterByCause(t *testing.T) {
	v := contract.Verdict{Tests: []contract.TestResult{
		{Target: "t.py::c::a", Gate: contract.GateTestsDiff, Verdict: contract.FAIL, Reason: "x", Message: "assert f(2) == 4"},
		{Target: "t.py::c::b", Gate: contract.GateTestsDiff, Verdict: contract.FAIL, Reason: "y", Message: "assert f(31) == 62"},
		{Target: "t.py::c::c", Gate: contract.GateTestsDiff, Verdict: contract.FAIL, Reason: "z", Message: "KeyError: 'k'"},
	}}
	e := Escalations(v)
	if len(e) != 3 || e[0].ClusterID == "" || e[0].ClusterID != e[1].ClusterID || e[0].ClusterID == e[2].ClusterID || e[0].ID == e[1].ID {
		t.Fatalf("%+v", e)
	}
}
