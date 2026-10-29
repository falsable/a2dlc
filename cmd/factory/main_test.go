package main

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	a2run "github.com/falsable/a2dlc/internal/run"
)

type neverLauncher struct{ t *testing.T }

func (l neverLauncher) Launch(context.Context, contract.Role, a2run.LaunchSpec) (a2run.LaunchResult, error) {
	l.t.Fatal("a launch for an issue that precheck skips")
	return a2run.LaunchResult{}, nil
}

func testEnv(t *testing.T) (env, *bytes.Buffer, *bytes.Buffer) {
	var out, errb bytes.Buffer
	return env{launcher: neverLauncher{t}, out: &out, err: &errb, newApp: func(r gitx.Repo) app.App { return app.App{Repo: r} }}, &out, &errb
}

func repoWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"commit", "-q", "--allow-empty", "-m", "base"}} {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if b, err := c.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s", err, b)
		}
	}
	for rel, content := range files {
		if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const configJSON = `{"schema_version":1,"roles_image":"a2dlc-roles:test","argv":["python3","/a2dlc/adapter.py"],"network":"none","model":"haiku",
"tools":["Read"],"concurrency":1,"caps":{"lane_usd":1,"lane_seconds":60,"lane_turns":10,"run_stop_new_usd":4,"run_kill_usd":6,"total_usd":25}}`

const batchJSON = `{"schema_version":1,"batch_id":"t.1","issues":[
{"id":"issue-1","title":"x","body":"y","done_command":""},{"id":"issue-2","title":"x","body":"y","done_command":"  "}]}`

func TestVersionAndUsage(t *testing.T) {
	e, out, errb := testEnv(t)
	if code := run([]string{"version"}, e); code != 0 || strings.TrimSpace(out.String()) != version {
		t.Fatalf("%d %q", code, out)
	}
	if code := run(nil, e); code != 64 || !strings.Contains(errb.String(), "run --config") {
		t.Fatalf("%d %q", code, errb)
	}
	if code := run([]string{"merge"}, e); code != 64 {
		t.Fatal(code)
	}
}

func TestPrecheckListsWhatWouldReachAModel(t *testing.T) {
	dir := repoWith(t, map[string]string{"batch.json": `{"schema_version":1,"batch_id":"t.1","issues":[
{"id":"issue-1","title":"x","body":"y","done_command":"pytest"},{"id":"issue-2","title":"x","body":"y","done_command":""}]}`})
	e, out, _ := testEnv(t)
	if code := run([]string{"precheck", "--batch", filepath.Join(dir, "batch.json")}, e); code != 0 {
		t.Fatal(code)
	}
	if got := out.String(); got != "issue-1\tready\nissue-2\tskip\tno done_command: not a ticket\n" {
		t.Fatalf("%q", got)
	}
}

func TestRunWritesAReportAndLaunchesNothingForSkippedIssues(t *testing.T) {
	dir := repoWith(t, map[string]string{"config.json": configJSON, "batch.json": batchJSON})
	chdir(t, dir)
	e, out, errb := testEnv(t)
	outDir := t.TempDir()
	code := run([]string{"run", "--config", "config.json", "--batch", "batch.json", "--out", outDir}, e)
	if code != 0 {
		t.Fatalf("%d %s", code, errb)
	}
	reports, _ := filepath.Glob(filepath.Join(outDir, "run-*", "report.json"))
	if len(reports) != 1 || !strings.Contains(out.String(), "issue-1\tskipped\t0.0000 usd\tno done_command") || !strings.Contains(out.String(), "0 accepted") {
		t.Fatalf("%v\n%s", reports, out)
	}
	b, _ := os.ReadFile(reports[0])
	var rep contract.FactoryReport
	if err := contract.DecodeStrictBytes(b, &rep); err != nil || rep.Validate() != nil || !strings.HasPrefix(rep.ConfigSHA256, "sha256:") {
		t.Fatalf("%v %+v", err, rep)
	}
}

func TestRunRefusesAConfigWithoutAllCaps(t *testing.T) {
	dir := repoWith(t, map[string]string{"config.json": strings.Replace(configJSON, `"lane_turns":10,`, "", 1), "batch.json": batchJSON})
	chdir(t, dir)
	e, _, errb := testEnv(t)
	if code := run([]string{"run", "--config", "config.json", "--batch", "batch.json", "--out", t.TempDir()}, e); code != 1 || !strings.Contains(errb.String(), "lane_turns") {
		t.Fatalf("%d %q", code, errb)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}
