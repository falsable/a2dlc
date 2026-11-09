package contract

// JournalEvent is one line of .a2dlc/reports/journal.jsonl (journal.schema.json, draft): the checkpoints and decisions
// of a change that a person reads. Chained like RunLedger; the fields of one event only are omitted from the others.
type JournalEvent struct {
	SchemaVersion  int             `json:"schema_version"`
	Seq            int             `json:"seq"`
	PrevHash       *string         `json:"prev_hash"`
	Hash           string          `json:"hash"`
	Event          string          `json:"event"`
	At             string          `json:"at"`
	By             string          `json:"by"`
	TreeHash       string          `json:"tree_hash,omitempty"`
	Commit         string          `json:"commit,omitempty"`
	Title          string          `json:"title,omitempty"`
	Summary        string          `json:"summary,omitempty"`
	ID             string          `json:"id,omitempty"`
	Question       string          `json:"question,omitempty"`
	Options        []JournalOption `json:"options,omitempty"`
	Recommendation string          `json:"recommendation,omitempty"`
	Chosen         string          `json:"chosen,omitempty"`
	Via            string          `json:"via,omitempty"`
	Reason         string          `json:"reason,omitempty"`
	Refs           []string        `json:"refs,omitempty"`
}

// JournalOption is one option of a decision_asked event, with what follows from choosing it.
type JournalOption struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Consequence string `json:"consequence"`
}

// Events of the journal.
const (
	JournalCheckpoint       = "checkpoint"
	JournalDecisionAsked    = "decision_asked"
	JournalDecisionAnswered = "decision_answered"
)
