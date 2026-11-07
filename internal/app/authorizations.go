package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Pre-authorized behavior changes (RFC-0007 Part B). An authorization is a person's signature, made before the code exists,
// that stands in for the approval of a waived engine target. It is evaluated in one place (authorize) that check, verify
// and the push policy all call, so they cannot disagree. Everything here fails closed: a file that cannot be read, a date
// that cannot be parsed or a signature that does not verify means the target is not covered and still waits for an approval.

const (
	pathAuthorizations = ".a2dlc/authorizations.json"
	// pathIntents is where an intent committed in the judged tree can be named by an authorization (`a2dlc check`).
	pathIntents = ".a2dlc/intents/"
)

// authInputs is what an authorization is matched against.
type authInputs struct {
	baseRef string          // the receipt's base_ref
	at      time.Time       // the receipt's created_at: an authorization must not have expired by then
	intents map[string]bool // bare sha256 of the intents the receipt may be bound to
	cfg     contract.Config // the base's config, which says what is critical or protected
}

// authResult says which waived targets an authorization covered and why it refused the ones it matched but could not cover.
type authResult struct {
	covered map[string]contract.AuthorizedTarget
	refused map[string]string
}

// IntentHash is the bare sha256 hex of intent bytes, the form an authorization's intent_sha256 takes.
func IntentHash(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// authorizations reads .a2dlc/authorizations.json at commit ("" is the working tree). A missing file is no authorizations.
func (a App) authorizations(commit string) ([]contract.Authorization, error) {
	var doc contract.Authorizations
	if err := a.read(commit, pathAuthorizations, &doc); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return doc.Authorizations, nil
}

// committedIntents are the intents in the tree under .a2dlc/intents/, by bare sha256.
func (a App) committedIntents(tree string) map[string]bool {
	out := map[string]bool{}
	files, err := a.Repo.Files(tree)
	if err != nil {
		return out
	}
	for _, f := range files {
		if !strings.HasPrefix(f, pathIntents) {
			continue
		}
		if b, err := a.Repo.Show(tree, f); err == nil {
			out[IntentHash(b)] = true
		}
	}
	return out
}

// authorize decides which of cands, the waived engine targets of a receipt, an authorization covers. cands must hold
// only targets of engine results: tests, check findings and protected paths never come here, which is how "what it never
// covers" is kept without a list of exceptions to forget.
func (a App) authorize(commit string, waivers map[string]contract.Waiver, cands []string, in authInputs) authResult {
	res := authResult{covered: map[string]contract.AuthorizedTarget{}, refused: map[string]string{}}
	if len(cands) == 0 {
		return res
	}
	auths, err := a.authorizations(commit)
	if err != nil {
		for _, t := range cands {
			res.refused[t] = pathAuthorizations + " is not valid: " + err.Error()
		}
		return res
	}
	if len(auths) == 0 {
		return res
	}
	signers, cleanup, serr := approval.AllowedSigners()
	defer cleanup()
	for _, au := range auths {
		named := map[string]bool{}
		for _, t := range au.Targets {
			named[t] = true
		}
		for _, t := range cands {
			if !named[t] || au.BaseRef != in.baseRef || !in.intents[au.IntentSHA256] {
				continue
			}
			w, ok := waivers[t]
			if !ok || w.Decision != au.Decision || (au.Decision != contract.EnforceNewBehavior && au.Decision != contract.PreserveLegacy) {
				continue
			}
			if _, done := res.covered[t]; done {
				continue
			}
			if verdict.IsCritical(in.cfg.CriticalCode, t) || guarded(in.cfg, contract.TargetFile(t)) {
				res.refused[t] = "an authorization never covers critical or protected code: it needs an approval"
				continue
			}
			exp, err := time.Parse(time.RFC3339, au.ExpiresAt)
			switch {
			case err != nil:
				res.refused[t] = "the authorization's expires_at is not a date: " + err.Error()
				continue
			case in.at.IsZero():
				res.refused[t] = "the receipt has no readable created_at to measure the authorization's expiry against"
				continue
			case in.at.After(exp):
				res.refused[t] = "the authorization expired at " + au.ExpiresAt
				continue
			case serr != nil:
				res.refused[t] = "authorization signature not checked: " + serr.Error()
				continue
			}
			payload := approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets)
			if err := approval.VerifyAuthorization(signers, au.Principal, payload, au.Signature); err != nil {
				res.refused[t] = "authorization refused: " + err.Error()
				continue
			}
			res.covered[t] = contract.AuthorizedTarget{Target: t, ID: approval.AuthorizationID(payload), Principal: au.Principal, ExpiresAt: au.ExpiresAt}
			delete(res.refused, t)
		}
	}
	return res
}

