package run

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// Stage 7: Progressive Delivery (protocol §5). The binary does not deploy: from intent.delivery and the approved
// candidate it builds the plan that the platform executes (delivery.json): feature flag, canary rings (1 % → 10 % → 100 %
// by default), Rollback Sentinel (error_rate_delta, latency_p99, invariants at runtime with periodic reconciliation
// for the expensive ones), and CB-06: a slice with writes and no compensation strategy does not go past the
// 1 % ring: the plan keeps only the rings up to 1 % with max_ring 1 and the run escalates missing_compensation. In
// refactor, legacy/new-value parity in the Scientist style only covers read-only paths; for write paths, canary with
// compensation. A changed path that intent.delivery.read_only_paths declares read-only escalates the run.

var ringsDefault = []int{1, 10, 100}

// The Rollback Sentinel thresholds a writing slice gets when neither the intent's sentinel nor its SLAs set them. No
// prose document names them yet; the values are the ones of contracts/examples/valid/delivery__cb06-refactor.json.
const (
	defaultErrorRateDeltaMax = 0.01
	defaultLatencyP99MsMax   = 200
)

func stageDelivery(_ context.Context, m *Machine) (string, string, string, error) {
	if reason := m.withoutPass("the delivery plan belongs to an approved candidate"); reason != "" {
		return contract.StageSkipped, reason, "", nil
	}
	if m.intent == nil || m.intent.Delivery == nil {
		return contract.StageSkipped, "the intent does not declare delivery: the platform decides rings, sentinel and compensation", "", nil
	}
	candidate, tree, err := m.judgedTree()
	if err != nil {
		return "", "", "", err
	}
	changed, deleted, err := m.app.Repo.Changed(m.baseSha, tree)
	if err != nil {
		return "", "", "", err
	}
	all := append(append([]string{}, changed...), deleted...)
	sort.Strings(all)
	plan := buildPlan(*m.intent, m.state.SliceID, m.state.Mode, candidate, all)
	breach, _ := app.Partition(m.intent.Delivery.ReadOnlyPaths, all)
	artifact, err := m.writeArtifact(ArtDelivery, plan)
	if err != nil {
		return "", "", "", err
	}
	note := sentinelNote(*m.intent)
	var escalations []contract.Escalation
	var reasons []string
	if plan.BlockedBy != nil {
		escalations = append(escalations, app.Escalation("missing_compensation", m.state.SliceID+"::delivery",
			fmt.Sprintf("CB-06: the slice writes data and the intent does not declare a compensation strategy; the plan does not go past the %d %% ring.", plan.MaxRing),
			contract.Evidence{Message: "intent.delivery.writes = true, intent.delivery.compensation = null"}))
		reasons = append(reasons, fmt.Sprintf("CB-06: writes without compensation, delivery stays at the %d %% ring", plan.MaxRing))
	}
	if len(breach) > 0 {
		msg := "the slice changes paths that intent.delivery.read_only_paths declares read-only: " + strings.Join(breach, ", ")
		escalations = append(escalations, app.Escalation("protected_boundary", m.state.SliceID+"::delivery", msg,
			contract.Evidence{Message: msg}))
		reasons = append(reasons, fmt.Sprintf("%d read-only path(s) changed", len(breach)))
	}
	if len(escalations) > 0 {
		if _, err := m.escalate(escalations); err != nil {
			return "", "", "", err
		}
		if note != "" {
			reasons = append(reasons, note)
		}
		return contract.StageEscalated, strings.Join(reasons, "; "), artifact, nil
	}
	reason := fmt.Sprintf("delivery plan with %d rings (%s) and flag %s", len(plan.Rings), listRings(plan.Rings), plan.FeatureFlag)
	if plan.Parity != nil {
		reason += "; legacy/new parity only on read-only paths"
	}
	if note != "" {
		reason += "; " + note
	}
	pass := contract.PASS
	m.state.Outcome = &pass
	return contract.StageDone, reason, artifact, nil
}

