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

type Config struct {
	// ProtectedPaths are globs an agent cannot change without a signed approval (protected_path).
	ProtectedPaths []string `json:"protected_paths,omitempty"`
	// CriticalCode are globs over "file" or "file::symbol" naming the code where a behavior divergence stays FAIL until a
	// signed waiver. Absent means nothing is critical: every divergence is a note in the receipt (VerdictTarget.Note).
	CriticalCode []string `json:"critical_code,omitempty"`
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
	Reruns              int           `json:"reruns,omitempty"`
	TimeoutS            int           `json:"timeout_s,omitempty"`
	Memory              string        `json:"memory,omitempty"`
	CPUs                float64       `json:"cpus,omitempty"`
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
	// ModifiedTests are the test ids whose body changed; the NOP gate judges them as new tests.
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
	// waiver: it stays in the receipt and does not fail the verdict. Absent in receipts before the notes existed. It is
	// a category of its own, apart from coverage.files[].noted.
	Note string `json:"note,omitempty"`
}

// NoteBehaviorDivergence is the value of VerdictTarget.Note: the engines found a counterexample on code not declared critical.
const NoteBehaviorDivergence = "behavior_divergence"

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
}

type Gate struct {
	Status string   `json:"status"`
	Reason string   `json:"reason,omitempty"`
	Cached bool     `json:"cached,omitempty"`
	Stubs  []string `json:"stubs,omitempty"`
	Trees  []string `json:"trees,omitempty"`
	Tests  []string `json:"tests,omitempty"`
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

// DecodeStrict decodes, rejecting unknown fields, like additionalProperties: false in the schemas.
func DecodeStrict(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if d.More() {
		return fmt.Errorf("extra content after the JSON document")
	}
	return nil
}

func DecodeStrictBytes(b []byte, v any) error { return DecodeStrict(bytes.NewReader(b), v) }

var (
	targetRe  = regexp.MustCompile(`^[^\\]+::.+$`)
	engines   = map[string]bool{"fast-check": true, "crosshair-diffbehavior": true, "hypothesis": true, "rapid": true, "blackbox": true}
	oosReason = map[string]bool{"no_engine": true, "not_pure": true, "no_types": true, "not_exported": true, "excluded_by_config": true, "deleted": true, "added": true}
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
