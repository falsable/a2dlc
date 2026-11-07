package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

const (
	authTarget = "src/lib/f.ts::f"
	authIntent = "the intent of the slice, as the run was bound to it\n"
	authExpiry = "2026-11-12T00:00:00Z"
	engineCfg  = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]%s}`
)

// authEnv is a repo whose code is not critical, a key the allowed signers list, a waiver for authTarget that enforces new
// behavior and a changed file: the situation in which, without an authorization, the target waits for an approval.
type authEnv struct {
	a      App
	dir    string
	key    string
	base   string
	intent string // bare sha256 of authIntent
}

func cfgWith(extra string) string { return strings.Replace(engineCfg, "%s", extra, 1) }

func newAuthEnv(t *testing.T, extraConfig string) authEnv {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", cfgWith(extraConfig))
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "config of the test", "--allow-empty")
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "me", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	a.Now = func() time.Time { return time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC) }
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"`+authTarget+`","decision":"enforce_new_behavior","reason":"on purpose"}]}`)
	return authEnv{a: a, dir: dir, key: key, base: git(t, dir, "rev-parse", "HEAD"), intent: IntentHash([]byte(authIntent))}
}

// authorize writes .a2dlc/authorizations.json with one authorization signed by key; mutate may change it after signing.
func (e authEnv) authorize(t *testing.T, key string, au contract.Authorization, mutate func(*contract.Authorization)) {
	t.Helper()
	text := approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets)
	sig, err := approval.SignAuthorization(key, text)
	if err != nil {
		t.Fatal(err)
	}
	au.Principal = "me@a2dlc"
	au.Signature = sig
	if mutate != nil {
		mutate(&au)
	}
	b, err := encode(contract.Authorizations{SchemaVersion: 1, Authorizations: []contract.Authorization{au}})
	if err != nil {
		t.Fatal(err)
	}
	write(t, e.dir, ".a2dlc/authorizations.json", string(b))
}

func (e authEnv) good() contract.Authorization {
	return contract.Authorization{BaseRef: e.base, IntentSHA256: e.intent, Targets: []string{authTarget}, Decision: "enforce_new_behavior", ExpiresAt: authExpiry}
}

func (e authEnv) check(t *testing.T) contract.Verdict {
	t.Helper()
	res, err := e.a.CheckWith(context.Background(), "HEAD", CheckOptions{IntentSHA256: e.intent})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func escalatedKinds(res contract.Verdict) []string {
	var out []string
	for _, x := range Escalations(res) {
		out = append(out, x.Kind+" "+x.Target)
	}
	return out
}

// A waived target that a valid, unexpired authorization names stops being pending: the run has no signature_pending to
// stop for, the receipt says it was authorized, and verify accepts the committed result.
func TestAuthorizationCoversAWaivedTarget(t *testing.T) {
	e := newAuthEnv(t, "")
	e.authorize(t, e.key, e.good(), nil)
	res := e.check(t)
	if res.Verdict != contract.PASS || len(res.PendingWaivers) != 0 {
		t.Fatalf("verdict %s pending %v", res.Verdict, res.PendingWaivers)
	}
	if got := escalatedKinds(res); len(got) != 0 {
		t.Fatalf("escalations %v, want none", got)
	}
	acct := res.Authorization
	if acct == nil || len(acct.Covered) != 1 || acct.Covered[0].Target != authTarget || acct.IntentSHA256 != e.intent {
		t.Fatalf("the receipt does not say which authorization covered the target: %+v", acct)
	}
	git(t, e.dir, "add", "-A")
	git(t, e.dir, "commit", "-qm", "change")
	if probs, err := e.a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify: %v %v", probs, err)
	}
	rep, err := e.a.VerifyPolicy("HEAD")
	if err != nil || len(rep.Blocked) != 0 {
		t.Fatalf("verify policy: %+v %v", rep, err)
	}
}
