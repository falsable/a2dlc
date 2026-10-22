// Package approval signs and verifies waiver approvals with ssh-keygen -Y (ADR meta/0004, "signing"). The signing key
// has a passphrase and does not live in ssh-agent: without a TTY, an agent cannot sign.
package approval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

const Namespace = "a2dlc-approval"

// WaiverSHA is the sha256 of the waiver serialized with the struct's fixed field order.
func WaiverSHA(w contract.Waiver) string {
	b, _ := json.Marshal(w)
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// Payload is the signed text: it binds the approval to the waiver and to the blob of the target file.
func Payload(target, waiverSHA, fileBlob string) []byte {
	return []byte(fmt.Sprintf("a2dlc-approval/1\n%s\n%s\n%s\n", target, waiverSHA, fileBlob))
}

func keygen() string {
	if b := os.Getenv("A2DLC_SSH_KEYGEN"); b != "" {
		return b
	}
	return "ssh-keygen"
}

// Sign signs with the given key. ssh-keygen asks for the passphrase through the terminal: that is why Stdin and
// Stderr are the process's own.
func Sign(keyPath string, payload []byte) (string, error) {
	dir, err := os.MkdirTemp("", "a2dlc-sign-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "payload")
	if err := os.WriteFile(f, payload, 0o600); err != nil {
		return "", err
	}
	c := exec.Command(keygen(), "-Y", "sign", "-f", keyPath, "-n", Namespace, f)
	c.Stdin, c.Stderr = os.Stdin, os.Stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("ssh-keygen -Y sign: %v", err)
	}
	sig, err := os.ReadFile(f + ".sig")
	return strings.ReplaceAll(string(sig), "\r\n", "\n"), err
}

// AllowedSigners returns the path of the allowed-signers file, which lives outside the repo:
// A2DLC_ALLOWED_SIGNERS (path), A2DLC_ALLOWED_SIGNERS_DATA (contents, for CI) or ~/.config/a2dlc/allowed_signers.
// Empty if there is none: then nothing is approved.
func AllowedSigners() (string, func(), error) {
	nothing := func() {}
	if p := os.Getenv("A2DLC_ALLOWED_SIGNERS"); p != "" {
		return p, nothing, nil
	}
	if data := os.Getenv("A2DLC_ALLOWED_SIGNERS_DATA"); data != "" {
		f, err := os.CreateTemp("", "a2dlc-signers-*")
		if err != nil {
			return "", nothing, err
		}
		f.WriteString(strings.TrimSpace(data) + "\n")
		f.Close()
		return f.Name(), func() { os.Remove(f.Name()) }, nil
	}
	if h, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(h, ".config", "a2dlc", "allowed_signers")
		if _, err := os.Stat(p); err == nil {
			return p, nothing, nil
		}
	}
	return "", nothing, nil
}

// Verify checks the signature of principal over payload against allowedSigners.
func Verify(allowedSigners, principal string, payload []byte, signature string) error {
	if allowedSigners == "" {
		return fmt.Errorf("no allowed signers configured")
	}
	dir, err := os.MkdirTemp("", "a2dlc-verify-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	sig := filepath.Join(dir, "sig")
	if err := os.WriteFile(sig, []byte(signature), 0o600); err != nil {
		return err
	}
	var out bytes.Buffer
	c := exec.Command(keygen(), "-Y", "verify", "-f", allowedSigners, "-I", principal, "-n", Namespace, "-s", sig)
	c.Stdin = bytes.NewReader(payload)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("invalid signature for %s: %s", principal, strings.TrimSpace(out.String()))
	}
	return nil
}
