// Package contract holds the types of contracts/*.schema.json and the contract rules that the core enforces on
// the engines. The schemas win: if this file and a schema differ, the bug is here.
package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
)

const SchemaVersion = 1

const (
	PASS         = "PASS"
	FAIL         = "FAIL"
	INCONCLUSIVE = "INCONCLUSIVE"
)

// Values of Config.RunDivergence.
const (
	RunDivergenceStrict       = "strict"
	RunDivergenceCriticalCode = "critical_code"
)

// Values of Config.RunDifferential (RFC-0005): where Stage 4 of a2dlc run takes the engines' diff from.
const (
	RunDifferentialFresh  = "fresh"
	RunDifferentialStage3 = "stage3"
)

// Values of Config.Rung, the Lite escalation rung a2dlc run climbs to (protocol section 4.4).
const (
	RungGates        = "gates"
	RungCriticalCode = "critical_code"
	RungMutation     = "mutation"
	// RungGatesNoMutation is the reason recorded for mutation (verdict test_gates.mutation, Stage 5 of the run) at RungGates.
	RungGatesNoMutation = "config.rung is gates: mutation is not part of this rung, so no mutant was generated and no kill rate was measured"
	// RungCriticalCodeNoMutation is the reason recorded for mutation (verdict test_gates.mutation, Stage 5 of the run) at
	// RungCriticalCode when no changed file matches config.critical_code and the route's criticality is low.
	RungCriticalCodeNoMutation = "config.rung is critical_code and the change touches no critical_code (route criticality low): mutation is not part of this rung, so no mutant was generated and no kill rate was measured"
	// RungCriticalCodeNoSaboteur is the reason Stage 4 records at RungCriticalCode when uncovered_change is outside
	// config.critical_code and the route's criticality is low.
	RungCriticalCodeNoSaboteur = "config.rung is critical_code and uncovered_change is outside critical_code (route criticality low): the saboteur is not launched"
	// RungCriticalCodeNoDualSpec is the reason Stage 1 records at RungCriticalCode when dual_spec_required is false
	// and the route's criticality is low (track N=1): the builder runs directly from intent.json with zero LLM spec overhead.
	RungCriticalCodeNoDualSpec = "config.rung is critical_code and dual_spec_required is false (route criticality low): dual-spec is not launched; the following stages run from intent.json"
)

// Values of TestsConfig.NopBasePassing (RFC-0003): what the NOP gate does with a new test that passes on the base in a
// change that declares the intent to change behavior.
const (
	NopBasePassingFail  = "fail"
	NopBasePassingGuard = "guard"
)

// Values of TestsConfig.Tier0Command (RFC-0004): which command step 4 of Tier 0 runs.
const (
	Tier0CommandTests    = "command"
	Tier0CommandBaseline = "baseline"
)

