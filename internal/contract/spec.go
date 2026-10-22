package contract

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// Types of Stage 1 of a2dlc run (block 6, ADR meta/0004 amendment 2026-10-17): the slice contract S* and its
// candidates (slice-contract), the capture of a refactor (capture), the blocked probes (probes), the assumption
// catalogue (assumption-catalogue) and the payloads of the Specifiers and of the Inquisitor (role-payloads).

const (
	SourceAlpha     = "specifier_alpha"
	SourceBeta      = "specifier_beta"
	SourceConsensus = "consensus"

	EquivalenceStrict  = "strict"
	EquivalenceRelaxed = "relaxed"

	FromCatalogue  = "catalogue" // assumptions[].source
	FromSpecifier  = "specifier"
	AssumptionOpen = "open"
)

type SliceContract struct {
	// IOContract declares the input and output schemas and the error taxonomy (optional).
	IOContract *IOContract `json:"io_contract,omitempty"`
	// Invariants are the slice-level invariants; DSL is the §6.2 expression the core translates to SMT.
	Invariants    []SliceInvariant `json:"invariants,omitempty"`
	SchemaVersion int              `json:"schema_version"`
	SliceID       string           `json:"slice_id"`
	Source        string           `json:"source"`
	Functions     []SpecFunction   `json:"functions"`
	Probes        []Probe          `json:"probes"`
	Assumptions   []Assumption     `json:"assumptions"`
	PiS           PiS              `json:"pi_s"`
}

type SpecFunction struct {
	// Lemmas are pure internal functions with declared postconditions (Critical slices).
	Lemmas     []Lemma     `json:"lemmas,omitempty"`
	Name       string      `json:"name"`
	Signature  *string     `json:"signature"`
	Pre        []string    `json:"pre"`
	Post       []string    `json:"post"`
	Invariants []string    `json:"invariants"`
	Errors     []SpecError `json:"errors"`
	SMT        []string    `json:"smt,omitempty"` // block 7: SMT-LIB 2 for Z3 (Stage 1.5)
}

type SpecError struct {
	Condition string `json:"condition"`
	Raises    string `json:"raises"`
}

// Probe is a boundary probe of S* (§6.1). File and Expr, optional and together, are the compiled assertion that Tier 0
// evaluates in the language engine (Executable); without them the probe stays unchecked on the candidate.
type Probe struct {
	ID          string  `json:"id"`
	Description string  `json:"description"`
	Input       string  `json:"input"`
	Expected    string  `json:"expected"`
	Kind        string  `json:"kind"`
	File        *string `json:"file,omitempty"`
	Expr        *string `json:"expr,omitempty"`
	// Assumption (optional): id of the S* assumption that the probe checks; a catalogue assumption with status accepted requires one (stage1.go).
	Assumption *string `json:"assumption,omitempty"`
}

// Executable reports whether Tier 0 can run the probe: it carries file and expr.
func (p Probe) Executable() bool {
	return p.File != nil && *p.File != "" && p.Expr != nil && *p.Expr != ""
}

