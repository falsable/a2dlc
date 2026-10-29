package contract

import (
	"fmt"
	"regexp"
)

// Types of cmd/factory (ADR meta/0008): factory.json, the batch of issues and the report of a run. They mirror
// factory-config, factory-batch and factory-report of contracts/; the strict decoding test fails if they drift apart.
// The a2dlc binary and the protocol do not read any of them.

// FactoryCaps are the spending and time limits of a run. All six are required by the schema: there is no default.
type FactoryCaps struct {
	LaneUSD       float64 `json:"lane_usd"`
	LaneSeconds   int     `json:"lane_seconds"`
	LaneTurns     int     `json:"lane_turns"`
	RunStopNewUSD float64 `json:"run_stop_new_usd"`
	RunKillUSD    float64 `json:"run_kill_usd"`
	TotalUSD      float64 `json:"total_usd"`
}

// Arms of the preregistered comparison: minimal is arm 1 (the factory's minimal claude flags, the default when the
// config does not say), control is arm 0 (default claude -p in the same role container).
const (
	ArmMinimal = "minimal"
	ArmControl = "control"
)

type FactoryConfig struct {
	SchemaVersion int         `json:"schema_version"`
	RolesImage    string      `json:"roles_image"`
	Argv          []string    `json:"argv"`
	EnvFile       string      `json:"env_file,omitempty"`
	Network       string      `json:"network"`
	EgressAllow   []string    `json:"egress_allow,omitempty"`
	Model         string      `json:"model"`
	Tools         []string    `json:"tools"`
	Arm           string      `json:"arm,omitempty"`
	Concurrency   int         `json:"concurrency"`
	Caps          FactoryCaps `json:"caps"`
}

// Validate checks what factory-config.schema.json cannot compare: the order of the caps, and that a provider network
// names the hosts it may reach.
func (c FactoryConfig) Validate() error {
	k := c.Caps
	// the schema requires every cap and has no default for any; a config that omits one decodes as zero, and a zero cap
	// would either stop everything or, read as "unset", stop nothing
	for name, v := range map[string]float64{"lane_usd": k.LaneUSD, "lane_seconds": float64(k.LaneSeconds), "lane_turns": float64(k.LaneTurns),
		"run_stop_new_usd": k.RunStopNewUSD, "run_kill_usd": k.RunKillUSD, "total_usd": k.TotalUSD} {
		if v <= 0 {
			return fmt.Errorf("caps.%s is missing or not positive", name)
		}
	}
	switch {
	case c.Concurrency < 1:
		return fmt.Errorf("concurrency must be at least 1")
	case len(c.Tools) == 0 || c.RolesImage == "" || len(c.Argv) == 0 || c.Model == "":
		return fmt.Errorf("roles_image, argv, model and tools are required")
	case c.Network != "none" && c.Network != "provider":
		return fmt.Errorf("network %q", c.Network)
	case c.Arm != "" && c.Arm != ArmMinimal && c.Arm != ArmControl:
		return fmt.Errorf("arm %q", c.Arm)
	case k.LaneUSD > k.RunKillUSD:
		return fmt.Errorf("caps.lane_usd %.4f is above caps.run_kill_usd %.4f", k.LaneUSD, k.RunKillUSD)
	case k.RunStopNewUSD > k.RunKillUSD:
		return fmt.Errorf("caps.run_stop_new_usd %.4f is above caps.run_kill_usd %.4f", k.RunStopNewUSD, k.RunKillUSD)
	case k.RunKillUSD > k.TotalUSD:
		return fmt.Errorf("caps.run_kill_usd %.4f is above caps.total_usd %.4f", k.RunKillUSD, k.TotalUSD)
	case c.Network == "provider" && len(c.EgressAllow) == 0:
		return fmt.Errorf("network provider needs egress_allow")
	}
	return nil
}

type FactoryIssue struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	DoneCommand string `json:"done_command"`
}

type FactoryBatch struct {
	SchemaVersion int            `json:"schema_version"`
	BatchID       string         `json:"batch_id"`
	Issues        []FactoryIssue `json:"issues"`
}

// Validate checks that issue ids are unique, which factory-batch.schema.json cannot say about a field of an object.
func (b FactoryBatch) Validate() error {
	seen := map[string]bool{}
	for _, is := range b.Issues {
		if seen[is.ID] {
			return fmt.Errorf("issue id %q appears twice", is.ID)
		}
		seen[is.ID] = true
	}
	return nil
}

