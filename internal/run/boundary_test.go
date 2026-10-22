package run

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// CB-04 on the intent's boundary: crossing it escalates the stage in the iteration that crosses it, without Tier 0 or
// another iteration, and the escalation is recorded in escalations.json as protected_boundary.

func TestCB04ProtectedFileEscalatesWithoutTier0(t *testing.T) {
	a, dir, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "boundary", contract.ModeLite, "f doubles"
		in.Boundary = &contract.Boundary{ForbiddenPaths: []string{"src/**"}}
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "boundary", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-04: ") || !strings.Contains(s3.Reason, "src/f.py::src/**") ||
		s3.Artifact == nil || s3.Artifact.Path != ArtEscalations || st.Stages[6].Status != contract.StagePending || *st.Outcome != "escalated" {
		t.Fatalf("%+v %+v", s3, st.Stages[6])
	}
	if engine.tier0 != 0 || len(launcherFake.seen) != 1 {
		t.Fatalf("tier0 ran %d times, the builder %d: the boundary had to escalate before Tier 0", engine.tier0, len(launcherFake.seen))
	}
	// the builder receives the boundary in the payload
	if len(requests) != 1 || requests[0].Boundary == nil || strings.Join(requests[0].Boundary.ForbiddenPaths, ",") != "src/**" {
		t.Fatalf("%+v", requests)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/boundary", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 {
		t.Fatalf("%+v", escalations.Items)
	}
	e := escalations.Items[0]
	if e.Kind != "protected_boundary" || e.Target != "src/f.py::src/**" || e.Builder != contract.RoleBuilderPrimary || e.ID != "esc-"+expectedID("protected_boundary", e.Target) ||
		!strings.Contains(e.Summary, "decision") || !strings.Contains(e.Evidence.Message, "return x * 2") {
		t.Fatalf("%+v", e)
	}
}

func TestCB04SymbolLineProtectedEscalates(t *testing.T) {
	a, dir, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		// changes the signature of f: the line that defines it contains the symbol
		return map[string]string{"src/f.py": "def f(x, y):\n    return x\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "boundary", contract.ModeLite, "f"
		in.Boundary = &contract.Boundary{DoNotRefactor: []string{"f"}}
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "boundary", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-04: ") || engine.tier0 != 0 {
		t.Fatalf("%+v tier0=%d", s3, engine.tier0)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/boundary", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "protected_boundary" || escalations.Items[0].Target != "src/f.py::f" ||
		escalations.Items[0].Evidence.Message != "+ def f(x, y):" {
		t.Fatalf("%+v", escalations.Items)
	}
}

// A change that does not touch the boundary passes: the symbol is not in the changed lines and the file is not protected.
func TestCB04CleanChangePasses(t *testing.T) {
	a, _, engine := repoBuilder(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "clean", contract.ModeLite, "f doubles"
		in.Boundary = &contract.Boundary{ForbiddenPaths: []string{"docs/**"}, DoNotRefactor: []string{"f"}}
	})
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "clean", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.Status != contract.StageDone || strings.Contains(s3.Reason, "CB-04") || s3.Artifact == nil || s3.Artifact.Path != ArtCandidate {
		t.Fatalf("%+v", s3)
	}
	if engine.tier0 != 1 || requests[0].Boundary == nil {
		t.Fatalf("tier0 %d, boundary %+v", engine.tier0, requests[0].Boundary)
	}
}

// With N=2 the boundary escalates the stage as soon as a builder crosses it: the escalation says which one, and the
// orthogonal builder crossed, so the primary already had a candidate and the stage still does not continue.
func TestCB04N2OrthogonalCrossesBoundaryEscalatesStage(t *testing.T) {
	withRouterN2(t)
	a, dir, engine := repoBuilder(t)
	write(t, dir, ".a2dlc/roles.json", rolesN2)
	path := intentFile(t, func(in *contract.Intent) {
		in.SliceID, in.Mode, in.Goal = "n2.v1", contract.ModeLite, "f doubles"
		in.Boundary = &contract.Boundary{DoNotRefactor: []string{"f"}}
	})
	var seen []launchRecord
	launcherFake := &fakeLauncher{script: buildersN2(t, &seen, func(role string, pl contract.BuilderRequest) map[string]string {
		if role == contract.RoleBuilderOrthogonal {
			return map[string]string{"src/f.py": "def f(x, y):\n    return x + x\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "n2.v1", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-04: ") || st.Stages[6].Status != contract.StagePending {
		t.Fatalf("%+v %+v", s3, st.Stages[6])
	}
	if len(seen) != 2 || seen[1].role != contract.RoleBuilderOrthogonal || engine.tier0 != 1 {
		t.Fatalf("%+v tier0=%d (the primary runs Tier 0 and the orthogonal escalates first)", seen, engine.tier0)
	}
	var escalations contract.Escalations
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/n2.v1", ArtEscalations), &escalations)
	if len(escalations.Items) != 1 || escalations.Items[0].Builder != contract.RoleBuilderOrthogonal || escalations.Items[0].Target != "src/f.py::f" {
		t.Fatalf("%+v", escalations.Items)
	}
}

func TestSymbolPatternMatchesOnlyWholeWord(t *testing.T) {
	f := symbolPattern("f")
	for _, l := range []string{"def f(x):", "f", "return f(x)", "x = f"} {
		if !f.MatchString(l) {
			t.Errorf("%q should match f", l)
		}
	}
	for _, l := range []string{"def fx(x):", "def_f(x):", "if(x):", "self.f_x"} {
		if f.MatchString(l) {
			t.Errorf("%q should not match f", l)
		}
	}
	// a symbol with a dot: as a whole word, not stuck to letters
	ab := symbolPattern("a.b")
	if !ab.MatchString("x = a.b()") || ab.MatchString("xa.b") || ab.MatchString("a.bc") {
		t.Error("a.b matched wrongly")
	}
}

// expectedID is the id the core gives an escalation: sha256 of kind, NUL and target, with 12 hex (escalation.schema.json).
func expectedID(kind, target string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + target))
	return hex.EncodeToString(h[:])[:12]
}