type Assumption struct {
	ID        string `json:"id"`
	Statement string `json:"statement"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	// Criticality (block 7, optional): low, medium, high or critical; "" means none was declared.
	Criticality string `json:"criticality,omitempty"`
}

type PiS struct {
	Equivalence string   `json:"equivalence"`
	Relaxations []string `json:"relaxations"`
}

// Capture is capture.json: the deterministic capture of a refactor (base tests that touch what changed, and signatures).
type Capture struct {
	// Traces are the recorded I/O of the changed functions, one entry per call, with the branches it took.
	Traces []CaptureTrace `json:"traces,omitempty"`
	// BranchCoverage is the fraction of branches the traces cover; nil when no traces were recorded.
	BranchCoverage *float64        `json:"branch_coverage,omitempty"`
	SchemaVersion  int             `json:"schema_version"`
	SliceID        string          `json:"slice_id"`
	BaseSha        string          `json:"base_sha"`
	ChangedFiles   []string        `json:"changed_files"`
	Selection      string          `json:"selection"`
	Tests          []string        `json:"tests"`
	Signatures     []SignatureFile `json:"signatures"`
	EngineErrors   []EngineError   `json:"engine_errors"`
}

// Probes is probes.json: the S* probes tied to the hash of contract.json.
type Probes struct {
	SchemaVersion int     `json:"schema_version"`
	SliceID       string  `json:"slice_id"`
	ContractHash  string  `json:"contract_hash"`
	Probes        []Probe `json:"probes"`
}

type AssumptionCatalogue struct {
	SchemaVersion int              `json:"schema_version"`
	Domain        string           `json:"domain"`
	Entries       []CatalogueEntry `json:"entries"`
}

type CatalogueEntry struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Question string `json:"question"`
}

// SpecDiscrepancy is a point where S_α and S_β diverge; Resolution nil means the Inquisitor did not resolve it.
type SpecDiscrepancy struct {
	Topic      string  `json:"topic"`
	Alpha      string  `json:"alpha"`
	Beta       string  `json:"beta"`
	Resolution *string `json:"resolution"`
}

type SpecifierRequest struct {
	Intent        json.RawMessage   `json:"intent"`
	Capture       *Capture          `json:"capture"`
	Round         int               `json:"round"`
	Discrepancies []SpecDiscrepancy `json:"discrepancies"`
	// EngineLanguage is the repo's engine language; absent means python (role-payloads.schema.json).
	EngineLanguage string `json:"engine_language,omitempty"`
	// Rejection is why the core rejected the previous answer of this same round; empty on the first attempt.
	Rejection string `json:"rejection,omitempty"`
}

type SpecifierResponse struct {
	Contract SliceContract `json:"contract"`
}

type InquisitorRequest struct {
	ContractAlpha SliceContract        `json:"contract_alpha"`
	ContractBeta  SliceContract        `json:"contract_beta"`
	Catalogue     *AssumptionCatalogue `json:"catalogue"`
	Round         int                  `json:"round"`
	// EngineLanguage is the repo's engine language; absent means python (role-payloads.schema.json).
	EngineLanguage string `json:"engine_language,omitempty"`
	// Rejection is why the core rejected the previous answer of this same round; empty on the first attempt.
	Rejection string `json:"rejection,omitempty"`
}

type InquisitorResponse struct {
	Contract        SliceContract     `json:"contract"`
	Discrepancies   []SpecDiscrepancy `json:"discrepancies"`
	OpenAssumptions []Assumption      `json:"open_assumptions"`
}

const AssumptionAccepted = "accepted"

var (
	// sliceRe lives in intent.go
	probeIDRe    = regexp.MustCompile(`^[A-Z][A-Z0-9]*-[0-9]+$`)
	assumptionRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,63}$`)
	probeKind    = map[string]bool{"example": true, "property": true, "negative": true}
	assumpSource = map[string]bool{FromCatalogue: true, FromSpecifier: true}
	assumpStatus = map[string]bool{AssumptionOpen: true, AssumptionAccepted: true, "rejected": true}
)

