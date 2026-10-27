package app

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// VerifyReport is what the pre-push policy decided: Blocked stops the push, Warnings are reported and let it through.
type VerifyReport struct {
	Blocked  []string
	Warnings []string
}

// Lines are the report as printed by a2dlc verify, in order, and whether the push is stopped. A push that goes through
// with warnings ends with the line that says why it does not block.
func (r VerifyReport) Lines() (lines []string, blocked bool) {
	for _, b := range r.Blocked {
		lines = append(lines, "a2dlc verify: blocked: "+b)
	}
	for _, w := range r.Warnings {
		lines = append(lines, "a2dlc verify: warning: "+w)
	}
	if len(r.Blocked) == 0 && len(r.Warnings) > 0 {
		lines = append(lines, "a2dlc verify: warning: this does not block the push: only a FAIL or critical or protected code left unjudged stops it (a2dlc verify --strict blocks on everything)")
	}
	return lines, len(r.Blocked) > 0
}

// criticalFile says whether file matches a critical_code glob. A glob over file::symbol counts for the whole file: this is
// used where the receipt does not say which symbol.
func criticalFile(globs []string, file string) bool {
	for _, g := range globs {
		gf, _, _ := strings.Cut(g, "::")
		if verdict.Match(gf, file) {
			return true
		}
	}
	return false
}

// guarded says whether file is critical or protected in the config of the base (critical_code or protected_paths).
func guarded(cfg contract.Config, file string) bool {
	return criticalFile(cfg.CriticalCode, file) || len(protectedMatches(cfg.ProtectedPaths, []string{file})) > 0
}

func guardedFiles(cfg contract.Config, files []string) []string {
	var out []string
	for _, f := range files {
		if guarded(cfg, f) {
			out = append(out, f)
		}
	}
	return out
}

// VerifyPolicy is what the pre-push runs: it blocks only on evidence about the tree being pushed. Evidence is a FAIL, a
// change to a protected path without a valid approval, a waiver without a signature that exempts one of those, or
// critical or protected code (the base's config, never the pushed tree's) that a2dlc did not judge. Whatever else it
// could not judge is a warning. Like Verify it runs no engine and writes no file. The error is a failure of a2dlc or of
// git, not a verdict.
func (a App) VerifyPolicy(commit string) (VerifyReport, error) {
	var rep VerifyReport
	sha, err := a.Repo.ResolveCommit(commit)
	if err != nil {
		return rep, err
	}
	tree, err := a.Repo.CommitTree(sha)
	if err != nil {
		return rep, err
	}
	pushed := a.pushedBase()

	// A receipt that cannot be read is no receipt; so is one over another tree or whose base leaves unpushed commits out.
	var v contract.Verdict
	rerr := a.read("", pathVerdict, &v)
	fresh, why := false, ""
	switch {
	case errors.Is(rerr, os.ErrNotExist):
		why = pathVerdict + " does not exist"
	case rerr != nil:
		why = pathVerdict + " cannot be read: " + rerr.Error()
	case strings.TrimPrefix(v.TreeHash, treePrefix) != tree:
		why = "the verdict in " + pathVerdict + " was issued over another tree"
		if diffs, err := a.Repo.DiffNames(strings.TrimPrefix(v.TreeHash, treePrefix), tree); err == nil && len(diffs) > 0 {
			why += "; changed: " + strings.Join(truncate(diffs, 10), ", ")
		}
		if v.Verdict == contract.FAIL {
			why += "; that verdict was FAIL"
		}
	case pushed != "" && !a.Repo.IsAncestor(v.BaseRef, pushed):
		why = "the verdict's base (" + short(v.BaseRef) + ") is not in what was pushed, so it does not cover the unpushed commits"
		if v.Verdict == contract.FAIL {
			why += "; that verdict was FAIL"
		}
	default:
		fresh = true
	}

	// The base of what is judged: the receipt's when it is fresh, else the pushed range's.
	base := pushed
	if fresh {
		base = v.BaseRef
	}
	cfg, cfgErr := a.policyConfig(base, sha, rerr == nil, v.BaseRef)
	if cfgErr != nil {
		// Without the base's config nothing can be said to be uncritical.
		cfg = contract.Config{CriticalCode: allCritical}
		rep.Warnings = append(rep.Warnings, "cannot read the base's config, so every file counts as critical: "+cfgErr.Error())
	}
	var files []string
	rangeKnown := base != ""
	if rangeKnown {
		changed, deleted, err := a.Repo.Changed(base, tree)
		if err != nil {
			return rep, err
		}
		files = append(changed, deleted...)
	}

	waivers, err := a.waivers(sha)
	if err != nil {
		return rep, err
	}
	approvals, err := a.approvals(sha)
	if err != nil {
		return rep, err
	}
	approved, reason := a.approved(tree, waivers, approvals)

	// Protected paths: a change without a valid approval stops the push with or without a receipt.
	for _, f := range sortedKeys(protectedMatches(cfg.ProtectedPaths, files)) {
		if !approved[f+protectedSuffix] {
			rep.Blocked = append(rep.Blocked, fmt.Sprintf("%s matches protected_paths and has no valid approval: write a waiver for %s in %s, then a person runs a2dlc approve %s",
				f, f+protectedSuffix, pathWaivers, f+protectedSuffix))
		}
	}

	if !fresh {
		rep.Warnings = append(rep.Warnings, "no verdict for this tree: nothing examined ("+why+")")
		switch {
		case rangeKnown:
			if g := guardedFiles(cfg, files); len(g) > 0 {
				rep.Blocked = append(rep.Blocked, "the pushed range "+short(base)+".."+short(sha)+" changes critical or protected code ("+strings.Join(truncate(g, 10), ", ")+") and there is no verdict for this tree: run a2dlc check")
			}
		case len(cfg.CriticalCode)+len(cfg.ProtectedPaths) > 0:
			rep.Blocked = append(rep.Blocked, "there is no verdict for this tree, the range being pushed cannot be determined (no upstream and no origin/HEAD) and the config declares critical_code or protected_paths: run a2dlc check")
		}
		return rep, nil
	}

	// A fresh receipt. FAIL stops the push, as does anything the divergence notes already refused.
	if v.Verdict == contract.FAIL {
		rep.Blocked = append(rep.Blocked, "the verdict is FAIL"+failing(v))
	}
	for _, p := range a.noteProblems(v) {
		rep.Blocked = append(rep.Blocked, p)
	}
	for _, w := range unsignedWaivers(v, cfg, approved, reason) {
		if w.blocks {
			rep.Blocked = append(rep.Blocked, w.text)
		} else {
			rep.Warnings = append(rep.Warnings, w.text)
		}
	}
	if v.Verdict == contract.INCONCLUSIVE {
		u := unjudgedOf(v)
		rep.Warnings = append(rep.Warnings, u.summary())
		if hit := u.guarded(cfg, files); len(hit) > 0 {
			rep.Blocked = append(rep.Blocked, "the verdict is INCONCLUSIVE and leaves critical or protected code unjudged ("+strings.Join(truncate(hit, 10), ", ")+"): run a2dlc check again or write a waiver")
		}
	}
	return rep, nil
}

