package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// a2dlc-31w: check judges with the base's config, so a tests block that the diff itself adds does not run. That is
// deliberate (a candidate that supplies its own judge could certify itself), but it must not be silent: the receipt and
// the agent message say that the block was ignored and what to do.
func TestCandidateTestsBlockAbsentFromBaseIsIgnoredAndSaid(t *testing.T) {
	t.Setenv("A2DLC_CACHE_DIR", t.TempDir())
	a, _, dir := newRepo(t)
	tf := &fakeTests{}
	a.Tests = tf
	write(t, dir, ".a2dlc/config.json", gatesConfig)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nNO_TARGET\nmore\n")
	write(t, dir, "tests/n.t", "t_new passed\ncovers src/lib/f.ts:2-3\n")

	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("v=%s err=%v", v.Verdict, err)
	}
	if v.TestGates != nil || len(v.Tests) != 0 || len(tf.runs) != 0 {
		t.Fatalf("the candidate's tests block must not run: gates=%+v tests=%d runs=%d", v.TestGates, len(v.Tests), len(tf.runs))
	}
	if v.Coverage == nil || v.Coverage.Status != contract.CoverageNotConfigured {
		t.Fatalf("%+v", v.Coverage)
	}
	for _, want := range []string{"tests", "base", "config"} {
		if !strings.Contains(v.Coverage.Reason, want) {
			t.Fatalf("coverage.reason must explain the ignored tests block (missing %q): %q", want, v.Coverage.Reason)
		}
	}
	msg := message(v, nil, "")
	if !strings.Contains(msg, "tests block") || !strings.Contains(msg, "base") {
		t.Fatalf("the agent message does not say why no test ran:\n%s", msg)
	}

	// Once the config is in the base, the same diff runs the gate and the reason is gone.
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "adopt the tests block")
	write(t, dir, "src/lib/f.ts", "export const f = 1\nNO_TARGET\nmore\nand more\n")
	v, err = a.Check(context.Background(), "HEAD")
	if err != nil || v.TestGates == nil || v.Coverage == nil || v.Coverage.Status == contract.CoverageNotConfigured || v.Coverage.Reason != "" {
		t.Fatalf("err=%v gates=%+v cov=%+v", err, v.TestGates, v.Coverage)
	}
}

// A repo without any tests block, in the base and in the candidate, keeps the old receipt: nothing was ignored.
func TestNoTestsBlockAnywhereHasNoReason(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 1\nNO_TARGET\nmore\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Coverage == nil || v.Coverage.Status != contract.CoverageNotConfigured || v.Coverage.Reason != "" {
		t.Fatalf("err=%v cov=%+v", err, v.Coverage)
	}
}
