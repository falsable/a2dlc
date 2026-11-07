package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// The plan's acceptance (RFC-0008 section 4): `plan run` runs a plan only if a person signed it or an authorization covers
// plan.accept, and says which in the state; verify-run checks it again. Everything fails closed: an unreadable file, a date
// that does not parse, a signer that is not allowed or a signature that does not verify leaves the plan unaccepted.

const (
	approvalFile       = "approval.json"
	pathAuthorizations = ".a2dlc/authorizations.json"
)

// AcceptFunc decides whether plan p, whose file hashes to planSHA (bare hex), may run on baseSha for the feature intent
// whose file hashes to intentSHA (bare hex).
type AcceptFunc func(p contract.Plan, planSHA, baseSha, intentSHA string) (contract.PlanAcceptance, error)

// PlanAcceptTarget is the target a plan.accept authorization names.
func PlanAcceptTarget(feature string) string { return "plan::" + feature }

// DefaultAccept is the acceptance `plan run` applies: the signed approval in the feature's state folder, else an
// authorization of .a2dlc/authorizations.json that covers plan.accept for this feature, intent and base. now is the time
// both are judged at.
func DefaultAccept(repoDir string, now func() time.Time) AcceptFunc {
	return func(p contract.Plan, planSHA, baseSha, intentSHA string) (contract.PlanAcceptance, error) {
		at := now().UTC()
		signers, cleanup, err := approval.AllowedSigners()
		defer cleanup()
		if err != nil || signers == "" {
			return contract.PlanAcceptance{}, refused([]string{"no allowed signers are configured, so no signature can be checked"})
		}
		var why []string
		if b, err := os.ReadFile(filepath.Join(StateDir(repoDir, p.FeatureID), approvalFile)); err == nil {
			var ap contract.PlanApproval
			if err := contract.DecodeStrictBytes(b, &ap); err != nil {
				why = append(why, approvalFile+" does not match its contract: "+err.Error())
			} else if err := checkApproval(ap, signers, planSHA, baseSha, at); err != nil {
				why = append(why, approvalFile+": "+err.Error())
			} else {
				return contract.PlanAcceptance{Kind: contract.AcceptedByApproval, At: at.Format(time.RFC3339), Approval: &ap}, nil
			}
		}
		auths, err := readAuthorizations(repoDir)
		if err != nil {
			why = append(why, err.Error())
		}
		for _, au := range auths {
			if au.Decision != contract.PlanAccept || !contains(au.Targets, PlanAcceptTarget(p.FeatureID)) {
				continue
			}
			if err := checkAuthorization(au, signers, p.FeatureID, baseSha, intentSHA, at); err != nil {
				why = append(why, "an authorization for "+PlanAcceptTarget(p.FeatureID)+": "+err.Error())
				continue
			}
			return contract.PlanAcceptance{Kind: contract.AcceptedByAuthorization, At: at.Format(time.RFC3339), Authorization: &au}, nil
		}
		return contract.PlanAcceptance{}, refused(why)
	}
}

