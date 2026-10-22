package contract

import (
	"fmt"
	"regexp"
	"strings"
)

// Types of Stage 6 (Integration) and Stage 7 (Progressive Delivery) of a2dlc run (protocol §5): the integration
// section of config.json, the delivery section of the intent, and the two artifacts the stages leave behind. They
// mirror config.schema.json#integration, intent.schema.json#delivery, integration.schema.json and delivery.schema.json.

// IntegrationConfig is config.integration: what fits in the provider repo's binary.
type IntegrationConfig struct {
	// GeneratedClients are globs of generated client code (OpenAPI or protobuf output).
	GeneratedClients []string          `json:"generated_clients,omitempty"`
	Consumers        []ConsumerConfig  `json:"consumers,omitempty"`
	Migrations       *MigrationsConfig `json:"migrations,omitempty"`
	Indexer          *IndexerConfig    `json:"indexer,omitempty"`
}

// IndexerConfig is config.integration.indexer: Code Graph Indexer configuration (ChronosGraph).
type IndexerConfig struct {
	Command  string `json:"command"`
	MaxDepth int    `json:"max_depth,omitempty"`
}

// BlastRadiusReport is integration.blast_radius: downstream impact and risk ranking from the Code Graph Indexer.
type BlastRadiusReport struct {
	TotalImpacted int                `json:"total_impacted"`
	Ranking       []BlastRadiusEntry `json:"ranking"`
}

// BlastRadiusEntry is one file's risk and impact in blast_radius.ranking.
type BlastRadiusEntry struct {
	File              string  `json:"file"`
	BlastRadius       int     `json:"blast_radius"`
	RiskScore         float64 `json:"risk_score"`
	HighRiskFunctions int     `json:"high_risk_functions"`
}

// IndexerResponse is the output of the Code Graph Indexer (ChronosGraph).
type IndexerResponse struct {
	SchemaVersion int                `json:"schema_version"`
	Edges         []DepsEdge         `json:"edges"`
	BlastRadius   *BlastRadiusReport `json:"blast_radius,omitempty"`
	Error         *string            `json:"error,omitempty"`
}

// ConsumerConfig is a consumer of the contract matrix: an image with its Tier 1 suite or its Pact contracts, which
// runs without network against the candidate in /work. Contracts are globs of the provider it consumes; empty = always runs.
type ConsumerConfig struct {
	Name      string   `json:"name"`
	Image     string   `json:"image"`
	Command   string   `json:"command"`
	Contracts []string `json:"contracts,omitempty"`
	TimeoutS  int      `json:"timeout_s,omitempty"`
	Memory    string   `json:"memory,omitempty"`
	CPUs      float64  `json:"cpus,omitempty"`
	// Auto: a consumer discovered inside the repo (import graph) runs its tests only when this is true.
	Auto bool `json:"auto,omitempty"`
}

// MigrationsConfig is the destructive DDL linter (expand-and-contract): which files it looks at and which regexes
// are added to the core's.
type MigrationsConfig struct {
	Paths     []string `json:"paths"`
	Forbidden []string `json:"forbidden,omitempty"`
}

var consumerRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

// Validate repeats in Go the rules of config.schema.json#integration that DecodeStrict does not see: name with pattern
// and not repeated, non-empty image and command, migrations with at least one path and extra regexes that compile
// (RE2). Without this, migrations without paths would not look at any file and the stage would silently PASS.
func (ic IntegrationConfig) Validate() error {
	seen := map[string]bool{}
	for i, c := range ic.Consumers {
		switch {
		case !consumerRe.MatchString(c.Name):
			return fmt.Errorf("integration.consumers[%d].name %q: ^[a-z][a-z0-9_.-]{0,63}$", i, c.Name)
		case seen[c.Name]:
			return fmt.Errorf("integration.consumers: %q repeated", c.Name)
		case strings.TrimSpace(c.Image) == "":
			return fmt.Errorf("integration.consumers[%s] without image", c.Name)
		case strings.TrimSpace(c.Command) == "":
			return fmt.Errorf("integration.consumers[%s] without command", c.Name)
		}
		seen[c.Name] = true
		for _, g := range c.Contracts {
			if g == "" {
				return fmt.Errorf("integration.consumers[%s].contracts with an empty glob", c.Name)
			}
		}
	}
	if m := ic.Migrations; m != nil {
		if len(m.Paths) == 0 {
			return fmt.Errorf("integration.migrations.paths is empty: the linter would not look at any file")
		}
		for _, g := range m.Paths {
			if g == "" {
				return fmt.Errorf("integration.migrations.paths with an empty glob")
			}
		}
		for _, e := range m.Forbidden {
			if _, err := regexp.Compile("(?i)" + e); err != nil {
				return fmt.Errorf("integration.migrations.forbidden %q: %v", e, err)
			}
		}
	}
	if idx := ic.Indexer; idx != nil {
		if strings.TrimSpace(idx.Command) == "" {
			return fmt.Errorf("integration.indexer without command")
		}
	}
	return nil
}

