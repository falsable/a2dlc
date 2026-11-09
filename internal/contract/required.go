package contract

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/falsable/a2dlc/contracts"
)

// DecodeStrict rejects unknown fields (additionalProperties: false) by itself; this file adds what it cannot see: the
// `required` fields that the JSON Schema of the type declares, at every depth. The schema files are read from the
// embedded copy of contracts/ (the contract itself), so no list of required fields lives in Go to drift from them.
//
// shortcut: only `required` is enforced (not type, pattern, minimum...), and a oneOf/anyOf is enforced only on the
// alternative the value selects; where it selects none the value is left to the field-level checks that exist
// (Validate methods, invalidResponse). `not` is never enforced (findings.schema.json uses it inside its allOf/if-then
// branches to forbid the fields of another event kind): the `required` of an if-then branch is.
// Upgrade to a full JSON Schema validator if a second keyword needs enforcing.

// schemaOf maps a decoded type to the schema (file#pointer) that is its contract. A type that is not here is decoded
// as before, with no required check; TestSchemaOfCoversTheExampleTypes ties the top-level documents to their files.
var schemaOf = map[reflect.Type]string{
	reflect.TypeFor[Config]():                "config.schema.json",
	reflect.TypeFor[Request]():               "engine-request.schema.json",
	reflect.TypeFor[Response]():              "engine-response.schema.json",
	reflect.TypeFor[Verdict]():               "verdict.schema.json",
	reflect.TypeFor[CheckResponse]():         "check-response.schema.json",
	reflect.TypeFor[Waivers]():               "waivers.schema.json",
	reflect.TypeFor[Approvals]():             "approvals.schema.json",
	reflect.TypeFor[Authorizations]():        "authorization.schema.json",
	reflect.TypeFor[StubResponse]():          "engine-stub-response.schema.json",
	reflect.TypeFor[Escalations]():           "escalation.schema.json",
	reflect.TypeFor[SignaturesResponse]():    "engine-signatures-response.schema.json",
	reflect.TypeFor[Roles]():                 "roles.schema.json",
	reflect.TypeFor[RoleRequest]():           "role-request.schema.json",
	reflect.TypeFor[RoleResponse]():          "role-response.schema.json",
	reflect.TypeFor[RunState]():              "run-state.schema.json",
	reflect.TypeFor[RunLedger]():             "run-ledger.schema.json",
	reflect.TypeFor[Intent]():                "intent.schema.json",
	reflect.TypeFor[Route]():                 "route.schema.json",
	reflect.TypeFor[Plan]():                  "plan.schema.json",
	reflect.TypeFor[PlanState]():             "plan-state.schema.json",
	reflect.TypeFor[Tier0Response]():         "engine-tier0-response.schema.json",
	reflect.TypeFor[Candidate]():             "candidate.schema.json",
	reflect.TypeFor[MutateResponse]():        "engine-mutate-response.schema.json",
	reflect.TypeFor[MutationReport]():        "mutation.schema.json",
	reflect.TypeFor[SliceContract]():         "slice-contract.schema.json",
	reflect.TypeFor[Capture]():               "capture.schema.json",
	reflect.TypeFor[Probes]():                "probes.schema.json",
	reflect.TypeFor[AssumptionCatalogue]():   "assumption-catalogue.schema.json",
	reflect.TypeFor[ContractLint]():          "contract-lint.schema.json",
	reflect.TypeFor[Backtranslation]():       "backtranslation.schema.json",
	reflect.TypeFor[BacktranslationReview](): "backtranslation-review.schema.json",
	reflect.TypeFor[Z3Request]():             "z3-request.schema.json",
	reflect.TypeFor[Z3Response]():            "z3-response.schema.json",
	reflect.TypeFor[ComplexityResponse]():    "engine-complexity-response.schema.json",
	reflect.TypeFor[Selection]():             "selection.schema.json",
	reflect.TypeFor[ClassifyResponse]():      "engine-classify-lines-response.schema.json",
	reflect.TypeFor[ListTestsResponse]():     "engine-list-tests-response.schema.json",
	reflect.TypeFor[IntegrationReport]():     "integration.schema.json",
	reflect.TypeFor[DeliveryPlan]():          "delivery.schema.json",
	reflect.TypeFor[Baseline]():              "baseline.schema.json",
	reflect.TypeFor[Answers]():               "answers.schema.json",
	reflect.TypeFor[Golden]():                "golden.schema.json",
	reflect.TypeFor[DepsAllow]():             "deps-allow.schema.json",
	reflect.TypeFor[FuzzResponse]():          "engine-fuzz-response.schema.json",
	reflect.TypeFor[LemmasResponse]():        "engine-lemmas-response.schema.json",
	reflect.TypeFor[DepsResponse]():          "engine-deps-response.schema.json",
	reflect.TypeFor[GoldenResponse]():        "engine-golden-response.schema.json",
	reflect.TypeFor[Proposals]():             "proposals.schema.json",
	reflect.TypeFor[TracesResponse]():        "engine-traces-response.schema.json",
	reflect.TypeFor[IndexerResponse]():       "indexer-response.schema.json",
	reflect.TypeFor[Doctor]():                "doctor.schema.json",
	reflect.TypeFor[FindingEvent]():          "findings.schema.json",
	reflect.TypeFor[JournalEvent]():          "journal.schema.json",
	// The payload of an ok role response, decoded on its own by the stage that reads it.
	reflect.TypeFor[PlanApproval]():         "plan-state.schema.json#/$defs/plan_approval",
	reflect.TypeFor[BuilderResponse]():      "role-payloads.schema.json#/$defs/builder_response",
	reflect.TypeFor[SaboteurResponse]():     "role-payloads.schema.json#/$defs/saboteur_response",
	reflect.TypeFor[SpecifierResponse]():    "role-payloads.schema.json#/$defs/specifier_response",
	reflect.TypeFor[InquisitorResponse]():   "role-payloads.schema.json#/$defs/inquisitor_response",
	reflect.TypeFor[BTText]():               "role-payloads.schema.json#/$defs/back_translator_response",
	reflect.TypeFor[InquisitorBTResponse](): "role-payloads.schema.json#/$defs/inquisitor_bt_response",
	reflect.TypeFor[ProposeResponse]():      "role-payloads.schema.json#/$defs/propose_response",
	reflect.TypeFor[PlannerResponse]():      "role-payloads.schema.json#/$defs/planner_response",
}

