package contract

// Types of the v3.4 contract additions (contracts/*.schema.json). Each one mirrors a schema; the json tags are the
// schema's property names and TestExamplesOfContractDecode decodes the valid examples strictly into them.

// DiffBudget is verdict.diff_budget: the per-diff time budget of the engines, and what it cut.
type DiffBudget struct {
	TotalS float64   `json:"total_s"`
	UsedS  float64   `json:"used_s"`
	Cut    []DiffCut `json:"cut"`
}

// DiffCut is a function that was not run because the per-diff budget ran out.
type DiffCut struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// SupplyChain is config.supply_chain: where the dependency allowlist lives.
type SupplyChain struct {
	AllowlistFile string `json:"allowlist_file,omitempty"`
}

// TraceCoverageConfig is config.tests.trace_coverage: the minimum branch coverage of the recorded traces.
// (TraceCoverage, without the suffix, is the verdict of the trace gate in stage15.go.)
type TraceCoverageConfig struct {
	MinBranch float64 `json:"min_branch"`
}

// GoldenMaster is config.tests.golden_master: the black-box oracle recorded on the base (refactor and legacy only).
type GoldenMaster struct {
	Command string `json:"command"`
	Image   string `json:"image"`
	Cases   string `json:"cases"`
}

// SLA is one service-level objective of intent.slas.
type SLA struct {
	Metric string  `json:"metric"`
	Op     string  `json:"op"`
	Value  float64 `json:"value"`
	Unit   string  `json:"unit"`
}

// Capabilities is intent.capabilities: what the changed code may not use.
type Capabilities struct {
	ForbiddenImports []string `json:"forbidden_imports,omitempty"`
}

// IOContract is slice-contract.io_contract: the slice's input and output schemas and its error taxonomy.
type IOContract struct {
	InputSchemaRef  string   `json:"input_schema_ref"`
	OutputSchemaRef string   `json:"output_schema_ref"`
	ErrorTaxonomy   []string `json:"error_taxonomy"`
}

// SliceInvariant is an entry of slice-contract.invariants: the statement, its §6.2 DSL expression and how it is checked.
type SliceInvariant struct {
	Text         string `json:"text"`
	DSL          string `json:"dsl,omitempty"`
	Verification string `json:"verification,omitempty"`
}

// Lemma is a pure internal function's declared postcondition (slice-contract functions[].lemmas).
type Lemma struct {
	Name          string `json:"name"`
	Postcondition string `json:"postcondition"`
}

// CandidateLemma is the verdict of one lemma in candidate.json.
type CandidateLemma struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// DependencyChange is one manifest change of a candidate, checked against the allowlist.
type DependencyChange struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Allowed   bool   `json:"allowed"`
}

// FamilyException is roles.family_exception: the written reason that allows two roles of one family.
type FamilyException struct {
	Reason string `json:"reason"`
}

// Canary is delivery.canary: the write paths that get a canary with compensation.
type Canary struct {
	Paths []string `json:"paths"`
}

// DiscoveredEdge is an edge found inside the repo for integration.discovered (blast radius).
type DiscoveredEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Via  string `json:"via"`
}

// CaptureTrace is one recorded call of a changed function in capture.traces.
type CaptureTrace struct {
	Function  string   `json:"function"`
	Input     string   `json:"input"`
	Output    string   `json:"output"`
	BranchIDs []string `json:"branch_ids"`
}

// FuzzResponse is engine-fuzz-response: the findings of the deterministic fuzzing of Stage 4.
type FuzzResponse struct {
	SchemaVersion int           `json:"schema_version"`
	Findings      []FuzzFinding `json:"findings"`
	Runs          int           `json:"runs"`
	Seconds       float64       `json:"seconds"`
	// Unsupported are the changed public functions that could not be fuzzed, with the reason.
	Unsupported []FuzzUnsupported `json:"unsupported,omitempty"`
}

// FuzzUnsupported is a changed public function the engine could not fuzz.
type FuzzUnsupported struct {
	Function string `json:"function"`
	Reason   string `json:"reason"`
}

// FuzzFinding is an input that raised an unexpected error, with the seed that reproduces it.
type FuzzFinding struct {
	Function string `json:"function"`
	Input    string `json:"input"`
	Error    string `json:"error"`
	Seed     int    `json:"seed"`
}

// LemmasResponse is engine-lemmas-response: one result per declared lemma.
type LemmasResponse struct {
	SchemaVersion int           `json:"schema_version"`
	Lemmas        []LemmaResult `json:"lemmas"`
	Error         *string       `json:"error,omitempty"`
}

// LemmaResult is the verdict of one lemma: pass, fail (with a counterexample), inconclusive or no_pair.
type LemmaResult struct {
	Name           string         `json:"name"`
	Status         string         `json:"status"`
	Counterexample map[string]any `json:"counterexample,omitempty"`
	Message        string         `json:"message"`
}