// Validate checks what DecodeStrict does not see of slice-contract.schema.json: required fields (a nil slice means a
// missing field or null), enums, patterns, a consistent pi_S and unique ids. What comes from an LLM is validated here
// before it is stored as an artifact.
func (c SliceContract) Validate() error {
	switch {
	case c.SchemaVersion != SchemaVersion:
		return fmt.Errorf("schema_version %d", c.SchemaVersion)
	case !sliceRe.MatchString(c.SliceID):
		return fmt.Errorf("slice_id %q is invalid", c.SliceID)
	case c.Source != SourceAlpha && c.Source != SourceBeta && c.Source != SourceConsensus:
		return fmt.Errorf("source %q is unknown", c.Source)
	case c.checkV34() != nil:
		return c.checkV34()
	case c.Functions == nil || c.Probes == nil || c.Assumptions == nil || c.PiS.Relaxations == nil:
		return fmt.Errorf("functions, probes, assumptions and pi_s.relaxations are required")
	}
	for _, f := range c.Functions {
		if f.Name == "" || f.Pre == nil || f.Post == nil || f.Invariants == nil || f.Errors == nil {
			return fmt.Errorf("function %q: name, pre, post, invariants and errors are required", f.Name)
		}
		if f.Signature != nil && *f.Signature == "" {
			return fmt.Errorf("function %s: empty signature (null if not set)", f.Name)
		}
		for _, l := range [][]string{f.Pre, f.Post, f.Invariants} {
			for _, s := range l {
				if s == "" {
					return fmt.Errorf("function %s: empty condition", f.Name)
				}
			}
		}
		for _, e := range f.Errors {
			if e.Condition == "" || e.Raises == "" {
				return fmt.Errorf("function %s: error without condition or raises", f.Name)
			}
		}
		if f.SMT != nil && len(f.SMT) == 0 {
			return fmt.Errorf("function %s: empty smt (omit the key instead)", f.Name)
		}
		for _, s := range f.SMT {
			if s == "" {
				return fmt.Errorf("function %s: empty smt fragment", f.Name)
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range c.Probes {
		switch {
		case !probeIDRe.MatchString(p.ID):
			return fmt.Errorf("probe id %q is invalid", p.ID)
		case seen[p.ID]:
			return fmt.Errorf("probe %s repeated", p.ID)
		case p.Description == "" || p.Input == "" || p.Expected == "":
			return fmt.Errorf("probe %s: description, input and expected are required", p.ID)
		case !probeKind[p.Kind]:
			return fmt.Errorf("probe %s: unknown kind %q", p.ID, p.Kind)
		}
		seen[p.ID] = true
	}
	seen = map[string]bool{}
	for _, a := range c.Assumptions {
		if err := a.validate(); err != nil {
			return err
		}
		if seen[a.ID] {
			return fmt.Errorf("assumption %s repeated", a.ID)
		}
		seen[a.ID] = true
	}
	for _, p := range c.Probes {
		if p.Assumption != nil && !seen[*p.Assumption] {
			return fmt.Errorf("probe %s cites assumption %q, which is not in assumptions", p.ID, *p.Assumption)
		}
	}
	switch c.PiS.Equivalence {
	case EquivalenceStrict:
		if len(c.PiS.Relaxations) > 0 {
			return fmt.Errorf("pi_s is strict but has relaxations")
		}
	case EquivalenceRelaxed:
		if len(c.PiS.Relaxations) == 0 {
			return fmt.Errorf("pi_s is relaxed but does not say what it relaxes")
		}
	default:
		return fmt.Errorf("pi_s.equivalence %q is unknown", c.PiS.Equivalence)
	}
	return nil
}

func (a Assumption) validate() error {
	switch {
	case !assumptionRe.MatchString(a.ID):
		return fmt.Errorf("assumption id %q is invalid", a.ID)
	case a.Statement == "":
		return fmt.Errorf("assumption %s without statement", a.ID)
	case !assumpSource[a.Source]:
		// The message goes back to the role on a retry, so it names the accepted values (a Gemini specifier wrote
		// "intent" twice in a row when the message did not).
		return fmt.Errorf("assumption %s: unknown source %q (it is %q or %q)", a.ID, a.Source, FromSpecifier, FromCatalogue)
	case !assumpStatus[a.Status]:
		return fmt.Errorf("assumption %s: unknown status %q (it is %q, %q or %q)", a.ID, a.Status, AssumptionOpen, AssumptionAccepted, "rejected")
	case a.Criticality != "" && CriticalityLevel(a.Criticality) == 0:
		return fmt.Errorf("assumption %s: unknown criticality %q", a.ID, a.Criticality)
	}
	return nil
}

// Validate checks a catalogue: entries with unique id, category and question.
func (c AssumptionCatalogue) Validate() error {
	if c.SchemaVersion != SchemaVersion || c.Domain == "" || len(c.Entries) == 0 {
		return fmt.Errorf("catalogue without schema_version 1, domain or entries")
	}
	seen := map[string]bool{}
	for _, e := range c.Entries {
		if !assumptionRe.MatchString(e.ID) || e.Category == "" || e.Question == "" || seen[e.ID] {
			return fmt.Errorf("catalogue entry %q is invalid or repeated", e.ID)
		}
		seen[e.ID] = true
	}
	return nil
}

// Validate checks the Inquisitor's response: a valid consensus S*, complete discrepancies and well-formed open
// assumptions. Whether catalogue assumptions cite the catalogue is checked by whoever holds the catalogue.
func (r InquisitorResponse) Validate() error {
	if err := r.Contract.Validate(); err != nil {
		return err
	}
	if r.Contract.Source != SourceConsensus {
		return fmt.Errorf("contract.source %q: S* comes from the Inquisitor as consensus", r.Contract.Source)
	}
	if r.Discrepancies == nil || r.OpenAssumptions == nil {
		return fmt.Errorf("discrepancies and open_assumptions are required")
	}
	for _, d := range r.Discrepancies {
		if d.Topic == "" || d.Alpha == "" || d.Beta == "" || (d.Resolution != nil && *d.Resolution == "") {
			return fmt.Errorf("discrepancy %q is incomplete", d.Topic)
		}
	}
	for _, a := range r.OpenAssumptions {
		if err := a.validate(); err != nil {
			return err
		}
	}
	return nil
}
