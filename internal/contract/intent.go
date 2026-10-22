package contract

import (
	"fmt"
	"regexp"
	"strings"
)

// Types of block 4 of a2dlc run (ADR meta/0004, amendment 2026-10-17): the Stage 0 intent and the decision of the
// Router of Stage 2. They mirror intent.schema.json and route.schema.json.

const (
	CriticalityLow      = "low"
	CriticalityMedium   = "medium"
	CriticalityHigh     = "high"
	CriticalityCritical = "critical"

	TrackN1 = "N=1"
	TrackN2 = "N=2"
)

// Criticalities in order: the Router raises one level by index.
var Criticalities = []string{CriticalityLow, CriticalityMedium, CriticalityHigh, CriticalityCritical}

// CriticalityRank gives the position of c in Criticalities, or -1 if it is not a contract criticality.
func CriticalityRank(c string) int {
	for i, x := range Criticalities {
		if x == c {
			return i
		}
	}
	return -1
}

type Intent struct {
	// SLAs are the service-level objectives of the slice (intent.schema.json#/properties/slas).
	SLAs []SLA `json:"slas,omitempty"`
	// ChangeKind is feature, bugfix or refactor, as declared by the person; "" is unknown.
	ChangeKind string `json:"change_kind,omitempty"`
	// CriticalityTags raise the effective criticality one level, the same rule as CriticalPaths.
	CriticalityTags []string `json:"criticality_tags,omitempty"`
	// Capabilities restricts what the changed code may use (intent.schema.json#/properties/capabilities).
	Capabilities  *Capabilities `json:"capabilities,omitempty"`
	SchemaVersion int           `json:"schema_version"`
	SliceID       string        `json:"slice_id"`
	Mode          string        `json:"mode"`
	Goal          string        `json:"goal"`
	Invariants    []string      `json:"invariants"`
	NonGoals      []string      `json:"non_goals"`
	Criticality   string        `json:"criticality"`
	Budget        *IntentBudget `json:"budget"`
	CriticalPaths []string      `json:"critical_paths"`
	Acceptance    []string      `json:"acceptance"`
	// RejectedAlternatives (§6.3): discarded alternatives, which the core passes to the Builder in its @a2dlc header.
	RejectedAlternatives []string `json:"rejected_alternatives,omitempty"`
	// Boundary (CB-04, §7): nil means there is no boundary; with one, Stage 3 escalates if the builder crosses it (boundary.go).
	Boundary *Boundary `json:"boundary,omitempty"`
	// Delivery (Stage 7): nil leaves the stage skipped; with it, the binary builds delivery.json (integration.go).
	Delivery *Delivery `json:"delivery,omitempty"`
}

// Boundary is the protected boundary of the slice (intent.schema.json#/$defs/boundary): what the builder cannot change.
// A field that is absent (nil) protects nothing; a present one has at least one element, because [] declares nothing.
type Boundary struct {
	ForbiddenPaths []string `json:"forbidden_paths,omitempty"`
	DoNotRefactor  []string `json:"do_not_refactor,omitempty"`
	// ForbiddenSideEffects are protocol names (network.http.outbound, fs.write, ...) the changed code may not produce.
	ForbiddenSideEffects []string `json:"forbidden_side_effects,omitempty"`
}

// validate repeats in Go the rules of intent.schema.json#/$defs/boundary: at least one field, non-empty lists and
// non-empty elements.
func (b Boundary) validate() error {
	if b.ForbiddenPaths == nil && b.DoNotRefactor == nil && b.ForbiddenSideEffects == nil {
		return fmt.Errorf("boundary without forbidden_paths, do_not_refactor or forbidden_side_effects: omit it or declare at least one")
	}
	for _, l := range []struct {
		field string
		v     []string
	}{{"forbidden_paths", b.ForbiddenPaths}, {"do_not_refactor", b.DoNotRefactor}, {"forbidden_side_effects", b.ForbiddenSideEffects}} {
		if l.v == nil {
			continue
		}
		if len(l.v) == 0 {
			return fmt.Errorf("boundary.%s is empty: omit it", l.field)
		}
		for _, s := range l.v {
			if s == "" {
				return fmt.Errorf("boundary.%s has an empty element", l.field)
			}
		}
	}
	return nil
}

type IntentBudget struct {
	// MaxTokens caps the tokens the roles may spend; nil means no cap.
	MaxTokens                 *int     `json:"max_tokens,omitempty"`
	MaxCostUSD                *float64 `json:"max_cost_usd"`
	MaxInnerIterations        int      `json:"max_inner_iterations"`
	MaxTier1AdversarialRounds int      `json:"max_tier1_adversarial_rounds"`
	MaxMutants                *int     `json:"max_mutants"`
}

var sliceRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,99}$`)

// Validate repeats in Go the rules of the schema that DecodeStrict does not see: required fields (a missing array
// decodes to nil; [] does not), enums and minimums.
func (in Intent) Validate() error {
	switch {
	case in.SchemaVersion != SchemaVersion:
		return fmt.Errorf("schema_version %d", in.SchemaVersion)
	case !sliceRe.MatchString(in.SliceID):
		return fmt.Errorf("slice_id %q is invalid", in.SliceID)
	case in.Mode != ModeGreenfield && in.Mode != ModeRefactor && in.Mode != ModeBrownfield && in.Mode != ModeLite:
		return fmt.Errorf("mode %q: greenfield, refactor, brownfield_facade or lite", in.Mode)
	case strings.TrimSpace(in.Goal) == "":
		return fmt.Errorf("goal is missing")
	case CriticalityRank(in.Criticality) < 0:
		return fmt.Errorf("criticality %q: low, medium, high or critical", in.Criticality)
	case in.Budget == nil:
		return fmt.Errorf("budget is missing")
	case in.Budget.MaxCostUSD != nil && *in.Budget.MaxCostUSD <= 0:
		return fmt.Errorf("budget.max_cost_usd %v: must be greater than 0, or null for no cap", *in.Budget.MaxCostUSD)
	case in.Budget.MaxInnerIterations < 1:
		return fmt.Errorf("budget.max_inner_iterations %d: minimum 1", in.Budget.MaxInnerIterations)
	case in.Budget.MaxTier1AdversarialRounds < 1:
		return fmt.Errorf("budget.max_tier1_adversarial_rounds %d: minimum 1", in.Budget.MaxTier1AdversarialRounds)
	case in.checkV34() != nil:
		return in.checkV34()
	case in.Budget.MaxMutants != nil && *in.Budget.MaxMutants < 1:
		return fmt.Errorf("budget.max_mutants %d: minimum 1, or null", *in.Budget.MaxMutants)
	}
	for _, l := range []struct {
		field string
		v     []string
	}{{"invariants", in.Invariants}, {"non_goals", in.NonGoals}, {"critical_paths", in.CriticalPaths}, {"acceptance", in.Acceptance}} {
		if l.v == nil {
			return fmt.Errorf("%s is missing (empty is [])", l.field)
		}
		for _, s := range l.v {
			if s == "" {
				return fmt.Errorf("%s has an empty element", l.field)
			}
		}
	}
	for _, a := range in.RejectedAlternatives {
		if a == "" {
			return fmt.Errorf("rejected_alternatives has an empty element")
		}
	}
	if in.Boundary != nil {
		if err := in.Boundary.validate(); err != nil {
			return err
		}
	}
	if in.Delivery != nil {
		return in.Delivery.validate()
	}
	return nil
}

// validate repeats in Go the rules of intent.schema.json#delivery and adds the one the schema does not express: the rings
// are strictly increasing and the last is 100 (the canary ends on all the traffic).
func (d Delivery) validate() error {
	if d.Writes == nil {
		return fmt.Errorf("delivery.writes is missing")
	}
	if d.Rings != nil && len(d.Rings) == 0 {
		return fmt.Errorf("delivery.rings is empty: omit it for the default rings 1, 10, 100")
	}
	if c := d.Compensation; c != nil {
		switch c.Strategy {
		case CompensatingEvents, DataRepairScript, VersionedRecords:
		default:
			return fmt.Errorf("delivery.compensation.strategy %q: compensating_events, data_repair_script or versioned_records", c.Strategy)
		}
		if strings.TrimSpace(c.Description) == "" {
			return fmt.Errorf("delivery.compensation without description")
		}
	}
	for i, r := range d.Rings {
		switch {
		case r < 1 || r > 100:
			return fmt.Errorf("delivery.rings[%d] = %d: between 1 and 100", i, r)
		case i > 0 && r <= d.Rings[i-1]:
			return fmt.Errorf("delivery.rings is not strictly increasing: %v", d.Rings)
		}
	}
	if name := len(d.Rings); name > 0 && d.Rings[name-1] != 100 {
		return fmt.Errorf("delivery.rings ends at %d: the last ring must be 100", d.Rings[name-1])
	}
	if s := d.Sentinel; s != nil {
		if d.Writes != nil && *d.Writes && (s.ErrorRateDeltaMax == nil || s.LatencyP99MsMax == nil) {
			return fmt.Errorf("delivery.sentinel: error_rate_delta_max and latency_p99_ms_max must be numbers when writes is true (omit the sentinel to take the defaults)")
		}
		if s.ErrorRateDeltaMax != nil && *s.ErrorRateDeltaMax < 0 {
			return fmt.Errorf("delivery.sentinel.error_rate_delta_max is negative")
		}
		if s.LatencyP99MsMax != nil && *s.LatencyP99MsMax < 1 {
			return fmt.Errorf("delivery.sentinel.latency_p99_ms_max %d: minimum 1", *s.LatencyP99MsMax)
		}
		for _, inv := range s.Invariants {
			if inv == "" {
				return fmt.Errorf("delivery.sentinel.invariants has an empty element")
			}
		}
	}
	return nil
}

// Route is the decision of the Router (route.json).
type Route struct {
	SchemaVersion        int      `json:"schema_version"`
	Track                string   `json:"track"`
	DualSpecRequired     bool     `json:"dual_spec_required"`
	CriticalityEffective string   `json:"criticality_effective"`
	Reasons              []string `json:"reasons"`
	TouchedCritical      []string `json:"touched_critical"`
	// LemmasRequired is true in N=2 routes: Stage 3 asks the builder for lemmas (route.schema.json lemmas_required).
	LemmasRequired bool `json:"lemmas_required"`
	// Metrics of S* (route_metrics.go): pointers to tell null (without S*) from 0. Without omitempty: the Router always
	// writes them, null when there is no S*; older route.json without them still decode (they stay nil).
	RuleCount          *int    `json:"rule_count"`
	StateMutationDepth *int    `json:"state_mutation_depth"`
	MetricsReason      *string `json:"metrics_reason"`
}
