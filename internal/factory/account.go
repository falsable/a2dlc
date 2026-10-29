package factory

import (
	"context"
	"fmt"
	"maps"
	"math"
	"os"
	"path/filepath"
	"sync"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/run"
)

// CapError is a launch the factory refused because a cap was spent. Scope is lane, run or total; a run or total refusal
// means the run was killed.
type CapError struct{ Scope, Reason string }

func (e *CapError) Error() string { return "factory cap (" + e.Scope + "): " + e.Reason }

// accountant keeps what the roles reported, per lane and for the run, and applies the caps. One instance is shared by
// every lane of a run. A cap is checked before a launch and a cost is added after it, so a call in flight can overshoot
// by its own cost; --max-budget-usd on the call bounds that for the spend caps.
type accountant struct {
	mu         sync.Mutex
	caps       contract.FactoryCaps
	prior      float64
	run        contract.Counters
	lanes      map[string]*contract.Counters
	killed     bool
	killReason string
	cancel     context.CancelFunc
}

func newAccountant(caps contract.FactoryCaps, prior float64, cancel context.CancelFunc) *accountant {
	return &accountant{caps: caps, prior: prior, lanes: map[string]*contract.Counters{}, cancel: cancel}
}

func (a *accountant) lane(id string) *contract.Counters {
	c := a.lanes[id]
	if c == nil {
		c = &contract.Counters{}
		a.lanes[id] = c
	}
	return c
}

// admit says whether the lane may launch another model call. A cap counts as spent when the spend reaches it.
func (a *accountant) admit(lane string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.killed {
		return &CapError{"run", a.killReason}
	}
	l := a.lane(lane)
	switch {
	case l.USD >= a.caps.LaneUSD:
		return &CapError{"lane", fmt.Sprintf("lane usd: %.4f spent of %.4f", l.USD, a.caps.LaneUSD)}
	case l.Turns >= a.caps.LaneTurns:
		return &CapError{"lane", fmt.Sprintf("lane turns: %d used of %d", l.Turns, a.caps.LaneTurns)}
	}
	return nil
}

// remaining is the most the next call of the lane may spend: the smallest of what is left under the lane cap, the run's
// kill threshold and the total.
func (a *accountant) remaining(lane string) float64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return math.Max(0, math.Min(a.caps.LaneUSD-a.lane(lane).USD,
		math.Min(a.caps.RunKillUSD-a.run.USD, a.caps.TotalUSD-a.prior-a.run.USD)))
}

// add records one launch and kills the run if it reached the kill threshold or the total.
func (a *accountant) add(lane string, cost *contract.Cost) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lane(lane).Add(cost)
	a.run.Add(cost)
	switch {
	case a.killed:
	case a.run.USD >= a.caps.RunKillUSD:
		a.killLocked(fmt.Sprintf("run usd: %.4f spent reached run_kill_usd %.4f", a.run.USD, a.caps.RunKillUSD))
	case a.prior+a.run.USD >= a.caps.TotalUSD:
		a.killLocked(fmt.Sprintf("total usd: %.4f spent reached total_usd %.4f", a.prior+a.run.USD, a.caps.TotalUSD))
	}
}

func (a *accountant) killLocked(reason string) {
	a.killed, a.killReason = true, reason
	if a.cancel != nil {
		a.cancel()
	}
}

// mayStartLane says whether a new lane may start, and why not.
func (a *accountant) mayStartLane() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.killed:
		return false, a.killReason
	case a.run.USD >= a.caps.RunStopNewUSD:
		return false, fmt.Sprintf("no new lanes: run usd %.4f reached run_stop_new_usd %.4f", a.run.USD, a.caps.RunStopNewUSD)
	case a.prior+a.run.USD >= a.caps.TotalUSD:
		return false, fmt.Sprintf("no new lanes: total usd %.4f reached total_usd %.4f", a.prior+a.run.USD, a.caps.TotalUSD)
	}
	return true, ""
}

func (a *accountant) wasKilled() (bool, string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.killed, a.killReason
}

// capLauncher wraps the launcher of one lane. It is the only place the factory touches a launch: it refuses one when a
// cap is spent, gives the launch its remaining budget as --max-budget-usd, and reads the cost the role reported.
type capLauncher struct {
	inner run.Launcher
	acct  *accountant
	lane  string
	cfg   contract.FactoryConfig
	guard *testGuard // nil = no check of base test files
}

func (l capLauncher) Launch(ctx context.Context, r contract.Role, s run.LaunchSpec) (run.LaunchResult, error) {
	if err := l.acct.admit(l.lane); err != nil {
		return run.LaunchResult{}, err
	}
	env := make(map[string]string, len(r.Env)+1)
	maps.Copy(env, r.Env)
	env[EnvLLMCmd] = ClaudeCommand(l.cfg, l.acct.remaining(l.lane))
	r.Env = env
	res, err := l.inner.Launch(ctx, r, s)
	if err == nil && l.guard != nil && s.WorkDir != "" {
		if touched := l.guard.check(s.WorkDir); len(touched) > 0 {
			err = veto(s.OutDir, touched)
		}
	}
	l.acct.add(l.lane, reportedCost(s.OutDir))
	return res, err
}

// reportedCost reads the cost in the role's response.json the way run does: strictly, and a negative or inconsistent
// cost counts as none.
func reportedCost(outDir string) *contract.Cost {
	b, err := os.ReadFile(filepath.Join(outDir, "response.json"))
	if err != nil {
		return nil
	}
	var resp contract.RoleResponse
	if contract.DecodeStrictBytes(b, &resp) != nil || resp.Cost == nil || resp.Cost.Invalid() != "" {
		return nil
	}
	return resp.Cost
}
