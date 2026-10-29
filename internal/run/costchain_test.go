package run

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The cost chain of a lane that a caller drives (calibration C7): what the adapter makes of a claude -p result
// (roles/tests/golden/claude_result.cost.json, pinned to the adapter by roles/tests/test_read_claude.py) comes back from
// every fake role call, goes into the ledger and the budget, and CB-03 cuts by tokens and by cost. No model runs.

func goldenCost(t *testing.T) contract.Cost {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "roles", "tests", "golden", "claude_result.cost.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c contract.Cost
	if err := contract.DecodeStrictBytes(b, &c); err != nil {
		t.Fatal(err)
	}
	if want := (contract.Cost{USD: 0.04, InputTokens: 140, CacheReadInputTokens: 10, CacheCreationInputTokens: 30, OutputTokens: 50, Turns: 3}); c != want {
		t.Fatalf("the golden cost is %+v, the test is written for %+v", c, want)
	}
	return c
}

// costed makes every call of a fake builder report the golden cost (fakeBuilder reports 0.1 USD and nothing else).
func costed(inner func(LaunchSpec) (*contract.RoleResponse, int, bool), c contract.Cost) func(LaunchSpec) (*contract.RoleResponse, int, bool) {
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		resp, exit, timedOut := inner(s)
		resp.Cost = &c
		return resp, exit, timedOut
	}
}

func TestCostChainFourCountersAddUpAndCB03CutsByTokensAndByCost(t *testing.T) {
	cost := goldenCost(t)
	const perCall = 140 + 50 // input (cache included) plus output

	run := func(t *testing.T, slice string, opts Options, maxTokens *int) (contract.RunState, *fakeLauncher, contract.RunLedger) {
		t.Helper()
		a, dir, _ := repoBuilder(t)
		var requests []contract.BuilderRequest
		launcher := &fakeLauncher{script: costed(fakeBuilder(t, &requests, broken), cost)}
		opts.Slice = slice
		opts.Intent = intentFile(t, func(in *contract.Intent) {
			in.SliceID, in.Mode, in.Goal = slice, contract.ModeLite, "f doubles"
			in.Budget.MaxTokens = maxTokens
		})
		st, err := Run(context.Background(), a, launcher, opts)
		if err != nil {
			t.Fatal(err)
		}
		return st, launcher, ledgerOf(t, filepath.Join(dir, ".a2dlc/run", slice))
	}

	t.Run("the four counters add up in the ledger and in the budget", func(t *testing.T) {
		st, launcher, lg := run(t, "c7.sum", Options{MaxInner: 3}, nil)
		n := len(launcher.seen)
		if n != 3 || len(lg.Entries) != n {
			t.Fatalf("launches %d, ledger entries %d; wanted 3 builder rounds", n, len(lg.Entries))
		}
		var in, read, write, out, turns int
		var usd float64
		for _, e := range lg.Entries {
			if e.Cost == nil || *e.Cost != cost {
				t.Fatalf("seq %d carries %+v, not the reported cost", e.Seq, e.Cost)
			}
			if e.Cost.InputTokens < e.Cost.CacheReadInputTokens+e.Cost.CacheCreationInputTokens {
				t.Fatalf("seq %d: the cache counters are a breakdown of input_tokens and exceed it: %+v", e.Seq, e.Cost)
			}
			in, read, write, out, turns, usd = in+e.Cost.InputTokens, read+e.Cost.CacheReadInputTokens, write+e.Cost.CacheCreationInputTokens, out+e.Cost.OutputTokens, turns+e.Cost.Turns, usd+e.Cost.USD
		}
		b := st.Budget
		// input_tokens is the total, so spent_tokens = input + output; the cache counters break the input down and are not added again
		if b.SpentTokens == nil || *b.SpentTokens != in+out || *b.SpentTokens != n*perCall {
			t.Fatalf("spent_tokens %v; ledger input %d + output %d, %d calls of %d", b.SpentTokens, in, out, n, perCall)
		}
		if b.SpentCacheReadTokens == nil || *b.SpentCacheReadTokens != read || read != n*10 ||
			b.SpentCacheCreationTokens == nil || *b.SpentCacheCreationTokens != write || write != n*30 {
			t.Fatalf("cache sums read %v write %v; ledger read %d write %d", b.SpentCacheReadTokens, b.SpentCacheCreationTokens, read, write)
		}
		if b.SpentUSD != usd || b.ByRole[contract.RoleBuilderPrimary] != usd || b.UnreportedRoles != 0 || turns != n*3 {
			t.Fatalf("usd %v by_role %v unreported %d; ledger usd %v turns %d", b.SpentUSD, b.ByRole, b.UnreportedRoles, usd, turns)
		}
		// state.json and the ledger tell the same story, whichever of the two is read
		if rebuilt := spendOfLedger(lg.Entries); tokensOf(rebuilt) != tokensOf(b) || rebuilt.SpentUSD != b.SpentUSD ||
			*rebuilt.SpentCacheReadTokens != read || *rebuilt.SpentCacheCreationTokens != write {
			t.Fatalf("rebuilt from the ledger %+v, live %+v", rebuilt, b)
		}
	})

	t.Run("CB-03 cuts by tokens", func(t *testing.T) {
		max := 300 // two calls reach 380 > 300; one call (190) does not
		st, launcher, lg := run(t, "c7.tok", Options{MaxInner: 5}, &max)
		s3 := byID(st)["3"]
		if want := "CB-03: 380 tokens exceed budget.max_tokens 300"; s3.Status != contract.StageEscalated || s3.Reason != want {
			t.Fatalf("%+v; wanted reason %q", s3, want)
		}
		if len(launcher.seen) != 2 || len(lg.Entries) != 2 || st.Budget.SpentUSD > 0.1 {
			t.Fatalf("launches %d, ledger %d, usd %v: the token cap had to cut at 2 calls, far below any cost cap", len(launcher.seen), len(lg.Entries), st.Budget.SpentUSD)
		}
	})

	t.Run("CB-03 cuts by cost", func(t *testing.T) {
		st, launcher, lg := run(t, "c7.usd", Options{MaxInner: 5, MaxCostUSD: 0.05}, nil)
		s3 := byID(st)["3"]
		if s3.Status != contract.StageEscalated || !strings.HasPrefix(s3.Reason, "CB-03: cost 0.0800 USD exceeds max_cost_usd 0.0500") {
			t.Fatalf("%+v", s3)
		}
		if len(launcher.seen) != 2 || len(lg.Entries) != 2 || st.Budget.SpentTokens == nil || *st.Budget.SpentTokens != 2*perCall {
			t.Fatalf("launches %d, ledger %d, tokens %v: the cost cap had to cut at 2 calls with no token cap set", len(launcher.seen), len(lg.Entries), st.Budget.SpentTokens)
		}
	})

	t.Run("CB-03 names the cost when both caps are over", func(t *testing.T) {
		max := 300
		st, _, _ := run(t, "c7.both", Options{MaxInner: 5, MaxCostUSD: 0.05}, &max)
		if s3 := byID(st)["3"]; !strings.HasPrefix(s3.Reason, "CB-03: cost ") {
			t.Fatalf("%+v", s3)
		}
	})
}
