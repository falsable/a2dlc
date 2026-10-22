package contract

import (
	"encoding/json"
	"fmt"
)

// Types of Stage 1.5 of a2dlc run (block 7): the S* lint (contract-lint), the blind back-translation and its
// review (backtranslation, backtranslation-review), the z3 engine (z3-request, z3-response) and the payloads of the
// Back-translator and of the Inquisitor of 1.5 (role-payloads).

const (
	SeverityError   = "error"
	SeverityWarning = "warning"

	Z3Sat     = "sat"
	Z3Unsat   = "unsat"
	Z3Unknown = "unknown"
	Z3Skipped = "skipped"
)

// CriticalityLevel orders low < medium < high < critical (1..4); 0 is unknown.
func CriticalityLevel(c string) int {
	switch c {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	case "critical":
		return 4
	}
	return 0
}

// ContractLint is lint.json: what a2dlc-lint found in contract.json.
type ContractLint struct {
	SchemaVersion int           `json:"schema_version"`
	SliceID       string        `json:"slice_id"`
	ContractHash  string        `json:"contract_hash"`
	OK            bool          `json:"ok"`
	Findings      []LintFinding `json:"findings"`
	TraceCoverage TraceCoverage `json:"trace_coverage"`
}

type LintFinding struct {
	Severity string `json:"severity"`
	Check    string `json:"check"`
	Where    string `json:"where"`
	Message  string `json:"message"`
}

type TraceCoverage struct {
	Rule      string            `json:"rule"`
	Severity  string            `json:"severity"`
	Uncovered []UncoveredClause `json:"uncovered"`
}

type UncoveredClause struct {
	Function string `json:"function"`
	Kind     string `json:"kind"` // post | invariant
	Clause   string `json:"clause"`
}

// BTText is what the Back-translator writes: prose and concrete claims.
type BTText struct {
	Prose    string   `json:"prose"`
	Examples []string `json:"examples"`
}

// Validate: non-empty prose and examples present (there may be none, but not null) and non-empty.
func (b BTText) Validate() error {
	if b.Prose == "" || b.Examples == nil {
		return fmt.Errorf("prose and examples are required")
	}
	for _, e := range b.Examples {
		if e == "" {
			return fmt.Errorf("an empty example")
		}
	}
	return nil
}

// Backtranslation is backtranslation.json.
type Backtranslation struct {
	SchemaVersion int      `json:"schema_version"`
	SliceID       string   `json:"slice_id"`
	ContractHash  string   `json:"contract_hash"`
	Prose         string   `json:"prose"`
	Examples      []string `json:"examples"`
}

type BTDiscrepancy struct {
	Clause          string `json:"clause"`
	IntentSays      string `json:"intent_says"`
	ContractImplies string `json:"contract_implies"`
}

// BacktranslationReview is backtranslation_review.json.
type BacktranslationReview struct {
	SchemaVersion int             `json:"schema_version"`
	SliceID       string          `json:"slice_id"`
	ContractHash  string          `json:"contract_hash"`
	Matches       bool            `json:"matches"`
	Discrepancies []BTDiscrepancy `json:"discrepancies"`
}

type BackTranslatorRequest struct {
	ContractHash string `json:"contract_hash"`
}

type BackTranslatorResponse = BTText

type InquisitorBTRequest struct {
	Intent          json.RawMessage `json:"intent"`
	Backtranslation BTText          `json:"backtranslation"`
}

type InquisitorBTResponse struct {
	Matches       bool            `json:"matches"`
	Discrepancies []BTDiscrepancy `json:"discrepancies"`
}

// Validate: matches and discrepancies say the same thing, and each discrepancy is complete.
func (r InquisitorBTResponse) Validate() error {
	if r.Discrepancies == nil {
		return fmt.Errorf("discrepancies is required")
	}
	if r.Matches != (len(r.Discrepancies) == 0) {
		return fmt.Errorf("matches %v with %d discrepancies", r.Matches, len(r.Discrepancies))
	}
	for _, d := range r.Discrepancies {
		if d.Clause == "" || d.IntentSays == "" || d.ContractImplies == "" {
			return fmt.Errorf("discrepancy %q is incomplete", d.Clause)
		}
	}
	return nil
}

// Z3Request is /io/request.json of the z3 engine.
type Z3Request struct {
	SchemaVersion int          `json:"schema_version"`
	TimeoutMS     int          `json:"timeout_ms"`
	Functions     []Z3Function `json:"functions"`
}

type Z3Function struct {
	Function string   `json:"function"`
	SMT      []string `json:"smt"`
}

// Z3Response is /io/response.json of the z3 engine; the core stores it as z3.json.
type Z3Response struct {
	SchemaVersion int        `json:"schema_version"`
	Z3Version     string     `json:"z3_version"`
	Results       []Z3Result `json:"results"`
	Error         *string    `json:"error"`
}

type Z3Result struct {
	Function string  `json:"function"`
	Status   string  `json:"status"`
	Model    *string `json:"model"`
	Reason   *string `json:"reason"`
}

// Validate checks what the schema requires and what the request fixes: one result per requested function, in order;
// sat with a model, the rest without a model and with a reason.
func (r Z3Response) Validate(req Z3Request) error {
	if r.SchemaVersion != SchemaVersion || r.Z3Version == "" || r.Results == nil {
		return fmt.Errorf("z3 response without schema_version 1, z3_version or results")
	}
	if r.Error != nil {
		return nil
	}
	if len(r.Results) != len(req.Functions) {
		return fmt.Errorf("z3 gave %d results for %d functions", len(r.Results), len(req.Functions))
	}
	for i, x := range r.Results {
		switch {
		case x.Function != req.Functions[i].Function:
			return fmt.Errorf("result %d is for %q, but %q was requested", i, x.Function, req.Functions[i].Function)
		case x.Status == Z3Sat && x.Model == nil:
			return fmt.Errorf("%s: sat without model", x.Function)
		case x.Status != Z3Sat && x.Status != Z3Unsat && x.Status != Z3Unknown && x.Status != Z3Skipped:
			return fmt.Errorf("%s: unknown status %q", x.Function, x.Status)
		case x.Status != Z3Sat && (x.Model != nil || x.Reason == nil || *x.Reason == ""):
			return fmt.Errorf("%s: %s must have no model and a reason", x.Function, x.Status)
		}
	}
	return nil
}
