package run

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// A declared golden master is recorded even without tests.baseline_command: Stage −1 does not skip it silently. The
// stage is done with the golden record, and the baseline is said to be absent.
func TestStageMinusOneRecordsGoldenWithoutBaselineCommand(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, "cases/a.json", "{}\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run","golden_master":{"command":"run-cases","image":"bb:1","cases":"cases/*.json"}}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	a.Runner = &goldenRunner{}
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.ng", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	s := st.Stages[0]
	if s.ID != "-1" || s.Status != contract.StageDone || !strings.Contains(s.Reason, "no tests.baseline_command") ||
		!strings.Contains(s.Reason, "golden master of 1 cases") || s.Artifact == nil || s.Artifact.Path != ArtGolden {
		t.Fatalf("%+v", s)
	}
	var g contract.Golden
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lb.ng", ArtGolden), &g)
	if len(g.Cases) != 1 || g.SliceID != "lb.ng" {
		t.Fatalf("%+v", g)
	}
}

// Without a golden master and without baseline_command the skip stays a skip with its reason.
func TestStageMinusOneNothingToRecordStaysSkipped(t *testing.T) {
	a, dir, _ := repoWithTests(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"x","paths":["tests/**"],"source_paths":["src/**"],"command":"run"}}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "baseline")
	a.Runner = &goldenRunner{}
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "lb.ns", Mode: contract.ModeRefactor})
	if err != nil {
		t.Fatal(err)
	}
	if s := st.Stages[0]; s.Status != contract.StageSkipped || !strings.HasPrefix(s.Reason, reasonWithoutBaseline) {
		t.Fatalf("%+v", s)
	}
}