type Config struct {
	// ProtectedPaths are globs an agent cannot change without a signed approval (protected_path).
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	// CriticalCode are globs over "file" or "file::symbol" naming the code where a behavior divergence stays FAIL until a
	// signed waiver. Absent means nothing is critical: every divergence is a note in the receipt (VerdictTarget.Note).
	CriticalCode []string `json:"critical_code,omitempty"`
	// RunDivergence is how a2dlc run judges a behavior divergence in Stage 4: RunDivergenceStrict (also when absent) keeps
	// every one a FAIL; RunDivergenceCriticalCode applies CriticalCode as a2dlc check does.
	RunDivergence string `json:"run_divergence,omitempty"`
	// RunDifferential is RunDifferentialFresh (also when absent) or RunDifferentialStage3: Stage 4 of a2dlc run takes, for each
	// engine, the diff answer that Stage 3 got on the builder's last round when the candidate is that round's tree and the
	// request is the same, instead of running the engine again (RFC-0005).
	RunDifferential string `json:"run_differential,omitempty"`
	// Rung is how far a2dlc run climbs: RungMutation (also when absent) runs Stage 5 after Stage 4; RungGates stops after the
	// oracle, the tests and their gates, skips Stage 5 and records that mutation was not run; RungCriticalCode climbs to the
	// Saboteur in Stage 4 and to Stage 5 mutation only when a changed file matches CriticalCode or the Stage 2 route has
	// criticality_effective above low.
	Rung string `json:"rung,omitempty"`
	// SupplyChain names the dependency allowlist file (config.supply_chain).
	SupplyChain   *SupplyChain    `json:"supply_chain,omitempty"`
	SchemaVersion int             `json:"schema_version"`
	Engines       []EngineConfig  `json:"engines"`
	MaxFiles      int             `json:"max_files,omitempty"`
	AgentHook     *AgentHook      `json:"agent_hook,omitempty"`
	Tests         *TestsConfig    `json:"tests,omitempty"`
	Mutation      *MutationConfig `json:"mutation,omitempty"`
	// Tier0 is the AST policy of Stage 3 (policy.go); nil means the engines' default policy.
	Tier0 *PolicyTier0 `json:"tier0,omitempty"`
	// Integration (Stage 6 of a2dlc run): consumer matrix and migrations linter; nil leaves the stage skipped.
	Integration *IntegrationConfig `json:"integration,omitempty"`
	// Checks are the pluggable deterministic checks (protocol §5 Checks, ADR meta/0006): they lower the verdict, never raise it.
	Checks []CheckConfig `json:"checks,omitempty"`
}

// Check severities (config.checks[].severity): blocking lowers the verdict, advisory is recorded only.
const (
	CheckAdvisory = "advisory"
	CheckBlocking = "blocking"
)

// Statuses of a check in verdict.checks ($defs/check). A response says ran or not_applicable; the core adds the rest.
const (
	CheckRan           = "ran"
	CheckNotApplicable = "not_applicable"
	CheckNotConfigured = "not_configured"
	CheckError         = "error"
	CheckTimeout       = "timeout"
)

// CheckConfig is one pluggable deterministic check: an image run without network over /base:ro and /cand:ro that
// writes a CheckResponse to $A2DLC_OUT. FireOn defaults to ["check"]; Matches absent means every changed file.
type CheckConfig struct {
	ID       string   `json:"id"`
	Image    string   `json:"image"`
	Command  string   `json:"command"`
	Severity string   `json:"severity"`
	FireOn   []string `json:"fire_on,omitempty"`
	Matches  []string `json:"matches,omitempty"`
	TimeoutS int      `json:"timeout_s,omitempty"`
	Memory   string   `json:"memory,omitempty"`
	CPUs     float64  `json:"cpus,omitempty"`
}

// CheckResponse is what a check writes to $A2DLC_OUT (check-response.schema.json). It never says "error" about
// itself: the core records that.
type CheckResponse struct {
	SchemaVersion int            `json:"schema_version"`
	ID            string         `json:"id"`
	Status        string         `json:"status"`
	Pass          bool           `json:"pass"`
	Findings      []CheckFinding `json:"findings"`
	Tool          *CheckTool     `json:"tool,omitempty"`
}

// CheckFinding is bound to a file: <file>::<check id> or <file>::L<line>.
type CheckFinding struct {
	Target  string `json:"target"`
	Message string `json:"message"`
}

type CheckTool struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// VerdictCheck is one check in verdict.checks: the mirror of Gate for pluggable checks. Pass and Findings are set only
// when it ran (Findings points to an empty list when it ran clean: the key is present, as the schema requires).
type VerdictCheck struct {
	ID       string                 `json:"id"`
	Severity string                 `json:"severity"`
	Status   string                 `json:"status"`
	Reason   string                 `json:"reason,omitempty"`
	Pass     *bool                  `json:"pass,omitempty"`
	Findings *[]VerdictCheckFinding `json:"findings,omitempty"`
	Tool     *CheckTool             `json:"tool,omitempty"`
}

