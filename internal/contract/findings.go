package contract

// FindingEvent is one line of .a2dlc/reports/findings.jsonl (findings.schema.json): an append-only record of what happened
// to each finding a check raised. The fields that belong to one event only are omitted from the others.
type FindingEvent struct {
	SchemaVersion int    `json:"schema_version"`
	Event         string `json:"event"`
	At            string `json:"at"`
	ID            string `json:"id"`
	BaseRef       string `json:"base_ref"`
	TreeHash      string `json:"tree_hash,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Source        string `json:"source,omitempty"`
	Target        string `json:"target,omitempty"`
	State         string `json:"state,omitempty"`
	Decision      string `json:"decision,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
	By            string `json:"by,omitempty"`
	Via           string `json:"via,omitempty"`
	Reason        string `json:"reason,omitempty"`
}

// Events of a finding's record.
const (
	FindingRaised      = "raised"
	FindingFixed       = "fixed"
	FindingIntentional = "intentional"
	FindingLabeled     = "labeled"
)

// States of a raised finding: parked waits on an open escalation (a person), open has nobody asked.
const (
	FindingOpen   = "open"
	FindingParked = "parked"
)

// Outcomes that only a label can set: the core never infers them.
const (
	OutcomeFalse      = "false"
	OutcomeIrrelevant = "irrelevant"
)

// Surfaces a label comes through (FindingEvent.Via).
const (
	ViaCLI = "cli"
	ViaMCP = "mcp"
)