// buildPlan is the delivery plan of an intent with delivery (the caller guarantees it): the intent's rings or the
// defaults; each ring requires the sentinel green of the previous one, those past the first require the declared
// compensation when the slice writes, and the last one requires reconciliation green when there are invariants. The
// sentinel's invariants are the ones it declares, or the slice's. A writing slice gets concrete thresholds (the
// schema requires them): the declared ones, else the intent's p99 latency SLA, else the defaults. Without compensation
// CB-06 keeps only the rings up to 1 % (§5: one does not go past 1 % whatever rings the intent declares), sets
// max_ring to 1 and blocked_by to CB-06. changed are the slice's changed and deleted paths: parity runs on the
// read-only ones in refactor, and the rest get the canary with compensation when the slice writes.
func buildPlan(in contract.Intent, slice, mode string, candidate *string, changed []string) contract.DeliveryPlan {
	d := in.Delivery
	write := d.Writes != nil && *d.Writes // Validate requires writes; nil only for an intent that did not pass through it
	rings := d.Rings
	if len(rings) == 0 {
		rings = ringsDefault
	}
	sent := contract.DeliverySentinel{Invariants: []string{}, Reconciliation: contract.ReconciliationNone}
	invariants := in.Invariants
	if s := d.Sentinel; s != nil {
		sent.ErrorRateDeltaMax, sent.LatencyP99MsMax = s.ErrorRateDeltaMax, s.LatencyP99MsMax
		if s.Invariants != nil {
			invariants = s.Invariants
		}
	}
	if write {
		if sent.ErrorRateDeltaMax == nil {
			v := defaultErrorRateDeltaMax
			sent.ErrorRateDeltaMax = &v
		}
		if sent.LatencyP99MsMax == nil {
			v, ok, _ := latencyP99FromSLAs(in.SLAs)
			if !ok {
				v = defaultLatencyP99MsMax
			}
			sent.LatencyP99MsMax = &v
		}
	}
	sent.Invariants = append(sent.Invariants, invariants...)
	if len(sent.Invariants) > 0 {
		sent.Reconciliation = contract.ReconciliationPeriodic
	}
	plan := contract.DeliveryPlan{SchemaVersion: contract.SchemaVersion, SliceID: slice, Mode: mode, Candidate: candidate,
		FeatureFlag: "a2dlc." + slice, Writes: write, Compensation: d.Compensation, Rings: []contract.Ring{},
		MaxRing: rings[len(rings)-1], Sentinel: sent}
	for i, p := range rings {
		req := []string{contract.RequiresSentinelGreen}
		if write && i > 0 {
			req = append(req, contract.RequiresCompensation)
		}
		if i == len(rings)-1 && len(sent.Invariants) > 0 {
			req = append(req, contract.RequiresReconciliationGreen)
		}
		plan.Rings = append(plan.Rings, contract.Ring{Percent: p, Requires: req})
	}
	if mode == contract.ModeRefactor {
		plan.Parity = &contract.Parity{Mode: "scientist_read_only", WritePaths: "canary_with_compensation"}
		if len(d.ReadOnlyPaths) > 0 {
			plan.Parity.Paths = append([]string{}, d.ReadOnlyPaths...)
		}
	}
	if write {
		if _, rest := app.Partition(d.ReadOnlyPaths, changed); len(rest) > 0 {
			plan.Canary = &contract.Canary{Paths: rest}
		}
	}
	if write && d.Compensation == nil {
		plan.MaxRing = 1
		b := contract.BlockedByCB06
		plan.BlockedBy = &b
		plan.Rings = ringsUpTo(plan.Rings, 1)
	}
	return plan
}

