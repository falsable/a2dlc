package main

import (
	"fmt"

	"github.com/falsable/a2dlc/internal/contract"
)

// clipAt bounds each counterexample field on stdout; the whole value stays in .a2dlc/verdict.json.
const clipAt = 120

// blockingLines is one line per target or test that keeps the verdict from PASS, with what the agent needs to fix it.
// Notes (targets that do not count toward the verdict) and waived or passing results are left out.
func blockingLines(v contract.Verdict) []string {
	var out []string
	for _, t := range v.Targets {
		if t.Waived || t.Verdict == contract.PASS || t.Note != "" {
			continue
		}
		l := fmt.Sprintf("  %s %s", t.Verdict, t.Target)
		if c := t.Counterexample; c != nil {
			l += fmt.Sprintf(": args=%s old=%s new=%s", clip(c.Args), clip(c.Old), clip(c.New))
		}
		if t.Reason != "" {
			l += " (" + t.Reason + ")"
		}
		out = append(out, l)
	}
	for _, t := range v.Tests {
		if t.Waived || t.Verdict == contract.PASS {
			continue
		}
		out = append(out, fmt.Sprintf("  %s test %s (%s): %s", t.Verdict, t.Target, t.Gate, t.Reason))
	}
	return out
}

func clip(s string) string {
	if r := []rune(s); len(r) > clipAt {
		return string(r[:clipAt]) + "…"
	}
	return s
}
