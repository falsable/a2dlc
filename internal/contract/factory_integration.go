package contract

import "fmt"

// Types of `factory integrate` (bead a2dlc-3fu.4): the sealed holdout's spec and the record of an integration. They
// mirror factory-holdout and factory-integration of contracts/; the strict decoding test fails if they drift apart.

// FactoryHoldoutUnit says which holdout files decide one issue.
type FactoryHoldoutUnit struct {
	IssueID string   `json:"issue_id"`
	Files   []string `json:"files"`
}

type FactoryHoldout struct {
	SchemaVersion int                  `json:"schema_version"`
	Dir           string               `json:"dir"`
	Seal          string               `json:"seal"`
	Image         string               `json:"image"`
	Command       string               `json:"command"`
	Units         []FactoryHoldoutUnit `json:"units"`
	TimeoutS      int                  `json:"timeout_s,omitempty"`
	Memory        string               `json:"memory,omitempty"`
	CPUs          float64              `json:"cpus,omitempty"`
}

// Validate checks what the schema cannot compare: an issue and a file are listed once.
func (h FactoryHoldout) Validate() error {
	issues, files := map[string]bool{}, map[string]string{}
	for _, u := range h.Units {
		if issues[u.IssueID] {
			return fmt.Errorf("issue %q has two units", u.IssueID)
		}
		issues[u.IssueID] = true
		for _, f := range u.Files {
			if o, ok := files[f]; ok {
				return fmt.Errorf("file %q is a unit of %q and of %q", f, o, u.IssueID)
			}
			files[f] = u.IssueID
		}
	}
	return nil
}

// Outcomes of one lane in an integration.
const (
	IntegrationIntegrated    = "integrated"
	IntegrationConflict      = "conflict"
	IntegrationCheckFailed   = "check_failed"
	IntegrationHoldoutFailed = "holdout_failed"
	IntegrationHoldoutError  = "holdout_error"
	IntegrationNoHoldout     = "no_holdout"
	IntegrationNotQueued     = "not_queued"
)

type FactoryIntegrationLane struct {
	IssueID       string   `json:"issue_id"`
	QueuePosition *int     `json:"queue_position"`
	Status        string   `json:"status"`
	Reason        string   `json:"reason"`
	Rebased       bool     `json:"rebased"`
	Conflicts     []string `json:"conflicts,omitempty"`
	Commit        *string  `json:"commit"`
	CheckVerdict  *string  `json:"check_verdict"`
	HoldoutFiles  []string `json:"holdout_files"`
	HoldoutExit   *int     `json:"holdout_exit"`
}

type FactoryIntegration struct {
	SchemaVersion     int                      `json:"schema_version"`
	RunID             string                   `json:"run_id"`
	BatchID           string                   `json:"batch_id"`
	BaseSha           string                   `json:"base_sha"`
	HoldoutSeal       string                   `json:"holdout_seal"`
	MainRef           string                   `json:"main_ref"`
	MainShaBefore     string                   `json:"main_sha_before"`
	MainShaAfter      string                   `json:"main_sha_after"`
	IntegrationBranch string                   `json:"integration_branch"`
	HeadSha           string                   `json:"head_sha"`
	StartedAt         string                   `json:"started_at"`
	FinishedAt        string                   `json:"finished_at"`
	Integrated        int                      `json:"integrated"`
	Lanes             []FactoryIntegrationLane `json:"lanes"`
}

// Validate checks what the schema cannot: main did not move, the integrated count is the lanes that say so, a commit
// exists exactly for an integrated lane, and a lane that was never queued has no position.
func (r FactoryIntegration) Validate() error {
	if r.MainShaBefore != r.MainShaAfter {
		return fmt.Errorf("main moved from %s to %s: an integration never writes main", r.MainShaBefore, r.MainShaAfter)
	}
	n := 0
	for _, l := range r.Lanes {
		in := l.Status == IntegrationIntegrated
		if in {
			n++
		}
		switch {
		case in != (l.Commit != nil):
			return fmt.Errorf("lane %s: status %s and commit %v", l.IssueID, l.Status, l.Commit != nil)
		case in && (l.CheckVerdict == nil || *l.CheckVerdict != PASS || l.HoldoutExit == nil || *l.HoldoutExit != 0):
			return fmt.Errorf("lane %s is integrated without a PASS check and a passing holdout", l.IssueID)
		case (l.Status == IntegrationNotQueued) != (l.QueuePosition == nil):
			return fmt.Errorf("lane %s: status %s and queue_position %v", l.IssueID, l.Status, l.QueuePosition)
		}
	}
	if n != r.Integrated {
		return fmt.Errorf("integrated is %d and %d lanes are integrated", r.Integrated, n)
	}
	return nil
}
