package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// formatCheck flags src/f.py while it carries the UGLY marker, and notes how often it ran.
type formatCheck struct{ runs int }

func (f *formatCheck) RunCheck(_ context.Context, c contract.CheckConfig, files []string, _, candDir string) (engine.CheckRun, error) {
	f.runs++
	r := contract.CheckResponse{SchemaVersion: 1, ID: c.ID, Status: contract.CheckRan, Pass: true, Findings: []contract.CheckFinding{}}
	for _, file := range files {
		b, _ := os.ReadFile(filepath.Join(candDir, file))
		if strings.Contains(string(b), "UGLY") {
			r.Pass = false
			r.Findings = append(r.Findings, contract.CheckFinding{Target: file + "::fmt", Message: "would reformat " + file})
		}
	}
	return engine.CheckRun{Response: r}, nil
}

// RFC-0011 (Draft): a blocking check that fires at stage:3 reaches the builder in Tier 0. Round 1 leaves what the check
// flags, round 2 gets it as a finding of kind check and fixes it, and Stage 4, where the same check fires at check, passes.
func TestStage3BlockingCheckFeedsTheBuilder(t *testing.T) {
	a, dir, _ := repoBuilder(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}],`+
		`"checks":[{"id":"fmt","image":"fmt:1","command":"fmt","severity":"blocking","fire_on":["check","stage:3"],"matches":["src/**"]}]}`)
	git(t, dir, "commit", "-qam", "checks")
	fc := &formatCheck{}
	a.Checks = fc
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback == nil {
			return map[string]string{"src/f.py": "def f(x):\n    return x * 2  # UGLY\n"}
		}
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "b.v1"})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.ID != "3" || s3.Status != contract.StageDone || s3.Iterations != 2 {
		t.Fatalf("%+v", s3)
	}
	if len(requests) != 2 || requests[1].Tier0Feedback == nil {
		t.Fatalf("%+v", requests)
	}
	fb := requests[1].Tier0Feedback.Findings
	if len(fb) != 1 || fb[0].Kind != contract.Tier0Check || fb[0].File != "src/f.py" || fb[0].Message != "fmt: would reformat src/f.py" {
		t.Fatalf("round 2 feedback %+v", fb)
	}
	var v contract.Verdict
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/run/b.v1", ArtVerdict))
	if err := contract.DecodeStrictBytes(b, &v); err != nil || len(v.Checks) != 1 || v.Checks[0].Status != contract.CheckRan || len(v.Checks[0].FindingList()) != 0 {
		t.Fatalf("%v %+v", err, v.Checks)
	}
	// two Tier 0 rounds and Stage 4
	if fc.runs != 3 {
		t.Fatalf("the check ran %d times", fc.runs)
	}
}
