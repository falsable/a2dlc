package contract

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// missingRequiredInvalid lists the invalid examples whose only fault is a required field the document lacks (built by
// generate_invalid.py with pop), with the path DecodeStrict must name. A struct decode used to fill in a zero value.
var missingRequiredInvalid = map[string]string{
	"role-response__builder-missing-files-changed.json":             "$.payload.files_changed",
	"role-response__builder-missing-summary.json":                   "$.payload.summary",
	"role-response__builder-missing-cost.json":                      "$.cost",
	"role-response__builder-missing-attempt.json":                   "$.attempt",
	"role-response__builder-lemma-missing-postcondition.json":       "$.payload.lemmas[0].postcondition",
	"role-response__saboteur-test-missing-hash.json":                "$.payload.test_files[0].sha256",
	"role-response__inquisitor-discrepancy-missing-resolution.json": "$.payload.discrepancies[0].resolution",
	"role-response__back-translator-missing-prose.json":             "$.payload.prose",
	"role-response__planner-plan-cyclic-shape.json":                 "$.payload.plan.slices",
	"role-response__refused-missing-reason.json":                    "$.reason", // required by an if/then on status
	"roles__missing-argv.json":                                      "argv",
	"roles__tier-up-missing-argv.json":                              "argv",
	"plan__without-shared.json":                                     "$.shared",
	"plan-state__slice-without-reason.json":                         "reason",
	"plan-state__slice-without-definition.json":                     "definition_sha256",
	"baseline__missing-cached.json":                                 "cached",
	"capture__missing-selection.json":                               "selection",
	"approvals__missing-file-blob.json":                             "file_blob",
	"doctor__finding-without-fix.json":                              "fix",
	"authorization__missing-expires-at.json":                        "expires_at",
	"authorization__missing-intent-sha256.json":                     "intent_sha256",
	"run-ledger__authorization-used-unsigned.json":                  "signature",
	"run-ledger__authorization-binding-without-sha256.json":         "sha256",
	"findings__without-base-ref.json":                               "$.base_ref",
	"findings__raised-without-target.json":                          "$.target",
	"findings__label-without-by.json":                               "$.by",
	"findings__intentional-without-decision.json":                   "$.decision",
}

// Each document of the list lacks a required field: DecodeStrict rejects it and the error names the field.
func TestDecodeStrictRejectsMissingRequiredField(t *testing.T) {
	types := exampleTypes()
	for name, path := range missingRequiredInvalid {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(invalidDir, name))
			if err != nil {
				t.Fatal(err)
			}
			err = DecodeStrictBytes(b, types[strings.SplitN(name, "__", 2)[0]]())
			if err == nil || !strings.Contains(err.Error(), "required field missing") || !strings.Contains(err.Error(), path) {
				t.Errorf("err = %v, want a required-field error naming %s", err, path)
			}
		})
	}
}

