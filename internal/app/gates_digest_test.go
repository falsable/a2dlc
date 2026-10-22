package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// noDigestTests is the fake tests runner of an image that was built locally and never pushed: no RepoDigests.
type noDigestTests struct{ *fakeTests }

func (noDigestTests) ImageDigest(context.Context, string) (string, error) { return "", nil }

// A run that gets past Stage −1 (or never had it) must not key the baseline cache without a digest: the gate is an error
// and the baseline command does not run on the base.
func TestGateBaselineWithoutDigestIsAnErrorAndRunsNothing(t *testing.T) {
	a, _, tf, dir := gatesRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n") // something changed, so the baseline gate has work
	a.Tests = noDigestTests{tf}
	v, _ := a.Check(context.Background(), "")
	g := v.TestGates.Baseline
	if g.Status != contract.GateError || !strings.Contains(g.Reason, "no RepoDigests") {
		t.Fatalf("baseline gate: %+v", g)
	}
	for _, c := range tf.runs {
		if c == "baseline" {
			t.Fatalf("the baseline ran without a digest: %v", tf.runs)
		}
	}
}
