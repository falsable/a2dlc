package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// fakeEngine compares each file request between base and candidate: different is FAIL, equal is PASS, and a candidate
// with "INCONCLUSIVE" or "CRASH" gives that. "NO_TARGET" leaves the file out of scope (not_exported), like a change
// inside a method for the Python engine; "LINES a-b" bounds the target to those lines.
type fakeEngine struct{ calls int }

func (m *fakeEngine) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error) {
	m.calls++
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
	for _, f := range req.ChangedFiles {
		old, _ := os.ReadFile(filepath.Join(baseDir, f))
		newContent, _ := os.ReadFile(filepath.Join(candDir, f))
		raw := string(newContent)
		// The IMPL lines are symbols (the fake tests in gates_test.go use them): adding or removing them does not change
		// the behavior of f.
		old, newContent = withoutImpl(old), withoutImpl(newContent)
		if strings.Contains(string(newContent), "CRASH") {
			return r, os.ErrDeadlineExceeded
		}
		if strings.Contains(string(newContent), "NO_TARGET") {
			r.OutOfScope = append(r.OutOfScope, contract.OutOfScope{File: f, Reason: "not_exported"})
			continue
		}
		t := contract.TargetResult{Target: f + "::f", Engine: "fast-check", EngineVersion: "fake", Verdict: contract.PASS,
			SearchBudget: json.RawMessage(`{"examples":1,"seed":1}`), Generators: "typed_default", Equivalence: "strict"}
		if i := strings.Index(raw, "LINES "); i >= 0 {
			var a, b int
			fmt.Sscanf(raw[i:], "LINES %d-%d", &a, &b)
			t.Lines = []int{a, b}
		}
		switch {
		case strings.Contains(string(newContent), "INCONCLUSIVE"):
			t.Verdict, t.Reason = contract.INCONCLUSIVE, "flagged"
		case !bytes.Equal(old, newContent):
			t.Verdict, t.Counterexample = contract.FAIL, &contract.Counterexample{Args: "[]", Old: string(old), New: string(newContent)}
		}
		r.Targets = append(r.Targets, t)
		// Like the engines: a relaxation this fake does not apply (marker UNSUPPORTED) is reported back.
		if strings.Contains(string(newContent), "UNSUPPORTED") {
			r.EquivalenceUnsupported = req.EquivalenceRelaxations
		}
	}
	return r, nil
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, rel, s string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRepo(t *testing.T) (App, *fakeEngine, string) {
	t.Helper()
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	write(t, dir, "README.md", "hello\n")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	write(t, dir, ".gitignore", ".a2dlc/verdict.json\n.a2dlc/reports/\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := &fakeEngine{}
	return App{Repo: repo, Runner: m, Version: "test", Now: func() time.Time { return time.Unix(0, 0) }}, m, dir
}

func TestCheckWithoutChangesDoesNotCallEngine(t *testing.T) {
	a, m, _ := newRepo(t)
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || m.calls != 0 {
		t.Fatalf("v=%s err=%v calls=%d", v.Verdict, err, m.calls)
	}
}

func TestCheckFailAndFileWithoutEngine(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, "README.md", "bye\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.FAIL || len(v.Targets) != 1 || len(v.OutOfScope) != 1 || v.OutOfScope[0].Reason != "no_engine" {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

func TestOverMaxFilesDoesNotRunAndSaysSo(t *testing.T) {
	a, m, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"max_files":1}`)
	git(t, dir, "commit", "-qam", "cap 1")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, "src/lib/g.ts", "export const g = 1\n")
	write(t, dir, "README.md", "outside the engine, does not count toward the cap\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE || m.calls != 0 || len(v.EngineErrors) != 1 ||
		len(v.EngineErrors[0].Files) != 2 || !strings.Contains(v.EngineErrors[0].Error, "max_files=1") {
		t.Fatalf("v=%+v err=%v calls=%d", v, err, m.calls)
	}
	write(t, dir, "src/lib/g.ts", "")
	os.Remove(filepath.Join(dir, "src/lib/g.ts"))
	if v, _ = a.Check(context.Background(), "HEAD"); v.Verdict != contract.FAIL || m.calls != 1 {
		t.Fatalf("at the cap it does run: %s calls=%d", v.Verdict, m.calls)
	}
}

func TestCheckEngineDownIsInconclusive(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "CRASH\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE || len(v.EngineErrors) != 1 {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

func TestWaiverWithoutSignaturePassesCheckAndStopsVerify(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"on purpose"}]}`)
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 1 {
		t.Fatalf("check: v=%+v err=%v", v, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")
	probs, err := a.Verify("HEAD")
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "unsigned") {
		t.Fatalf("verify: %v %v", probs, err)
	}
}

// The signature is valid for the signed blob: editing the file after approving stops the push again.
func TestSignatureBoundToFile(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a, _, dir := newRepo(t)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)

	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"on purpose"}]}`)
	if err := a.Approve("src/lib/f.ts::f", key, "me@a2dlc"); err != nil {
		t.Fatal(err)
	}
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 0 {
		t.Fatalf("check after signing: %+v %v", v, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "signed")
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify signed: %v %v", probs, err)
	}

	write(t, dir, "src/lib/f.ts", "export const f = 3\n")
	if _, err := a.Check(context.Background(), "HEAD~1"); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qam", "edited after signing")
	probs, err := a.Verify("HEAD")
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "the file changed") {
		t.Fatalf("verify after editing: %v %v", probs, err)
	}
}

// Committing and running check again against HEAD must not wash a waiver without a signature: with an upstream, the
// default base is the upstream, and verify rejects a verdict whose base is not pushed.
func TestCheckAgainstHEADDoesNotWashCommittedChanges(t *testing.T) {
	a, _, dir := newRepo(t)
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "-u", "origin", "HEAD")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"x"}]}`)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")

	v, err := a.Check(context.Background(), "")
	if err != nil || len(v.PendingWaivers) != 1 {
		t.Fatalf("the default base is not the upstream: %+v %v", v, err)
	}
	if v, err = a.Check(context.Background(), "HEAD"); err != nil || len(v.Targets) != 0 {
		t.Fatalf("against HEAD it should see nothing: %+v %v", v, err)
	}
	probs, err := a.Verify("HEAD")
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "is not in the upstream") {
		t.Fatalf("verify washed the committed changes: %v %v", probs, err)
	}
}

