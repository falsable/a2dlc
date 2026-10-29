package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// PriorSpend is what earlier runs under out reported (out/<run id>/report.json). The total cap is cumulative across
// runs, so a report that cannot be read stops the run rather than count as zero.
func PriorSpend(out string) (float64, error) {
	files, err := filepath.Glob(filepath.Join(out, "*", "report.json"))
	if err != nil {
		return 0, err
	}
	var usd float64
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return 0, err
		}
		var r contract.FactoryReport
		if err := contract.DecodeStrictBytes(b, &r); err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		if err := r.Validate(); err != nil {
			return 0, fmt.Errorf("%s: %w", f, err)
		}
		usd += r.Usage.USD
	}
	return usd, nil
}

func newRunID(batchID, base string, now time.Time) string {
	sum := sha256.Sum256(fmt.Appendf(nil, "%s\n%s\n%d", batchID, base, now.UnixNano()))
	return "run-" + hex.EncodeToString(sum[:])[:12]
}

// RunBatch runs the batch's issues as lanes, at most cfg.Concurrency at once, under the caps. It returns the report it
// also wrote to <out>/<run id>/report.json. Lanes are always reported in the batch's order.
func RunBatch(ctx context.Context, d Deps, cfg contract.FactoryConfig, batch contract.FactoryBatch) (contract.FactoryReport, error) {
	if err := cfg.Validate(); err != nil {
		return contract.FactoryReport{}, err
	}
	if err := batch.Validate(); err != nil {
		return contract.FactoryReport{}, err
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	base := d.Base
	if base == "" {
		var err error
		if base, err = gitIn(d.Repo.Dir, "rev-parse", "HEAD"); err != nil {
			return contract.FactoryReport{}, err
		}
	}
	prior, err := PriorSpend(d.Out)
	if err != nil {
		return contract.FactoryReport{}, err
	}
	started := d.Now()
	runID := newRunID(batch.BatchID, base, started)
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	acct := newAccountant(cfg.Caps, prior, cancel)
	stopWall := acct.watchWall()
	defer stopWall()
	wt := &worktrees{dir: d.Repo.Dir}

	lanes := make([]contract.FactoryLane, len(batch.Issues))
	slots := make(chan struct{}, cfg.Concurrency)
	var wg sync.WaitGroup
	stoppedNew := false
admit:
	for i, is := range batch.Issues {
		if why := Precheck(is); why != "" {
			lanes[i] = notRun(is.ID, contract.LaneSkipped, why)
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-runCtx.Done():
			why := "run killed before the lane started"
			if _, reason := acct.wasKilled(); reason != "" {
				why += ": " + reason
			}
			lanes[i] = notRun(is.ID, contract.LaneKilled, why)
			continue admit
		}
		if ok, why := acct.mayStartLane(); !ok {
			<-slots
			stoppedNew = true
			lanes[i] = notRun(is.ID, contract.LaneSkipped, why)
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			defer func() {
				if p := recover(); p != nil {
					lanes[i] = notRun(is.ID, contract.LaneError, fmt.Sprint("panic: ", p))
				}
			}()
			lanes[i] = runLane(runCtx, d, cfg, acct, wt, runID, base, is)
		}()
	}
	wg.Wait()
	stopWall()

	killed, _ := acct.wasKilled()
	outcome := contract.RunCompleted
	switch {
	case killed:
		outcome = contract.RunKilled
	case stoppedNew:
		outcome = contract.RunStoppedNewLanes
	}
	rep := contract.FactoryReport{SchemaVersion: 1, RunID: runID, BatchID: batch.BatchID, BaseSha: base,
		ConfigSHA256: d.ConfigSHA256, StartedAt: started.UTC().Format(time.RFC3339), FinishedAt: d.Now().UTC().Format(time.RFC3339),
		Outcome: outcome, PriorSpendUSD: prior, Lanes: lanes}
	if killed {
		rep.KilledBy = acct.killedByCap()
	}
	for _, l := range lanes {
		rep.Usage.Plus(l.Usage)
		if l.Status == contract.LaneAccepted {
			rep.Accepted++
		}
	}
	if rep.Accepted > 0 {
		c := rep.Usage.USD / float64(rep.Accepted)
		rep.CostPerAcceptedUSD = &c
	}
	if err := rep.Validate(); err != nil {
		return rep, fmt.Errorf("report: %w", err)
	}
	return rep, writeJSON(filepath.Join(d.Out, runID, "report.json"), rep)
}
