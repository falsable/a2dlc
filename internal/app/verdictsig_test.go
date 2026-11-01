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

// signerSetup makes a key without a passphrase, an allowed-signers file that lists it for tester@a2dlc, and points the
// environment at both, as an operator would. It returns the key path.
func signerSetup(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "tester", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(dir, "allowed")
	os.WriteFile(signers, []byte("tester@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_SIGNING_KEY", key)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	return key
}

// A verdict for the repo's current tree, saved the way a2dlc run saves its selected candidate.
func savedVerdict(t *testing.T, a App) {
	t.Helper()
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	v := contract.Verdict{SchemaVersion: contract.SchemaVersion, Verdict: contract.PASS, TreeHash: treePrefix + tree}
	if err := a.SaveVerdict(v); err != nil {
		t.Fatal(err)
	}
}

// With a key configured, SaveVerdict leaves a verdict.sig over the exact bytes of verdict.json, and state inspection
// reports it valid, signed by the principal the allowed signers list for that key.
func TestSaveVerdictSignsWithTheConfiguredKey(t *testing.T) {
	signerSetup(t)
	a, _, dir := newRepo(t)
	savedVerdict(t, a)
	raw, _ := os.ReadFile(filepath.Join(dir, pathVerdict))
	sig, err := os.ReadFile(filepath.Join(dir, pathVerdictSig))
	if err != nil {
		t.Fatalf("no verdict.sig: %v", err)
	}
	signers, _, _ := approval.AllowedSigners()
	if p, err := approval.VerifyVerdict(signers, raw, string(sig)); err != nil || p != "tester@a2dlc" {
		t.Fatalf("the signature does not verify over the bytes of verdict.json: %q %v", p, err)
	}
	s, err := a.VerdictSignature()
	if err != nil || s.Status != SigValid || s.Principal != "tester@a2dlc" {
		t.Fatalf("state: %+v %v", s, err)
	}
	// It is not an approval: the namespace keeps the two apart.
	if err := approval.Verify(signers, "tester@a2dlc", raw, string(sig)); err == nil {
		t.Fatal("a verdict signature verifies as an approval")
	}
}

// Without a key there is no verdict.sig and no error, a stale one from an earlier signed run is removed, and the state
// says unsigned.
func TestSaveVerdictWithoutKeyWritesNoSignatureAndSaysUnsigned(t *testing.T) {
	signerSetup(t)
	a, _, dir := newRepo(t)
	savedVerdict(t, a)
	if _, err := os.Stat(filepath.Join(dir, pathVerdictSig)); err != nil {
		t.Fatal("setup: the signed run left no verdict.sig")
	}
	t.Setenv("A2DLC_SIGNING_KEY", "")
	savedVerdict(t, a)
	if _, err := os.Stat(filepath.Join(dir, pathVerdictSig)); !os.IsNotExist(err) {
		t.Fatalf("an unsigned verdict sits next to the signature of an earlier one: %v", err)
	}
	s, err := a.VerdictSignature()
	if err != nil || s.Status != SigUnsigned {
		t.Fatalf("state: %+v %v", s, err)
	}
}

// A key that cannot sign leaves the verdict unsigned and does not fail the save.
func TestSaveVerdictWithAnUnusableKeyIsUnsignedNotAnError(t *testing.T) {
	signerSetup(t)
	t.Setenv("A2DLC_SIGNING_KEY", filepath.Join(t.TempDir(), "missing"))
	a, _, dir := newRepo(t)
	savedVerdict(t, a)
	if _, err := os.Stat(filepath.Join(dir, pathVerdictSig)); !os.IsNotExist(err) {
		t.Fatalf("verdict.sig exists: %v", err)
	}
	if s, _ := a.VerdictSignature(); s.Status != SigUnsigned {
		t.Fatalf("state: %+v", s)
	}
}

// Editing verdict.json after it was signed (a FAIL turned into a PASS) breaks the signature, even for one byte.
func TestTamperedVerdictIsRejected(t *testing.T) {
	signerSetup(t)
	a, _, dir := newRepo(t)
	savedVerdict(t, a)
	p := filepath.Join(dir, pathVerdict)
	raw, _ := os.ReadFile(p)
	os.WriteFile(p, []byte(strings.Replace(string(raw), `"PASS"`, `"FAIL"`, 1)), 0o644)
	s, err := a.VerdictSignature()
	if err != nil || s.Status != SigInvalid {
		t.Fatalf("a tampered verdict is %+v %v", s, err)
	}
	// The intact verdict does not pass against allowed signers that list only another key.
	os.WriteFile(p, raw, 0o644)
	other := filepath.Join(t.TempDir(), "other")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "other", "-f", other).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(other + ".pub")
	signers := filepath.Join(t.TempDir(), "allowed")
	os.WriteFile(signers, []byte("other@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	if s, _ := a.VerdictSignature(); s.Status != SigInvalid {
		t.Fatalf("a key outside the allowed signers is %+v", s)
	}
}

// A good signature over a verdict issued for another tree is not a verdict for this one.
func TestSignedVerdictOfAnotherTreeIsStale(t *testing.T) {
	signerSetup(t)
	a, _, dir := newRepo(t)
	savedVerdict(t, a)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	s, err := a.VerdictSignature()
	if err != nil || s.Status != SigStale || s.Principal != "tester@a2dlc" {
		t.Fatalf("state after the tree moved: %+v %v", s, err)
	}
}

// Without allowed signers nothing can be verified: the state says so instead of calling the signature good.
func TestSignatureWithoutAllowedSignersIsUnverifiable(t *testing.T) {
	signerSetup(t)
	a, _, _ := newRepo(t)
	savedVerdict(t, a)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", "")
	t.Setenv("A2DLC_ALLOWED_SIGNERS_DATA", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if s, _ := a.VerdictSignature(); s.Status != SigUnverifiable {
		t.Fatalf("state: %+v", s)
	}
}

// The signature is not part of the tree_hash (gitx.Excluded): a check signs its verdict and the working tree, now with an
// untracked verdict.sig in it, still has the tree the verdict names.
func TestCheckSignsAndTheSignatureDoesNotMoveTheTree(t *testing.T) {
	signerSetup(t)
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.TreeHash == "" {
		t.Fatalf("v=%+v err=%v", v, err)
	}
	if _, err := os.Stat(filepath.Join(dir, pathVerdictSig)); err != nil {
		t.Fatalf("check left no verdict.sig: %v", err)
	}
	if s, err := a.VerdictSignature(); err != nil || s.Status != SigValid {
		t.Fatalf("state: %+v %v", s, err)
	}
}
