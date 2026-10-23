package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// FireOnCheck is the firing point of a2dlc check (the pre-push gate); a2dlc run fires "stage:<id>".
const FireOnCheck = "check"

// fires says whether a check is bound to the firing point: fire_on absent means ["check"].
func fires(c contract.CheckConfig, on string) bool {
	if len(c.FireOn) == 0 {
		return on == FireOnCheck
	}
	return slices.Contains(c.FireOn, on)
}

// matched keeps the changed files that match some glob of the check; no globs means every changed file.
func matched(c contract.CheckConfig, changed []string) []string {
	if len(c.Matches) == 0 {
		return changed
	}
	var out []string
	for _, f := range changed {
		for _, g := range c.Matches {
			if verdict.Match(g, f) {
				out = append(out, f)
				break
			}
		}
	}
	return out
}

// runChecks fires the checks of cfg.Checks bound to `on` over the changed files, in config order, and returns
// verdict.checks plus the accept_finding waivers that still need a signature. export is called before the first check
// that runs. A check that cannot produce a valid response is status error (or timeout), never a pass (protocol §5
// Checks, ADR meta/0006).
func (a App) runChecks(ctx context.Context, cfg contract.Config, on string, changed []string, export func() error, baseDir, candDir *string,
	waivers map[string]contract.Waiver, approved map[string]bool) (out []contract.VerdictCheck, pending []string) {
	for _, c := range cfg.Checks {
		if !fires(c, on) {
			continue
		}
		vc := contract.VerdictCheck{ID: c.ID, Severity: c.Severity}
		files := matched(c, changed)
		fail := func(status, reason string) {
			vc.Status, vc.Reason = status, reason
			out = append(out, vc)
		}
		switch {
		case len(files) == 0 && len(c.Matches) == 0:
			fail(contract.CheckNotApplicable, "no changed files")
			continue
		case len(files) == 0:
			fail(contract.CheckNotApplicable, fmt.Sprintf("0 of %d changed files match %s", len(changed), strings.Join(c.Matches, ", ")))
			continue
		case a.Checks == nil:
			fail(contract.CheckError, "no check runner: this build cannot run checks")
			continue
		}
		if err := export(); err != nil {
			fail(contract.CheckError, "the trees could not be exported: "+err.Error())
			continue
		}
		r, err := a.Checks.RunCheck(ctx, c, files, *baseDir, *candDir)
		switch {
		case errors.Is(err, engine.ErrCheckTimeout):
			fail(contract.CheckTimeout, err.Error())
			continue
		case err != nil:
			fail(contract.CheckError, err.Error())
			continue
		}
		if err := validCheckResponse(c, files, r.Response); err != nil {
			fail(contract.CheckError, "$A2DLC_OUT outside contract: "+err.Error())
			continue
		}
		vc.Tool = r.Response.Tool
		if r.Response.Status == contract.CheckNotApplicable {
			fail(contract.CheckNotApplicable, "the check reported not_applicable over "+fmt.Sprint(len(files))+" file(s)")
			continue
		}
		pass := r.Response.Pass
		vc.Status, vc.Pass, vc.Findings = contract.CheckRan, &pass, []contract.VerdictCheckFinding{}
		for _, f := range r.Response.Findings {
			w, ok := waivers[f.Target]
			waived := ok && w.Decision == contract.AcceptFinding
			if waived && !approved[f.Target] {
				pending = append(pending, f.Target)
			}
			vc.Findings = append(vc.Findings, contract.VerdictCheckFinding{Target: f.Target, Message: f.Message, Waived: waived})
		}
		out = append(out, vc)
	}
	return out, pending
}

// validCheckResponse applies the check-response.schema.json rules that matter to the core: the response answers for
// the check, pass and findings agree, and every finding is on a file the check received (the waiver binds that blob).
func validCheckResponse(c contract.CheckConfig, files []string, r contract.CheckResponse) error {
	switch {
	case r.ID != c.ID:
		return fmt.Errorf("it answers for %q, not for %q", r.ID, c.ID)
	case r.Status != contract.CheckRan && r.Status != contract.CheckNotApplicable:
		return fmt.Errorf("status %q is not ran or not_applicable", r.Status)
	case r.Findings == nil:
		return errors.New("findings is missing")
	case r.Pass && len(r.Findings) > 0:
		return errors.New("pass is true with findings")
	case r.Status == contract.CheckNotApplicable && (!r.Pass || len(r.Findings) > 0):
		return errors.New("not_applicable must pass with no findings")
	}
	received := map[string]bool{}
	for _, f := range files {
		received[f] = true
	}
	for _, f := range r.Findings {
		if !strings.Contains(f.Target, "::") || f.Message == "" {
			return fmt.Errorf("finding %q needs a <file>::<id> target and a message", f.Target)
		}
		if !received[contract.TargetFile(f.Target)] {
			return fmt.Errorf("finding on %s, a file the check did not receive", contract.TargetFile(f.Target))
		}
	}
	return nil
}

// applyChecks lowers the verdict with the checks, never raises it: a blocking check with an unwaived finding is FAIL;
// a blocking check in error or timeout is INCONCLUSIVE; advisory ones are recorded only. The unsigned accept_finding
// waivers go to pending_waivers, where verify blocks on them.
func applyChecks(v *contract.Verdict, checks []contract.VerdictCheck, pending []string) {
	if len(checks) == 0 {
		return
	}
	v.Checks = checks
	fail, inc := false, false
	for _, c := range checks {
		if c.Severity != contract.CheckBlocking {
			continue
		}
		switch c.Status {
		case contract.CheckError, contract.CheckTimeout:
			inc = true
		case contract.CheckRan:
			for _, f := range c.Findings {
				fail = fail || !f.Waived
			}
		}
	}
	switch {
	case fail:
		v.Verdict = contract.FAIL
	case inc && v.Verdict == contract.PASS:
		v.Verdict = contract.INCONCLUSIVE
	}
	seen := map[string]bool{}
	for _, t := range v.PendingWaivers {
		seen[t] = true
	}
	for _, t := range pending {
		if !seen[t] {
			seen[t] = true
			v.PendingWaivers = append(v.PendingWaivers, t)
		}
	}
	sort.Strings(v.PendingWaivers)
}

// checkEscalations is what a person decides about the checks: a blocking finding (check_failed, resolved only with a
// signed accept_finding), an accept_finding waiting for its signature, and a blocking check that could not run
// (tooling: no waiver turns it into a PASS). Advisory checks escalate nothing: they are recorded in verdict.checks.
func checkEscalations(v contract.Verdict, pending map[string]bool) []contract.Escalation {
	out := []contract.Escalation{}
	for _, c := range v.Checks {
		blocking := c.Severity == contract.CheckBlocking
		switch c.Status {
		case contract.CheckError, contract.CheckTimeout:
			if blocking {
				out = append(out, escalation("tooling", pathConfig+"::checks."+c.ID, "The blocking check "+c.ID+" could not run ("+c.Status+"): "+c.Reason, contract.Evidence{Message: c.Reason}))
			}
		case contract.CheckRan:
			for _, f := range c.Findings {
				switch {
				case f.Waived && pending[f.Target]:
					out = append(out, escalation("signature_pending", f.Target, "The waiver awaits a signature: a2dlc approve "+f.Target, contract.Evidence{Message: f.Message}))
				case !f.Waived && blocking:
					out = append(out, escalation("check_failed", f.Target, "Blocking check "+c.ID+" found: "+f.Message, contract.Evidence{Message: f.Message}, contract.AcceptFinding))
				}
			}
		}
	}
	return out
}
