package contract

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// Block 8 (ADR meta/0004, amendment 2026-10-17): N=2 cross-builder and Occam selection. They mirror
// engine-complexity-response, selection and the new $defs of candidate; the v2 fields of verdict and escalation
// are in contract.go.

// ComplexityResponse is the response to operation: complexity.
type ComplexityResponse struct {
	SchemaVersion int              `json:"schema_version"`
	Files         []ComplexityFile `json:"files"`
	Error         *string          `json:"error"`
}

// ComplexityFile measures a changed file. Cyclomatic and ASTNodes are nil when the file is not engine code or
// does not parse: they do not count as zero.
type ComplexityFile struct {
	File       string `json:"file"`
	LocAdded   int    `json:"loc_added"`
	LocRemoved int    `json:"loc_removed"`
	Cyclomatic *int   `json:"cyclomatic"`
	ASTNodes   *int   `json:"ast_nodes"`
}

// Complexity holds the totals of a candidate over the measured files, plus those nobody measured.
type Complexity struct {
	LocAdded   int              `json:"loc_added"`
	LocRemoved int              `json:"loc_removed"`
	Cyclomatic *int             `json:"cyclomatic"`
	ASTNodes   *int             `json:"ast_nodes"`
	Files      []ComplexityFile `json:"files"`
	Unmeasured []string         `json:"unmeasured"`
}

// Selection is selection.json: the two candidates judged, the chosen one (nil if none) and why.
type Selection struct {
	SchemaVersion int                  `json:"schema_version"`
	Candidates    []SelectionCandidate `json:"candidates"`
	Selected      *string              `json:"selected"`
	Reason        string               `json:"reason"`
}

type SelectionCandidate struct {
	Builder    string      `json:"builder"`
	Commit     string      `json:"commit"`
	Verdict    string      `json:"verdict"`
	Complexity *Complexity `json:"complexity"`
}

// CandidateEntry is a candidate from the candidates list of candidate.json (N=2).
type CandidateEntry struct {
	Builder    string   `json:"builder"`
	Commit     string   `json:"commit"`
	TreeHash   string   `json:"tree_hash"`
	Iterations int      `json:"iterations"`
	Unchecked  []string `json:"unchecked"`
	// @a2dlc headers without a slice_id line in this builder (§6.3; see header.go).
	HeadersMissing []string `json:"headers_missing,omitempty"`
	// Tier0Reviewed: whether any engine ran Tier 0 on this candidate (a2dlc run escalates if not; see tier0note.go).
	Tier0Reviewed *bool `json:"tier0_reviewed,omitempty"`
	// Lemmas are the verdicts of the lemmas this builder declared (§5 Stage 2, decision 12; see run/lemmas.go).
	Lemmas []CandidateLemma `json:"lemmas,omitempty"`
}

// DroppedBuilder is a builder that left no candidate with N=2, with its reason.
type DroppedBuilder struct {
	Builder string `json:"builder"`
	Reason  string `json:"reason"`
}

var digits, spaces = regexp.MustCompile(`[0-9]+`), regexp.MustCompile(`\s+`)

// ClusterID groups escalations that share a kind and a normalized message (escalation v2). That is an approximation of a
// root cause, not the root cause itself: the target is not part of the key. "clu-" and 12 hex of the sha256 of kind + NUL + the
// normalized message (lowercase, each run of digits replaced by #, spaces collapsed).
func ClusterID(kind, message string) string {
	m := spaces.ReplaceAllString(digits.ReplaceAllString(strings.ToLower(message), "#"), " ")
	h := sha256.Sum256([]byte(kind + "\x00" + strings.TrimSpace(m)))
	return "clu-" + hex.EncodeToString(h[:])[:12]
}