func TestVerifyStaleNamesFile(t *testing.T) {
	a, _, dir := newRepo(t)
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	if probs, _ := a.Verify("HEAD"); len(probs) != 0 {
		t.Fatalf("a freshly checked tree should pass: %v", probs)
	}
	write(t, dir, "README.md", "other\n")
	git(t, dir, "commit", "-qam", "no check")
	probs, err := a.Verify("HEAD")
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "stale") || !strings.Contains(probs[0], "README.md") {
		t.Fatalf("verify: %v %v", probs, err)
	}
}

func TestGateOutputsDoNotChangeTree(t *testing.T) {
	a, _, dir := newRepo(t)
	before, _ := a.Repo.WorktreeTree()
	write(t, dir, ".a2dlc/approvals.json", "{}")
	write(t, dir, ".a2dlc/verdict.json", "{}")
	if after, _ := a.Repo.WorktreeTree(); after != before {
		t.Fatal("approvals.json or verdict.json changed the tree_hash")
	}
	write(t, dir, ".a2dlc/waivers.json", "{}")
	if after, _ := a.Repo.WorktreeTree(); after == before {
		t.Fatal("waivers.json must change the tree_hash")
	}
}

func TestHookClaudeBlocksAndExhausts(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"agent_hook":{"max_blocks":2}}`)
	git(t, dir, "commit", "-qam", "max 2")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	codes := []int{}
	var errb bytes.Buffer
	for i := 0; i < 3; i++ {
		codes = append(codes, a.Hook(context.Background(), "claude", strings.NewReader(`{"session_id":"s1"}`), &bytes.Buffer{}, &errb))
	}
	if codes[0] != 2 || codes[1] != 2 || codes[2] != 0 || !strings.Contains(errb.String(), "before export const f = 1") {
		t.Fatalf("codes %v, stderr %s", codes, errb.String())
	}
	var out bytes.Buffer
	if c := a.Hook(context.Background(), "gemini", strings.NewReader(`{"session_id":"g1"}`), &out, &bytes.Buffer{}); c != 0 || !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("gemini: %d %s", c, out.String())
	}
}

func TestInstallIgnoresRunFolder(t *testing.T) {
	a, _, dir := newRepo(t)
	if _, err := a.Install("/opt/a2dlc", []string{"claude"}); err != nil {
		t.Fatal(err)
	}
	gi, _ := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if !strings.Contains("\n"+string(gi), "\n.a2dlc/run/\n") {
		t.Fatalf(".gitignore:\n%s", gi)
	}
}

// The release pins the engine images by digest through -ldflags "-X"; install must write those, not a local tag.
func TestInstallWritesTheEngineImagesOfTheBuild(t *testing.T) {
	py, ts := enginePyImage, engineTSImage
	t.Cleanup(func() { enginePyImage, engineTSImage = py, ts })
	enginePyImage = "ghcr.io/falsable/a2dlc-engine-py@sha256:" + strings.Repeat("a", 64)
	engineTSImage = "ghcr.io/falsable/a2dlc-engine-ts@sha256:" + strings.Repeat("b", 64)
	a, _, dir := newRepo(t)
	if err := os.Remove(filepath.Join(dir, ".a2dlc/config.json")); err != nil {
		t.Fatal(err)
	}
	write(t, dir, "pyproject.toml", "[project]\nname = \"x\"\n")
	write(t, dir, "package.json", "{}")
	if _, err := a.Install("/opt/a2dlc", nil); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, ".a2dlc/config.json"))
	var c contract.Config
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, e := range c.Engines {
		got[e.Language] = e.Image
	}
	if got["python"] != enginePyImage || got["typescript"] != engineTSImage {
		t.Fatalf("engines: %v", got)
	}
}

func TestInstallCoexistsWithBeadsHook(t *testing.T) {
	a, _, dir := newRepo(t)
	git(t, dir, "config", "core.hooksPath", ".beads/hooks")
	write(t, dir, ".beads/hooks/pre-push", "#!/usr/bin/env sh\n# --- BEGIN BEADS INTEGRATION v1.3.0 ---\nbd hooks run pre-push \"$@\"\n# --- END BEADS INTEGRATION v1.3.0 ---\n")
	write(t, dir, ".claude/settings.json", `{"hooks":{"SessionStart":[{"hooks":[{"command":"bd prime --hook-json","type":"command"}],"matcher":""}]}}`)
	for i := 0; i < 2; i++ { // twice: idempotent
		if _, err := a.Install("/opt/a2dlc", []string{"claude"}); err != nil {
			t.Fatal(err)
		}
	}
	probesOfFile, _ := os.ReadFile(filepath.Join(dir, ".beads/hooks/pre-push"))
	s := string(probesOfFile)
	if strings.Count(s, blockStart) != 1 || !strings.HasPrefix(s, "#!/usr/bin/env sh\n"+blockStart) || !strings.Contains(s, "BEGIN BEADS") {
		t.Fatalf("pre-push:\n%s", s)
	}
	preCommitFile, _ := os.ReadFile(filepath.Join(dir, ".beads/hooks/pre-commit"))
	sCommit := string(preCommitFile)
	if strings.Count(sCommit, blockStart) != 1 || !strings.Contains(sCommit, "check || exit 1") {
		t.Fatalf("pre-commit:\n%s", sCommit)
	}
	st, _ := os.ReadFile(filepath.Join(dir, ".claude/settings.json"))
	if strings.Count(string(st), "hook claude") != 2 || !strings.Contains(string(st), "bd prime") || !strings.Contains(string(st), "PreToolUse") {
		t.Fatalf("settings:\n%s", st)
	}
}

func TestHookPreToolUseBlocksProtectedPath(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/**"],"budget":{}}],"protected_paths":["tests/human/**"]}`)
	var out, errb bytes.Buffer
	in := `{"session_id":"s1","tool_name":"Edit","tool_input":{"file_path":"tests/human/test_calc.py"}}`
	code := a.Hook(context.Background(), "claude", strings.NewReader(in), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "PreToolUse: cannot Edit \"tests/human/test_calc.py\": it matches protected_paths") {
		t.Fatalf("expected code 2 and protected path message, got code %d: %s", code, errb.String())
	}

	// For Gemini, it outputs JSON {"decision":"block"}
	out.Reset()
	errb.Reset()
	code = a.Hook(context.Background(), "gemini", strings.NewReader(in), &out, &errb)
	if code != 0 || !strings.Contains(out.String(), `"decision":"block"`) {
		t.Fatalf("expected gemini block, got code %d: %s", code, out.String())
	}
}

