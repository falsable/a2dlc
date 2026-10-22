package run

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// A p99 ceiling below 1 ms is raised to 1 ms, and the sentinel note says so: the plan enforces a looser threshold than the
// declared one, and that is never silent.
func TestSentinelNoteReportsP99RaisedToTheMinimum(t *testing.T) {
	writes := true
	in := contract.Intent{Delivery: &contract.Delivery{Writes: &writes},
		SLAs: []contract.SLA{{Metric: "p99 latency", Op: "<", Value: 0.4, Unit: "ms"}}}
	note := sentinelNote(in)
	if !strings.Contains(note, "p99 SLA below 1 ms raised to the 1 ms minimum") || !strings.Contains(note, "p99 latency < 0.4 ms") {
		t.Fatalf("note does not report the clamp: %q", note)
	}
	if got := clampedP99([]contract.SLA{{Metric: "p99", Op: "<", Value: 150, Unit: "ms"}}); len(got) != 0 {
		t.Fatalf("a 150 ms ceiling reported as raised: %v", got)
	}
}