// report is the receipt's account of res (nil when no authorization mattered).
func (r authResult) report(intentSHA string) *contract.VerdictAuthorization {
	if len(r.covered) == 0 && len(r.refused) == 0 {
		return nil
	}
	out := &contract.VerdictAuthorization{IntentSHA256: intentSHA, Covered: []contract.AuthorizedTarget{}, Refused: []contract.AuthorizationRefusal{}}
	for _, c := range r.covered {
		out.Covered = append(out.Covered, c)
	}
	for t, why := range r.refused {
		out.Refused = append(out.Refused, contract.AuthorizationRefusal{Target: t, Reason: why})
	}
	sort.Slice(out.Covered, func(i, j int) bool { return out.Covered[i].Target < out.Covered[j].Target })
	sort.Slice(out.Refused, func(i, j int) bool { return out.Refused[i].Target < out.Refused[j].Target })
	return out
}

// authorizeReceipt decides again, for a receipt v of commit, which of its waived engine targets an authorization covers. The
// receipt is a file the agent could edit, so nothing in it is believed except the intent hash it names, which the
// authorization's own signature must match; the base's config is read from the objects, never from the receipt. cfg, when
// the caller already has the base's config, saves reading it.
func (a App) authorizeReceipt(commit, tree string, rcpt contract.Verdict, waivers map[string]contract.Waiver, cfg *contract.Config) authResult {
	cands := waivedFailing(rcpt.Targets)
	if len(cands) == 0 {
		return authResult{}
	}
	if cfg == nil {
		c, err := a.configAt(rcpt.BaseRef)
		if err != nil {
			res := authResult{refused: map[string]string{}}
			for _, t := range cands {
				res.refused[t] = "authorization not checked: cannot read the base's config: " + err.Error()
			}
			return res
		}
		cfg = &c
	}
	at, _ := time.Parse(time.RFC3339, rcpt.CreatedAt) // a receipt with no readable date leaves at zero, and authorize refuses
	runBound := ""
	if acct := rcpt.Authorization; acct != nil {
		runBound = acct.IntentSHA256
	}
	return a.authorize(commit, waivers, cands, authInputs{baseRef: rcpt.BaseRef, at: at, intents: a.intentsOf(runBound, tree), cfg: *cfg})
}

// AuthorizeOptions is what a person gives to sign a pre-authorization.
type AuthorizeOptions struct {
	Key, Principal string
	Base           string   // "" is HEAD
	IntentFile     string   // the file whose sha256 the authorization names
	Decision       string   // enforce_new_behavior or preserve_legacy
	Expires        string   // RFC 3339, in the future
	Targets        []string // exact <path>::<symbol>
}

