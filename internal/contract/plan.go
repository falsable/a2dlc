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
	// Replans counts the slices decomposed again after CB-02b (bounded by plan run --max-replan).
	Replans int `json:"replans"`
	// Scheduler is how many slices the last invocation ran at once and why; nil before the first invocation.
	Scheduler *PlanScheduler `json:"scheduler"`
	// Acceptance is who accepted this plan to run (RFC-0008 section 4): a person's signed approval or an authorization
	// that covers plan.accept. `a2dlc plan run` refuses without one and verify-run checks it again.
	Acceptance *PlanAcceptance `json:"acceptance,omitempty"`
}

// Plan acceptance kinds (plan-state.schema.json).
const (
	AcceptedByApproval      = "approval"
	AcceptedByAuthorization = "authorization"
)

// PlanAcceptance records what let a plan run: Kind says which of Approval or Authorization is set, At is when it was used.
type PlanAcceptance struct {
	Kind          string         `json:"kind"`
	At            string         `json:"at"`
	Approval      *PlanApproval  `json:"approval,omitempty"`
	Authorization *Authorization `json:"authorization,omitempty"`
}

// PlanApproval is a person's signature over a plan's content hash and the base it runs on (namespace a2dlc-plan), in
// .a2dlc/run/_plan/<feature>/approval.json. The signed text is approval.PlanPayload.
type PlanApproval struct {
	PlanSHA256 string `json:"plan_sha256"`
	BaseRef    string `json:"base_ref"`
	ExpiresAt  string `json:"expires_at"`
	Principal  string `json:"principal"`
	Signature  string `json:"signature"`
}

// PlanAmount is CPUs and bytes of memory.
type PlanAmount struct {
	CPUs     float64 `json:"cpus"`
	MemBytes int64   `json:"mem_bytes"`
}

// PlanPlace is one place a plan's work runs (the host, the Docker daemon): what it has and what was free.
type PlanPlace struct {
	Name  string     `json:"name"`
	Total PlanAmount `json:"total"`
	Free  PlanAmount `json:"free"`
	Note  string     `json:"note,omitempty"`
	Err   string     `json:"error,omitempty"`
}

// PlanDemand is a slice's peak in each place.
type PlanDemand struct {
	Docker PlanAmount `json:"docker"`
	Host   PlanAmount `json:"host"`
}

// PlanScheduler records the decision of how many slices ran at once.
type PlanScheduler struct {
	Jobs      int        `json:"jobs"`
	Requested *int       `json:"requested"`
	Why       string     `json:"why"`
	Demand    PlanDemand `json:"demand"`
	Host      PlanPlace  `json:"host"`
	Docker    *PlanPlace `json:"docker"`
	SamePool  bool       `json:"same_pool"`
}

// PlanSliceState is one slice of a plan run.
type PlanSliceState struct {
	SliceID    string  `json:"slice_id"`
	Status     string  `json:"status"`
	Base       *string `json:"base"`
	Candidate  *string `json:"candidate"`
	FinishedIn int     `json:"finished_in"`
	Reason     string  `json:"reason"`
	// DefinitionSHA256 is the slice as the plan declares it: a replan keeps the state of an unchanged slice.
	DefinitionSHA256 string `json:"definition_sha256"`
	// RunDir is the folder of the slice's run, where a person answers an escalation; nil before it ran.
	RunDir *string `json:"run_dir"`
}
