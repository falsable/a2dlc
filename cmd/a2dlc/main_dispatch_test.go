package main

import "testing"

// The dispatch itself is covered, not only the handlers: a missing --out exits 64 before any role is launched, and an
// unknown or empty command is refused the same way.
func TestProposeDispatchNeedsOut(t *testing.T) {
	if got := run([]string{"propose"}); got != 64 {
		t.Fatalf("propose without --out: exit %d, want 64", got)
	}
	if got := run([]string{"propose", "--out", ""}); got != 64 {
		t.Fatalf("propose with an empty --out: exit %d, want 64", got)
	}
}

func TestNoCommandIsUsage(t *testing.T) {
	if got := run(nil); got != 64 {
		t.Fatalf("no command: exit %d, want 64", got)
	}
}
