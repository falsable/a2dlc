package run

import "testing"

func TestDescribedWithoutTierUp(t *testing.T) {
	want := "roles.json declares no tier_up: the escalation goes to a person directly (protocol §7 allows it)"
	if got := (exhaustion{role: "builder_primary"}).described(); got != want {
		t.Fatalf("described() = %q, want %q", got, want)
	}
}