var (
	schemaMu    sync.Mutex
	schemaCache = map[string]any{}
)

// loadSchema parses one embedded schema file once.
func loadSchema(file string) (any, error) {
	schemaMu.Lock()
	defer schemaMu.Unlock()
	if s, ok := schemaCache[file]; ok {
		return s, nil
	}
	b, err := contracts.FS.ReadFile(file)
	if err != nil {
		return nil, fmt.Errorf("schema %s is not embedded: %w", file, err)
	}
	var s any
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("schema %s: %w", file, err)
	}
	schemaCache[file] = s
	return s, nil
}

// resolveRef follows "<file>#<json pointer>", "#<json pointer>" (inside doc) or "<file>"; it returns the document the
// target lives in, because a "#/..." ref inside it is relative to that file.
func resolveRef(doc, ref string) (string, any, error) {
	file, ptr, _ := strings.Cut(ref, "#")
	if file == "" {
		file = doc
	}
	node, err := loadSchema(file)
	if err != nil {
		return "", nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(ptr, "/"), "/") {
		if part == "" {
			continue
		}
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch n := node.(type) {
		case map[string]any:
			node = n[part]
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(n) {
				return "", nil, fmt.Errorf("ref %s: no %q", ref, part)
			}
			node = n[i]
		default:
			return "", nil, fmt.Errorf("ref %s: no %q", ref, part)
		}
		if node == nil {
			return "", nil, fmt.Errorf("ref %s: no %q", ref, part)
		}
	}
	return file, node, nil
}