const (
	ConsumerPassed       = "passed"
	ConsumerFailed       = "failed"
	ConsumerInconclusive = "inconclusive"
	ConsumerNotRun       = "not_run"
)

// IntegrationReport is integration.json, the artifact of Stage 6.
type IntegrationReport struct {
	// Discovered are the edges found inside the repo (imports, OpenAPI, protobuf, code graph).
	Discovered    []DiscoveredEdge   `json:"discovered,omitempty"`
	BlastRadius   *BlastRadiusReport `json:"blast_radius,omitempty"`
	SchemaVersion int                `json:"schema_version"`
	SliceID       string             `json:"slice_id"`
	BaseSha       string             `json:"base_sha"`
	Candidate     *string            `json:"candidate"`
	ChangedFiles  []string           `json:"changed_files"`
	Consumers     []ConsumerResult   `json:"consumers"`
	Migrations    MigrationsReport   `json:"migrations"`
	Verdict       string             `json:"verdict"`
}

type ConsumerResult struct {
	Name        string         `json:"name"`
	Image       string         `json:"image"`
	AffectedBy  []string       `json:"affected_by"`
	Status      string         `json:"status"`
	Tests       *ConsumerTests `json:"tests,omitempty"`
	FailedTests []string       `json:"failed_tests,omitempty"`
	Reason      string         `json:"reason,omitempty"`
}

type ConsumerTests struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type MigrationsReport struct {
	Files       []string         `json:"files"`
	Destructive []DestructiveDDL `json:"destructive"`
}

type DestructiveDDL struct {
	File      string `json:"file"`
	Line      int    `json:"line"`
	Rule      string `json:"rule"`
	Statement string `json:"statement"`
}

// Delivery is intent.delivery: what the slice declares for the Stage 7 plan.
type Delivery struct {
	// ReadOnlyPaths are globs the slice reads and must not write; parity runs on them (delivery.parity.paths).
	ReadOnlyPaths []string      `json:"read_only_paths,omitempty"`
	Writes        *bool         `json:"writes"` // required in the schema; a pointer to tell absent from false
	Compensation  *Compensation `json:"compensation,omitempty"`
	Rings         []int         `json:"rings,omitempty"`
	Sentinel      *Sentinel     `json:"sentinel,omitempty"`
}

const (
	CompensatingEvents = "compensating_events"
	DataRepairScript   = "data_repair_script"
	VersionedRecords   = "versioned_records"
)

type Compensation struct {
	Strategy    string `json:"strategy"`
	Description string `json:"description"`
}

// Sentinel is the Rollback Sentinel thresholds; nil in a threshold = the platform sets it.
type Sentinel struct {
	ErrorRateDeltaMax *float64 `json:"error_rate_delta_max,omitempty"`
	LatencyP99MsMax   *int     `json:"latency_p99_ms_max,omitempty"`
	Invariants        []string `json:"invariants,omitempty"`
}

const (
	RequiresSentinelGreen       = "sentinel_green"
	RequiresCompensation        = "compensation_declared"
	RequiresReconciliationGreen = "reconciliation_green"

	BlockedByCB06 = "CB-06"

	ReconciliationPeriodic = "periodic"
	ReconciliationNone     = "none"
)

// DeliveryPlan is delivery.json, the Stage 7 artifact: what the platform executes.
type DeliveryPlan struct {
	// Canary is the canary-with-compensation scope, the write paths of the slice.
	Canary        *Canary          `json:"canary,omitempty"`
	SchemaVersion int              `json:"schema_version"`
	SliceID       string           `json:"slice_id"`
	Mode          string           `json:"mode"`
	Candidate     *string          `json:"candidate"`
	FeatureFlag   string           `json:"feature_flag"`
	Writes        bool             `json:"writes"`
	Compensation  *Compensation    `json:"compensation"`
	Rings         []Ring           `json:"rings"`
	MaxRing       int              `json:"max_ring"`
	BlockedBy     *string          `json:"blocked_by"`
	Sentinel      DeliverySentinel `json:"sentinel"`
	Parity        *Parity          `json:"parity"`
}

type Ring struct {
	Percent  int      `json:"percent"`
	Requires []string `json:"requires"`
}

type DeliverySentinel struct {
	ErrorRateDeltaMax *float64 `json:"error_rate_delta_max"`
	LatencyP99MsMax   *int     `json:"latency_p99_ms_max"`
	Invariants        []string `json:"invariants"`
	Reconciliation    string   `json:"reconciliation"`
}

type Parity struct {
	// Paths are the read-only globs the parity runs on (intent.delivery.read_only_paths).
	Paths      []string `json:"paths,omitempty"`
	Mode       string   `json:"mode"`
	WritePaths string   `json:"write_paths"`
}