// policyConfig is the config that says what is critical: the base's. With no base to name (no upstream, no origin/HEAD)
// it is the receipt's base, if there is a receipt, and else the pushed commit's own, because with nothing earlier there is
// no scope to narrow. A base without a config declares nothing.
func (a App) policyConfig(base, sha string, haveReceipt bool, receiptBase string) (contract.Config, error) {
	if base == "" && haveReceipt && receiptBase != "" {
		if _, err := a.Repo.ResolveCommit(receiptBase); err == nil {
			base = receiptBase
		}
	}
	var cfg contract.Config
	var err error
	if base != "" {
		cfg, _, err = a.baseConfigNote(base, sha)
	} else {
		cfg, err = a.configAt(sha)
	}
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return contract.Config{}, nil
	}
	return cfg, err
}

// failing lists the targets and tests that failed, for the message.
func failing(v contract.Verdict) string {
	var names []string
	for _, t := range v.Targets {
		if t.Verdict == contract.FAIL && !t.Waived && t.Note == "" {
			names = append(names, t.Target)
		}
	}
	for _, t := range v.Tests {
		if t.Verdict == contract.FAIL && !t.Waived {
			names = append(names, t.Target)
		}
	}
	if len(names) == 0 {
		return ": run a2dlc check"
	}
	return ": " + strings.Join(truncate(names, 10), ", ")
}

type waiverLine struct {
	text   string
	blocks bool
}

// unsignedWaivers are the waivers of the receipt without a valid signature. One blocks where what it exempts would
// block: a FAIL, or an INCONCLUSIVE on critical or protected code. Any other is a warning.
func unsignedWaivers(v contract.Verdict, cfg contract.Config, approved map[string]bool, reason map[string]string) []waiverLine {
	var out []waiverLine
	seen := map[string]bool{}
	add := func(target, exempts string, critical bool) {
		if approved[target] || seen[target] {
			return
		}
		seen[target] = true
		m := reason[target]
		if m == "" {
			m = "unsigned: approve it with a2dlc approve " + target
		}
		out = append(out, waiverLine{text: "waiver for " + target + ": " + m, blocks: exempts == contract.FAIL || exempts == contract.INCONCLUSIVE && critical})
	}
	for _, t := range v.Targets {
		if t.Waived {
			add(t.Target, t.Verdict, verdict.IsCritical(cfg.CriticalCode, t.Target) || guarded(cfg, contract.TargetFile(t.Target)))
		}
	}
	for _, t := range v.Tests {
		if t.Waived {
			add(t.Target, t.Verdict, verdict.IsCritical(cfg.CriticalCode, t.Target) || guarded(cfg, contract.TargetFile(t.Target)))
		}
	}
	for _, c := range v.Checks {
		for _, f := range c.FindingList() {
			if f.Waived {
				// A blocking finding is a FAIL; an advisory one is only recorded.
				ex := contract.PASS
				if c.Severity == contract.CheckBlocking {
					ex = contract.FAIL
				}
				add(f.Target, ex, false)
			}
		}
	}
	return out
}

