package contract

// Types of Stage 5 of a2dlc run (block 5, ADR meta/0004 amendment 2026-10-17): operation: mutate of the engine
// (engine-mutate-response), the artifact mutation.json (mutation), config.mutation and the live mutants that travel to
// the Saboteur (role-payloads#/$defs/saboteur_survivor).

// Mutation operators (engine-mutate-response.schema.json#/$defs/operator).
const (
	OpArithmetic      = "arithmetic"
	OpComparison      = "comparison"
	OpNegateCondition = "negate_condition"
	OpConstant        = "constant"
	OpReturnNone      = "return_none"
	OpRemoveCall      = "remove_call"
)

// MutateBudget is the budget the core builds for operation: mutate: the only lines that may be mutated, and the limit.
type MutateBudget struct {
	ChangedLines map[string][]int `json:"changed_lines"`
	MaxMutants   int              `json:"max_mutants"`
}

// MutateResponse is the response to operation: mutate. Each mutant is /io/mutants/<id>/<file>.
type MutateResponse struct {
	SchemaVersion int            `json:"schema_version"`
	Mutants       []Mutant       `json:"mutants"`
	NotGenerated  []FileReason   `json:"not_generated"`
	Pruned        []PrunedMutant `json:"pruned"`
	Error         *string        `json:"error"`
}

// PrunedMutant is an equivalent mutant that the engine did not generate (engine-mutate-response#/$defs/pruned): the
// requested line where the AST pruning discarded a mutant, and the reason from the engine's closed list. It does not
// count toward the kill rate.
type PrunedMutant struct {
	File   string `json:"file"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// Prune reasons (engine-mutate-response.schema.json#/$defs/pruned/properties/reason): closed list of the engine.
const (
	PruneLogging    = "equivalent: logging-only statement"
	PruneAssert     = "equivalent: assert"
	PruneDeadBranch = "equivalent: dead branch (constant condition)"
	// PruneDeadBranchTypes: the branch is dead by the types (mypy --warn-unreachable), not by a constant.
	PruneDeadBranchTypes = "equivalent: type-guaranteed dead branch"
)

type Mutant struct {
	ID          string `json:"id"`
	File        string `json:"file"`
	Line        int    `json:"line"`
	Operator    string `json:"operator"`
	Description string `json:"description"`
}

type FileReason struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// MutationConfig is config.mutation. MinKillRate nil means 0.8; MaxMutants 0 means 30.
type MutationConfig struct {
	MinKillRate *float64 `json:"min_kill_rate,omitempty"`
	MaxMutants  int      `json:"max_mutants,omitempty"`
}

// States of a mutant in mutation.json.
const (
	MutantKilled       = "killed"
	MutantSurvived     = "survived"
	MutantInconclusive = "inconclusive"
	MutantNotRun       = "not_run"
)

// MutationReport is .a2dlc/run/<slice>/mutation.json, the kill gate of Stage 5.
type MutationReport struct {
	// UnsupportedFiles are changed files in scope that no engine mutated; any of them blocks PASS.
	UnsupportedFiles []string       `json:"unsupported_files,omitempty"`
	SchemaVersion    int            `json:"schema_version"`
	Round            int            `json:"round"`
	Threshold        float64        `json:"threshold"`
	MaxMutants       int            `json:"max_mutants"`
	Tests            []string       `json:"tests"`
	RedOnCandidate   []string       `json:"red_on_candidate"`
	Mutants          []MutantResult `json:"mutants"`
	NotGenerated     []FileReason   `json:"not_generated"`
	PrunedEquivalent []PrunedMutant `json:"pruned_equivalent"`
	Unsupported      []FileReason   `json:"unsupported"`
	KillRate         *float64       `json:"kill_rate"`
	Verdict          string         `json:"verdict"`
	Reason           string         `json:"reason,omitempty"`
}

type MutantResult struct {
	Mutant
	Status   string   `json:"status"`
	KilledBy []string `json:"killed_by"`
	Reason   string   `json:"reason,omitempty"`
}

// SaboteurSurvivor is a live mutant as the Saboteur sees it: without id or code.
type SaboteurSurvivor struct {
	File        string `json:"file"`
	Line        int    `json:"line"`
	Operator    string `json:"operator"`
	Description string `json:"description"`
}
