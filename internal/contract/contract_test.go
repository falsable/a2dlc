package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func responseOK() Response {
	return Response{SchemaVersion: 1, OutOfScope: []OutOfScope{}, Targets: []TargetResult{{
		Target: "src/a.ts::f", Engine: "fast-check", EngineVersion: "3.23.2", Verdict: FAIL,
		SearchBudget: json.RawMessage(`{"examples":2000,"seed":42}`), Generators: "typed_default", Equivalence: "strict",
		Counterexample: &Counterexample{Args: "[1]", Old: "1", New: "2"}}}}
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Response){
		"":                              func(*Response) {},
		"FAIL without a counterexample": func(r *Response) { r.Targets[0].Counterexample = nil },
		"INCONCLUSIVE without reason":   func(r *Response) { r.Targets[0].Verdict = INCONCLUSIVE },
		"a file that was not requested": func(r *Response) { r.Targets[0].Target = "src/b.ts::f" },
		"the form <file>::<symbol>":     func(r *Response) { r.Targets[0].Target = "f" },
		"without search_budget":         func(r *Response) { r.Targets[0].SearchBudget = nil },
		"verdict \"WAIVED\"":            func(r *Response) { r.Targets[0].Verdict = "WAIVED" },
		"are required":                  func(r *Response) { r.OutOfScope = nil },
		"unknown engine":                func(r *Response) { r.Targets[0].Engine = "other" },
		"invalid out_of_scope":          func(r *Response) { r.OutOfScope = []OutOfScope{{File: "src/a.ts", Reason: "because I said so"}} },
	}
	for want, breaks := range cases {
		r := responseOK()
		breaks(&r)
		err := r.Validate([]string{"src/a.ts"})
		switch {
		case want == "" && err != nil:
			t.Errorf("the valid response failed: %v", err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

const (
	validDir   = "../../contracts/examples/valid"
	invalidDir = "../../contracts/examples/invalid"
	schemaDir  = "../../contracts"
)

// exampleTypes maps the prefix of an example's file name (the schema it belongs to) to the struct that decodes it.
func exampleTypes() map[string]func() any {
	return map[string]func() any{
		"config": func() any { return &Config{} }, "engine-request": func() any { return &Request{} },
		"engine-response": func() any { return &Response{} }, "verdict": func() any { return &Verdict{} },
		"check-response": func() any { return &CheckResponse{} },
		"waivers":        func() any { return &Waivers{} }, "approvals": func() any { return &Approvals{} },
		"engine-stub-response": func() any { return &StubResponse{} }, "escalation": func() any { return &Escalations{} },
		"engine-signatures-response": func() any { return &SignaturesResponse{} },
		"roles":                      func() any { return &Roles{} }, "role-request": func() any { return &RoleRequest{} },
		"role-response": func() any { return &RoleResponse{} }, "run-state": func() any { return &RunState{} },
		"run-ledger": func() any { return &RunLedger{} },
		"intent":     func() any { return &Intent{} }, "route": func() any { return &Route{} },
		// block 3
		"engine-tier0-response": func() any { return &Tier0Response{} }, "candidate": func() any { return &Candidate{} },
		// block 5
		"engine-mutate-response": func() any { return &MutateResponse{} }, "mutation": func() any { return &MutationReport{} },
		// block 6
		"slice-contract": func() any { return &SliceContract{} }, "capture": func() any { return &Capture{} },
		"probes": func() any { return &Probes{} }, "assumption-catalogue": func() any { return &AssumptionCatalogue{} },
		// block 7
		"contract-lint": func() any { return &ContractLint{} }, "backtranslation": func() any { return &Backtranslation{} },
		"backtranslation-review": func() any { return &BacktranslationReview{} },
		"z3-request":             func() any { return &Z3Request{} }, "z3-response": func() any { return &Z3Response{} },
		// block 8
		"engine-complexity-response": func() any { return &ComplexityResponse{} }, "selection": func() any { return &Selection{} },
		"engine-classify-lines-response": func() any { return &ClassifyResponse{} },
		// Stages 6 and 7
		"integration": func() any { return &IntegrationReport{} }, "delivery": func() any { return &DeliveryPlan{} },
		// Stage -1
		"baseline": func() any { return &Baseline{} },
		// v3.4 (wave 0): the schemas that the protocol's new stages and CLI commands write or read
		"answers": func() any { return &Answers{} }, "golden": func() any { return &Golden{} },
		"deps-allow": func() any { return &DepsAllow{} }, "engine-fuzz-response": func() any { return &FuzzResponse{} },
		"engine-lemmas-response": func() any { return &LemmasResponse{} }, "engine-deps-response": func() any { return &DepsResponse{} },
		"engine-golden-response": func() any { return &GoldenResponse{} },
		// v3.4 (wave 2a): capture_traces
		"engine-traces-response": func() any { return &TracesResponse{} },
		// Code Graph Indexer (Stage 6)
		"indexer-response": func() any { return &IndexerResponse{} },
		// cmd/factory (ADR meta/0008)
		"factory-config": func() any { return &FactoryConfig{} }, "factory-batch": func() any { return &FactoryBatch{} },
		"factory-report": func() any { return &FactoryReport{} },
		// factory integrate (bead a2dlc-3fu.4)
		"factory-holdout": func() any { return &FactoryHoldout{} }, "factory-integration": func() any { return &FactoryIntegration{} },
	}
}

// inOtherPackage names the schemas whose struct lives in an engine, not in the core: that one has its own decoding test.
var inOtherPackage = map[string]string{"blackbox": "engines/blackbox TestBlackboxExamplesDecode"}

// unknownPropertyInvalid lists the invalid examples that generate_invalid.py builds by adding or keeping a property
// that the schema does not declare (additionalProperties: false). Each must be rejected by DecodeStrict with an
// "unknown field" error.
var unknownPropertyInvalid = []string{
	"verdict__extra-field.json",
	"check-response__findings-count.json",
	"intent__extra-field.json",
	"baseline__extra-field.json",
	"golden__extra-field.json",
	"config__tier0-unknown-key.json",
	"engine-request__tier0-policy-unknown-key.json",
	"engine-request__tier0-budget-foreign.json",
	"engine-request__capture-traces-budget-foreign.json",
	"engine-traces-response__extra-field.json",
	"verdict__coverage-noted-extra-field.json",
	"engine-classify-lines-response__note-extra-field.json",
	"engine-request__classify-lines-budget-foreign.json",
	"roles__mounts-in-config.json",
	"roles__tier-up-nested.json",
	"capture__signature-with-body.json",
	"engine-signatures-response__symbol-with-body.json",
	"run-state__by-role-unknown.json",
	"waivers__approved-by-of-the-pilot.json",
	"factory-config__cap-foreign.json",
	"factory-batch__extra-field.json",
	"factory-report__stage-extra-field.json",
	"factory-report__usage-extra-field.json",
	"factory-report__tests-touched-extra-field.json",
	"factory-holdout__extra-field.json",
	"factory-report__killed-by-extra-field.json",
	"factory-integration__lane-extra-field.json",
	"factory-integration__usage-extra-field.json",
}

// knownLooseDecodes names invalid examples that the struct accepts although the schema rejects them. They are
// skipped, not failed, until the struct is fixed; the skip message is the gap. Remove the entry when it is fixed.
// Empty: every invalid example in unknownPropertyInvalid is rejected by its struct.
var knownLooseDecodes = map[string]string{}

// knownForeignKeys names the places where a valid example's re-marshalled struct emits a key the example does not
// have, keyed by "<example>:<path>". Such a place is skipped with its reason until the struct is fixed. Empty: none is
// known. The check cannot see a field tagged omitempty that the schema does not declare (marshal drops it); the
// top-level case of that is TestStructFieldsAreSchemaProperties.
var knownForeignKeys = map[string]string{}

// The valid examples of the contract decode strictly with these types: if a schema gains a field that the struct
// does not have, this fails.
func TestExamplesOfContractDecode(t *testing.T) {
	types := exampleTypes()
	examples, _ := filepath.Glob(filepath.Join(validDir, "*.json"))
	if len(examples) == 0 {
		t.Fatal("could not find the contract examples")
	}
	for _, e := range examples {
		prefix := strings.SplitN(filepath.Base(e), "__", 2)[0]
		if _, ok := inOtherPackage[prefix]; ok {
			continue
		}
		newValue, ok := types[prefix]
		if !ok {
			t.Errorf("%s: no type", e)
			continue
		}
		b, _ := os.ReadFile(e)
		if err := DecodeStrictBytes(b, newValue()); err != nil {
			t.Errorf("%s: %v", filepath.Base(e), err)
		}
	}
}

// Every valid example, decoded into its struct and marshalled again, must not produce a key the example lacks: a
// field the struct has but the schema does not would appear here (extra keys are checked at every depth). The limit
// is in knownForeignKeys.
func TestExamplesReMarshalWithoutForeignKeys(t *testing.T) {
	types := exampleTypes()
	examples, _ := filepath.Glob(filepath.Join(validDir, "*.json"))
	for _, e := range examples {
		name := filepath.Base(e)
		prefix := strings.SplitN(name, "__", 2)[0]
		if _, ok := inOtherPackage[prefix]; ok {
			continue
		}
		newValue, ok := types[prefix]
		if !ok {
			t.Errorf("%s: no type", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(e)
			if err != nil {
				t.Fatal(err)
			}
			v := newValue()
			if err := DecodeStrictBytes(b, v); err != nil {
				t.Fatal(err)
			}
			out, err := json.Marshal(v)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			var example, marshalled any
			if err := json.Unmarshal(b, &example); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(out, &marshalled); err != nil {
				t.Fatal(err)
			}
			var gaps []string
			for _, p := range keysMissingFrom("$", example, marshalled) {
				if gap, known := knownForeignKeys[name+":"+p]; known {
					gaps = append(gaps, p+" ("+gap+")")
					continue
				}
				t.Errorf("the struct emits %s, which the example does not have", p)
			}
			if len(gaps) > 0 {
				t.Skip("known gap: " + strings.Join(gaps, "; "))
			}
		})
	}
}

// keysMissingFrom returns the paths of the object keys in out that example does not have at the same place,
// recursing into objects and into arrays element by element. A value of another JSON type, or an array of another
// length, is reported too: the comparison would otherwise skip it silently.
func keysMissingFrom(path string, example, out any) []string {
	switch o := out.(type) {
	case map[string]any:
		e, ok := example.(map[string]any)
		if !ok {
			return []string{path + " (the struct emits an object where the example has another JSON type)"}
		}
		keys := make([]string, 0, len(o))
		for k := range o {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var missing []string
		for _, k := range keys {
			ev, has := e[k]
			if !has {
				missing = append(missing, path+"."+k)
				continue
			}
			missing = append(missing, keysMissingFrom(path+"."+k, ev, o[k])...)
		}
		return missing
	case []any:
		e, ok := example.([]any)
		if !ok {
			return []string{path + " (the struct emits an array where the example has another JSON type)"}
		}
		if len(o) != len(e) {
			return []string{path + " (the struct emits " + strconv.Itoa(len(o)) + " elements, the example has " + strconv.Itoa(len(e)) + ")"}
		}
		var missing []string
		for i := range o {
			missing = append(missing, keysMissingFrom(path+"["+strconv.Itoa(i)+"]", e[i], o[i])...)
		}
		return missing
	}
	return nil
}

// TestStructFieldsAreSchemaProperties checks the top level of every struct against its schema: each json field of the
// struct must be a property of the schema of the same name. It sees the omitempty fields that the re-marshal test
// cannot. Nested levels are covered by the strict decode of the valid examples, not here.
func TestStructFieldsAreSchemaProperties(t *testing.T) {
	for prefix, newValue := range exampleTypes() {
		if _, ok := inOtherPackage[prefix]; ok {
			continue
		}
		b, err := os.ReadFile(filepath.Join(schemaDir, prefix+".schema.json"))
		if err != nil {
			t.Errorf("%s: %v", prefix, err)
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(b, &schema); err != nil {
			t.Errorf("%s: %v", prefix, err)
			continue
		}
		typ := reflect.TypeOf(newValue()).Elem()
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("json"), ",")[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = f.Name
			}
			if _, ok := schema.Properties[name]; !ok {
				t.Errorf("%s: the struct %s has the field %q, which %s.schema.json does not declare", prefix, typ.Name(), name, prefix)
			}
		}
	}
}

// The invalid examples whose rule is an unknown property must fail the strict decode: if the struct gains a field
// the schema does not have, or loses the check, this fails.
// A null budget is not an object: the schema requires one for every typed operation and the struct must agree.
func TestRequestBudgetNullIsRejected(t *testing.T) {
	b, err := os.ReadFile("../../contracts/examples/invalid/engine-request__budget-null.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Request
	if err := DecodeStrictBytes(b, &r); err == nil || !strings.Contains(err.Error(), "must be an object") {
		t.Fatalf("null budget decoded: %v", err)
	}
}

func TestUnknownPropertyExamplesFailStrictDecode(t *testing.T) {
	types := exampleTypes()
	for _, name := range unknownPropertyInvalid {
		t.Run(name, func(t *testing.T) {
			newValue, ok := types[strings.SplitN(name, "__", 2)[0]]
			if !ok {
				t.Fatalf("no struct for %s", name)
			}
			b, err := os.ReadFile(filepath.Join(invalidDir, name))
			if err != nil {
				t.Fatalf("the list names a file the generator does not write: %v", err)
			}
			if gap, known := knownLooseDecodes[name]; known {
				t.Skip("known gap: " + gap)
			}
			err = DecodeStrictBytes(b, newValue())
			switch {
			case err == nil:
				t.Error("the struct accepts a property the schema does not declare")
			case !strings.Contains(err.Error(), "unknown field"):
				t.Errorf("rejected for another reason: %v", err)
			}
		})
	}
}
