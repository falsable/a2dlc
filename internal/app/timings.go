package app

import (
	"context"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// timer collects verdict.timings. It rides in the context so each step records itself without a new parameter, and it is
// safe for the concurrent steps of one check. A context without a timer records nothing.
type timer struct {
	mu    sync.Mutex
	t     contract.Timings
	start time.Time
}

type timerKey struct{}
type labelKey struct{}

func withTimer(ctx context.Context) (context.Context, *timer) {
	t := &timer{start: time.Now()}
	return context.WithValue(ctx, timerKey{}, t), t
}

func timerOf(ctx context.Context) *timer {
	t, _ := ctx.Value(timerKey{}).(*timer)
	return t
}

// withLabel names the test runs made with ctx, over the name a.run would infer from the tree it runs.
func withLabel(ctx context.Context, label string) context.Context {
	return context.WithValue(ctx, labelKey{}, label)
}

func labelOf(ctx context.Context) string {
	s, _ := ctx.Value(labelKey{}).(string)
	return s
}

// ms rounds up to 1 so that a step that ran never reads as 0.
func ms(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	if m := d.Milliseconds(); m > 0 {
		return m
	}
	return 1
}

// testRun records one test command run, in the order they finish.
func (t *timer) testRun(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.t.TestRuns = append(t.t.TestRuns, contract.Timing{Name: name, Ms: ms(d)})
	t.mu.Unlock()
}

// engineOp records one engine operation.
func (t *timer) engineOp(name string, d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.t.EngineOps = append(t.t.EngineOps, contract.Timing{Name: name, Ms: ms(d)})
	if name == "stub" {
		t.t.StubsMs += ms(d)
	}
	t.mu.Unlock()
}

// sync adds to the tree sync time (export of the trees or sync of the warm tree).
func (t *timer) sync(d time.Duration) {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.t.SyncMs += ms(d)
	t.mu.Unlock()
}

// result closes the measurement: the whole check from the timer's creation.
func (t *timer) result() *contract.Timings {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := t.t
	out.TotalMs = ms(time.Since(t.start))
	out.TestRuns = append([]contract.Timing(nil), t.t.TestRuns...)
	out.EngineOps = append([]contract.Timing(nil), t.t.EngineOps...)
	return &out
}
