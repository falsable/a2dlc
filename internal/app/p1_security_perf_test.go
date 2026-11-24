package app

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

func TestVerifyEnforcesVerdictSignatureWhenConfigured(t *testing.T) {
	key := signerSetup(t)
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"ok"}]}`)
	if err := a.Approve("src/lib/f.ts::f", key, "tester@a2dlc"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "signed pass")

	// Valid signature passes Verify.
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("expected clean verify with valid verdict.sig, got probs=%v err=%v", probs, err)
	}

	// Tampering verdict.json fails Verify.
	vp := filepath.Join(dir, pathVerdict)
	orig, _ := os.ReadFile(vp)
	_ = os.WriteFile(vp, append(bytes.TrimSpace(orig), ' '), 0o644)
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) == 0 || !strings.Contains(strings.Join(probs, ";"), "invalid") {
		t.Fatalf("expected tampered verdict failure, got probs=%v err=%v", probs, err)
	}
	_ = os.WriteFile(vp, orig, 0o644)

	// Deleting verdict.sig while A2DLC_SIGNING_KEY is set fails closed (not silent unsigned PASS).
	_ = os.Remove(filepath.Join(dir, pathVerdictSig))
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) == 0 || !strings.Contains(strings.Join(probs, ";"), "unsigned") {
		t.Fatalf("expected unsigned failure when A2DLC_SIGNING_KEY is set, got probs=%v err=%v", probs, err)
	}
}

func TestHookPreToolUseBlocksBypassesAndArtifactWrites(t *testing.T) {
	a, _, _ := newRepo(t)
	var out, errb bytes.Buffer

	// Direct Edit on .a2dlc/verdict.json or .git/hooks/pre-push is blocked.
	for _, target := range []string{".a2dlc/verdict.json", ".a2dlc/verdict.sig", ".git/hooks/pre-push"} {
		errb.Reset()
		in, _ := json.Marshal(map[string]any{"session_id": "s1", "tool_name": "Edit", "tool_input": map[string]string{"file_path": target}})
		if code := a.Hook(context.Background(), "claude", bytes.NewReader(in), &out, &errb); code != 2 {
			t.Fatalf("Edit %s: expected block code 2, got %d (%s)", target, code, errb.String())
		}
	}

	cases := map[string]int{
		`git push -n origin main`:                          2,
		`git push -qn origin main`:                         2,
		`bash -c "git push --no-verify origin"`:            2,
		`sh -c 'git push -n origin'`:                       2,
		`git -c core.hooksPath=/dev/null push origin main`: 2,
		`echo '{}' > .a2dlc/verdict.json`:                  2,
		`cat forged | tee .a2dlc/verdict.json`:             2,
		`rm -f .a2dlc/verdict.sig`:                         2,
		`printf '#!/bin/sh\n' > .git/hooks/pre-push`:       2,
		`cat .a2dlc/verdict.json`:                          0,
		`git commit -n -m "wip"`:                           0,
	}
	for cmd, want := range cases {
		errb.Reset()
		in, _ := json.Marshal(map[string]any{"session_id": "s1", "tool_name": "Bash", "tool_input": map[string]string{"command": cmd}})
		if code := a.Hook(context.Background(), "claude", bytes.NewReader(in), &out, &errb); code != want {
			t.Fatalf("%q: got code %d, want %d (stderr=%s)", cmd, code, want, errb.String())
		}
	}
}

func TestTier0WithExportsAtMostOnceAndAttachesWarmImages(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{
		"schema_version":1,
		"engines":[{"language":"typescript","image":"eng-ts:1","paths":["src/lib/**"],"budget":{}}],
		"checks":[{"id":"lint","image":"chk-lint:1","command":"lint","severity":"blocking","fire_on":["stage:3"],"matches":["src/**"]}],
		"tests":{"image":"tests:1","paths":["tests/**"],"command":"all"}
	}`)
	write(t, dir, "tests/f.t", "t1 uses:added\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "cfg")

	write(t, dir, "src/lib/f.ts", "export const f = 2\nIMPL added\n")
	cand := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\nIMPL added\n"})

	chk := &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", true)}}
	a.Checks = chk
	ft := &fakeTests{}
	a.Tests = ft

	// Verify Tier0With shares a single export across engine, coverage, and checks, and does not error.
	r, _, err := a.Tier0With(context.Background(), "HEAD", cand, nil, Tier0Extra{Tests: true})
	if err != nil {
		t.Fatalf("Tier0With failed: %v", err)
	}
	if !r.Reviewed || len(chk.files["lint"]) != 1 {
		t.Fatalf("unexpected Tier0With result: %+v checks=%v", r, chk.files)
	}

	// Verify attachWarmImages populates WarmFromContext for engine/check images when A2DLC_WARM_TREE=1.
	t.Setenv(warmEnv, "1")
	wf := &warmFake{fakeTests: ft, vol: t.TempDir()}
	baseSha, _ := a.Repo.ResolveCommit("HEAD")
	candTree, _ := a.Repo.CommitTree(cand)
	wctx, clean := a.attachWarmImages(context.Background(), wf, []string{"eng-ts:1", "chk-lint:1"}, baseSha, candTree)
	defer clean()
	for _, img := range []string{"eng-ts:1", "chk-lint:1"} {
		wt, ok := engine.WarmFromContext(wctx, img)
		if !ok || wt.Volume == "" || wt.CandDelta == "" {
			t.Fatalf("expected warm context for %s, got ok=%v wt=%+v", img, ok, wt)
		}
		if _, err := os.Stat(filepath.Join(wt.CandDelta, "src/lib/f.ts")); err != nil {
			t.Fatalf("cand delta missing changed file for %s: %v", img, err)
		}
	}
}