// FindingList is the findings of a check that ran; nil otherwise.
func (c VerdictCheck) FindingList() []VerdictCheckFinding {
	if c.Findings == nil {
		return nil
	}
	return *c.Findings
}

type VerdictCheckFinding struct {
	Target  string `json:"target"`
	Message string `json:"message"`
	Waived  bool   `json:"waived"`
}

// TestsConfig holds the cheap Tier 1 gates (protocol §5, Stage 4 step 0).
type TestsConfig struct {
	// TraceCoverage is the minimum branch coverage of the recorded traces; nil means it was not declared (INCONCLUSIVE).
	TraceCoverage *TraceCoverageConfig `json:"trace_coverage,omitempty"`
	// GoldenMaster is the refactor and legacy oracle recorded on the base (Stage -1).
	GoldenMaster        *GoldenMaster `json:"golden_master,omitempty"`
	Image               string        `json:"image"`
	Paths               []string      `json:"paths"`
	SourcePaths         []string      `json:"source_paths,omitempty"`
	Command             string        `json:"command"`
	BaselineCommand     string        `json:"baseline_command,omitempty"`
	InvalidBaseFailures []string      `json:"invalid_base_failures,omitempty"`
	// NopBasePassing is NopBasePassingFail (also when absent) or NopBasePassingGuard: a base-passing new test is a
	// regression guard when another new test in the same test file proves the change.
	NopBasePassing string `json:"nop_base_passing,omitempty"`
	// Tier0Command is Tier0CommandTests (also when absent) or Tier0CommandBaseline: Tier 0's step 4 runs baseline_command,
	// with the selection and the cache of Stage 4's baseline gate.
	Tier0Command string  `json:"tier0_command,omitempty"`
	Reruns       int     `json:"reruns,omitempty"`
	TimeoutS     int     `json:"timeout_s,omitempty"`
	Memory       string  `json:"memory,omitempty"`
	CPUs         float64 `json:"cpus,omitempty"`
	// StubEngine is asked for stubs only (the NOP gate's base): it is no entry of Config.Engines, so it reviews no file and
	// counts for no Tier 0 review. Its language also picks the default InvalidBaseFailures.
	StubEngine *EngineConfig `json:"stub_engine,omitempty"`
}

type EngineConfig struct {
	Language string          `json:"language"`
	Image    string          `json:"image"`
	Paths    []string        `json:"paths"`
	Budget   json.RawMessage `json:"budget"`
	Memory   string          `json:"memory,omitempty"`
	CPUs     float64         `json:"cpus,omitempty"`
}

type AgentHook struct {
	MaxBlocks int `json:"max_blocks,omitempty"`
}

type Request struct {
	SchemaVersion      int             `json:"schema_version"`
	Language           string          `json:"language"`
	BaseRef            string          `json:"base_ref"`
	BaseDir            string          `json:"base_dir"`
	CandidateDir       string          `json:"candidate_dir"`
	ChangedFiles       []string        `json:"changed_files"`
	Budget             json.RawMessage `json:"budget"`
	GeneratorsOverride *string         `json:"generators_override"`
	Operation          string          `json:"operation,omitempty"`
	// EquivalenceRelaxations (block 7): pi_s.relaxations of S*, only in diff; nil leaves it out of the request.
	EquivalenceRelaxations []string `json:"equivalence_relaxations,omitempty"`
}

// UnmarshalJSON decodes a request strictly, like DecodeStrict does at the top level, and then its budget into the type
// that engine-request.schema.json gives the operation (the allOf branches). Without it the budget was an opaque
// RawMessage, and an unknown key in it was accepted.
func (r *Request) UnmarshalJSON(b []byte) error {
	type plain Request
	var p plain
	if err := decodeStrictValue(b, &p); err != nil {
		return err
	}
	if err := checkBudget(p.Operation, p.Budget); err != nil {
		return err
	}
	*r = Request(p)
	return nil
}