// Authorize signs an authorization with the person's key and saves it to .a2dlc/authorizations.json, replacing an entry
// with the same signed text. The signature is checked against the allowed signers before anything is written, so a key
// that nobody trusts does not leave a file that looks like a decision. The key's passphrase is asked by ssh-keygen at the
// terminal; it never passes through this code.
func (a App) Authorize(o AuthorizeOptions) (contract.Authorization, error) {
	var au contract.Authorization
	switch {
	case o.Decision != contract.EnforceNewBehavior && o.Decision != contract.PreserveLegacy && o.Decision != contract.PlanAccept:
		return au, fmt.Errorf("--decision %q: only %s, %s and %s can be pre-authorized", o.Decision, contract.EnforceNewBehavior, contract.PreserveLegacy, contract.PlanAccept)
	case len(o.Targets) == 0:
		return au, fmt.Errorf("--target is missing: name each function that may change")
	case o.IntentFile == "":
		return au, fmt.Errorf("--intent is missing: an authorization is bound to the intent it was signed for")
	}
	for _, t := range o.Targets {
		if (o.Decision == contract.PlanAccept) != strings.HasPrefix(t, "plan::") {
			return au, fmt.Errorf("--target %q: plan::<feature_id> goes with --decision %s and only with it", t, contract.PlanAccept)
		}
		if !strings.Contains(t, "::") || strings.ContainsAny(t, "*\\") || strings.HasSuffix(t, "::") || strings.HasPrefix(t, "::") {
			return au, fmt.Errorf("--target %q: an exact <path>::<symbol>, no globs and no <file>::*", t)
		}
	}
	exp, err := time.Parse(time.RFC3339, o.Expires)
	if err != nil {
		return au, fmt.Errorf("--expires %q: RFC 3339 date-time, e.g. 2026-11-12T00:00:00Z", o.Expires)
	}
	if !exp.After(a.Now()) {
		return au, fmt.Errorf("--expires %s is already past", o.Expires)
	}
	intent, err := os.ReadFile(o.IntentFile)
	if err != nil {
		return au, fmt.Errorf("--intent: %w", err)
	}
	base := o.Base
	if base == "" {
		base = "HEAD"
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return au, fmt.Errorf("--base %q: %w", base, err)
	}
	sorted := append([]string(nil), o.Targets...)
	sort.Strings(sorted)
	uniq := sorted[:0]
	for i, t := range sorted {
		if i == 0 || t != sorted[i-1] {
			uniq = append(uniq, t)
		}
	}
	au = contract.Authorization{BaseRef: baseSha, IntentSHA256: IntentHash(intent), Targets: uniq, Decision: o.Decision,
		ExpiresAt: exp.UTC().Format(time.RFC3339), Principal: o.Principal}
	text := approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets)
	if au.Signature, err = approval.SignAuthorization(o.Key, text); err != nil {
		return au, err
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return au, err
	}
	if err := approval.VerifyAuthorization(signers, au.Principal, text, au.Signature); err != nil {
		return au, fmt.Errorf("signed, but the signature does not pass against the allowed signers (is your key missing from allowed_signers?): %w", err)
	}
	current, err := a.authorizations("")
	if err != nil {
		return au, err
	}
	out := contract.Authorizations{SchemaVersion: contract.SchemaVersion, Authorizations: []contract.Authorization{}}
	for _, x := range current {
		if x.Signature != au.Signature && approval.AuthorizationID(approval.AuthorizationPayload(x.BaseRef, x.IntentSHA256, x.Decision, x.ExpiresAt, x.Targets)) != approval.AuthorizationID(text) {
			out.Authorizations = append(out.Authorizations, x)
		}
	}
	out.Authorizations = append(out.Authorizations, au)
	return au, a.write(pathAuthorizations, out)
}

// LedgerAuthorizations are the ledger records for the targets v says an authorization covered: the whole signed
// authorization, found in the working tree's authorizations.json by its id. A covered target whose authorization is no
// longer there is an error: the ledger must not say less than the receipt does.
func (a App) LedgerAuthorizations(v contract.Verdict, stage string, at time.Time) ([]contract.LedgerAuthorization, error) {
	if v.Authorization == nil || len(v.Authorization.Covered) == 0 {
		return nil, nil
	}
	auths, err := a.authorizations("")
	if err != nil {
		return nil, err
	}
	byID := map[string]contract.Authorization{}
	for _, au := range auths {
		byID[approval.AuthorizationID(approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets))] = au
	}
	var out []contract.LedgerAuthorization
	for _, c := range v.Authorization.Covered {
		au, ok := byID[c.ID]
		if !ok {
			return nil, fmt.Errorf("the receipt says authorization %s covered %s, but %s has no such authorization", c.ID[:12], c.Target, pathAuthorizations)
		}
		out = append(out, contract.LedgerAuthorization{Stage: stage, At: at.UTC().Format(time.RFC3339), Target: c.Target, Authorization: au})
	}
	return out, nil
}

// mergeInto folds res into the maps verify reads: a covered target counts as approved for the strict policy and a refused
// one keeps its reason, so the problem line says why the authorization did not stand in for an approval.
func (r authResult) mergeInto(approved map[string]bool, reason map[string]string) {
	for t := range r.covered {
		approved[t] = true
		delete(reason, t)
	}
	for t, why := range r.refused {
		if !approved[t] && reason[t] == "" {
			reason[t] = why
		}
	}
}

// waivedFailing are the engine targets of targets that a waiver exempts from a FAIL: the only ones an authorization may cover.
func waivedFailing(targets []contract.VerdictTarget) []string {
	var out []string
	for _, t := range targets {
		if t.Waived {
			out = append(out, t.Target)
		}
	}
	return out
}

// intentsOf is the set an authorization's intent_sha256 may name: the run-bound intent (if any) and the intents committed in tree.
func (a App) intentsOf(runBound, tree string) map[string]bool {
	out := a.committedIntents(tree)
	if runBound != "" {
		out[runBound] = true
	}
	return out
}
