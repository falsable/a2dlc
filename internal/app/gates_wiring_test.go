package app

import (
	"context"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// goldenReplayer is a runner that also replays a golden master: the reply is set by the test.
type goldenReplayer struct {
	*fakeEngine
	replay contract.GoldenResponse
	err    error
	asked  []string
}

func (g *goldenReplayer) Golden(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string, recorded []byte) (contract.GoldenResponse, error) {
	g.asked = append(g.asked, req.Operation)
	if recorded == nil {
		panic("a replay must send the recorded golden.json")
	}
	return g.replay, g.err
}

// goldenRecord is a golden master of two recorded cases.
func goldenRecord() *contract.Golden {
	return &contract.Golden{SchemaVersion: contract.SchemaVersion, Image: "golden:1",
		Cases: []contract.GoldenCase{{ID: "a.t", OutputHash: "sha256:a"}, {ID: "b.t", OutputHash: "sha256:b"}}}
}

// The new test of a feature passes on the base: with change_kind feature that is a FAIL through CheckWith; the same
// test with no declared kind is a characterization test and passes.
func TestCheckWithChangeKindFailsANewPassingTest(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, "tests/new.t", "newcase uses:old\n")
	v, err := a.CheckWith(context.Background(), "", CheckOptions{ChangeKind: "feature"})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != contract.FAIL || testsOf(v)["newcase"].Verdict != contract.FAIL {
		t.Fatalf("feature: %s %+v", v.Verdict, v.Tests)
	}
	v, err = a.CheckWith(context.Background(), "", CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if testsOf(v)["newcase"].Verdict != contract.PASS {
		t.Fatalf("undeclared kind: %+v", v.Tests)
	}
}

// Every case replayed with the same output: the golden master adds no target and the check passes.
func TestCheckWithGoldenReplayPassesWhenNothingDiffers(t *testing.T) {
	a, m, _, _ := gatesRepo(t)
	g := &goldenReplayer{fakeEngine: m, replay: contract.GoldenResponse{SchemaVersion: contract.SchemaVersion,
		Cases: []contract.GoldenCase{{ID: "a.t"}, {ID: "b.t"}}, Diffs: []contract.GoldenDiff{}}}
	a.Runner = g
	v, err := a.CheckWith(context.Background(), "", CheckOptions{Golden: goldenRecord()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != contract.PASS || len(g.asked) != 1 || g.asked[0] != "golden_replay" {
		t.Fatalf("%s asked=%v %+v", v.Verdict, g.asked, v.Tests)
	}
	for _, r := range v.Tests {
		if r.Gate == GateGolden {
			t.Fatalf("a replay with no difference added %+v", r)
		}
	}
}

// A case whose output differs is FAIL at golden::<id>, and an enforce_new_behavior waiver covers it like any FAIL: without
// a signature the waiver is pending and verify stops.
func TestCheckWithGoldenDifferenceIsAWaivableFail(t *testing.T) {
	a, m, _, dir := gatesRepo(t)
	g := &goldenReplayer{fakeEngine: m, replay: contract.GoldenResponse{SchemaVersion: contract.SchemaVersion,
		Cases: []contract.GoldenCase{{ID: "a.t"}, {ID: "b.t"}}, Diffs: []contract.GoldenDiff{{ID: "b.t", Old: "sha256:b", New: "sha256:c"}}}}
	a.Runner = g
	v, err := a.CheckWith(context.Background(), "", CheckOptions{Golden: goldenRecord()})
	if err != nil {
		t.Fatal(err)
	}
	ts := testsOf(v)
	if v.Verdict != contract.FAIL || ts["b.t"].Verdict != contract.FAIL || ts["b.t"].Gate != GateGolden || ts["b.t"].Target != "golden::b.t" {
		t.Fatalf("%s %+v", v.Verdict, v.Tests)
	}
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"golden::b.t","decision":"enforce_new_behavior","reason":"intended"}]}`)
	v, err = a.CheckWith(context.Background(), "", CheckOptions{Golden: goldenRecord()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != contract.PASS || len(v.PendingWaivers) != 1 || v.PendingWaivers[0] != "golden::b.t" || !testsOf(v)["b.t"].Waived {
		t.Fatalf("%s pending=%v %+v", v.Verdict, v.PendingWaivers, v.Tests)
	}
}

// A recorded case the candidate did not replay is a FAIL too: nothing recorded goes unchecked.
func TestCheckWithGoldenMissingCaseIsAFail(t *testing.T) {
	a, m, _, _ := gatesRepo(t)
	g := &goldenReplayer{fakeEngine: m, replay: contract.GoldenResponse{SchemaVersion: contract.SchemaVersion,
		Cases: []contract.GoldenCase{{ID: "a.t"}}, Diffs: []contract.GoldenDiff{}}}
	a.Runner = g
	v, err := a.CheckWith(context.Background(), "", CheckOptions{Golden: goldenRecord()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != contract.FAIL || testsOf(v)["b.t"].Verdict != contract.FAIL {
		t.Fatalf("%s %+v", v.Verdict, v.Tests)
	}
}

// An engine that answers with an error replayed nothing: the check is INCONCLUSIVE with the engine's reason, and no case
// is reported as a difference.
func TestCheckWithGoldenEngineErrorIsInconclusiveWithItsReason(t *testing.T) {
	a, m, _, _ := gatesRepo(t)
	reason := "the candidate does not build"
	g := &goldenReplayer{fakeEngine: m, replay: contract.GoldenResponse{SchemaVersion: contract.SchemaVersion, Cases: []contract.GoldenCase{}, Diffs: []contract.GoldenDiff{}, Error: &reason}}
	a.Runner = g
	v, err := a.CheckWith(context.Background(), "", CheckOptions{Golden: goldenRecord()})
	if err != nil {
		t.Fatal(err)
	}
	var inc *contract.TestResult
	for i := range v.Tests {
		if v.Tests[i].Gate == GateGolden {
			inc = &v.Tests[i]
		}
	}
	if v.Verdict != contract.INCONCLUSIVE || inc == nil || inc.Verdict != contract.INCONCLUSIVE || !strings.Contains(inc.Reason, reason) {
		t.Fatalf("%s %+v", v.Verdict, v.Tests)
	}
}

// A runner without the golden operation cannot replay: INCONCLUSIVE, not a silent pass.
func TestCheckWithGoldenWithoutRunnerIsInconclusive(t *testing.T) {
	a, _, _, _ := gatesRepo(t)
	v, err := a.CheckWith(context.Background(), "", CheckOptions{Golden: goldenRecord()})
	if err != nil {
		t.Fatal(err)
	}
	if v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("%s %+v", v.Verdict, v.Tests)
	}
}

// A golden master with no tests block in the base config has no gate to run in: it is refused, not dropped.
func TestCheckWithGoldenWithoutTestsBlockIsRefused(t *testing.T) {
	a, _, _ := newRepo(t)
	if _, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{Golden: goldenRecord()}); err == nil {
		t.Fatal("a golden master with no tests block was accepted")
	}
}