// The payload of an ok response is decoded on its own by the stage that reads it: it enforces required too, so a builder
// that omits files_changed is rejected there as well as in the response that carries it.
func TestDecodeStrictBytesRejectsBuilderPayloadWithoutFilesChanged(t *testing.T) {
	for payload, want := range map[string]string{
		`{"summary":"s"}`:                    "$.files_changed",
		`{"files_changed":[]}`:               "$.summary",
		`{"summary":"s","files_changed":[]}`: "",
		`{"summary":"s","files_changed":[],"lemmas":[{"name":"f"}]}`: "$.lemmas[0].postcondition",
	} {
		var r BuilderResponse
		err := DecodeStrictBytes([]byte(payload), &r)
		switch {
		case want == "" && err != nil:
			t.Errorf("%s: %v", payload, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("%s: err = %v, want %s", payload, err, want)
		}
	}
}

// The schemaOf registry and the example types are the same list: a top-level document that gains a type must gain its
// schema, or its required fields would be left unchecked without a sound.
func TestSchemaOfCoversTheExampleTypes(t *testing.T) {
	for prefix, newValue := range exampleTypes() {
		got := schemaOf[reflect.TypeOf(newValue()).Elem()]
		if got != prefix+".schema.json" {
			t.Errorf("%s: schemaOf = %q, want %q", prefix, got, prefix+".schema.json")
		}
	}
	for typ, ref := range schemaOf {
		if _, _, err := resolveRef("", ref); err != nil {
			t.Errorf("%s: %v", typ, err)
		}
	}
}

// Control: removing a top-level key that the schema requires makes the valid example fail naming it, and removing one
// that no `required` of its schema (or of the files it refers to) mentions leaves it valid. The sets come from the
// schema files read as plain JSON here, not from the code under test.
func TestRequiredTopLevelKeysOfValidExamples(t *testing.T) {
	types := exampleTypes()
	examples, _ := filepath.Glob(filepath.Join(validDir, "*.json"))
	removedRequired, removedOptional := 0, 0
	for _, e := range examples {
		name := filepath.Base(e)
		prefix := strings.SplitN(name, "__", 2)[0]
		if _, ok := inOtherPackage[prefix]; ok {
			continue
		}
		topRequired, anyRequired := schemaRequired(t, prefix+".schema.json")
		b, _ := os.ReadFile(e)
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(b, &doc); err != nil {
			continue // an example that is not an object has no key to remove
		}
		for key := range doc {
			isTop, isAny := topRequired[key], anyRequired[key]
			if !isTop && isAny {
				continue // required only under a condition (if/then) or nested: whether it may go depends on the example
			}
			rest := map[string]json.RawMessage{}
			for k, v := range doc {
				if k != key {
					rest[k] = v
				}
			}
			out, _ := json.Marshal(rest)
			err := DecodeStrictBytes(out, types[prefix]())
			switch {
			case isTop:
				removedRequired++
				if err == nil || !strings.Contains(err.Error(), "required field missing") || !strings.Contains(err.Error(), "$."+key) {
					t.Errorf("%s without %s: err = %v, want a required-field error naming it", name, key, err)
				}
			default:
				removedOptional++
				if err != nil {
					t.Errorf("%s without %s (not required anywhere): %v", name, key, err)
				}
			}
		}
	}
	if removedRequired < 100 || removedOptional < 50 {
		t.Errorf("the control exercised %d required and %d optional removals: the examples or the schemas moved", removedRequired, removedOptional)
	}
}

// schemaRequired returns the keys in the top-level `required` of a schema file and the keys in any `required` array of
// that file or of the files it refers to, transitively (over-inclusive on purpose: it only decides what the control skips).
func schemaRequired(t *testing.T, file string) (top, all map[string]bool) {
	t.Helper()
	top, all = map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
	var visit func(f string, isRoot bool)
	visit = func(f string, isRoot bool) {
		if seen[f] {
			return
		}
		seen[f] = true
		b, err := os.ReadFile(filepath.Join(schemaDir, f))
		if err != nil {
			t.Fatal(err)
		}
		var doc any
		if err := json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		var walk func(n any, depth int)
		walk = func(n any, depth int) {
			switch v := n.(type) {
			case map[string]any:
				for k, x := range v {
					switch {
					case k == "required":
						for _, r := range strs(x) {
							all[r] = true
							if isRoot && depth == 0 {
								top[r] = true
							}
						}
					case k == "$ref":
						if s, ok := x.(string); ok {
							if ref, _, _ := strings.Cut(s, "#"); ref != "" {
								visit(ref, false)
							}
						}
					default:
						walk(x, depth+1)
					}
				}
			case []any:
				for _, x := range v {
					walk(x, depth+1)
				}
			}
		}
		walk(doc, 0)
	}
	visit(file, true)
	return top, all
}

// Control: an optional field may be left out; the builder's lemmas are optional.
func TestOptionalFieldsStayOptional(t *testing.T) {
	var r BuilderResponse
	if err := DecodeStrictBytes([]byte(`{"summary":"s","files_changed":["a.py"]}`), &r); err != nil {
		t.Errorf("without the optional lemmas: %v", err)
	}
}
