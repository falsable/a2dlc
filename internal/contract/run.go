package contract

import (
	"encoding/json"
	"fmt"
)

// Types of a2dlc run (ADR meta/0004, amendment 2026-10-17): roles.json, the request/response of a role, state.json and
// ledger.json. They mirror roles, role-request, role-response, run-state and run-ledger of contracts/; the strict
// decoding test fails if they drift apart.

// Protocol roles (§3) that run as a command. The Oracle, the Router and the Indexer are the core.
const (
	RoleSpecifierAlpha    = "specifier_alpha"
	RoleSpecifierBeta     = "specifier_beta"
	RoleInquisitor        = "inquisitor"
	RoleBackTranslator    = "back_translator"
	RoleBuilderPrimary    = "builder_primary"
	RoleBuilderOrthogonal = "builder_orthogonal"
	RoleSaboteur          = "saboteur"
	RoleProposeProperties = "propose_properties"
)

// Stages that a2dlc run runs, in order (§5, from −1 to 7). Stage 6 (integration) and Stage 7 (progressive delivery)
// run the part that fits in the repo's binary: deterministic gates over what is declared in config and intent, and
// the delivery plan as an artifact the platform executes.
var Stages = []string{"-1", "0", "1", "1.5", "2", "3", "4", "5", "6", "7"}

const (
	ModeGreenfield = "greenfield"
	ModeRefactor   = "refactor"
	ModeBrownfield = "brownfield_facade"
	ModeLite       = "lite"
)

type Roles struct {
	// FamilyException is the written reason that allows two roles of the same family (roles.schema.json).
	FamilyException *FamilyException `json:"family_exception,omitempty"`
	SchemaVersion   int              `json:"schema_version"`
	Roles           map[string]Role  `json:"roles"`
}

// roleNames is the closed set of roles (run-defs.schema.json#/$defs/role).
var roleNames = map[string]bool{
	RoleSpecifierAlpha: true, RoleSpecifierBeta: true, RoleInquisitor: true, RoleBackTranslator: true,
	RoleBuilderPrimary: true, RoleBuilderOrthogonal: true, RoleSaboteur: true, RoleProposeProperties: true,
}

type Role struct {
	// Paradigm is the programming paradigm the role is asked to follow (prompt-enforced).
	Paradigm string `json:"paradigm,omitempty"`
	// EgressAllow lists the hostnames a provider role may reach through the egress proxy, which enforces it.
	EgressAllow []string `json:"egress_allow,omitempty"`
	Family      string   `json:"family"`
	Image       string   `json:"image"`
	Argv        []string `json:"argv"`
	EnvFile     string   `json:"env_file,omitempty"`
	Network     string   `json:"network,omitempty"`
	TimeoutS    int      `json:"timeout_s,omitempty"`
	Retries     *int     `json:"retries,omitempty"`
	Memory      string   `json:"memory,omitempty"`
	CPUs        float64  `json:"cpus,omitempty"`

	Env map[string]string `json:"env,omitempty"` // not secret; the credential goes in EnvFile

	// TierUp is the builder's upper tier (CB-02b): the same shape as a role, without its own tier_up (the schema closes it).
	TierUp *Role `json:"tier_up,omitempty"` // a tier_up with its own TierUp is rejected by Role.UnmarshalJSON
}

// HashedFile is a file as the role saw it inside the container, with its sha256.
type HashedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// Cost is what the role's CLI reported; nil if it did not report.
type Cost struct {
	USD          float64 `json:"usd"`
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
}

type RoleRequest struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	SliceID       string          `json:"slice_id"`
	Mode          string          `json:"mode"`
	Role          string          `json:"role"`
	Stage         string          `json:"stage"`
	Attempt       int             `json:"attempt"`
	Inputs        []HashedFile    `json:"inputs"`
	Workdir       *string         `json:"workdir"`
	Payload       json.RawMessage `json:"payload"`
}

const (
	RoleOK      = "ok"
	RoleRefused = "refused"
	RoleError   = "error"
)