func refused(why []string) error {
	msg := "the plan is not accepted: a person must sign it (a2dlc plan approve) or an authorization must cover plan.accept (a2dlc authorize --decision plan.accept)"
	if len(why) > 0 {
		msg += "\n  - " + strings.Join(why, "\n  - ")
	}
	return fmt.Errorf("%s", msg)
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func readAuthorizations(repoDir string) ([]contract.Authorization, error) {
	b, err := os.ReadFile(filepath.Join(repoDir, filepath.FromSlash(pathAuthorizations)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var doc contract.Authorizations
	if err := contract.DecodeStrictBytes(b, &doc); err != nil {
		return nil, fmt.Errorf("%s does not match its contract: %w", pathAuthorizations, err)
	}
	return doc.Authorizations, nil
}

// notExpired is nil when at is not after the expiry; an expiry that is not a date is an error.
func notExpired(expiresAt string, at time.Time) error {
	exp, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return fmt.Errorf("expires_at %q is not a date", expiresAt)
	}
	if at.After(exp) {
		return fmt.Errorf("it expired at %s", expiresAt)
	}
	return nil
}

func checkApproval(ap contract.PlanApproval, signers, planSHA, baseSha string, at time.Time) error {
	if ap.PlanSHA256 != planSHA {
		return fmt.Errorf("it approves another plan (sha256 %.12s, this one is %.12s): a plan edited after its approval needs a new one", ap.PlanSHA256, planSHA)
	}
	if ap.BaseRef != baseSha {
		return fmt.Errorf("it approves a run on base %.12s, not %.12s", ap.BaseRef, baseSha)
	}
	if err := notExpired(ap.ExpiresAt, at); err != nil {
		return err
	}
	return approval.VerifyPlan(signers, ap.Principal, approval.PlanPayload(ap.PlanSHA256, ap.BaseRef, ap.ExpiresAt), ap.Signature)
}

// PlanSHA is the bare sha256 hex of a plan file: what an approval signs and what the state records.
func PlanSHA(planBytes []byte) string {
	s := sha256.Sum256(planBytes)
	return hex.EncodeToString(s[:])
}

// checkAuthorization: intentSHA "" skips the intent comparison (verify-run has no feature intent, only the state).
func checkAuthorization(au contract.Authorization, signers, feature, baseSha, intentSHA string, at time.Time) error {
	switch {
	case au.Decision != contract.PlanAccept || !contains(au.Targets, PlanAcceptTarget(feature)):
		return fmt.Errorf("it does not carry plan.accept for %s", PlanAcceptTarget(feature))
	case au.BaseRef != baseSha:
		return fmt.Errorf("it is for base %.12s, not %.12s", au.BaseRef, baseSha)
	case intentSHA != "" && au.IntentSHA256 != intentSHA:
		return fmt.Errorf("it is for another feature intent")
	}
	if err := notExpired(au.ExpiresAt, at); err != nil {
		return err
	}
	return approval.VerifyAuthorization(signers, au.Principal, approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets), au.Signature)
}

// VerifyAcceptance checks again what a plan state says accepted it, against the allowed signers and the state's own plan
// hash, base and feature: the approval or authorization must exist, be for this plan, verify, and not have expired when it
// was used (Acceptance.At). The state is a file in the folder, so this rejects a run that was never accepted or whose
// acceptance was altered, not one whose whole folder was rewritten.
func VerifyAcceptance(st contract.PlanState) []string {
	acc := st.Acceptance
	if acc == nil {
		return []string{"the plan state records no acceptance: no signed approval and no authorization covered plan.accept"}
	}
	at, err := time.Parse(time.RFC3339, acc.At)
	if err != nil {
		return []string{"acceptance: the time of use is not a date"}
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil || signers == "" {
		return []string{"acceptance: no allowed signers are configured, so it cannot be checked"}
	}
	switch {
	case acc.Kind == contract.AcceptedByApproval && acc.Approval != nil && acc.Authorization == nil:
		if err := checkApproval(*acc.Approval, signers, st.PlanSHA256, st.BaseSha, at); err != nil {
			return []string{"acceptance: approval: " + err.Error()}
		}
	case acc.Kind == contract.AcceptedByAuthorization && acc.Authorization != nil && acc.Approval == nil:
		if err := checkAuthorization(*acc.Authorization, signers, st.FeatureID, st.BaseSha, "", at); err != nil {
			return []string{"acceptance: authorization: " + err.Error()}
		}
	default:
		return []string{"acceptance: kind " + acc.Kind + " does not match what the record holds"}
	}
	return nil
}

// ApproveOptions is what a person gives to sign a plan.
type ApproveOptions struct {
	Key, Principal string
	Base           string // "" is HEAD
	Expires        string // RFC 3339, in the future
}

// Approve signs the plan (its bytes, as `plan run` will read them) with the person's key and writes it to the feature's
// state folder as approval.json. The signature is checked against the allowed signers before anything is written, so a
// key nobody trusts leaves no file that looks like a decision. The passphrase is asked by ssh-keygen and never reaches here.
func Approve(repo gitx.Repo, planBytes []byte, o ApproveOptions, now time.Time) (contract.PlanApproval, error) {
	var ap contract.PlanApproval
	var p contract.Plan
	if err := contract.DecodeStrictBytes(planBytes, &p); err != nil {
		return ap, fmt.Errorf("the plan does not match its contract: %w", err)
	}
	exp, err := time.Parse(time.RFC3339, o.Expires)
	if err != nil {
		return ap, fmt.Errorf("--expires %q: RFC 3339 date-time, e.g. 2026-11-12T00:00:00Z", o.Expires)
	}
	if !exp.After(now) {
		return ap, fmt.Errorf("--expires %s is already past", o.Expires)
	}
	base := o.Base
	if base == "" {
		base = "HEAD"
	}
	baseSha, err := repo.ResolveCommit(base)
	if err != nil {
		return ap, fmt.Errorf("--base %q: %w", base, err)
	}
	ap = contract.PlanApproval{PlanSHA256: PlanSHA(planBytes), BaseRef: baseSha, ExpiresAt: exp.UTC().Format(time.RFC3339), Principal: o.Principal}
	text := approval.PlanPayload(ap.PlanSHA256, ap.BaseRef, ap.ExpiresAt)
	if ap.Signature, err = approval.SignPlan(o.Key, text); err != nil {
		return ap, err
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return ap, err
	}
	if err := approval.VerifyPlan(signers, ap.Principal, text, ap.Signature); err != nil {
		return ap, fmt.Errorf("signed, but the signature does not pass against the allowed signers (is your key missing from allowed_signers?): %w", err)
	}
	dir := StateDir(repo.Dir, p.FeatureID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ap, err
	}
	b, err := json.MarshalIndent(ap, "", " ")
	if err != nil {
		return ap, err
	}
	return ap, os.WriteFile(filepath.Join(dir, approvalFile), append(b, '\n'), 0o644)
}

// VerifyRun re-reads a feature's plan state and checks that the run was accepted (VerifyAcceptance). It returns the
// problems; empty means ok.
func VerifyRun(repoDir, feature string) ([]string, error) {
	st, err := LoadState(repoDir, feature)
	if err != nil {
		return nil, err
	}
	return VerifyAcceptance(st), nil
}
