package factory

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

const twoFunctions = "def first(x):\n    return x\n\n\n# padding\n# padding\n# padding\n# padding\n# padding\n# padding\n\n\ndef second(x):\n    return x\n"

// Two lanes edit the same file in places far apart. Both are built from the base, so the second one only lands
// because the queue rebases it on top of the first: the branch ends with both edits, and nothing is merged by hand.
func TestIntegrateRebasesALaneOnTheSameFileWhenTheEditsDoNotOverlap(t *testing.T) {
	d := target(t)
	write(t, d.Repo.Dir, "src/two.py", twoFunctions)
	git(t, d.Repo.Dir, "add", "-A")
	git(t, d.Repo.Dir, "commit", "-qm", "two functions")
	base := git(t, d.Repo.Dir, "rev-parse", "HEAD")
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	rep := reportOf(d,
		acceptedLane("i1", candidate(t, d, base, "i1", map[string]string{"src/two.py": strings.Replace(twoFunctions, "def first(x):\n    return x\n", "def first(x):\n    return x + 1\n", 1)})),
		acceptedLane("i2", candidate(t, d, base, "i2", map[string]string{"src/two.py": strings.Replace(twoFunctions, "def second(x):\n    return x\n", "def second(x):\n    return x * 2\n", 1)})))
	rec, err := Integrate(context.Background(), integrateDeps(d, &fakeHoldout{}), holdoutSpec(dir, seal, "i1", "i2"), rep)
	if err != nil {
		t.Fatal(err)
	}
	l := laneByIssue(rec)
	if l["i1"].Status != contract.IntegrationIntegrated || l["i2"].Status != contract.IntegrationIntegrated || l["i1"].Rebased || !l["i2"].Rebased {
		t.Fatalf("%+v", rec.Lanes)
	}
	got := git(t, d.Repo.Dir, "show", rec.HeadSha+":src/two.py")
	if !strings.Contains(got, "return x + 1") || !strings.Contains(got, "return x * 2") {
		t.Errorf("the integrated file lacks one of the edits:\n%s", got)
	}
	if git(t, d.Repo.Dir, "rev-parse", "main") != base || rec.MainShaAfter != base {
		t.Error("main moved")
	}
}
