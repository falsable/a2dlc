// Package verdict is the pure logic of the verdict: aggregating the targets, crossing waivers and approvals, and
// choosing which files each engine looks at.
package verdict

import (
	"regexp"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// Aggregate builds the verdict: FAIL if any FAIL target has no waiver; otherwise INCONCLUSIVE if any target is
// INCONCLUSIVE or engines went down; otherwise PASS. A FAIL with a waiver does not count, approved or not; without
// approval it goes to pending. A FAIL with a counterexample, no waiver, on code that critical (config.critical_code) does
// not name is a note on its target (Note) and does not count either; with no critical globs nothing is critical.
func Aggregate(targets []contract.TargetResult, waived, approved map[string]bool, engineErrors int, critical []string) (string, []contract.VerdictTarget, []string) {
	out := make([]contract.VerdictTarget, 0, len(targets))
	pending := []string{}
	fail, inc := false, engineErrors > 0
	for _, t := range targets {
		w := t.Verdict == contract.FAIL && waived[t.Target]
		note := ""
		switch {
		case t.Verdict == contract.FAIL && !w && t.Counterexample != nil && !IsCritical(critical, t.Target):
			note = contract.NoteBehaviorDivergence
		case t.Verdict == contract.FAIL && !w:
			fail = true
		case t.Verdict == contract.INCONCLUSIVE:
			inc = true
		}
		if w && !approved[t.Target] {
			pending = append(pending, t.Target)
		}
		out = append(out, contract.VerdictTarget{TargetResult: t, Waived: w, Note: note})
	}
	sort.Strings(pending)
	switch {
	case fail:
		return contract.FAIL, out, pending
	case inc:
		return contract.INCONCLUSIVE, out, pending
	}
	return contract.PASS, out, pending
}

// ExitCode: 0 PASS, 1 FAIL, 2 INCONCLUSIVE.
func ExitCode(v string) int {
	switch v {
	case contract.PASS:
		return 0
	case contract.FAIL:
		return 1
	}
	return 2
}

// CompleteDiffBudget makes the cut list of the verdict's diff_budget an array when the verdict is written: the schema
// requires it, and a nil slice would marshal as null.
func CompleteDiffBudget(v *contract.Verdict) {
	if v.DiffBudget != nil && v.DiffBudget.Cut == nil {
		v.DiffBudget.Cut = []contract.DiffCut{}
	}
}

// MergeDiffBudget adds one engine's diff budget to the verdict's: the seconds add up over the engines and the cut
// functions are listed. A nil b leaves acc as it is.
func MergeDiffBudget(acc, b *contract.DiffBudget) *contract.DiffBudget {
	if b == nil {
		return acc
	}
	if acc == nil {
		acc = &contract.DiffBudget{Cut: []contract.DiffCut{}}
	}
	acc.TotalS += b.TotalS
	acc.UsedS += b.UsedS
	acc.Cut = append(acc.Cut, b.Cut...)
	return acc
}

// MarkCut makes each cut function that an engine answered INCONCLUSIVE, with the cut as the reason. A FAIL stays a
// FAIL: a counterexample is a finding even when the budget ran out elsewhere. A cut function that no engine answered
// has no target to mark (the target schema names the engines); it stays in diff_budget.cut, and HasCut keeps the
// verdict from PASSing.
func MarkCut(targets []contract.TargetResult, cuts []contract.DiffCut) {
	for _, c := range cuts {
		for i := range targets {
			if t := &targets[i]; t.Target == c.Target && t.Verdict != contract.FAIL {
				t.Verdict, t.Reason = contract.INCONCLUSIVE, "cut by the per-diff budget: "+c.Reason
			}
		}
	}
}

// HasCut says whether the budget cut some function, so that the function was not judged.
func HasCut(b *contract.DiffBudget) bool { return b != nil && len(b.Cut) > 0 }

// globRe translates a glob with ** into an anchored regex: ** crosses folders, * and ? do not.
func globRe(p string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(p); i++ {
		c := p[i]
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			b.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i++
		case c == '*':
			b.WriteString("[^/]*")
		case c == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// Match says whether the path (with /) matches the glob.
func Match(glob, path string) bool { return globRe(glob).MatchString(path) }

// IsCritical says whether target ("file::symbol") matches some glob of config.critical_code. A glob without "::" names
// files and covers every symbol in them; "file::symbol" matches the file part and the symbol part separately, so a "*" in
// the symbol never crosses into the path.
func IsCritical(globs []string, target string) bool {
	file, symbol, hasSymbol := strings.Cut(target, "::")
	for _, g := range globs {
		gf, gs, glooksSymbol := strings.Cut(g, "::")
		switch {
		case !glooksSymbol:
			if Match(gf, file) {
				return true
			}
		case hasSymbol && Match(gf, file) && Match(gs, symbol):
			return true
		}
	}
	return false
}

// Assign distributes the files among engines: the first one in the config whose glob matches keeps it. It returns
// byEngine (keyed by engine index) and the files no engine covers.
func Assign(engines []contract.EngineConfig, files []string) (map[int][]string, []string) {
	byEngine := map[int][]string{}
	without := []string{}
	for _, f := range files {
		assigned := false
		for i, e := range engines {
			for _, g := range e.Paths {
				if Match(g, f) {
					byEngine[i] = append(byEngine[i], f)
					assigned = true
					break
				}
			}
			if assigned {
				break
			}
		}
		if !assigned {
			without = append(without, f)
		}
	}
	return byEngine, without
}