// decodeStrictValue decodes b into v with unknown fields rejected at every depth.
func decodeStrictValue(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}

// checkBudget decodes the budget of the operations that the schema types strictly. diff, stub, signatures and
// complexity take any object: their budget is opaque by design (each engine reads its own keys).
func checkBudget(operation string, budget json.RawMessage) error {
	if len(budget) == 0 {
		return nil
	}
	if string(bytes.TrimSpace(budget)) == "null" {
		return fmt.Errorf("budget of operation %s: must be an object", operation)
	}
	var shape any
	switch operation {
	case "tier0":
		shape = &Tier0Budget{}
	case "mutate":
		shape = &MutateBudget{}
	case "fuzz":
		shape = &FuzzBudget{}
	case "lemmas":
		shape = &LemmasBudget{}
	case "deps_graph":
		shape = &struct{}{}
	case "golden_record", "golden_replay":
		shape = &GoldenBudget{}
	case "capture_traces":
		shape = &CaptureTracesBudget{}
	case "classify_lines":
		shape = &ClassifyBudget{}
	default:
		return nil
	}
	if err := decodeStrictValue(budget, shape); err != nil {
		return fmt.Errorf("budget of operation %s: %w", operation, err)
	}
	return nil
}

// FuzzBudget is the budget of operation: fuzz (engine-request, seconds and seed, both required).
type FuzzBudget struct {
	Seconds int `json:"seconds"`
	Seed    int `json:"seed"`
}

// LemmasBudget is the budget of operation: lemmas (the declared postconditions and how to check them).
type LemmasBudget struct {
	Functions []LemmaSpec `json:"functions"`
	Mode      string      `json:"mode"`
}

// LemmaSpec is one declared lemma: a pure internal function and its postcondition.
type LemmaSpec struct {
	Name          string `json:"name"`
	Postcondition string `json:"postcondition"`
}

// GoldenBudget is the budget of operation: golden_record and golden_replay (the case ids of the golden master).
type GoldenBudget struct {
	Cases []string `json:"cases"`
}

// CaptureTracesBudget is the budget of operation: capture_traces. TimeoutS nil means the engine's default (300 s).
type CaptureTracesBudget struct {
	TimeoutS *float64 `json:"timeout_s,omitempty"`
}

// StubResponse is the response to operation: stub (engine-stub-response.schema.json).
type StubResponse struct {
	SchemaVersion int        `json:"schema_version"`
	Stubbed       []StubFile `json:"stubbed"`
	Unstubbed     []string   `json:"unstubbed"`
	// ModifiedTests are the test ids (file::Class.name or file::name) whose body changed; the NOP gate judges them like
	// new tests, and one that still passes on the base with stubs is INCONCLUSIVE: it may have lost its assert.
	ModifiedTests []string `json:"modified_tests,omitempty"`
	Error         *string  `json:"error"`
}

type StubFile struct {
	File    string   `json:"file"`
	Symbols []string `json:"symbols"`
}

// SignaturesResponse is the response to operation: signatures (engine-signatures-response.schema.json): the
// public signatures of the candidate, without bodies. It is the only part of the code the Saboteur sees.
type SignaturesResponse struct {
	SchemaVersion int             `json:"schema_version"`
	Files         []SignatureFile `json:"files"`
	Error         *string         `json:"error"`
}

type SignatureFile struct {
	File    string      `json:"file"`
	Symbols []Signature `json:"symbols"`
}

type Signature struct {
	Name      string  `json:"name"`
	Signature string  `json:"signature"`
	Doc       *string `json:"doc"`
	Lines     []int   `json:"lines"`
}

type Counterexample struct {
	Args string `json:"args"`
	Old  string `json:"old"`
	New  string `json:"new"`
}

type DomainGuard struct {
	Expr   string `json:"expr"`
	Source string `json:"source"`
}

