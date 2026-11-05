package contract

// Plan is a feature plan (contracts/plan.schema.json, RFC-0008): the slices of one feature, the files each owns and the
// slices each depends on.
type Plan struct {
	SchemaVersion int         `json:"schema_version"`
	FeatureID     string      `json:"feature_id"`
	Slices        []PlanSlice `json:"slices"`
	Shared        []string    `json:"shared"`
}

// PlanSlice is one slice of a plan.
type PlanSlice struct {
	SliceID     string     `json:"slice_id"`
	Goal        string     `json:"goal"`
	Owns        []string   `json:"owns"`
	DependsOn   []string   `json:"depends_on"`
	Criticality string     `json:"criticality"`
	Budget      PlanBudget `json:"budget"`
	Acceptance  []string   `json:"acceptance"`
	Invariants  []string   `json:"invariants"`
	Covers      []int      `json:"covers"`
}

// PlanBudget is a slice's share of the feature's budget.
type PlanBudget struct {
	MaxCostUSD float64 `json:"max_cost_usd"`
}

// Plan slice statuses (plan-state.schema.json).
const (
	PlanPending   = "pending"
	PlanPassed    = "passed"
	PlanEscalated = "escalated"
	PlanFailed    = "failed"
	PlanBlocked   = "blocked"
)

// PlanState is what `a2dlc plan run` leaves after each invocation (contracts/plan-state.schema.json).
type PlanState struct {
	SchemaVersion int              `json:"schema_version"`
	FeatureID     string           `json:"feature_id"`
	PlanSHA256    string           `json:"plan_sha256"`
	BaseSha       string           `json:"base_sha"`
	Invocation    int              `json:"invocation"`
	Slices        []PlanSliceState `json:"slices"`
	Merged        *string          `json:"merged"`
	Outcome       *string          `json:"outcome"`
	Reason        string           `json:"reason"`
}

// PlanSliceState is one slice of a plan run.
type PlanSliceState struct {
	SliceID    string  `json:"slice_id"`
	Status     string  `json:"status"`
	Base       *string `json:"base"`
	Candidate  *string `json:"candidate"`
	FinishedIn int     `json:"finished_in"`
	Reason     string  `json:"reason"`
}
