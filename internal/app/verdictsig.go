package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

const pathVerdictSig = ".a2dlc/verdict.sig"

// Statuses of VerdictSignature.Status.
const (
	SigNoVerdict    = "no_verdict"   // there is no verdict.json to sign
	SigUnsigned     = "unsigned"     // verdict.json exists and verdict.sig does not
	SigUnverifiable = "unverifiable" // verdict.sig exists but there are no allowed signers to check it against
	SigInvalid      = "invalid"      // verdict.sig does not verify over verdict.json against the allowed signers
	SigStale        = "stale"        // the signature is valid but the verdict was issued over another tree than the current one
	SigValid        = "valid"        // valid, and bound to the tree_hash of the verdict and of the current tree
)

// VerdictSignature is what state inspection reports about .a2dlc/verdict.sig. It is derived, never stored: the verdict
// schema is untouched, so a verdict with no signature stays a valid receipt.
type VerdictSignature struct {
	Status    string `json:"status"`
	Principal string `json:"principal,omitempty"` // whose key signed it, from the allowed signers; only when valid or stale
	TreeHash  string `json:"tree_hash,omitempty"` // the tree the signed verdict was issued over
	Reason    string `json:"reason,omitempty"`
}

// signingKey is the private key that signs verdicts: A2DLC_SIGNING_KEY, the variable `a2dlc approve --key` already
// defaults to. Empty means verdicts are written unsigned.
func signingKey() string { return strings.TrimSpace(os.Getenv("A2DLC_SIGNING_KEY")) }

// writeVerdict writes .a2dlc/verdict.json and, when a signing key is configured, .a2dlc/verdict.sig over the very bytes
// it wrote. A stale verdict.sig is removed first, so the two files never disagree about which verdict they describe.
// Failing to sign never fails the check (an agent can have the variable set and no way to unlock a passphrase); the
// verdict is left unsigned, a line says why, and state inspection reports it as unsigned.
func (a App) writeVerdict(v contract.Verdict) error {
	b, err := encode(v)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(a.path(dirA2dlc), 0o755); err != nil {
		return err
	}
	if err := os.Remove(a.path(pathVerdictSig)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.WriteFile(a.path(pathVerdict), b, 0o644); err != nil {
		return err
	}
	key := signingKey()
	if key == "" {
		return nil
	}
	sig, err := approval.SignVerdict(key, b)
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc: the verdict was left unsigned:", err)
		return nil
	}
	return os.WriteFile(a.path(pathVerdictSig), []byte(sig), 0o644)
}

// VerdictSignature checks .a2dlc/verdict.sig against the allowed signers (outside the repo, as for approvals) and
// against the tree: the signed bytes carry the verdict's tree_hash, and that must be the tree of the working directory.
func (a App) VerdictSignature() (VerdictSignature, error) {
	raw, err := os.ReadFile(a.path(pathVerdict))
	if errors.Is(err, os.ErrNotExist) {
		return VerdictSignature{Status: SigNoVerdict, Reason: "there is no " + pathVerdict}, nil
	}
	if err != nil {
		return VerdictSignature{}, err
	}
	sig, err := os.ReadFile(a.path(pathVerdictSig))
	if errors.Is(err, os.ErrNotExist) {
		return VerdictSignature{Status: SigUnsigned, Reason: "there is no " + pathVerdictSig + " (set A2DLC_SIGNING_KEY before a2dlc check to sign verdicts)"}, nil
	}
	if err != nil {
		return VerdictSignature{}, err
	}
	var v struct {
		TreeHash string `json:"tree_hash"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return VerdictSignature{Status: SigInvalid, Reason: pathVerdict + " is not valid JSON: " + err.Error()}, nil
	}
	out := VerdictSignature{TreeHash: v.TreeHash}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return VerdictSignature{}, err
	}
	if signers == "" {
		out.Status, out.Reason = SigUnverifiable, "no allowed signers configured (A2DLC_ALLOWED_SIGNERS, A2DLC_ALLOWED_SIGNERS_DATA or ~/.config/a2dlc/allowed_signers)"
		return out, nil
	}
	principal, err := approval.VerifyVerdict(signers, raw, strings.ReplaceAll(string(sig), "\r\n", "\n"))
	if err != nil {
		out.Status, out.Reason = SigInvalid, err.Error()
		return out, nil
	}
	out.Principal = principal
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		return VerdictSignature{}, err
	}
	if strings.TrimPrefix(v.TreeHash, treePrefix) != tree {
		out.Status, out.Reason = SigStale, "the signed verdict was issued over another tree than the current one: run a2dlc check"
		return out, nil
	}
	out.Status = SigValid
	return out, nil
}

// Line is the one-line form for the CLI.
func (s VerdictSignature) Line() string {
	if s.Status == SigValid {
		return "verdict signature: valid, signed by " + s.Principal
	}
	return "verdict signature: " + s.Status + ": " + s.Reason
}