func TestHookPreToolUseAllowsNormalPath(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/**"],"budget":{}}],"protected_paths":["tests/human/**"]}`)
	var out, errb bytes.Buffer
	in := `{"session_id":"s1","tool_name":"Write","tool_input":{"file_path":"src/lib/calc.ts"}}`
	code := a.Hook(context.Background(), "claude", strings.NewReader(in), &out, &errb)
	if code != 0 {
		t.Fatalf("expected code 0, got %d: %s", code, errb.String())
	}
}

func TestHookPreToolUseBlocksBypassCommand(t *testing.T) {
	a, _, _ := newRepo(t)
	var out, errb bytes.Buffer
	in := `{"session_id":"s1","tool_name":"Bash","tool_input":{"command":"git commit -m 'skip' --no-verify"}}`
	code := a.Hook(context.Background(), "claude", strings.NewReader(in), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "cannot execute git commit with --no-verify") {
		t.Fatalf("expected code 2 and bypass message, got code %d: %s", code, errb.String())
	}
}

// The config that check applies is the base's: a diff that drops the glob from the working tree's config cannot make
// the file leave the engines' scope and turn a behavior change into PASS.
func TestConfigFromWorktreeCannotDropAGlob(t *testing.T) {
	a, m, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["other/**"],"budget":{}}]}`)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict == contract.PASS || m.calls != 1 {
		t.Fatalf("the base config covers src/lib/**: v=%s err=%v calls=%d out_of_scope=%+v", v.Verdict, err, m.calls, v.OutOfScope)
	}
}

// A base without a config fails closed: check does not fall back to the working tree's config.
// First adoption: the base never had a config, so check judges with the candidate's instead of blocking the commit
// that adds it (the pre-commit hook would otherwise make that commit impossible).
func TestCheckAdoptsTheCandidateConfigWhenTheBaseNeverHadOne(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base without config")
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := App{Repo: repo, Runner: &fakeEngine{}, Version: "test", Now: func() time.Time { return time.Unix(0, 0) }}
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	write(t, dir, "src/lib/f.ts", "export const f = 2 // FAIL\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.FAIL {
		t.Fatalf("the candidate's config must judge the change: v=%+v err=%v", v, err)
	}
	if _, err := os.Stat(filepath.Join(dir, pathVerdict)); err != nil {
		t.Fatalf("the verdict must be written: %v", err)
	}
}

// A config that existed in the base's history and was deleted is not replaced by the candidate's: an agent could
// delete it in one commit and add a narrower one in the next.
func TestCheckFailsClosedWhenTheBaseDeletedItsConfig(t *testing.T) {
	a, _, dir := newRepo(t)
	git(t, dir, "rm", "-q", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "delete the config")
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/other/**"],"budget":{}}]}`)
	write(t, dir, "src/lib/f.ts", "export const f = 2 // FAIL\n")
	if _, err := a.Check(context.Background(), "HEAD"); err == nil || !strings.Contains(err.Error(), "existed in its history") {
		t.Fatalf("the base deleted its config: want a clear error, got %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, pathVerdict)); err == nil {
		t.Fatal("no verdict must be written when the base deleted its config")
	}
	// Restoring its last version unchanged is not narrowing: check judges with it.
	git(t, dir, "checkout", "-q", "HEAD~1", "--", ".a2dlc/config.json")
	if v, err := a.Check(context.Background(), "HEAD"); err != nil || v.Verdict != contract.FAIL {
		t.Fatalf("an unchanged restore must be judged with: v=%+v err=%v", v, err)
	}
}

// An equivalence relaxation the engine did not apply makes what it judged INCONCLUSIVE, and says which relaxation.
func TestUnsupportedRelaxationMakesTargetsInconclusive(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2 // UNSUPPORTED\n")
	relax := "compare receipts without trace_id"
	v, err := a.CheckWith(context.Background(), "HEAD", CheckOptions{EquivalenceRelaxations: []string{relax}})
	if err != nil || v.Verdict != contract.INCONCLUSIVE || len(v.Targets) != 1 || v.Targets[0].Verdict != contract.INCONCLUSIVE ||
		!strings.Contains(v.Targets[0].Reason, relax) {
		t.Fatalf("v=%+v err=%v", v, err)
	}
}

// protected_paths: a changed file that matches a glob and has no valid approval makes the verdict INCONCLUSIVE, with a
// protected_path escalation; a signed approval lets it pass.
func TestProtectedPathWithoutApprovalIsInconclusive(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"protected_paths":["tests/human/**"]}`)
	git(t, dir, "commit", "-qam", "protect tests/human")
	write(t, dir, "tests/human/test_calc.py", "def test_add():\n    assert 1 + 1 == 2\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("unsigned protected file: v=%s err=%v", v.Verdict, err)
	}
	var esc contract.Escalations
	if err := a.read("", pathEscalations, &esc); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range esc.Items {
		if e.Kind == "protected_path" && e.Target == "tests/human/test_calc.py::*" && e.Evidence.Message != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("no protected_path escalation: %+v", esc.Items)
	}
	// The working tree drops the glob: the base's config still guards the file.
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	if v, _ = a.Check(context.Background(), "HEAD"); v.Verdict != contract.INCONCLUSIVE {
		t.Fatalf("the glob was dropped from the working tree: v=%s", v.Verdict)
	}
	// A file outside the globs is not affected.
	os.Remove(filepath.Join(dir, "tests/human/test_calc.py"))
	write(t, dir, "README.md", "outside the globs\n")
	if v, _ = a.Check(context.Background(), "HEAD"); v.Verdict != contract.PASS {
		t.Fatalf("a file outside protected_paths: %s", v.Verdict)
	}
}

func TestProtectedPathSignedPasses(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a, _, dir := newRepo(t)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)

	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"protected_paths":["tests/human/**"]}`)
	git(t, dir, "commit", "-qam", "protect tests/human")
	write(t, dir, "tests/human/test_calc.py", "def test_add():\n    assert 1 + 1 == 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"tests/human/test_calc.py::*","decision":"preserve_legacy","reason":"human-owned test"}]}`)
	if err := a.Approve("tests/human/test_calc.py::*", key, "me@a2dlc"); err != nil {
		t.Fatal(err)
	}
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 0 {
		t.Fatalf("signed protected file: v=%+v err=%v", v, err)
	}
	// The working tree drops the glob: the base's config still guards the file, and the signature still binds its blob.
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`)
	if v, err = a.Check(context.Background(), "HEAD"); err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 0 {
		t.Fatalf("the glob was dropped from the working tree: v=%+v err=%v", v, err)
	}
}
