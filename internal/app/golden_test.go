package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

const goldenConfig = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],
 "tests":{"image":"fake","paths":["tests/**"],"command":"nop","baseline_command":"baseline","invalid_base_failures":["ImportError"],
 "golden_master":{"command":"run-cases","image":"bb:1","cases":"cases/*.json"}}}`

// goldenFake answers the golden operations: it records every case it is asked for, omits the ones in omit from the
// replay, and reports diffs when replaying.
type goldenFake struct {
	*fakeEngine
	omit  []string
	diffs []contract.GoldenDiff
	ops   []string
}

func (g *goldenFake) Golden(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string, _ []byte) (contract.GoldenResponse, error) {
	g.ops = append(g.ops, req.Operation)
	var budget struct {
		Cases []string `json:"cases"`
	}
	if err := json.Unmarshal(req.Budget, &budget); err != nil {
		return contract.GoldenResponse{}, err
	}
	h := "sha256:" + strings.Repeat("1", 64)
	out := contract.GoldenResponse{SchemaVersion: contract.SchemaVersion, Cases: []contract.GoldenCase{}, Diffs: []contract.GoldenDiff{}}
	for _, id := range budget.Cases {
		if !slices.Contains(g.omit, id) {
			out.Cases = append(out.Cases, contract.GoldenCase{ID: id, InputHash: h, OutputHash: h})
		}
	}
	if req.Operation == "golden_replay" {
		out.Diffs = append(out.Diffs, g.diffs...)
	}
	return out, nil
}

// goldenRepo is gatesRepo with two committed cases on the base and golden_master declared.
func goldenRepo(t *testing.T) (App, *goldenFake, string, string) {
	t.Helper()
	a, m, _, dir := gatesRepo(t)
	write(t, dir, "cases/a.json", "{}\n")
	write(t, dir, "cases/b.json", "{}\n")
	write(t, dir, ".a2dlc/config.json", goldenConfig)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "cases")
	g := &goldenFake{fakeEngine: m}
	a.Runner = g
	base := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	return a, g, base, dir
}

func TestRecordGoldenRecordsEveryMatchingCaseOnTheBase(t *testing.T) {
	a, g, base, _ := goldenRepo(t)
	rec, err := a.RecordGolden(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if rec.Base != base || rec.Command != "run-cases" || rec.Image != "bb:1" || len(rec.Cases) != 2 ||
		rec.Cases[0].ID != "cases/a.json" || rec.Cases[1].ID != "cases/b.json" || rec.RecordedAt == "" {
		t.Fatalf("%+v", rec)
	}
	if len(g.ops) != 1 || g.ops[0] != "golden_record" {
		t.Fatalf("operations %v", g.ops)
	}
}

func TestRecordGoldenWithoutDeclarationIsNotConfigured(t *testing.T) {
	a, _, _, _ := gatesRepo(t)
	if _, err := a.RecordGolden(context.Background(), "HEAD"); err != ErrGoldenNotConfigured {
		t.Fatalf("err %v", err)
	}
}

func TestRecordGoldenWithoutMatchingCaseIsAnError(t *testing.T) {
	a, _, _, dir := goldenRepo(t)
	write(t, dir, ".a2dlc/config.json", strings.Replace(goldenConfig, `"cases/*.json"`, `"nothing/*.json"`, 1))
	git(t, dir, "commit", "-qam", "no case")
	if _, err := a.RecordGolden(context.Background(), "HEAD"); err == nil || !strings.Contains(err.Error(), "no committed case") {
		t.Fatalf("err %v", err)
	}
}

// The replay gate: each case whose output differs is a FAIL target named by the case id.
func TestGateGoldenReplayFailsTheDivergentCase(t *testing.T) {
	a, g, base, dir := goldenRepo(t)
	golden := contract.Golden{Base: base, Image: "bb:1", Command: "run-cases",
		Cases: []contract.GoldenCase{{ID: "cases/a.json"}, {ID: "cases/b.json"}}}
	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL old\n")
	git(t, dir, "commit", "-qam", "candidate")
	head := strings.TrimSpace(git(t, dir, "rev-parse", "HEAD"))
	g.diffs = []contract.GoldenDiff{{ID: "cases/b.json", Old: "sha256:aa", New: "sha256:bb"}}
	gate, res := a.gateGolden(context.Background(), gateInput{baseSha: base, tree: head}, golden)
	if gate.Status != contract.GateRan || len(res) != 1 {
		t.Fatalf("%+v %+v", gate, res)
	}
	if r := res[0]; r.Target != "golden::cases/b.json" || r.Gate != GateGolden || r.Verdict != contract.FAIL {
		t.Fatalf("%+v", r)
	}
	if g.ops[len(g.ops)-1] != "golden_replay" {
		t.Fatalf("operations %v", g.ops)
	}
}

// A recorded case that the candidate did not replay is a FAIL too: nothing recorded goes unchecked.
func TestGateGoldenReplayFlagsTheCaseNotReplayed(t *testing.T) {
	a, g, base, _ := goldenRepo(t)
	g.omit = []string{"cases/a.json"}
	golden := contract.Golden{Base: base, Image: "bb:1", Command: "run-cases",
		Cases: []contract.GoldenCase{{ID: "cases/a.json"}, {ID: "cases/b.json"}}}
	_, res := a.gateGolden(context.Background(), gateInput{baseSha: base, tree: base}, golden)
	if len(res) != 1 || res[0].Target != "golden::cases/a.json" || res[0].Verdict != contract.FAIL {
		t.Fatalf("%+v", res)
	}
}

func TestGateGoldenReplayCleanGivesNoTargets(t *testing.T) {
	a, _, base, _ := goldenRepo(t)
	golden := contract.Golden{Base: base, Image: "bb:1", Command: "run-cases",
		Cases: []contract.GoldenCase{{ID: "cases/a.json"}, {ID: "cases/b.json"}}}
	gate, res := a.gateGolden(context.Background(), gateInput{baseSha: base, tree: base}, golden)
	if gate.Status != contract.GateRan || len(res) != 0 {
		t.Fatalf("%+v %+v", gate, res)
	}
}
