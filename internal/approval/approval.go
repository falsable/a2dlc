// Package approval signs and verifies waiver approvals with ssh-keygen -Y (ADR meta/0004, "signing"). The signing key
// has a passphrase and does not live in ssh-agent: without a TTY, an agent cannot sign.
package approval

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

const Namespace = "a2dlc-approval"

// VerdictNamespace is the ssh signature namespace of .a2dlc/verdict.sig. It is not Namespace on purpose: the same key
// signs approvals and verdicts, and a namespace of its own keeps a signature made for one from verifying as the other.
const VerdictNamespace = "a2dlc-verdict"

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
	return sign(keyPath, Namespace, payload, os.Stdin, os.Stderr)
}

// SignVerdict signs the bytes of verdict.json under VerdictNamespace. Unlike Sign it never reads the process's stdin,
// which under `a2dlc mcp` is the protocol transport: a passphrase key can only be unlocked at a terminal ssh-keygen
// opens itself, and without one the signing fails instead of hanging or eating protocol bytes.
func SignVerdict(keyPath string, payload []byte) (string, error) {
	return sign(keyPath, VerdictNamespace, payload, nil, nil)
}

// SignMany signs every payload under Namespace in one ssh-keygen call, so a key with a passphrase is unlocked once for the
// whole batch (measured with OpenSSH 8.9: one passphrase prompt for N files). The signatures come back in the order of
// the payloads. A batch either signs everything or returns an error and no signature.
func SignMany(keyPath string, payloads [][]byte) ([]string, error) {
	if len(payloads) == 0 {
		return nil, nil
	}
	dir, err := os.MkdirTemp("", "a2dlc-sign-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	files := make([]string, len(payloads))
	for i, p := range payloads {
		files[i] = filepath.Join(dir, fmt.Sprintf("payload-%04d", i))
		if err := os.WriteFile(files[i], p, 0o600); err != nil {
			return nil, err
		}
	}
	c := exec.Command(keygen(), append([]string{"-Y", "sign", "-f", keyPath, "-n", Namespace}, files...)...)
	c.Stdin, c.Stderr = os.Stdin, os.Stderr
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("ssh-keygen -Y sign: %v", err)
	}
	sigs := make([]string, len(files))
	for i, f := range files {
		b, err := os.ReadFile(f + ".sig")
		if err != nil {
			return nil, fmt.Errorf("ssh-keygen -Y sign wrote no signature for payload %d: %v", i, err)
		}
		sigs[i] = strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	return sigs, nil
}

func sign(keyPath, namespace string, payload []byte, stdin io.Reader, stderr io.Writer) (string, error) {
	dir, err := os.MkdirTemp("", "a2dlc-sign-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	f := filepath.Join(dir, "payload")
	if err := os.WriteFile(f, payload, 0o600); err != nil {
		return "", err
	}
	var errb bytes.Buffer
	c := exec.Command(keygen(), "-Y", "sign", "-f", keyPath, "-n", namespace, f)
	if stdin != nil {
		c.Stdin = stdin
	}
	if stderr != nil {
		c.Stderr = stderr
	} else {
		c.Stderr = &errb
	}
	if err := c.Run(); err != nil {
		if msg := strings.TrimSpace(errb.String()); msg != "" {
			return "", fmt.Errorf("ssh-keygen -Y sign: %v: %s", err, msg)
		}
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
	return verify(allowedSigners, principal, Namespace, payload, signature)
}

// VerifyVerdict checks a verdict signature against allowedSigners and returns the principal whose key made it. The
// signature file does not name a principal, so they are the ones allowedSigners lists for the signing key
// (ssh-keygen -Y find-principals); the signature is valid if it verifies for any of them.
func VerifyVerdict(allowedSigners string, payload []byte, signature string) (string, error) {
	if allowedSigners == "" {
		return "", fmt.Errorf("no allowed signers configured")
	}
	dir, err := os.MkdirTemp("", "a2dlc-verify-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	sig := filepath.Join(dir, "sig")
	if err := os.WriteFile(sig, []byte(signature), 0o600); err != nil {
		return "", err
	}
	var out bytes.Buffer
	c := exec.Command(keygen(), "-Y", "find-principals", "-f", allowedSigners, "-n", VerdictNamespace, "-s", sig)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("the signing key is not in the allowed signers for namespace %s: %s", VerdictNamespace, strings.TrimSpace(out.String()))
	}
	var last error
	for _, p := range strings.Fields(out.String()) {
		if last = verify(allowedSigners, p, VerdictNamespace, payload, signature); last == nil {
			return p, nil
		}
	}
	if last == nil {
		last = fmt.Errorf("no principal for the signing key")
	}
	return "", last
}

func verify(allowedSigners, principal, namespace string, payload []byte, signature string) error {
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
	c := exec.Command(keygen(), "-Y", "verify", "-f", allowedSigners, "-I", principal, "-n", namespace, "-s", sig)
	c.Stdin = bytes.NewReader(payload)
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		return fmt.Errorf("invalid signature for %s: %s", principal, strings.TrimSpace(out.String()))
	}
	return nil
}