// unjudged is what the receipt says a2dlc did not examine. Files are unjudged at the level of the file; targets carry the
// symbol, so a symbol-level critical_code entry can tell them apart. noFile is set when the receipt has a cause that names
// no file (a test gate in error, an inconclusive test, a check that did not run), or no cause at all.
type unjudged struct {
	lines   int
	files   []string // files with changed lines not examined
	outOf   []string // files out of scope
	targets []string // file::symbol that stayed INCONCLUSIVE or were cut by the budget
	noFile  bool
}

func unjudgedOf(v contract.Verdict) unjudged {
	var u unjudged
	set := func(dst *[]string, s string) {
		for _, x := range *dst {
			if x == s {
				return
			}
		}
		*dst = append(*dst, s)
	}
	if v.Coverage != nil {
		for _, f := range v.Coverage.Files {
			if len(f.Uncovered) > 0 {
				u.lines += len(f.Uncovered)
				set(&u.files, f.File)
			}
		}
	}
	for _, o := range v.OutOfScope {
		set(&u.outOf, o.File)
	}
	for _, e := range v.EngineErrors {
		for _, f := range e.Files {
			set(&u.outOf, f)
		}
	}
	for _, t := range v.Targets {
		if t.Verdict == contract.INCONCLUSIVE && !t.Waived {
			set(&u.targets, t.Target)
		}
	}
	if v.DiffBudget != nil {
		for _, c := range v.DiffBudget.Cut {
			set(&u.targets, c.Target)
		}
	}
	for _, t := range v.Tests {
		if t.Verdict == contract.INCONCLUSIVE && !t.Waived {
			u.noFile = true
		}
	}
	if g := v.TestGates; g != nil {
		u.noFile = u.noFile || g.NOPOracle.Status == contract.GateError || g.Baseline.Status == contract.GateError ||
			g.TestsDiff != nil && g.TestsDiff.Status == contract.GateError
	}
	for _, c := range v.Checks {
		if c.Severity == contract.CheckBlocking && (c.Status == contract.CheckError || c.Status == contract.CheckTimeout) {
			u.noFile = true
		}
	}
	if len(u.files)+len(u.outOf)+len(u.targets) == 0 {
		u.noFile = true
	}
	sort.Strings(u.files)
	sort.Strings(u.outOf)
	return u
}

// guarded returns the critical or protected files among what was left unjudged. When the receipt names a cause with no
// file, any critical or protected file among the changed ones counts.
func (u unjudged) guarded(cfg contract.Config, changed []string) []string {
	var hit []string
	add := func(s string) {
		for _, x := range hit {
			if x == s {
				return
			}
		}
		hit = append(hit, s)
	}
	if u.noFile {
		for _, f := range guardedFiles(cfg, changed) {
			add(f)
		}
	}
	for _, f := range append(append([]string(nil), u.files...), u.outOf...) {
		if guarded(cfg, f) {
			add(f)
		}
	}
	for _, t := range u.targets {
		if verdict.IsCritical(cfg.CriticalCode, t) || len(protectedMatches(cfg.ProtectedPaths, []string{contract.TargetFile(t)})) > 0 {
			add(t)
		}
	}
	return hit
}

// summary is the warning for an INCONCLUSIVE receipt, written from what the receipt already holds.
func (u unjudged) summary() string {
	parts := []string{"the verdict is INCONCLUSIVE"}
	if u.lines > 0 {
		parts = append(parts, fmt.Sprintf("%d changed lines were not examined in %s", u.lines, strings.Join(truncate(u.files, 10), ", ")))
	}
	if len(u.outOf) > 0 {
		parts = append(parts, fmt.Sprintf("%d files out of scope: %s", len(u.outOf), strings.Join(truncate(u.outOf, 10), ", ")))
	}
	if len(u.targets) > 0 {
		parts = append(parts, fmt.Sprintf("%d functions not judged: %s", len(u.targets), strings.Join(truncate(u.targets, 10), ", ")))
	}
	return strings.Join(parts, "; ")
}
