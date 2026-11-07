package app

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

func coveredCount(res contract.Verdict) int {
	if res.Authorization == nil {
		return 0
	}
	return len(res.Authorization.Covered)
}

// Everything the RFC makes an authorization depend on, and what it never covers: each change leaves the target pending,
// as without the file.
func TestAuthorizationDoesNotCover(t *testing.T) {
	cases := map[string]struct {
		extra  string // added to the base config
		change func(au *contract.Authorization)
	}{
		"another target":       {change: func(au *contract.Authorization) { au.Targets = []string{"src/lib/f.ts::g"} }},
		"a glob":               {change: func(au *contract.Authorization) { au.Targets = []string{"src/lib/f.ts::*"} }},
		"another base":         {change: func(au *contract.Authorization) { au.BaseRef = strings.Repeat("a", 40) }},
		"another intent":       {change: func(au *contract.Authorization) { au.IntentSHA256 = strings.Repeat("c", 64) }},
		"another decision":     {change: func(au *contract.Authorization) { au.Decision = "preserve_legacy" }},
		"an accept_finding":    {change: func(au *contract.Authorization) { au.Decision = "accept_finding" }},
		"expired at the check": {change: func(au *contract.Authorization) { au.ExpiresAt = "2026-11-07T00:00:00Z" }},
		"critical code":        {extra: `,"critical_code":["src/lib/**"]`, change: func(au *contract.Authorization) {}},
		"a protected path":     {extra: `,"protected_paths":["src/lib/**"]`, change: func(au *contract.Authorization) {}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newAuthEnv(t, c.extra)
			au := e.good()
			c.change(&au)
			e.authorize(t, e.key, au, nil)
			res := e.check(t)
			if len(res.PendingWaivers) != 1 {
				t.Fatalf("pending %v, want the target still waiting for an approval (verdict %s)", res.PendingWaivers, res.Verdict)
			}
			if coveredCount(res) != 0 {
				t.Fatalf("covered %+v", res.Authorization)
			}
			if got := escalatedKinds(res); len(got) == 0 || !strings.HasPrefix(got[0], "signature_pending") {
				t.Fatalf("escalations %v, want signature_pending", got)
			}
		})
	}
}

// A signature that is wrong is refused, and the refusal shows up where a person reads it: the signature_pending
// escalation of the target. Never treated as authorized, never silent.
func TestAuthorizationWithABadSignatureIsRefusedWithTheReason(t *testing.T) {
	e := newAuthEnv(t, "")
	stranger := filepath.Join(t.TempDir(), "stranger")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "me", "-f", stranger).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	approvalNS := func(au *contract.Authorization) {
		text := approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets)
		sig, err := approval.Sign(e.key, text)
		if err != nil {
			t.Fatal(err)
		}
		au.Signature = sig
	}
	for name, c := range map[string]struct {
		key    string
		mutate func(*contract.Authorization)
	}{
		"a key the allowed signers do not list": {key: stranger},
		"another namespace":                     {key: e.key, mutate: approvalNS},
		"a text edited after signing":           {key: e.key, mutate: func(au *contract.Authorization) { au.ExpiresAt = "2027-01-01T00:00:00Z" }},
	} {
		t.Run(name, func(t *testing.T) {
			e.authorize(t, c.key, e.good(), c.mutate)
			res := e.check(t)
			if len(res.PendingWaivers) != 1 || coveredCount(res) != 0 {
				t.Fatalf("pending %v authorization %+v", res.PendingWaivers, res.Authorization)
			}
			acct := res.Authorization
			if acct == nil || len(acct.Refused) != 1 || !strings.Contains(acct.Refused[0].Reason, "authorization refused") {
				t.Fatalf("the receipt does not say why: %+v", acct)
			}
			esc := Escalations(res)
			if len(esc) == 0 || esc[0].Kind != "signature_pending" || !strings.Contains(esc[0].Evidence.Message, "authorization refused") {
				t.Fatalf("the escalation does not carry the refusal: %+v", esc)
			}
		})
	}
}

// A malformed authorizations.json is not an authorization: the waived target stays pending and the error is in the receipt.
func TestUnreadableAuthorizationsCoverNothing(t *testing.T) {
	e := newAuthEnv(t, "")
	write(t, e.dir, ".a2dlc/authorizations.json", `{"schema_version":1,"authorizations":[{"targets":["`+authTarget+`"]}]}`)
	res := e.check(t)
	acct := res.Authorization
	if len(res.PendingWaivers) != 1 || acct == nil || len(acct.Refused) != 1 || !strings.Contains(acct.Refused[0].Reason, "is not valid") {
		t.Fatalf("pending %v authorization %+v", res.PendingWaivers, acct)
	}
}

// The intent an authorization names may also be committed under .a2dlc/intents/ (a2dlc check has no run to bind it).
func TestAuthorizationNamesACommittedIntent(t *testing.T) {
	e := newAuthEnv(t, "")
	write(t, e.dir, ".a2dlc/intents/slice.json", authIntent)
	e.authorize(t, e.key, e.good(), nil)
	res, err := e.a.CheckWith(context.Background(), "HEAD", CheckOptions{})
	if err != nil || len(res.PendingWaivers) != 0 || coveredCount(res) != 1 {
		t.Fatalf("pending %v authorization %+v err %v", res.PendingWaivers, res.Authorization, err)
	}
	// without the intent committed or bound, the same authorization covers nothing
	os.Remove(filepath.Join(e.dir, ".a2dlc", "intents", "slice.json"))
	res, err = e.a.CheckWith(context.Background(), "HEAD", CheckOptions{})
	if err != nil || len(res.PendingWaivers) != 1 {
		t.Fatalf("pending %v err %v", res.PendingWaivers, err)
	}
}

// verify decides again: an authorization that was valid when the receipt was made but whose signer is no longer allowed
// no longer lets the push through.
func TestVerifyRefusesAReceiptThatReliedOnAnInvalidAuthorization(t *testing.T) {
	e := newAuthEnv(t, "")
	e.authorize(t, e.key, e.good(), nil)
	e.check(t)
	git(t, e.dir, "add", "-A")
	git(t, e.dir, "commit", "-qm", "change")
	if probs, err := e.a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify before: %v %v", probs, err)
	}
	empty := filepath.Join(t.TempDir(), "allowed")
	os.WriteFile(empty, []byte(""), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", empty)
	probs, err := e.a.Verify("HEAD")
	if err != nil || len(probs) != 1 || !strings.Contains(probs[0], "authorization") {
		t.Fatalf("verify with the signer gone: %v %v", probs, err)
	}
	rep, err := e.a.VerifyPolicy("HEAD")
	if err != nil || len(rep.Blocked) != 1 {
		t.Fatalf("verify policy with the signer gone: %+v %v", rep, err)
	}
}