// ringsUpTo keeps the rings at or below percent, and the 1 % ring when the intent declared none that low: the canary
// always opens at 1 %, and delivery.json needs at least one ring.
func ringsUpTo(rings []contract.Ring, percent int) []contract.Ring {
	kept := []contract.Ring{}
	for _, r := range rings {
		if r.Percent <= percent {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		kept = append(kept, contract.Ring{Percent: percent, Requires: []string{contract.RequiresSentinelGreen}})
	}
	return kept
}

// latencyP99FromSLAs is the tightest p99 latency ceiling, in milliseconds, that the intent's SLAs declare ("p99 < 200
// ms" gives 200, "p99 < 0.2 s" gives 200). Only metrics that name p99 set the threshold: a p50 or bare latency SLA does
// not, and neither does an error-rate SLA, since the sentinel's error_rate_delta_max is a delta against the previous
// ring, not an absolute rate. A p99 SLA that cannot be read as a ceiling (another op or unit) is returned in unmapped
// so the caller can report it instead of dropping it.
func latencyP99FromSLAs(slas []contract.SLA) (ms int, ok bool, unmapped []string) {
	best := 0
	for _, s := range slas {
		if !strings.Contains(strings.ToLower(s.Metric), "p99") {
			continue
		}
		v := s.Value
		switch strings.ToLower(strings.TrimSpace(s.Unit)) {
		case "ms", "milliseconds":
		case "s", "seconds":
			v *= 1000
		default:
			unmapped = append(unmapped, describeSLA(s))
			continue
		}
		if s.Op != "<" && s.Op != "<=" {
			unmapped = append(unmapped, describeSLA(s))
			continue
		}
		// the epsilon keeps a value like 0.29 s (289.99999999999997 ms in float) at 290
		n := int(math.Floor(v + 1e-9))
		if n < 1 {
			n = 1
		}
		if !ok || n < best {
			best, ok = n, true
		}
	}
	return best, ok, unmapped
}

func describeSLA(s contract.SLA) string {
	return fmt.Sprintf("%s %s %g %s", s.Metric, s.Op, s.Value, s.Unit)
}

// clampedP99 lists the p99 ceilings that latencyP99FromSLAs raised to 1 ms because they are below it, so the sentinel
// note can say the plan enforces a looser threshold than the declared one.
func clampedP99(slas []contract.SLA) []string {
	var out []string
	for _, s := range slas {
		if !strings.Contains(strings.ToLower(s.Metric), "p99") || (s.Op != "<" && s.Op != "<=") {
			continue
		}
		v := s.Value
		switch strings.ToLower(strings.TrimSpace(s.Unit)) {
		case "ms", "milliseconds":
		case "s", "seconds":
			v *= 1000
		default:
			continue
		}
		if math.Floor(v+1e-9) < 1 {
			out = append(out, describeSLA(s))
		}
	}
	return out
}

// sentinelNote says which Rollback Sentinel thresholds of a writing slice were not declared and took a value the
// intent does not state, and why a p99 SLA did not set the latency one. Empty when nothing was filled in.
func sentinelNote(in contract.Intent) string {
	d := in.Delivery
	if d == nil || d.Writes == nil || !*d.Writes {
		return ""
	}
	var parts []string
	if s := d.Sentinel; s == nil || s.ErrorRateDeltaMax == nil {
		parts = append(parts, fmt.Sprintf("error_rate_delta_max takes the default %g", defaultErrorRateDeltaMax))
	}
	if s := d.Sentinel; s == nil || s.LatencyP99MsMax == nil {
		_, ok, unmapped := latencyP99FromSLAs(in.SLAs)
		if raised := clampedP99(in.SLAs); len(raised) > 0 {
			parts = append(parts, "p99 SLA below 1 ms raised to the 1 ms minimum (the schema's): "+strings.Join(raised, ", "))
		}
		if len(unmapped) > 0 {
			parts = append(parts, "p99 SLA not mapped to latency_p99_ms_max (only < or <= in ms or s): "+strings.Join(unmapped, ", "))
		}
		if !ok {
			parts = append(parts, fmt.Sprintf("latency_p99_ms_max takes the default %d ms", defaultLatencyP99MsMax))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "sentinel: " + strings.Join(parts, "; ")
}

func listRings(rings []contract.Ring) string {
	s := ""
	for i, r := range rings {
		if i > 0 {
			s += " → "
		}
		s += fmt.Sprintf("%d %%", r.Percent)
	}
	return s
}
