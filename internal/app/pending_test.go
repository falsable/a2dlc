package app

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// threePending changes three functions on purpose: two for one stated reason, one for another, all waived and unsigned.
func threePending(t *testing.T) (App, string) {
	t.Helper()
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/g.ts", "export const g = 1\n")
	write(t, dir, "src/lib/h.ts", "export const h = 1\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "g and h")
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, "src/lib/g.ts", "export const g = 2\n")
	write(t, dir, "src/lib/h.ts", "export const h = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[`+
		`{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"Prices now round up"},`+
		`{"target":"src/lib/g.ts::f","decision":"enforce_new_behavior","reason":"Prices now round up"},`+
		`{"target":"src/lib/h.ts::f","decision":"enforce_new_behavior","reason":"Empty carts return 0"}]}`)
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || len(v.PendingWaivers) != 3 {
		t.Fatalf("setup: pending=%v err=%v", v.PendingWaivers, err)
	}
	return a, dir
}

// The person sees one group per reason the agent gave, the old and the new result of each function, what they are
// asked to accept, and what the signature binds.
func TestPendingGroupsByReasonForThePerson(t *testing.T) {
	a, _ := threePending(t)
	r, err := a.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if r.Count != 3 || len(r.Groups) != 2 || len(r.Groups[0].Items) != 2 || r.Groups[0].Reason != "Prices now round up" {
		t.Fatalf("groups: %+v", r.Groups)
	}
	text := r.Text()
	for _, want := range []string{
		"3 changes wait for your signature, in 2 group(s)",
		`"Prices now round up"`, `"Empty carts return 0"`,
		"Asks you to: keep the new behavior.",
		"src/lib/g.ts::f: with input [] it returned export const g = 1",
		"any later edit to one of them needs a new signature",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the person's text lacks %q:\n%s", want, text)
		}
	}
}

// The agent's side is the same report as compact JSON with stable keys.
func TestPendingJSONIsForAgents(t *testing.T) {
	a, _ := threePending(t)
	r, _ := a.Pending()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	if json.Unmarshal(b, &back) != nil || back["count"].(float64) != 3 || len(back["groups"].([]any)) != 2 {
		t.Fatalf("json: %s", b)
	}
	if strings.Contains(string(b), "\n") {
		t.Fatal("the agent's JSON is not compact")
	}
}

// A receipt about another tree is never signed from: the person would approve files they were not shown.
func TestPendingRefusesAReceiptOfAnotherTree(t *testing.T) {
	a, dir := threePending(t)
	write(t, dir, "src/lib/f.ts", "export const f = 9\n")
	if _, err := a.Pending(); err == nil || !strings.Contains(err.Error(), "another tree") {
		t.Fatalf("err=%v", err)
	}
	empty := PendingReport{}
	if !strings.Contains(empty.Text(), "Nothing waits") {
		t.Fatal(empty.Text())
	}
}

// The batch signs every target with one ssh-keygen call (one passphrase prompt), and what it writes is ordinary
// approvals: the check sees no pending waiver and verify passes, as after three `a2dlc approve` calls.
func TestApprovePendingSignsTheBatchOnce(t *testing.T) {
	real, err := exec.LookPath("ssh-keygen")
	if err != nil {
		t.Skip("no ssh-keygen")
	}
	a, dir := threePending(t)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command(real, "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	// A wrapper that counts the signing calls and hands everything to the real ssh-keygen.
	calls := filepath.Join(keys, "calls")
	wrapper := filepath.Join(keys, "keygen.sh")
	os.WriteFile(wrapper, []byte("#!/bin/sh\n[ \"$2\" = sign ] && echo x >> "+calls+"\nexec "+real+" \"$@\"\n"), 0o755)
	t.Setenv("A2DLC_SSH_KEYGEN", wrapper)

	r, err := a.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ApprovePending(r, key, "me@a2dlc"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(calls); strings.Count(string(b), "x") != 1 {
		t.Fatalf("signing calls: %q, want one for the whole batch", b)
	}
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || len(v.PendingWaivers) != 0 || v.Verdict != contract.PASS {
		t.Fatalf("check after the batch: verdict=%s pending=%v err=%v", v.Verdict, v.PendingWaivers, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "signed in a batch")
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify after the batch: %v %v", probs, err)
	}
}