type TargetResult struct {
	Target         string          `json:"target"`
	Engine         string          `json:"engine"`
	EngineVersion  string          `json:"engine_version"`
	Verdict        string          `json:"verdict"`
	Reason         string          `json:"reason,omitempty"`
	SearchBudget   json.RawMessage `json:"search_budget"`
	Generators     string          `json:"generators"`
	Typed          bool            `json:"typed"`
	DomainGuard    *DomainGuard    `json:"domain_guard"`
	Equivalence    string          `json:"equivalence"`
	Lines          []int           `json:"lines,omitempty"` // [first, last] line of the symbol in the candidate; empty = whole file
	Counterexample *Counterexample `json:"counterexample"`
}

// Covers reports whether the target examined line l of the candidate: without lines, a target covers its whole file.
func (t TargetResult) Covers(l int) bool {
	return len(t.Lines) != 2 || (t.Lines[0] <= l && l <= t.Lines[1])
}

type OutOfScope struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

type Response struct {
	SchemaVersion int            `json:"schema_version"`
	Targets       []TargetResult `json:"targets"`
	OutOfScope    []OutOfScope   `json:"out_of_scope"`
	Error         *string        `json:"error"`
	// Identical: the "file::name" symbols the engine did not judge because no input can tell them apart in base and
	// candidate (engine-response.schema.json).
	Identical []string `json:"identical,omitempty"`
	// EquivalenceUnsupported (block 7): the requested relaxations the engine could not apply.
	EquivalenceUnsupported []string `json:"equivalence_unsupported,omitempty"`
	// DiffBudget is the engine's per-diff budget (same shape as verdict.diff_budget); the core copies it into the verdict.
	DiffBudget *DiffBudget `json:"diff_budget,omitempty"`
}

type VerdictTarget struct {
	TargetResult
	Waived bool `json:"waived"`
	// Note is set on a FAIL target with a counterexample that is not critical code (config.critical_code) and has no
	// waiver (behavior_divergence), or on an INCONCLUSIVE target with no waiver, on code that is not critical, whose changed
	// lines the candidate's tests executed (inconclusive_covered_by_tests): it stays in the receipt and does not count
	// toward the verdict. Absent in receipts before the notes existed. It is a category of its own, apart from
	// coverage.files[].noted.
	Note string `json:"note,omitempty"`
}

// NoteBehaviorDivergence is a value of VerdictTarget.Note: the engines found a counterexample on code not declared critical.
const NoteBehaviorDivergence = "behavior_divergence"

// NoteInconclusiveCoveredByTests is a value of VerdictTarget.Note: the engine could not judge the target, and the tests of
// the candidate executed every changed line of it, so the engine's silence does not make the diff INCONCLUSIVE.
const NoteInconclusiveCoveredByTests = "inconclusive_covered_by_tests"

type EngineError struct {
	Language string   `json:"language"`
	Files    []string `json:"files"`
	Error    string   `json:"error"`
}

type Verdict struct {
	// DiffBudget is the per-diff time budget of the engines; the functions it cut are listed, never omitted.
	DiffBudget     *DiffBudget     `json:"diff_budget,omitempty"`
	SchemaVersion  int             `json:"schema_version"`
	A2dlcVersion   string          `json:"a2dlc_version"`
	Verdict        string          `json:"verdict"`
	BaseRef        string          `json:"base_ref"`
	TreeHash       string          `json:"tree_hash"`
	CreatedAt      string          `json:"created_at"`
	Stage          string          `json:"stage,omitempty"`         // v2 (block 8): a2dlc run stage that emitted it
	ContractHash   string          `json:"contract_hash,omitempty"` // v2 (block 8): sha256 of the run's contract.json
	Targets        []VerdictTarget `json:"targets"`
	OutOfScope     []OutOfScope    `json:"out_of_scope"`
	EngineErrors   []EngineError   `json:"engine_errors,omitempty"`
	PendingWaivers []string        `json:"pending_waivers"`
	TestGates      *TestGates      `json:"test_gates,omitempty"`
	Coverage       *Coverage       `json:"coverage,omitempty"`
	Tests          []TestResult    `json:"tests,omitempty"`
	// Checks are the pluggable checks (config.checks) in config order; what did not run stays in view.
	Checks []VerdictCheck `json:"checks,omitempty"`
	// Authorization is RFC-0007 Part B's account of the pre-authorizations that mattered to this check (omitted otherwise).
	Authorization *VerdictAuthorization `json:"authorization,omitempty"`
	// Timings says where the check spent its time (ms); optional, the verdict does not depend on it.
	Timings *Timings `json:"timings,omitempty"`
	// Level and Static mark a level 0 receipt (a2dlc check --level 0). Level is a pointer because 0 is a value: omitempty
	// on an int would write a level 0 receipt as if it were level 1.
	Level  *int          `json:"level,omitempty"`
	Static *StaticReport `json:"static,omitempty"`
}