// DepsResponse is engine-deps-response: the import edges between the files of the candidate.
type DepsResponse struct {
	SchemaVersion int        `json:"schema_version"`
	Edges         []DepsEdge `json:"edges"`
	Unresolved    []string   `json:"unresolved"`
	Error         *string    `json:"error,omitempty"`
}

// DepsEdge is one import edge between two files of the repo.
type DepsEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// GoldenCase is one recorded case of the golden master: the hashes of its input and of its output.
type GoldenCase struct {
	ID         string `json:"id"`
	InputHash  string `json:"input_hash"`
	OutputHash string `json:"output_hash"`
	ExitCode   int    `json:"exit_code"`
}

// GoldenResponse is engine-golden-response: the cases of a golden record or replay, and the outputs that differ.
type GoldenResponse struct {
	SchemaVersion int          `json:"schema_version"`
	Cases         []GoldenCase `json:"cases"`
	Diffs         []GoldenDiff `json:"diffs"`
	Error         *string      `json:"error,omitempty"`
}

// GoldenDiff is a replayed case whose output differs from the recorded one.
type GoldenDiff struct {
	ID  string `json:"id"`
	Old string `json:"old"`
	New string `json:"new"`
}

// Golden is golden.json, the Stage -1 golden master record of a refactor or legacy slice.
type Golden struct {
	SchemaVersion int          `json:"schema_version"`
	SliceID       string       `json:"slice_id"`
	Base          string       `json:"base"`
	Image         string       `json:"image"`
	Command       string       `json:"command"`
	Cases         []GoldenCase `json:"cases"`
	RecordedAt    string       `json:"recorded_at"`
}

// Answers is answers.json, the probe answers of a run written by a2dlc answer.
type Answers struct {
	SchemaVersion int      `json:"schema_version"`
	RunID         string   `json:"run_id"`
	ContractHash  string   `json:"contract_hash"`
	Answers       []Answer `json:"answers"`
}

// Answer is one person's choice for one probe.
type Answer struct {
	ProbeID string `json:"probe_id"`
	Choice  string `json:"choice"`
	By      string `json:"by"`
	At      string `json:"at"`
	Reason  string `json:"reason"`
}

// TracesResponse is engine-traces-response: the traces of the base tests under the trace harness (operation capture_traces).
// Runner and TestExit are null when no runner ran; Error is set when the harness could not run at all.
type TracesResponse struct {
	SchemaVersion  int            `json:"schema_version"`
	Runner         *string        `json:"runner"`
	TestExit       *int           `json:"test_exit"`
	Traces         []CaptureTrace `json:"traces"`
	BranchCoverage *float64       `json:"branch_coverage,omitempty"`
	Skipped        []TraceSkip    `json:"skipped"`
	Error          *string        `json:"error"`
}

// TraceSkip is a file the trace harness did not trace, and why.
type TraceSkip struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// ProposeRequest is the payload of role propose_properties (role-payloads.schema.json#/$defs/propose_request).
type ProposeRequest struct {
	EngineLanguage string             `json:"engine_language"`
	Signatures     []ProposeSignature `json:"signatures"`
	Diff           string             `json:"diff"`
}

// ProposeSignature is one public signature of a changed function, without its body.
type ProposeSignature struct {
	File      string `json:"file"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

// ProposeResponse is the payload of an ok response from role propose_properties.
type ProposeResponse struct {
	Properties []ProposedProperty `json:"properties"`
}

// ProposedProperty is a property proposed for a target (<file>::<symbol>), and the file where it belongs.
type ProposedProperty struct {
	Target   string `json:"target"`
	Property string `json:"property"`
	File     string `json:"file"`
}

// DepsAllow is .a2dlc/deps.allow.json: the dependencies an agent may add, per ecosystem.
type DepsAllow struct {
	SchemaVersion int          `json:"schema_version"`
	Allow         []AllowEntry `json:"allow"`
}

// AllowEntry is one allowed dependency; Versions is an optional range in the ecosystem's syntax.
type AllowEntry struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Versions  string `json:"versions,omitempty"`
}

// Timing is one measured step of a check: what ran and how long it took, in milliseconds.
type Timing struct {
	Name string `json:"name"`
	Ms   int64  `json:"ms"`
}

// Timings is verdict.timings: where the check spent its time. Runs that overlap are listed separately, so the entries
// can add up to more than TotalMs.
type Timings struct {
	TotalMs   int64    `json:"total_ms"`
	SyncMs    int64    `json:"sync_ms,omitempty"`
	StubsMs   int64    `json:"stubs_ms,omitempty"`
	TestRuns  []Timing `json:"test_runs,omitempty"`
	EngineOps []Timing `json:"engine_ops,omitempty"`
}
