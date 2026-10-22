package run

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The ledger's network label follows NetworkMode: an empty network (the schema default) is none, so the ledger never states
// bridge for a role that the launcher gives none.
func TestNetworkLabelFollowsNetworkMode(t *testing.T) {
	cases := []struct {
		network string
		mode    string
		label   string
	}{
		{"", "none", "none"},
		{"none", "none", "none"},
		{"provider", "proxy-allowlist", "proxy-allowlist"},
	}
	for _, c := range cases {
		r := contract.Role{Network: c.network}
		if got := NetworkMode(r); got != c.mode {
			t.Errorf("network %q: mode %q, want %q", c.network, got, c.mode)
		}
		if got := networkOf(r); got != c.label {
			t.Errorf("network %q: label %q, want %q", c.network, got, c.label)
		}
	}
}
