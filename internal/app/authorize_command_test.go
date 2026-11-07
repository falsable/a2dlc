package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a person signs with `a2dlc authorize` is what check then honors: the file Authorize writes covers the waiver, and
// signing the same text again leaves one entry.
func TestAuthorizeWritesAFileThatCoversTheWaiver(t *testing.T) {
	e := newAuthEnv(t, "")
	intentFile := filepath.Join(t.TempDir(), "intent.json")
	os.WriteFile(intentFile, []byte(authIntent), 0o644)
	opts := AuthorizeOptions{Key: e.key, Principal: "me@a2dlc", IntentFile: intentFile, Decision: "enforce_new_behavior",
		Expires: authExpiry, Targets: []string{authTarget, authTarget}}
	signed, err := e.a.Authorize(opts)
	if err != nil {
		t.Fatal(err)
	}
	if signed.BaseRef != e.base || signed.IntentSHA256 != e.intent || len(signed.Targets) != 1 {
		t.Fatalf("signed %+v", signed)
	}
	if _, err := e.a.Authorize(opts); err != nil {
		t.Fatal(err)
	}
	listed, err := e.a.authorizations("")
	if err != nil || len(listed) != 1 {
		t.Fatalf("authorizations %+v err %v", listed, err)
	}
	res := e.check(t)
	if len(res.PendingWaivers) != 0 || coveredCount(res) != 1 {
		t.Fatalf("pending %v covered %d", res.PendingWaivers, coveredCount(res))
	}
}

// Authorize refuses what an authorization cannot carry before it signs anything.
func TestAuthorizeRefusesWhatItCannotCarry(t *testing.T) {
	e := newAuthEnv(t, "")
	intentFile := filepath.Join(t.TempDir(), "intent.json")
	os.WriteFile(intentFile, []byte(authIntent), 0o644)
	good := AuthorizeOptions{Key: e.key, Principal: "me@a2dlc", IntentFile: intentFile, Decision: "enforce_new_behavior", Expires: authExpiry, Targets: []string{authTarget}}
	cases := map[string]func(*AuthorizeOptions){
		"a glob":                func(o *AuthorizeOptions) { o.Targets = []string{"src/lib/f.ts::*"} },
		"a whole file":          func(o *AuthorizeOptions) { o.Targets = []string{"src/lib/f.ts"} },
		"a test decision":       func(o *AuthorizeOptions) { o.Decision = "characterization_test" },
		"accept_finding":        func(o *AuthorizeOptions) { o.Decision = "accept_finding" },
		"an expiry in the past": func(o *AuthorizeOptions) { o.Expires = "2026-11-01T00:00:00Z" },
		"no expiry":             func(o *AuthorizeOptions) { o.Expires = "" },
		"no intent":             func(o *AuthorizeOptions) { o.IntentFile = "" },
		"no target":             func(o *AuthorizeOptions) { o.Targets = nil },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			o := good
			change(&o)
			if _, err := e.a.Authorize(o); err == nil {
				t.Fatal("signed")
			}
			if _, err := os.Stat(filepath.Join(e.dir, ".a2dlc", "authorizations.json")); err == nil {
				t.Fatal("left a file that looks like a decision")
			}
		})
	}
	// a key the allowed signers do not list: signs, does not pass, writes nothing
	t.Setenv("A2DLC_ALLOWED_SIGNERS", filepath.Join(t.TempDir(), "empty"))
	if _, err := e.a.Authorize(good); err == nil || !strings.Contains(err.Error(), "allowed signers") {
		t.Fatalf("err %v", err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, ".a2dlc", "authorizations.json")); err == nil {
		t.Fatal("left a file signed by an untrusted key")
	}
}
