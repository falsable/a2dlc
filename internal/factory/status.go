package factory

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// laneStatus turns the end of a lane's run into a status. The run being killed wins over everything else, then a cap,
// then what a2dlc run decided.
func laneStatus(runCtx, laneCtx context.Context, acct *accountant, cfg contract.FactoryConfig, st contract.RunState, runErr error, touched []contract.TestTouch) (status, reason string) {
	var capErr *CapError
	killed, why := acct.wasKilled()
	switch {
	case killed || runCtx.Err() != nil:
		if why == "" {
			why = "run cancelled"
		}
		return contract.LaneKilled, why
	case len(touched) > 0:
		return contract.LaneRejected, touchReason(touched)
	case errors.As(runErr, &capErr):
		return contract.LaneCapped, capErr.Reason
	case errors.Is(laneCtx.Err(), context.DeadlineExceeded):
		return contract.LaneCapped, fmt.Sprintf("lane wall time: %d s", cfg.Caps.LaneSeconds)
	case runErr != nil:
		return contract.LaneError, runErr.Error()
	case st.Outcome != nil && *st.Outcome == contract.PASS:
		return contract.LaneAccepted, ""
	}
	why = "a2dlc run ended without a verdict"
	if st.Outcome != nil {
		why = "a2dlc run ended " + *st.Outcome
	}
	for _, s := range st.Stages {
		if s.Status == contract.StageEscalated || s.Status == contract.StageFailed {
			why = fmt.Sprintf("stage %s %s: %s", s.ID, s.Status, s.Reason)
			if strings.HasPrefix(s.Reason, "CB-03") {
				return contract.LaneCapped, why
			}
			break
		}
	}
	return contract.LaneRejected, why
}

// laneUsage totals the lane's own ledger and splits it by stage. The ledger is the record: a lane's usage is what its
// roles reported, and a launch that reported nothing counts in unreported.
func laneUsage(ledgerPath string, st contract.RunState) (total contract.Counters, unreported int, stages []contract.StageUsage) {
	stages = []contract.StageUsage{}
	for _, s := range st.Stages {
		stages = append(stages, contract.StageUsage{Stage: s.ID, Status: s.Status})
	}
	byStage := map[string]*contract.StageUsage{}
	for i := range stages {
		byStage[stages[i].Stage] = &stages[i]
	}
	b, err := os.ReadFile(ledgerPath)
	if err != nil {
		return total, 0, stages
	}
	var ledger contract.RunLedger
	if contract.DecodeStrictBytes(b, &ledger) != nil {
		return total, 0, stages
	}
	for _, e := range ledger.Entries {
		total.Add(e.Cost)
		if e.Cost == nil {
			unreported++
		}
		if s := byStage[e.Stage]; s != nil {
			s.Add(e.Cost)
		}
	}
	return total, unreported, stages
}
