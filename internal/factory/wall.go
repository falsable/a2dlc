package factory

import (
	"fmt"
	"math"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// The run-level wall-time cap (caps.run_wall_seconds, bead a2dlc-3fu.13). The clock starts when the accountant is made,
// at the start of the run, and it is the real clock: the cap bounds how long the run actually took, so it does not go
// through Deps.Now. Past it no new lane starts (checked at every admission) and the lanes in flight are killed (a timer),
// the same way a spent run_kill_usd kills them.

func wallLimit(caps contract.FactoryCaps) time.Duration {
	if caps.RunWallSeconds == nil {
		return 0
	}
	return time.Duration(*caps.RunWallSeconds) * time.Second
}

// checkWallLocked kills the run if the wall time is spent. The caller holds a.mu.
func (a *accountant) checkWallLocked() {
	limit := wallLimit(a.caps)
	if limit == 0 || a.killed || time.Since(a.start) < limit {
		return
	}
	a.killLocked(contract.KillRunWallSeconds, fmt.Sprintf("run wall time: %.0f s elapsed reached run_wall_seconds %d", time.Since(a.start).Seconds(), *a.caps.RunWallSeconds))
}

// watchWall arms the timer that kills the run when the cap passes, and returns what stops it.
func (a *accountant) watchWall() (stop func()) {
	limit := wallLimit(a.caps)
	if limit == 0 {
		return func() {}
	}
	t := time.AfterFunc(limit-time.Since(a.start), func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		a.checkWallLocked()
	})
	return func() { t.Stop() }
}

// killedBy is what the report says about the kill: nil if the run was not killed.
func (a *accountant) killedByCap() *contract.RunKill {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.killedBy
}

func (a *accountant) elapsedLocked() float64 {
	return math.Round(time.Since(a.start).Seconds()*1000) / 1000
}
