package run

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The schema's default is network none: a role that does not declare provider gets no network, and the ledger says so.
func TestNetworkDefaultsToNoneAndProviderIsBridge(t *testing.T) {
	cases := []struct {
		name, network, mode, label string
	}{
		{"empty is the schema default", "", "none", "none"},
		{"none", "none", "none", "none"},
		{"provider", "provider", "proxy-allowlist", "proxy-allowlist"},
	}
	for _, c := range cases {
		r := contract.Role{Family: "x", Image: "i", Argv: []string{"a"}, Network: c.network}
		if got := NetworkMode(r); got != c.mode {
			t.Errorf("%s: NetworkMode = %q, want %q", c.name, got, c.mode)
		}
		if got := networkOf(r); got != c.label {
			t.Errorf("%s: ledger network = %q, want %q", c.name, got, c.label)
		}
	}
}

// A provider role (the LLM roles of roles__claude-gemini) runs on the internal network of its egress proxy, never on
// the bridge, and the ledger labels exactly that launch. The openrouter backend declares its host by default.
func TestLauncherProviderRoleUsesTheProxy(t *testing.T) {
	bin, logPath := dockerLog(t, "")
	r := contract.Role{Family: "claude", Image: "a2dlc-roles:0.1", Argv: []string{"python3", "/a2dlc/adapter.py"}, Network: "provider",
		Env: map[string]string{"A2DLC_LLM": "openrouter"}}
	res, err := launch(t, bin, r, LaunchSpec{Name: "a2dlc-run-1-101-1"})
	if err != nil {
		t.Fatal(err)
	}
	if res.Network != networkOf(r) || res.Network != "proxy-allowlist" {
		t.Fatalf("launcher labels %q, ledger %q; both must be proxy-allowlist", res.Network, networkOf(r))
	}
	if line := firstCall(dockerCalls(t, logPath), "run -d"); !strings.Contains(line, "A2DLC_EGRESS_ALLOW=openrouter.ai ") {
		t.Fatalf("proxy run = %q, wanted the openrouter default allowlist", line)
	}
	for _, call := range dockerCalls(t, logPath) {
		if strings.Contains(call, "bridge") {
			t.Fatalf("a provider role touched the bridge: %q", call)
		}
	}
}

// budget.max_tokens is accumulated from what the roles report (input plus output); a role that reported no tokens adds none,
// and the rebuild from the ledger gives the same sum.
func TestTokensAccumulateFromReportsOnly(t *testing.T) {
	entries := []contract.LedgerEntry{
		{Role: contract.RoleBuilderPrimary, Cost: &contract.Cost{USD: 0.5, InputTokens: 10, OutputTokens: 5}},
		{Role: contract.RoleSaboteur},
		{Role: contract.RoleSaboteur, Cost: &contract.Cost{USD: 0.25}},
	}
	var b contract.Budget
	for _, e := range entries {
		sumSpend(&b, e)
	}
	if tokensOf(b) != 15 || b.UnreportedRoles != 1 || b.SpentUSD != 0.75 {
		t.Fatalf("live sum: tokens %d, unreported %d, usd %v", tokensOf(b), b.UnreportedRoles, b.SpentUSD)
	}
	var none contract.Budget
	sumSpend(&none, contract.LedgerEntry{Role: "saboteur", Cost: &contract.Cost{USD: 1}})
	if none.SpentTokens != nil {
		t.Fatalf("a report without tokens sets SpentTokens: %d", *none.SpentTokens)
	}
	if rebuilt := spendOfLedger(entries); tokensOf(rebuilt) != tokensOf(b) {
		t.Fatalf("rebuild gives %d tokens, live sum %d", tokensOf(rebuilt), tokensOf(b))
	}
}

// input_tokens is the total, cache included: the cache counters are a breakdown of it, so they do not add to
// spent_tokens (that would tighten max_tokens for nothing) and are summed apart. A report without them adds none, and the
// rebuild from the ledger gives the same sums.
func TestCacheCountersAreSummedApartAndNotDoubleCounted(t *testing.T) {
	entries := []contract.LedgerEntry{
		{Role: contract.RoleBuilderPrimary, Cost: &contract.Cost{USD: 0.5, InputTokens: 100, CacheReadInputTokens: 60, CacheCreationInputTokens: 30, OutputTokens: 5}},
		{Role: contract.RoleSaboteur, Cost: &contract.Cost{USD: 0.25, InputTokens: 20, CacheReadInputTokens: 12, OutputTokens: 4}},
		{Role: contract.RoleSaboteur, Cost: &contract.Cost{USD: 0.1, InputTokens: 7, OutputTokens: 1}},
	}
	var b contract.Budget
	for _, e := range entries {
		sumSpend(&b, e)
	}
	if got := tokensOf(b); got != 100+5+20+4+7+1 {
		t.Fatalf("spent_tokens = %d, wanted input plus output only (cache is inside input)", got)
	}
	if b.SpentCacheReadTokens == nil || *b.SpentCacheReadTokens != 72 || b.SpentCacheCreationTokens == nil || *b.SpentCacheCreationTokens != 30 {
		t.Fatalf("cache sums: read %v, write %v; wanted 72 and 30", b.SpentCacheReadTokens, b.SpentCacheCreationTokens)
	}
	var none contract.Budget
	sumSpend(&none, contract.LedgerEntry{Role: contract.RoleSaboteur, Cost: &contract.Cost{USD: 1, InputTokens: 7, OutputTokens: 1}})
	if none.SpentCacheReadTokens != nil || none.SpentCacheCreationTokens != nil {
		t.Fatal("a report without cache counters sets the cache sums")
	}
	rebuilt := spendOfLedger(entries)
	if tokensOf(rebuilt) != tokensOf(b) || *rebuilt.SpentCacheReadTokens != 72 || *rebuilt.SpentCacheCreationTokens != 30 {
		t.Fatalf("rebuild %+v differs from the live sum %+v", rebuilt, b)
	}
}

// A ledger that records fewer tokens than state.json was truncated, the same as one with less cost.
func TestLedgerWithLessTokensIsReported(t *testing.T) {
	twenty, ten := 20, 10
	state := contract.Budget{SpentTokens: &twenty}
	ledger := contract.Budget{SpentTokens: &ten}
	if got := ledgerWithLessSpend(state, ledger); !strings.Contains(got, "tokens") {
		t.Fatalf("%q", got)
	}
	if got := ledgerWithLessSpend(state, state); got != "" {
		t.Fatalf("equal ledger reported: %q", got)
	}
}