// States of the coverage precondition.
const (
	CoverageRan           = "ran"
	CoverageUnavailable   = "unavailable"
	CoverageNotConfigured = "not_configured"
)

// Coverage is the coverage precondition: per changed file in scope, which lines an engine examined, which a test
// executed on the candidate, and which nobody looked at. With uncovered non-empty the verdict cannot be PASS.
type Coverage struct {
	Status string         `json:"status"`
	Reason string         `json:"reason,omitempty"`
	Files  []CoverageFile `json:"files"`
}

type CoverageFile struct {
	File      string `json:"file"`
	ByEngine  []int  `json:"by_engine"`
	Covered   []int  `json:"covered"`
	Uncovered []int  `json:"uncovered"`
	// Noted are unexamined lines that the engine classified as a defensive handler or declaration-only code: they are
	// stated here and are not in Uncovered, so they do not block. Absent in receipts before the classification existed.
	Noted []LineNote `json:"noted,omitempty"`
}

// States of a testcase in a run (defs.schema.json#/$defs/test_status).
const (
	TestPassed  = "passed"
	TestFailed  = "failed"
	TestError   = "error"
	TestSkipped = "skipped"
	TestMissing = "missing"
)

// States of a test gate.
const (
	GateRan           = "ran"
	GateNotApplicable = "not_applicable"
	GateNotConfigured = "not_configured"
	GateError         = "error"
)

const (
	GateNOP       = "nop_oracle"
	GateBaseline  = "baseline"
	GateTestsDiff = "tests_diff"
)

type TestGates struct {
	// EquivalenceS is the N=2 differential (candidate against candidate on the same inputs).
	EquivalenceS *Gate `json:"equivalence_s,omitempty"`
	NOPOracle    Gate  `json:"nop_oracle"`
	Baseline     Gate  `json:"baseline"`
	TestsDiff    *Gate `json:"tests_diff,omitempty"`
	// Mutation is set only when config.rung skips mutation (RungGates, or RungCriticalCode when nothing critical changed):
	// not_applicable, with the rung as the reason.
	Mutation *Gate `json:"mutation,omitempty"`
}

type Gate struct {
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Cached bool     `json:"cached,omitempty"`
	Stubs  []string `json:"stubs,omitempty"`
	Trees  []string `json:"trees,omitempty"`
	Tests  []string `json:"tests,omitempty"`

	// CandidateCached is baseline only: the candidate's run came from the cache too, because the same tree had already
	// answered the same command in the same image.
	CandidateCached bool `json:"candidate_cached,omitempty"`
}

type TestResult struct {
	Target           string            `json:"target"`
	Gate             string            `json:"gate"`
	Verdict          string            `json:"verdict"`
	Reason           string            `json:"reason,omitempty"`
	Base             string            `json:"base"`
	Candidate        string            `json:"candidate"`
	Trees            map[string]string `json:"trees,omitempty"`
	Origin           string            `json:"origin,omitempty"`
	Message          string            `json:"message,omitempty"`
	Characterization bool              `json:"characterization,omitempty"`
	Guard            bool              `json:"guard,omitempty"`
	Flaky            bool              `json:"flaky,omitempty"`
	Reruns           *Reruns           `json:"reruns,omitempty"`
	Waived           bool              `json:"waived"`
}