// checkRequired verifies that document b has every field its schema (the one registered for v's type) requires.
func checkRequired(b []byte, v any) error {
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer || rv.IsNil() {
		return nil
	}
	ref, ok := schemaOf[rv.Type().Elem()]
	if !ok {
		return nil
	}
	doc, root, err := resolveRef("", ref)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(b, &value); err != nil {
		return err
	}
	var missing []string
	if err := missingRequired(doc, root, value, "$", &missing, 0); err != nil {
		return err
	}
	if len(missing) > 0 {
		return fmt.Errorf("%s: required field missing: %s", strings.SplitN(ref, "#", 2)[0], strings.Join(missing, ", "))
	}
	return nil
}

const maxSchemaDepth = 64

// missingRequired appends to out the path of every required field that value lacks, following the schema through
// $ref, allOf, properties, items, additionalProperties, if/then/else and the oneOf/anyOf alternative the value selects.
func missingRequired(doc string, schema, value any, path string, out *[]string, depth int) error {
	s, ok := schema.(map[string]any)
	if !ok || depth > maxSchemaDepth {
		return nil
	}
	if ref, ok := s["$ref"].(string); ok {
		d, n, err := resolveRef(doc, ref)
		if err != nil {
			return err
		}
		if err := missingRequired(d, n, value, path, out, depth+1); err != nil {
			return err
		}
	}
	for _, sub := range list(s["allOf"]) {
		if err := missingRequired(doc, sub, value, path, out, depth+1); err != nil {
			return err
		}
	}
	for _, kw := range []string{"oneOf", "anyOf"} {
		if err := missingInAlternative(doc, list(s[kw]), value, path, out, depth); err != nil {
			return err
		}
	}
	if cond, has := s["if"]; has {
		branch := s["else"]
		if m, err := matches(doc, cond, value, depth+1); err != nil {
			return err
		} else if m {
			branch = s["then"]
		}
		if err := missingRequired(doc, branch, value, path, out, depth+1); err != nil {
			return err
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for _, r := range strs(s["required"]) {
			if _, has := v[r]; !has {
				*out = append(*out, path+"."+r)
			}
		}
		props, _ := s["properties"].(map[string]any)
		for _, k := range sortedKeys(v) {
			sub, declared := props[k]
			if !declared {
				sub = s["additionalProperties"]
			}
			if err := missingRequired(doc, sub, v[k], path+"."+k, out, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for i, el := range v {
			if err := missingRequired(doc, s["items"], el, path+"["+strconv.Itoa(i)+"]", out, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// missingInAlternative enforces the alternative of a oneOf/anyOf that the value selects: the ones it matches, or the
// only one of its JSON type; with several candidates and no match it enforces nothing rather than guess.
func missingInAlternative(doc string, alts []any, value any, path string, out *[]string, depth int) error {
	if len(alts) == 0 {
		return nil
	}
	var typed, matching []any
	for _, a := range alts {
		if t, err := typeCompatible(doc, a, value, depth+1); err != nil {
			return err
		} else if !t {
			continue
		}
		typed = append(typed, a)
		if m, err := matches(doc, a, value, depth+1); err != nil {
			return err
		} else if m {
			matching = append(matching, a)
		}
	}
	chosen := matching
	if len(chosen) == 0 && len(typed) == 1 {
		chosen = typed
	}
	for _, a := range chosen {
		if err := missingRequired(doc, a, value, path, out, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// typeCompatible reports whether the schema's `type` (looked up through $ref) admits the value's JSON type; a schema
// with no type admits any.
func typeCompatible(doc string, schema, value any, depth int) (bool, error) {
	s, ok := schema.(map[string]any)
	if !ok || depth > maxSchemaDepth {
		return true, nil
	}
	if ref, ok := s["$ref"].(string); ok {
		d, n, err := resolveRef(doc, ref)
		if err != nil {
			return false, err
		}
		return typeCompatible(d, n, value, depth+1)
	}
	return hasType(s, value), nil
}

// matches reports whether value satisfies schema as far as the keywords below go (type, const, enum, required,
// properties, items, minItems, not, allOf, anyOf, oneOf, if/then/else, $ref); any other keyword is taken as satisfied,
// so it can say true for a value a full validator rejects, never the reverse. It selects the alternative to enforce.
func matches(doc string, schema, value any, depth int) (bool, error) {
	s, ok := schema.(map[string]any)
	if !ok {
		return schema != false, nil
	}
	if depth > maxSchemaDepth {
		return true, nil
	}
	if ref, ok := s["$ref"].(string); ok {
		d, n, err := resolveRef(doc, ref)
		if err != nil {
			return false, err
		}
		if m, err := matches(d, n, value, depth+1); err != nil || !m {
			return false, err
		}
	}
	if !hasType(s, value) {
		return false, nil
	}
	if c, has := s["const"]; has && !reflect.DeepEqual(c, value) {
		return false, nil
	}
	if e, has := s["enum"].([]any); has {
		found := false
		for _, x := range e {
			found = found || reflect.DeepEqual(x, value)
		}
		if !found {
			return false, nil
		}
	}
	switch v := value.(type) {
	case map[string]any:
		for _, r := range strs(s["required"]) {
			if _, has := v[r]; !has {
				return false, nil
			}
		}
		props, _ := s["properties"].(map[string]any)
		for _, k := range sortedKeys(props) {
			if pv, has := v[k]; has {
				if m, err := matches(doc, props[k], pv, depth+1); err != nil || !m {
					return false, err
				}
			}
		}
	case []any:
		if n, has := s["minItems"].(float64); has && float64(len(v)) < n {
			return false, nil
		}
		if s["items"] != nil {
			for _, el := range v {
				if m, err := matches(doc, s["items"], el, depth+1); err != nil || !m {
					return false, err
				}
			}
		}
	}
	if n, has := s["not"]; has {
		if m, err := matches(doc, n, value, depth+1); err != nil || m {
			return false, err
		}
	}
	for _, sub := range list(s["allOf"]) {
		if m, err := matches(doc, sub, value, depth+1); err != nil || !m {
			return false, err
		}
	}
	for _, kw := range []string{"anyOf", "oneOf"} {
		alts := list(s[kw])
		if len(alts) == 0 {
			continue
		}
		found := false
		for _, a := range alts {
			m, err := matches(doc, a, value, depth+1)
			if err != nil {
				return false, err
			}
			found = found || m
		}
		if !found {
			return false, nil
		}
	}
	if cond, has := s["if"]; has {
		branch := s["else"]
		if m, err := matches(doc, cond, value, depth+1); err != nil {
			return false, err
		} else if m {
			branch = s["then"]
		}
		if branch != nil {
			return matches(doc, branch, value, depth+1)
		}
	}
	return true, nil
}

// hasType reports whether the JSON type of value is one that schema's `type` allows (any, if it has none).
func hasType(schema map[string]any, value any) bool {
	var types []string
	switch t := schema["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		types = strs(t)
	default:
		return true
	}
	got := "null"
	switch v := value.(type) {
	case bool:
		got = "boolean"
	case float64:
		got = "number"
		if v == float64(int64(v)) {
			got = "integer"
		}
	case string:
		got = "string"
	case []any:
		got = "array"
	case map[string]any:
		got = "object"
	}
	for _, t := range types {
		if t == got || (t == "number" && got == "integer") {
			return true
		}
	}
	return false
}

func list(v any) []any { l, _ := v.([]any); return l }

func strs(v any) []string {
	var out []string
	for _, x := range list(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
