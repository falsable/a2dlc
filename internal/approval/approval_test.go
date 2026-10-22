package approval

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestWaiverSHAIsStableAndTextSensitive(t *testing.T) {
	w := contract.Waiver{Target: "a.ts::f", Decision: "enforce_new_behavior", Reason: "x"}
	if WaiverSHA(w) != WaiverSHA(w) {
		t.Fatal("not deterministic")
	}
	w2 := w
	w2.Reason = "y"
	if WaiverSHA(w) == WaiverSHA(w2) {
		t.Fatal("the sha does not change with the reason")
	}
}

// Signs with a test key without a passphrase and verifies: the signature is valid for its payload and not for another blob.
func TestSignAndVerify(t *testing.T) {
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
	p := Payload("a.ts::f", strings.Repeat("a", 64), strings.Repeat("b", 40))
	sig, err := Sign(key, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(signers, "tester@a2dlc", p, sig); err != nil {
		t.Fatalf("the good signature does not verify: %v", err)
	}
	if err := Verify(signers, "tester@a2dlc", Payload("a.ts::f", strings.Repeat("a", 64), strings.Repeat("c", 40)), sig); err == nil {
		t.Fatal("the signature is valid for another blob")
	}
	if err := Verify(signers, "other@a2dlc", p, sig); err == nil {
		t.Fatal("the signature is valid for another principal")
	}
	if err := Verify("", "tester@a2dlc", p, sig); err == nil {
		t.Fatal("no allowed signers must not approve")
	}
}
