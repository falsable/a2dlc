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
