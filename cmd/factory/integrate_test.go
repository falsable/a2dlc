package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/factory"
	"github.com/falsable/a2dlc/internal/gitx"
)

type noHoldout struct{ t *testing.T }

func (n noHoldout) Run(context.Context, contract.FactoryHoldout, string, []string) (int, string, error) {
	n.t.Fatal("the holdout ran for a run without an accepted lane")
	return 0, "", nil
}

// sealed writes a one-file holdout folder with its manifest and seal, and a spec that points at it by a relative dir.
func sealed(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(root, "holdout")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("a test\n")
	sum := sha256.Sum256(body)
	manifest := fmt.Sprintf("%s *a.test\n", hex.EncodeToString(sum[:]))
	seal := sha256.Sum256([]byte(manifest))
	for name, content := range map[string][]byte{"a.test": body, "MANIFEST.sha256": []byte(manifest), "SEAL": []byte(hex.EncodeToString(seal[:]) + "  MANIFEST.sha256\n")} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	spec, _ := json.Marshal(contract.FactoryHoldout{SchemaVersion: 1, Dir: "holdout", Seal: hex.EncodeToString(seal[:]), Image: "x", Command: "true",
		Units: []contract.FactoryHoldoutUnit{{IssueID: "issue-1", Files: []string{"a.test"}}}})
	path := filepath.Join(root, "holdout.json")
	if err := os.WriteFile(path, spec, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestIntegrateWritesTheRecordAndLeavesMainAlone(t *testing.T) {
	dir := repoWith(t, nil)
	chdir(t, dir)
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(head))
	rep := contract.FactoryReport{SchemaVersion: 1, RunID: "run-0123456789ab", BatchID: "t.1", BaseSha: base, Outcome: contract.RunCompleted,
		StartedAt: "2026-10-30T09:00:00Z", FinishedAt: "2026-10-30T09:01:00Z", ConfigSHA256: "sha256:" + strings.Repeat("c", 64),
		Lanes: []contract.FactoryLane{{IssueID: "issue-1", Status: contract.LaneRejected, Reason: "tests_touched", Stages: []contract.StageUsage{}}}}
	b, _ := json.Marshal(rep)
	root := t.TempDir()
	reportPath := filepath.Join(root, "report.json")
	if err := os.WriteFile(reportPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	old := holdoutRunner
	t.Cleanup(func() { holdoutRunner = old })
	holdoutRunner = noHoldout{t}
	e, out, errb := testEnv(t)
	outDir := filepath.Join(root, "out")
	args := []string{"integrate", "--report", reportPath, "--holdout", sealed(t, root), "--out", outDir, "--main-ref", "main"}
	if code := run(args, e); code != 0 {
		t.Fatalf("%d %q", code, errb)
	}
	if !strings.Contains(out.String(), "issue-1\tnot_queued\t") || !strings.Contains(out.String(), "main "+base+" unchanged") {
		t.Errorf("%q", out)
	}
	var rec contract.FactoryIntegration
	raw, err := os.ReadFile(filepath.Join(outDir, rep.RunID, "integration.json"))
	if err != nil || contract.DecodeStrictBytes(raw, &rec) != nil || rec.Validate() != nil || rec.MainShaAfter != base {
		t.Fatalf("%v %s", err, raw)
	}
	// a spec whose seal does not match the folder is an error, not a verdict
	spec := sealed(t, t.TempDir())
	if err := os.WriteFile(filepath.Join(filepath.Dir(spec), "holdout", "a.test"), []byte("edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, _, errb = testEnv(t)
	args[4] = spec
	if code := run(args, e); code != 1 || !strings.Contains(errb.String(), "holdout changed") {
		t.Fatalf("%d %q", code, errb)
	}
	if code := run([]string{"integrate", "--report", reportPath}, e); code != 64 {
		t.Errorf("missing --holdout: %d", code)
	}
}

type passTests struct{}

func (passTests) RunTests(context.Context, contract.TestsConfig, string, map[string]string, string) (engine.TestRun, error) {
	return engine.TestRun{Exit: 0, Log: "all passed"}, nil
}

// The reference integrator is the same command with another record: integration-reference.json, with its own usage, and
// it needs the factory config for its agent. A run with no accepted lane launches nothing and spends nothing.
func TestIntegrateReferenceWritesItsOwnRecordAndNeedsTheConfig(t *testing.T) {
	dir := repoWith(t, map[string]string{"config.json": configJSON})
	if err := os.MkdirAll(filepath.Join(dir, ".a2dlc"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".a2dlc", "config.json"), []byte(`{"tests":{"image":"x","paths":["tests/"],"command":"pytest"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "config"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
	chdir(t, dir)
	head, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	base := strings.TrimSpace(string(head))
	rep := contract.FactoryReport{SchemaVersion: 1, RunID: "run-0123456789ab", BatchID: "t.1", BaseSha: base, Outcome: contract.RunCompleted,
		StartedAt: "2026-10-30T09:00:00Z", FinishedAt: "2026-10-30T09:01:00Z", ConfigSHA256: "sha256:" + strings.Repeat("c", 64),
		Lanes: []contract.FactoryLane{{IssueID: "issue-1", Status: contract.LaneRejected, Reason: "tests_touched", Stages: []contract.StageUsage{}}}}
	b, _ := json.Marshal(rep)
	root := t.TempDir()
	reportPath := filepath.Join(root, "report.json")
	if err := os.WriteFile(reportPath, b, 0o644); err != nil {
		t.Fatal(err)
	}
	old := holdoutRunner
	t.Cleanup(func() { holdoutRunner = old })
	holdoutRunner = noHoldout{t}
	e, out, errb := testEnv(t)
	e.newApp = func(r gitx.Repo) app.App { return app.App{Repo: r, Tests: passTests{}} }
	outDir := filepath.Join(root, "out")
	args := []string{"integrate", "--report", reportPath, "--holdout", sealed(t, root), "--out", outDir, "--main-ref", "main", "--integrator", "reference", "--config", "config.json"}
	if code := run(args, e); code != 0 {
		t.Fatalf("%d %q", code, errb)
	}
	if !strings.Contains(out.String(), "issue-1\tnot_queued\t") || !strings.Contains(out.String(), "the integration's own calls: 0, 0.0000 usd") {
		t.Errorf("%q", out)
	}
	var rec contract.FactoryIntegration
	raw, err := os.ReadFile(filepath.Join(outDir, rep.RunID, "integration-reference.json"))
	if err != nil || contract.DecodeStrictBytes(raw, &rec) != nil || rec.Validate() != nil || rec.Integrator != contract.IntegratorReference || rec.Usage == nil || rec.Usage.Calls != 0 {
		t.Fatalf("%v %s", err, raw)
	}
	if _, err := os.Stat(filepath.Join(outDir, rep.RunID, "integration.json")); err == nil {
		t.Error("the reference record replaced the queue's")
	}
	// the reference needs its config, the queue does not take one, and nothing else is an integrator
	for _, bad := range [][]string{
		{"integrate", "--report", reportPath, "--holdout", "h.json", "--integrator", "reference"},
		{"integrate", "--report", reportPath, "--holdout", "h.json", "--config", "config.json"},
		{"integrate", "--report", reportPath, "--holdout", "h.json", "--integrator", "queue", "--config", "config.json"},
	} {
		if code := run(bad, e); code != 64 {
			t.Errorf("%v: %d", bad, code)
		}
	}
}

var _ factory.HoldoutRunner = noHoldout{}
