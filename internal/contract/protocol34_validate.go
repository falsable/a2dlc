package contract

import (
	"fmt"
	"regexp"
)

// Go repeats the v3.4 rules of intent.schema.json and slice-contract.schema.json that DecodeStrict does not see:
// enums, patterns, minimums and non-empty elements. The invalid examples of those schemas must fail here too
// (TestIntentValidateFollowsSchema, TestExamplesOfStage1).

var (
	changeKinds     = map[string]bool{"feature": true, "bugfix": true, "refactor": true}
	criticalityTags = map[string]bool{"security": true, "billing": true, "auth": true, "concurrency": true}
	slaOps          = map[string]bool{"<": true, "<=": true, ">": true, ">=": true}
	sideEffectNames = map[string]bool{"network.http.outbound": true, "fs.write": true,
		"db.raw_sql_unparameterized": true, "time.system_clock_direct": true}
	verifications = map[string]bool{"smt": true, "smt+pbt": true, "runtime_and_pbt": true}
	// relaxationRe is the vocabulary of pi_s.relaxations (slice-contract.schema.json): the relaxations the engines apply.
	relaxationRe = regexp.MustCompile(`^(ignore_order|set_semantics|errors_by_type|float_tolerance:[0-9.eE+-]+|ignore_fields:[A-Za-z0-9_.]+)$`)
)

// checkV34 is the v3.4 part of Intent.Validate.
func (in Intent) checkV34() error {
	if in.ChangeKind != "" && !changeKinds[in.ChangeKind] {
		return fmt.Errorf("change_kind %q: feature, bugfix or refactor", in.ChangeKind)
	}
	for _, t := range in.CriticalityTags {
		if !criticalityTags[t] {
			return fmt.Errorf("criticality_tags %q: security, billing, auth or concurrency", t)
		}
	}
	for _, s := range in.SLAs {
		if s.Metric == "" || s.Unit == "" || !slaOps[s.Op] {
			return fmt.Errorf("slas: %q needs a metric, an op (<, <=, >, >=) and a unit", s.Metric)
		}
	}
	if in.Budget != nil && in.Budget.MaxTokens != nil && *in.Budget.MaxTokens < 1 {
		return fmt.Errorf("budget.max_tokens %d: minimum 1, or null", *in.Budget.MaxTokens)
	}
	if in.Capabilities != nil {
		for _, s := range in.Capabilities.ForbiddenImports {
			if s == "" {
				return fmt.Errorf("capabilities.forbidden_imports has an empty element")
			}
		}
	}
	if in.Boundary != nil {
		for _, s := range in.Boundary.ForbiddenSideEffects {
			if !sideEffectNames[s] {
				return fmt.Errorf("boundary.forbidden_side_effects %q: network.http.outbound, fs.write, db.raw_sql_unparameterized or time.system_clock_direct", s)
			}
		}
	}
	if in.Delivery != nil {
		for _, p := range in.Delivery.ReadOnlyPaths {
			if p == "" {
				return fmt.Errorf("delivery.read_only_paths has an empty element")
			}
		}
	}
	return nil
}

// checkV34 is the v3.4 part of SliceContract.Validate: the relaxation vocabulary, io_contract, lemmas and invariants.
func (c SliceContract) checkV34() error {
	for _, r := range c.PiS.Relaxations {
		if !relaxationRe.MatchString(r) {
			return fmt.Errorf("pi_s.relaxations %q is not in the vocabulary (ignore_order, set_semantics, errors_by_type, float_tolerance:<eps>, ignore_fields:<path>)", r)
		}
	}
	if io := c.IOContract; io != nil {
		if io.InputSchemaRef == "" || io.OutputSchemaRef == "" || io.ErrorTaxonomy == nil {
			return fmt.Errorf("io_contract: input_schema_ref, output_schema_ref and error_taxonomy are required")
		}
		seen := map[string]bool{}
		for _, e := range io.ErrorTaxonomy {
			if e == "" || seen[e] {
				return fmt.Errorf("io_contract.error_taxonomy has an empty or repeated code %q", e)
			}
			seen[e] = true
		}
	}
	for _, f := range c.Functions {
		for _, l := range f.Lemmas {
			if l.Name == "" || l.Postcondition == "" {
				return fmt.Errorf("function %s: a lemma needs a name and a postcondition", f.Name)
			}
		}
	}
	for _, inv := range c.Invariants {
		if inv.Text == "" {
			return fmt.Errorf("invariants: an invariant without text")
		}
		if inv.Verification != "" && !verifications[inv.Verification] {
			return fmt.Errorf("invariant %q: verification %q is smt, smt+pbt or runtime_and_pbt", inv.Text, inv.Verification)
		}
	}
	return nil
}
