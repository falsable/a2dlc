package contract

import (
	"strings"
	"testing"
)

// The wall-time cap is optional: a config without it is still valid and has no run wall limit, one with it must be
// positive, and a report that says a cap killed the run must say the run was killed.
func TestRunWallSecondsIsOptionalAndPositive(t *testing.T) {
	if c := readFactory[FactoryConfig](t, "factory-config__claude-lite.json"); c.Validate() != nil || c.Caps.RunWallSeconds != nil {
		t.Fatalf("a config without the cap: %+v", c.Caps)
	}
	c := readFactory[FactoryConfig](t, "factory-config__run-wall-time.json")
	if err := c.Validate(); err != nil || c.Caps.RunWallSeconds == nil || *c.Caps.RunWallSeconds != 548 {
		t.Fatalf("%v %+v", err, c.Caps)
	}
	zero := 0
	c.Caps.RunWallSeconds = &zero
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "caps.run_wall_seconds") {
		t.Errorf("err = %v", err)
	}
}

func TestReportKilledByNeedsAKilledRun(t *testing.T) {
	r := readFactory[FactoryReport](t, "factory-report__run-wall-killed.json")
	if err := r.Validate(); err != nil || r.KilledBy == nil || r.KilledBy.Cap != KillRunWallSeconds || r.KilledBy.ElapsedSeconds < 548 {
		t.Fatalf("%v %+v", err, r.KilledBy)
	}
	r.Outcome = RunCompleted
	if err := r.Validate(); err == nil || !strings.Contains(err.Error(), "killed_by") {
		t.Errorf("err = %v", err)
	}
}
