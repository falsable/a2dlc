package main

import (
	"fmt"

	"github.com/falsable/a2dlc/internal/contract"
)

// testLines is one line per test that keeps the verdict from PASS. Waived and passing results are left out. The causes that
// are not tests are worded in internal/app (BlockingLines), shared with the Stop hook.
func testLines(v contract.Verdict) []string {
	var out []string
	for _, t := range v.Tests {
		if t.Waived || t.Verdict == contract.PASS {
			continue
		}
		out = append(out, fmt.Sprintf("  %s test %s (%s): %s", t.Verdict, t.Target, t.Gate, t.Reason))
	}
	return out
}