type Reruns struct {
	Base      []string `json:"base"`
	Candidate []string `json:"candidate"`
}

// Escalations is .a2dlc/escalations.json (escalation.schema.json).
type Escalations struct {
	SchemaVersion int          `json:"schema_version"`
	TreeHash      string       `json:"tree_hash"`
	CreatedAt     string       `json:"created_at"`
	Items         []Escalation `json:"items"`
}

type Escalation struct {
	// AnswerID is the probe id a person answered, when the escalation was resolved from answers.json.
	AnswerID    string   `json:"answer_id,omitempty"`
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Target      string   `json:"target"`
	Summary     string   `json:"summary"`
	Evidence    Evidence `json:"evidence"`
	ResolveWith []string `json:"resolve_with"`
	Needs       *Needs   `json:"needs,omitempty"`
	ClusterID   string   `json:"cluster_id,omitempty"` // v2 (block 8): root cause, see ClusterID
	Builder     string   `json:"builder,omitempty"`    // v2 (block 8): with N=2, which candidate it belongs to
}

// Needs is what an outside role (Inquisitor, Saboteur) must bring to resolve the escalation.
type Needs struct {
	Role     string `json:"role"`
	Artifact string `json:"artifact"`
}

type Evidence struct {
	Counterexample *Counterexample   `json:"counterexample,omitempty"`
	Base           string            `json:"base,omitempty"`
	Candidate      string            `json:"candidate,omitempty"`
	RerunsBase     []string          `json:"reruns_base,omitempty"`
	Trees          map[string]string `json:"trees,omitempty"`
	Lines          []int             `json:"lines,omitempty"`
	Message        string            `json:"message,omitempty"`
	Mutant         *Mutant           `json:"mutant,omitempty"`
}

// Waiver decisions: the first two cover engine targets; the next two, tests; the last one, a finding of a pluggable
// check (<file>::<check id> or <file>::L<line>).
const (
	EnforceNewBehavior   = "enforce_new_behavior"
	PreserveLegacy       = "preserve_legacy"
	CharacterizationTest = "characterization_test"
	QuarantineFlaky      = "quarantine_flaky"
	AcceptFinding        = "accept_finding"
)

// IsTestDecision reports whether a waiver decision belongs to the test class.
func IsTestDecision(d string) bool { return d == CharacterizationTest || d == QuarantineFlaky }