func TestVerifyPrePushIgnoresSignerAndKeygenEnvOverrides(t *testing.T) {
	a, _, dir := newRepo(t)
	_ = signerSetup(t) // sets A2DLC_SIGNING_KEY & A2DLC_ALLOWED_SIGNERS for tester@a2dlc
	realSignersPath := os.Getenv("A2DLC_ALLOWED_SIGNERS")
	realSignersBytes, err := os.ReadFile(realSignersPath)
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1}`)
	write(t, dir, "src/lib/f.ts", "export const f = 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "base")

	// Agent mints its own key and writes verdict.sig with it, setting A2DLC_ALLOWED_SIGNERS_DATA & A2DLC_SSH_KEYGEN.
	agentDir := t.TempDir()
	agentKey := filepath.Join(agentDir, "agent")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "agent", "-f", agentKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v %s", err, out)
	}
	agentPub, _ := os.ReadFile(agentKey + ".pub")
	agentSignersData := "agent@evil.test " + strings.TrimSpace(string(agentPub)) + "\n"

	// Human's real allowed_signers lives at ~/.config/a2dlc/allowed_signers (only trusts tester@a2dlc).
	fakeHome := t.TempDir()
	cfgDir := filepath.Join(fakeHome, ".config", "a2dlc")
	if err := os.MkdirAll(cfgDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "allowed_signers"), realSignersBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", fakeHome)

	t.Setenv("A2DLC_SIGNING_KEY", agentKey)
	savedVerdict(t, a)
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "commit with agent verdict")

	t.Setenv("A2DLC_ALLOWED_SIGNERS", "")
	t.Setenv("A2DLC_ALLOWED_SIGNERS_DATA", agentSignersData)
	fakeKeygen := filepath.Join(t.TempDir(), "fake-ssh-keygen.sh")
	if err := os.WriteFile(fakeKeygen, []byte("#!/bin/sh\necho agent@evil.test\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2DLC_SSH_KEYGEN", fakeKeygen)

	// Normal mode (CI / non-hook): respects A2DLC_ALLOWED_SIGNERS_DATA + A2DLC_SSH_KEYGEN.
	aNormal := a
	if probs, err := aNormal.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("normal Verify unexpected failure: probs=%v err=%v", probs, err)
	}

	// Pre-push mode (IgnoreSignerEnv = true): ignores env overrides, checks ~/.config/a2dlc/allowed_signers with real ssh-keygen -> fails!
	aHook := a
	aHook.IgnoreSignerEnv = true
	if probs, err := aHook.Verify("HEAD"); err != nil || len(probs) == 0 {
		t.Fatalf("expected --pre-push Verify to ignore signer/keygen env overrides and reject agent signature, got probs=%v err=%v", probs, err)
	}
}
