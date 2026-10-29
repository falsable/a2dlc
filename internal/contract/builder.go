package contract

import "encoding/json"

// Block 3 (ADR meta/0004, amendment 2026-10-17): Builder N=1 in sandbox and Tier 0. They mirror engine-tier0-response,
// probes, candidate and the builder_request/builder_response $defs of role-payloads.

// Tier 0 finding kinds.
const (
	Tier0Compile   = "compile"
	Tier0Typecheck = "typecheck"
	Tier0Lint      = "lint"
	Tier0Probe     = "probe"
)

// Tier0Response is the response to operation: tier0, and also tier0_feedback.json, what the Builder receives on the
// next round. OK if and only if there are no findings.
type Tier0Response struct {
	SchemaVersion int            `json:"schema_version"`
	OK            bool           `json:"ok"`
	Findings      []Tier0Finding `json:"findings"`
	Error         *string        `json:"error"`
	// Reviewed is set by the core (app.Tier0): true if any engine ran tier0 on anything, or the repo's tests ran with readable
	// JUnit on the files no engine covers. An engine does not write it.
	Reviewed bool `json:"reviewed,omitempty"`
}

type Tier0Finding struct {
	Kind    string `json:"kind"`
	File    string `json:"file"`
	Line    *int   `json:"line"`
	Message string `json:"message"`
}

// Tier0Budget is the budget of an engine-request with operation tier0: the core builds it from the S* probes
// (spec.go: Probe) that carry file and expr, the only ones an engine can evaluate.
type Tier0Budget struct {
	Probes []Probe      `json:"probes,omitempty"`
	Policy *PolicyTier0 `json:"policy,omitempty"`
}

// BuilderRequest is the payload of the request to a Builder in Stage 3. Intent and Contract go as-is (null if absent).
type BuilderRequest struct {
	Intent        json.RawMessage `json:"intent"`
	Contract      json.RawMessage `json:"contract"`
	Probes        []Probe         `json:"probes"`
	Round         int             `json:"round"`
	MaxRounds     int             `json:"max_rounds"`
	Tier0Feedback *Tier0Response  `json:"tier0_feedback"`
	// Header: the @a2dlc headers (§6.3) that the Builder copies into each source file. The core builds it (header.go).
	Header *BuilderHeader `json:"header,omitempty"`
	// Boundary (CB-04): the intent's boundary, so the builder does not touch it. Absent if the intent does not declare it.
	Boundary *Boundary `json:"boundary,omitempty"`
	// EngineLanguage is the repo's engine language; absent means python. Paradigm is what the role follows (prompt-enforced).
	EngineLanguage string `json:"engine_language,omitempty"`
	Paradigm       string `json:"paradigm,omitempty"`
	// Critical is the router's effective track being critical (N=2): the builder prompt then asks for lemmas. Nil when absent.
	Critical *bool `json:"critical,omitempty"`
}

// BuilderHeader is request.header (builder_header in role-payloads.schema.json): the @a2dlc header values
// that Stage 3 compares with each changed file. ContractHash is nil without S* (lite mode); the lists are empty, not nil.
type BuilderHeader struct {
	SliceID              string   `json:"slice_id"`
	Mode                 string   `json:"mode"`
	ContractHash         *string  `json:"contract_hash"`
	ProtectedInvariants  []string `json:"protected_invariants"`
	RejectedAlternatives []string `json:"rejected_alternatives"`
	DoNotRefactor        []string `json:"do_not_refactor"`
}

// BuilderResponse is the payload of an ok response from the Builder: a declaration, not proof (what counts is /work).
type BuilderResponse struct {
	Summary      string   `json:"summary"`
	FilesChanged []string `json:"files_changed"`
	// Lemmas are the pure internal functions with their postconditions (Critical slices). Names are unique.
	Lemmas []BuilderLemma `json:"lemmas,omitempty"`
}

// BuilderLemma is one declared lemma of a builder response: a function and its postcondition.
type BuilderLemma struct {
	Name          string `json:"name"`
	Postcondition string `json:"postcondition"`
}

// Candidate is candidate.json, the Stage 3 artifact: the state of /work as a commit on top of the base.
type Candidate struct {
	// Lemmas are the lemma verdicts of this candidate (Critical slices).
	Lemmas []CandidateLemma `json:"lemmas,omitempty"`
	// DependencyChanges are the manifest changes checked against the allowlist.
	DependencyChanges []DependencyChange `json:"dependency_changes,omitempty"`
	SchemaVersion     int                `json:"schema_version"`
	Builder           string             `json:"builder"`
	Commit            string             `json:"commit"`
	TreeHash          string             `json:"tree_hash"`
	Iterations        int                `json:"iterations"`
	Unchecked         []string           `json:"unchecked"`
	// @a2dlc headers without a slice_id line (§6.3): the stage does not fail. See header.go.
	HeadersMissing []string `json:"headers_missing,omitempty"`
	// Tier0Reviewed: whether Tier 0 ran anything on the candidate. Nil in candidates older than this field.
	Tier0Reviewed *bool `json:"tier0_reviewed,omitempty"`
	// N=2 (block 8): the ones that converged and the ones that did not. See n2.go.
	Candidates []CandidateEntry `json:"candidates,omitempty"`
	Dropped    []DroppedBuilder `json:"dropped,omitempty"`
}