type Waiver struct {
	Target   string `json:"target"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type Waivers struct {
	SchemaVersion int      `json:"schema_version"`
	Waivers       []Waiver `json:"waivers"`
}

type Approval struct {
	Target       string `json:"target"`
	WaiverSHA256 string `json:"waiver_sha256"`
	FileBlob     string `json:"file_blob"`
	Principal    string `json:"principal"`
	Signature    string `json:"signature"`
}

type Approvals struct {
	SchemaVersion int        `json:"schema_version"`
	Approvals     []Approval `json:"approvals"`
}

// Authorization is a person's signed pre-authorization of behavior changes (authorization.schema.json, RFC-0007 Part B).
type Authorization struct {
	BaseRef      string   `json:"base_ref"`
	IntentSHA256 string   `json:"intent_sha256"`
	Targets      []string `json:"targets"`
	Decision     string   `json:"decision"`
	ExpiresAt    string   `json:"expires_at"`
	Principal    string   `json:"principal"`
	Signature    string   `json:"signature"`
}

// VerdictAuthorization is the receipt's account of Part B: the run-bound intent, the targets an authorization covered and
// the ones it was refused for.
type VerdictAuthorization struct {
	IntentSHA256 string                 `json:"intent_sha256,omitempty"`
	Covered      []AuthorizedTarget     `json:"covered"`
	Refused      []AuthorizationRefusal `json:"refused"`
}

type AuthorizedTarget struct {
	Target    string `json:"target"`
	ID        string `json:"id"`
	Principal string `json:"principal"`
	ExpiresAt string `json:"expires_at"`
}

type AuthorizationRefusal struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

type Authorizations struct {
	SchemaVersion  int             `json:"schema_version"`
	Authorizations []Authorization `json:"authorizations"`
}

// DecodeStrict decodes, rejecting unknown fields (additionalProperties: false in the schemas) and a document that lacks
// a field its schema requires (required.go), at every depth. v is a pointer to the type of a registered schema; any other
// type is decoded with the unknown-field check only.
func DecodeStrict(r io.Reader, v any) error {
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return fmt.Errorf("extra content after the JSON document")
	}
	return checkRequired(b, v)
}

func DecodeStrictBytes(b []byte, v any) error { return DecodeStrict(bytes.NewReader(b), v) }

var (
	targetRe  = regexp.MustCompile(`^[^\\]+::.+$`)
	engines   = map[string]bool{"fast-check": true, "crosshair-diffbehavior": true, "hypothesis": true, "rapid": true, "blackbox": true}
	oosReason = map[string]bool{"no_engine": true, "not_pure": true, "no_types": true, "not_exported": true, "excluded_by_config": true, "deleted": true, "added": true, "test_code": true}
)

// TargetFile returns the path of the file of a target "<file>::<symbol>".
func TargetFile(target string) string {
	if i := bytes.Index([]byte(target), []byte("::")); i >= 0 {
		return target[:i]
	}
	return target
}

// Validate applies to an engine's response the engine-response.schema.json rules that matter to the core, and
// also checks that every target and every out-of-scope file is among the requested files.
func (r *Response) Validate(requests []string) error {
	if r.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version %d, expected %d", r.SchemaVersion, SchemaVersion)
	}
	if r.Targets == nil || r.OutOfScope == nil {
		return fmt.Errorf("targets and out_of_scope are required (a list, even if empty)")
	}
	request := map[string]bool{}
	for _, f := range requests {
		request[f] = true
	}
	for _, t := range r.Targets {
		switch {
		case !targetRe.MatchString(t.Target):
			return fmt.Errorf("target %q does not have the form <file>::<symbol>", t.Target)
		case !request[TargetFile(t.Target)]:
			return fmt.Errorf("target %q belongs to a file that was not requested", t.Target)
		case !engines[t.Engine]:
			return fmt.Errorf("target %q: unknown engine %q", t.Target, t.Engine)
		case t.EngineVersion == "":
			return fmt.Errorf("target %q: engine_version is missing", t.Target)
		case len(t.SearchBudget) == 0 || string(t.SearchBudget) == "null":
			return fmt.Errorf("target %q: a verdict without search_budget is not valid (§6.4)", t.Target)
		case t.Generators != "typed_default" && t.Generators != "repo_override":
			return fmt.Errorf("target %q: generators %q", t.Target, t.Generators)
		case t.Equivalence != "strict":
			return fmt.Errorf("target %q: equivalence %q", t.Target, t.Equivalence)
		}
		switch t.Verdict {
		case PASS:
		case FAIL:
			if t.Counterexample == nil {
				return fmt.Errorf("target %q: FAIL without a counterexample", t.Target)
			}
		case INCONCLUSIVE:
			if t.Reason == "" {
				return fmt.Errorf("target %q: INCONCLUSIVE without reason", t.Target)
			}
		default:
			return fmt.Errorf("target %q: verdict %q", t.Target, t.Verdict)
		}
	}
	for _, o := range r.OutOfScope {
		if !request[o.File] || !oosReason[o.Reason] {
			return fmt.Errorf("invalid out_of_scope: %+v", o)
		}
	}
	return nil
}