// Counters is what a unit of work spent: the four token counters, as run-defs cost has them (InputTokens is the total
// prompt, cache included), plus dollars, turns and the number of role launches.
type Counters struct {
	USD                      float64 `json:"usd"`
	Calls                    int     `json:"calls"`
	Turns                    int     `json:"turns"`
	InputTokens              int     `json:"input_tokens"`
	CacheReadInputTokens     int     `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int     `json:"cache_creation_input_tokens"`
	OutputTokens             int     `json:"output_tokens"`
}

// Add counts one role launch: a nil cost is a call that reported nothing.
func (c *Counters) Add(cost *Cost) {
	c.Calls++
	if cost == nil {
		return
	}
	c.USD += cost.USD
	c.Turns += cost.Turns
	c.InputTokens += cost.InputTokens
	c.CacheReadInputTokens += cost.CacheReadInputTokens
	c.CacheCreationInputTokens += cost.CacheCreationInputTokens
	c.OutputTokens += cost.OutputTokens
}

// Plus adds another set of counters.
func (c *Counters) Plus(o Counters) {
	c.USD += o.USD
	c.Calls += o.Calls
	c.Turns += o.Turns
	c.InputTokens += o.InputTokens
	c.CacheReadInputTokens += o.CacheReadInputTokens
	c.CacheCreationInputTokens += o.CacheCreationInputTokens
	c.OutputTokens += o.OutputTokens
}

// StageUsage is Counters for one stage of a lane's run.
type StageUsage struct {
	Counters
	Stage  string `json:"stage"`
	Status string `json:"status"`
}

const (
	LaneAccepted = "accepted"
	LaneRejected = "rejected"
	LaneSkipped  = "skipped"
	LaneCapped   = "capped"
	LaneKilled   = "killed"
	LaneError    = "error"
)

type FactoryLane struct {
	IssueID         string       `json:"issue_id"`
	SliceID         *string      `json:"slice_id"`
	Branch          *string      `json:"branch"`
	Status          string       `json:"status"`
	Reason          string       `json:"reason"`
	RunOutcome      *string      `json:"run_outcome"`
	CandidateRef    *string      `json:"candidate_ref"`
	WallSeconds     float64      `json:"wall_seconds"`
	UnreportedCalls int          `json:"unreported_calls"`
	Usage           Counters     `json:"usage"`
	Stages          []StageUsage `json:"stages"`
	// TestsTouched lists the base test files a rejected lane modified or deleted (empty for every other lane).
	TestsTouched []TestTouch `json:"tests_touched,omitempty"`
}

// TestTouch is one file of config.tests.paths that exists on the base and that a lane modified or deleted.
type TestTouch struct {
	Path   string `json:"path"`
	Change string `json:"change"`
}

const (
	TestModified = "modified"
	TestDeleted  = "deleted"
)

const (
	RunCompleted       = "completed"
	RunStoppedNewLanes = "stopped_new_lanes"
	RunKilled          = "killed"
)

type FactoryReport struct {
	SchemaVersion      int           `json:"schema_version"`
	RunID              string        `json:"run_id"`
	BatchID            string        `json:"batch_id"`
	BaseSha            string        `json:"base_sha"`
	ConfigSHA256       string        `json:"config_sha256"`
	StartedAt          string        `json:"started_at"`
	FinishedAt         string        `json:"finished_at"`
	Outcome            string        `json:"outcome"`
	PriorSpendUSD      float64       `json:"prior_spend_usd"`
	Usage              Counters      `json:"usage"`
	Accepted           int           `json:"accepted"`
	CostPerAcceptedUSD *float64      `json:"cost_per_accepted_usd"`
	Lanes              []FactoryLane `json:"lanes"`
}

var factoryRunID = regexp.MustCompile(`^run-[0-9a-f]{12}$`)

// Validate checks the arithmetic that factory-report.schema.json cannot: the run total is the sum of its lanes, the
// accepted count matches the lanes, and the cost per accepted change is the total over that count.
func (r FactoryReport) Validate() error {
	if !factoryRunID.MatchString(r.RunID) {
		return fmt.Errorf("run_id %q", r.RunID)
	}
	var sum Counters
	accepted := 0
	for _, l := range r.Lanes {
		sum.Plus(l.Usage)
		if l.Status == LaneAccepted {
			accepted++
		}
	}
	const eps = 1e-9
	switch {
	case sum.Calls != r.Usage.Calls || sum.InputTokens != r.Usage.InputTokens || sum.CacheReadInputTokens != r.Usage.CacheReadInputTokens ||
		sum.CacheCreationInputTokens != r.Usage.CacheCreationInputTokens || sum.OutputTokens != r.Usage.OutputTokens || sum.Turns != r.Usage.Turns ||
		sum.USD > r.Usage.USD+eps || sum.USD < r.Usage.USD-eps:
		return fmt.Errorf("usage is not the sum of the lanes")
	case accepted != r.Accepted:
		return fmt.Errorf("accepted is %d and %d lanes are accepted", r.Accepted, accepted)
	case accepted == 0 && r.CostPerAcceptedUSD != nil:
		return fmt.Errorf("cost_per_accepted_usd without an accepted lane")
	case accepted > 0 && (r.CostPerAcceptedUSD == nil || *r.CostPerAcceptedUSD > r.Usage.USD/float64(accepted)+eps || *r.CostPerAcceptedUSD < r.Usage.USD/float64(accepted)-eps):
		return fmt.Errorf("cost_per_accepted_usd is not usage.usd over accepted")
	}
	return nil
}