type RoleResponse struct {
	SchemaVersion int             `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Role          string          `json:"role"`
	Stage         string          `json:"stage"`
	Attempt       int             `json:"attempt"`
	Status        string          `json:"status"`
	Reason        string          `json:"reason,omitempty"`
	Cost          *Cost           `json:"cost"`
	Model         string          `json:"model,omitempty"`
	Payload       json.RawMessage `json:"payload"`
}

// SaboteurRequest is the payload of the request to the Saboteur (role-payloads.schema.json#/$defs/saboteur_request):
// which changed lines nobody examined and where the tests have to live. The builder's code does not travel.
type SaboteurRequest struct {
	Round         int                 `json:"round"`
	MaxRounds     int                 `json:"max_rounds"`
	Uncovered     []SaboteurUncovered `json:"uncovered"`
	TestsPaths    []string            `json:"tests_paths"`
	Command       string              `json:"command"`
	PreviousTests []string            `json:"previous_tests"`
	Survivors     []SaboteurSurvivor  `json:"survivors,omitempty"` // Stage 5 (block 5): live mutants
}

type SaboteurUncovered struct {
	File  string `json:"file"`
	Lines []int  `json:"lines"`
}

// SaboteurResponse is the payload of an ok response from the Saboteur: the tests it left in /out/tests/<path>.
type SaboteurResponse struct {
	TestFiles []HashedFile `json:"test_files"`
	Targets   []string     `json:"targets"`
}

const (
	StagePending   = "pending"
	StageRunning   = "running"
	StageDone      = "done"
	StageSkipped   = "skipped"
	StageFailed    = "failed"
	StageEscalated = "escalated"
)

type RunState struct {
	SchemaVersion int        `json:"schema_version"`
	RunID         string     `json:"run_id"`
	SliceID       string     `json:"slice_id"`
	Mode          string     `json:"mode"`
	BaseSha       string     `json:"base_sha"`
	CreatedAt     string     `json:"created_at"`
	UpdatedAt     string     `json:"updated_at"`
	Budget        Budget     `json:"budget"`
	Stages        []StageRun `json:"stages"`
	Outcome       *string    `json:"outcome"`
}

type Budget struct {
	// SpentTokens is the sum of the tokens the CLIs reported; roles without a report do not add.
	SpentTokens     *int      `json:"spent_tokens,omitempty"`
	MaxCostUSD      *float64  `json:"max_cost_usd"`
	SpentUSD        float64   `json:"spent_usd"`
	UnreportedRoles int       `json:"unreported_roles"`
	ByRole          RoleSpend `json:"by_role,omitempty"`
}

// RoleSpend is budget.by_role: the USD spent per role. Its keys are the roles of run-defs.schema.json (propertyNames),
// so an unknown role, such as the oracle, is rejected when it is decoded.
type RoleSpend map[string]float64

// UnmarshalJSON rejects a role that is not one of the roles of run-defs.schema.json.
func (s *RoleSpend) UnmarshalJSON(b []byte) error {
	var m map[string]float64
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for role := range m {
		if !roleNames[role] {
			return fmt.Errorf("unknown field %q in budget.by_role: not a role of run-defs.schema.json", role)
		}
	}
	*s = m
	return nil
}

// UnmarshalJSON rejects a tier_up that has its own tier_up: roles.schema.json closes it, and DecodeStrict cannot see
// into a nested role, so the check runs here.
func (r *Role) UnmarshalJSON(b []byte) error {
	type plain Role
	var p plain
	if err := decodeStrictValue(b, &p); err != nil {
		return err
	}
	if p.TierUp != nil && p.TierUp.TierUp != nil {
		return fmt.Errorf("tier_up.tier_up: unknown field \"tier_up\": a tier_up cannot have its own tier_up")
	}
	*r = Role(p)
	return nil
}

type StageRun struct {
	ID         string      `json:"id"`
	Status     string      `json:"status"`
	Reason     string      `json:"reason,omitempty"`
	Artifact   *HashedFile `json:"artifact"`
	Iterations int         `json:"iterations"`
	StartedAt  *string     `json:"started_at"`
	FinishedAt *string     `json:"finished_at"`
}

type RunLedger struct {
	SchemaVersion int           `json:"schema_version"`
	RunID         string        `json:"run_id"`
	Entries       []LedgerEntry `json:"entries"`
}

const (
	LedgerOK              = "ok"
	LedgerRefused         = "refused"
	LedgerError           = "error"
	LedgerTimeout         = "timeout"
	LedgerInvalidResponse = "invalid_response"
)

// LedgerEntry is one launch of a role. Hash = sha256(prev_hash + canonical JSON of the entry without hash); it is
// computed and verified by internal/run.
type LedgerEntry struct {
	Seq         int          `json:"seq"`
	PrevHash    *string      `json:"prev_hash"`
	Hash        string       `json:"hash"`
	At          string       `json:"at"`
	Stage       string       `json:"stage"`
	Role        string       `json:"role"`
	Attempt     int          `json:"attempt"`
	Family      string       `json:"family"`
	Image       string       `json:"image"`
	ImageDigest *string      `json:"image_digest"`
	ArgvSHA256  string       `json:"argv_sha256"`
	Inputs      []HashedFile `json:"inputs"`
	Outputs     []HashedFile `json:"outputs"`
	Network     string       `json:"network"`
	EnvKeys     []string     `json:"env_keys"`
	Exit        int          `json:"exit"`
	Status      string       `json:"status"`
	Cost        *Cost        `json:"cost"`
}
