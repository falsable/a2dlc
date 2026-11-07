package contract

// Proposals is what `a2dlc propose --from-escalations` writes to --out (proposals.schema.json).
type Proposals struct {
	SchemaVersion int            `json:"schema_version"`
	CreatedAt     string         `json:"created_at"`
	Proposals     []Proposal     `json:"proposals"`
	Skipped       []ProposalSkip `json:"skipped"`
}

// Proposal is one property for the next intent, from one counterexample of an earlier run. Hash identifies the content.
type Proposal struct {
	Hash     string         `json:"hash"`
	Target   string         `json:"target"`
	Property string         `json:"property"`
	File     string         `json:"file"`
	Source   ProposalSource `json:"source"`
	Probe    Probe          `json:"probe"`
}

// ProposalSource is the escalation the property came from.
type ProposalSource struct {
	EscalationID string `json:"escalation_id"`
	Kind         string `json:"kind"`
}

// ProposalSkip is an escalation that was read and gave no proposal, with the reason.
type ProposalSkip struct {
	EscalationID string `json:"escalation_id"`
	Target       string `json:"target"`
	Reason       string `json:"reason"`
}
