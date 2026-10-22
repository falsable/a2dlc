package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func readDelivery(t *testing.T, dir, slice string) contract.DeliveryPlan {
	t.Helper()
	var plan contract.DeliveryPlan
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run", slice, ArtDelivery), &plan)
	return plan
}

func withDelivery(writes bool, comp *contract.Compensation) func(*contract.Intent) {
	return func(in *contract.Intent) {
		in.SliceID = "payment.v1"
		in.Invariants = []string{"INV-01 conservation"}
		in.Delivery = &contract.Delivery{Writes: &writes, Compensation: comp}
	}
}

func b(v bool) *bool { return &v }

func TestStage7BuildsTheDeliveryPlanOfTheApprovedCandidate(t *testing.T) {
	a, dir := repoLite(t)
	intent := intentFile(t, withDelivery(true, &contract.Compensation{Strategy: contract.CompensatingEvents, Description: "reverse entry by tx_id"}))
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["7"].Status != contract.StageDone || byStage["7"].Artifact == nil || byStage["7"].Artifact.Path != ArtDelivery ||
		!strings.Contains(byStage["7"].Reason, "3 rings (1 % → 10 % → 100 %)") || !strings.Contains(byStage["7"].Reason, "a2dlc.payment.v1") || *st.Outcome != contract.PASS {
		t.Fatalf("%+v %v", byStage["7"], *st.Outcome)
	}
	plan := readDelivery(t, dir, "payment.v1")
	if plan.FeatureFlag != "a2dlc.payment.v1" || plan.Mode != contract.ModeGreenfield || plan.Candidate != nil || !plan.Writes ||
		plan.Compensation == nil || plan.MaxRing != 100 || plan.BlockedBy != nil || plan.Parity != nil {
		t.Fatalf("%+v", plan)
	}
	wants := "[{1 [sentinel_green]} {10 [sentinel_green compensation_declared]} {100 [sentinel_green compensation_declared reconciliation_green]}]"
	if fmt.Sprint(plan.Rings) != wants {
		t.Fatalf("%v", plan.Rings)
	}
	// a writing slice with no declared sentinel and no latency SLA gets the defaults, never null, and the reason says so
	if fmt.Sprint(plan.Sentinel.Invariants) != "[INV-01 conservation]" || plan.Sentinel.Reconciliation != contract.ReconciliationPeriodic ||
		plan.Sentinel.ErrorRateDeltaMax == nil || *plan.Sentinel.ErrorRateDeltaMax != defaultErrorRateDeltaMax ||
		plan.Sentinel.LatencyP99MsMax == nil || *plan.Sentinel.LatencyP99MsMax != defaultLatencyP99MsMax ||
		!strings.Contains(byStage["7"].Reason, "latency_p99_ms_max takes the default 200 ms") {
		t.Fatalf("%+v", plan.Sentinel)
	}
	if problems, err := VerifyRun(a, "payment.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

func TestStage7WriteWithoutCompensationIsCB06(t *testing.T) {
	a, dir := repoLite(t)
	intent := intentFile(t, withDelivery(true, nil))
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "payment.v1", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["7"].Status != contract.StageEscalated || !strings.Contains(byStage["7"].Reason, "CB-06") || !strings.Contains(byStage["7"].Reason, "1 % ring") ||
		byStage["7"].Artifact == nil || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", byStage["7"], *st.Outcome)
	}
	plan := readDelivery(t, dir, "payment.v1")
	if plan.MaxRing != 1 || plan.BlockedBy == nil || *plan.BlockedBy != contract.BlockedByCB06 || plan.Compensation != nil {
		t.Fatalf("%+v", plan)
	}
	escalations := readEscalations(t, dir, "payment.v1")
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "missing_compensation" || escalations.Items[0].Target != "payment.v1::delivery" {
		t.Fatalf("%+v", escalations.Items)
	}
	if problems, err := VerifyRun(a, "payment.v1"); err != nil || len(problems) != 0 {
		t.Fatalf("%v %v", problems, err)
	}
}

func TestBuildPlanRefactorRingsAndOwnSentinel(t *testing.T) {
	zero := 0.0
	in := contract.Intent{SliceID: "core.v1", Invariants: []string{"INV-01"}, Delivery: &contract.Delivery{Writes: b(false), Rings: []int{5, 100},
		Sentinel: &contract.Sentinel{ErrorRateDeltaMax: &zero, Invariants: []string{}}}}
	sha := "d165efc1a2b3c4d5e6f708192a3b4c5d6e7f8091"
	plan := buildPlan(in, "core.v1", contract.ModeRefactor, &sha, nil)
	// without writes nobody requires compensation; the sentinel carries its own invariants (none), so there is no
	// reconciliation; in refactor the parity is only read-only
	if fmt.Sprint(plan.Rings) != "[{5 [sentinel_green]} {100 [sentinel_green]}]" || plan.MaxRing != 100 || plan.BlockedBy != nil {
		t.Fatalf("%+v", plan)
	}
	if plan.Sentinel.Reconciliation != contract.ReconciliationNone || len(plan.Sentinel.Invariants) != 0 || *plan.Sentinel.ErrorRateDeltaMax != 0 ||
		plan.Sentinel.LatencyP99MsMax != nil {
		t.Fatalf("%+v", plan.Sentinel)
	}
	if plan.Parity == nil || plan.Parity.Mode != "scientist_read_only" || plan.Parity.WritePaths != "canary_with_compensation" || *plan.Candidate != sha ||
		plan.Parity.Paths != nil || plan.Canary != nil {
		t.Fatalf("%+v", plan)
	}
	// writes without compensation: CB-06 keeps max_ring at 1 %, even though the intent declares its own rings (§5)
	// and only the rings up to 1 % stay in the plan
	in.Delivery = &contract.Delivery{Writes: b(true), Rings: []int{10, 100}}
	plan = buildPlan(in, "core.v1", contract.ModeGreenfield, nil, nil)
	if plan.MaxRing != 1 || plan.BlockedBy == nil || fmt.Sprint(plan.Rings) != "[{1 [sentinel_green]}]" || plan.Parity != nil ||
		plan.Sentinel.Reconciliation != contract.ReconciliationPeriodic {
		t.Fatalf("%+v", plan)
	}
}

func TestBuildPlanWithoutCompensationKeepsOnlyTheOnePercentRing(t *testing.T) {
	in := contract.Intent{SliceID: "payment.v1", Delivery: &contract.Delivery{Writes: b(true)}}
	plan := buildPlan(in, "payment.v1", contract.ModeGreenfield, nil, nil)
	if fmt.Sprint(plan.Rings) != "[{1 [sentinel_green]}]" || plan.MaxRing != 1 || plan.BlockedBy == nil || *plan.BlockedBy != contract.BlockedByCB06 {
		t.Fatalf("%+v", plan)
	}
	// with compensation the same declaration keeps the three default rings
	in.Delivery.Compensation = &contract.Compensation{Strategy: contract.VersionedRecords, Description: "tag rows with slice version"}
	plan = buildPlan(in, "payment.v1", contract.ModeGreenfield, nil, nil)
	if len(plan.Rings) != 3 || plan.MaxRing != 100 || plan.BlockedBy != nil {
		t.Fatalf("%+v", plan)
	}
}

func TestBuildPlanRefactorAnillosYSentinelPropios(t *testing.T) {
	sha := "d165efc1a2b3c4d5e6f708192a3b4c5d6e7f8091"
	in := contract.Intent{SliceID: "core.v1", Invariants: []string{"INV-01 conservation"},
		SLAs: []contract.SLA{{Metric: "p99 latency", Op: "<", Value: 180.7, Unit: "ms"}},
		Delivery: &contract.Delivery{Writes: b(true), ReadOnlyPaths: []string{"legacy/**"},
			Compensation: &contract.Compensation{Strategy: contract.CompensatingEvents, Description: "reverse entry by tx_id"}}}
	changed := []string{"core/ledger.go", "legacy/report.go", "core/tx.go"}
	plan := buildPlan(in, "core.v1", contract.ModeRefactor, &sha, changed)
	// the read-only glob is the parity; the changed paths it does not match get the canary with compensation
	if plan.Parity == nil || fmt.Sprint(plan.Parity.Paths) != "[legacy/**]" {
		t.Fatalf("%+v", plan.Parity)
	}
	if plan.Canary == nil || fmt.Sprint(plan.Canary.Paths) != "[core/ledger.go core/tx.go]" {
		t.Fatalf("%+v", plan.Canary)
	}
	// the SLA sets the latency threshold (floor of 180.7), the sentinel has no error rate declared: the default
	if plan.Sentinel.LatencyP99MsMax == nil || *plan.Sentinel.LatencyP99MsMax != 180 ||
		plan.Sentinel.ErrorRateDeltaMax == nil || *plan.Sentinel.ErrorRateDeltaMax != defaultErrorRateDeltaMax {
		t.Fatalf("%+v", plan.Sentinel)
	}
	// outside refactor there is no parity, but the canary still covers the write paths
	plan = buildPlan(in, "core.v1", contract.ModeGreenfield, &sha, changed)
	if plan.Parity != nil || plan.Canary == nil || len(plan.Canary.Paths) != 2 {
		t.Fatalf("%+v", plan)
	}
	// a slice that writes nothing has no canary and keeps the thresholds it declared (none: null)
	in.Delivery = &contract.Delivery{Writes: b(false), ReadOnlyPaths: []string{"legacy/**"}}
	plan = buildPlan(in, "core.v1", contract.ModeRefactor, &sha, changed)
	if plan.Canary != nil || plan.Sentinel.ErrorRateDeltaMax != nil || plan.Sentinel.LatencyP99MsMax != nil {
		t.Fatalf("%+v", plan)
	}
}

// A writing slice that declares its sentinel keeps those thresholds and invariants: neither the SLA nor the defaults
// overwrite them.
func TestBuildPlanWritesKeepsTheDeclaredSentinel(t *testing.T) {
	errMax, lat := 0.05, 50
	in := contract.Intent{SliceID: "pay.v1", Invariants: []string{"INV-01"},
		SLAs: []contract.SLA{{Metric: "p99 latency", Op: "<", Value: 180, Unit: "ms"}},
		Delivery: &contract.Delivery{Writes: b(true),
			Compensation: &contract.Compensation{Strategy: contract.CompensatingEvents, Description: "reverse entry by tx_id"},
			Sentinel:     &contract.Sentinel{ErrorRateDeltaMax: &errMax, LatencyP99MsMax: &lat, Invariants: []string{"INV-02 no double charge"}}}}
	plan := buildPlan(in, "pay.v1", contract.ModeGreenfield, nil, []string{"pay/tx.go"})
	if plan.Sentinel.ErrorRateDeltaMax == nil || *plan.Sentinel.ErrorRateDeltaMax != 0.05 ||
		plan.Sentinel.LatencyP99MsMax == nil || *plan.Sentinel.LatencyP99MsMax != 50 ||
		fmt.Sprint(plan.Sentinel.Invariants) != "[INV-02 no double charge]" || plan.Sentinel.Reconciliation != contract.ReconciliationPeriodic {
		t.Fatalf("%+v", plan.Sentinel)
	}
	if note := sentinelNote(in); note != "" {
		t.Fatalf("declared thresholds need no note: %q", note)
	}
}

func TestLatencyP99FromSLAs(t *testing.T) {
	cases := []struct {
		name     string
		slas     []contract.SLA
		want     int
		ok       bool
		unmapped int
	}{
		{"none", nil, 0, false, 0},
		{"p99 upper bound", []contract.SLA{{Metric: "p99 latency", Op: "<", Value: 200, Unit: "ms"}}, 200, true, 0},
		{"tightest p99 bound wins", []contract.SLA{{Metric: "p99 latency", Op: "<=", Value: 300, Unit: "ms"}, {Metric: "p99", Op: "<", Value: 150.9, Unit: "ms"}}, 150, true, 0},
		{"p50 latency does not set the p99 ceiling", []contract.SLA{{Metric: "p50 latency", Op: "<", Value: 20, Unit: "ms"}}, 0, false, 0},
		{"bare latency does not set the p99 ceiling", []contract.SLA{{Metric: "latency", Op: "<=", Value: 50, Unit: "ms"}}, 0, false, 0},
		{"error rate does not set the p99 ceiling", []contract.SLA{{Metric: "error_rate", Op: "<", Value: 1, Unit: "%"}}, 0, false, 0},
		{"seconds convert to ms", []contract.SLA{{Metric: "p99 latency", Op: "<", Value: 0.2, Unit: "s"}}, 200, true, 0},
		{"0.29 s is 290 ms without float loss", []contract.SLA{{Metric: "p99", Op: "<", Value: 0.29, Unit: "s"}}, 290, true, 0},
		{"milliseconds spelled out", []contract.SLA{{Metric: "p99", Op: "<", Value: 150, Unit: "milliseconds"}}, 150, true, 0},
		{"fractions below 1 ms count as 1", []contract.SLA{{Metric: "p99", Op: "<", Value: 0.4, Unit: "ms"}}, 1, true, 0},
		{"lower bound is not a ceiling and is reported", []contract.SLA{{Metric: "p99", Op: ">", Value: 50, Unit: "ms"}}, 0, false, 1},
		{"unknown unit is reported", []contract.SLA{{Metric: "p99", Op: "<", Value: 200, Unit: "us"}}, 0, false, 1},
		{"a reported p99 does not hide a mapped one", []contract.SLA{{Metric: "p99", Op: ">", Value: 50, Unit: "ms"}, {Metric: "p99", Op: "<", Value: 300, Unit: "ms"}}, 300, true, 1},
	}
	for _, c := range cases {
		got, ok, unmapped := latencyP99FromSLAs(c.slas)
		if got != c.want || ok != c.ok || len(unmapped) != c.unmapped {
			t.Fatalf("%s: got %d %v unmapped %v, want %d %v unmapped %d", c.name, got, ok, unmapped, c.want, c.ok, c.unmapped)
		}
	}
}

func TestSentinelNoteNamesDefaultsAndUnmappedP99(t *testing.T) {
	const errDefault = "error_rate_delta_max takes the default 0.01"
	const latDefault = "latency_p99_ms_max takes the default 200 ms"
	errMax, lat := 0.05, 50
	cases := []struct {
		name     string
		in       contract.Intent
		contains []string
		absent   []string
	}{
		{"not writing: nothing filled in", contract.Intent{Delivery: &contract.Delivery{Writes: b(false)}}, nil, []string{errDefault, latDefault}},
		{"writing, no sentinel, no SLA: both defaults named", contract.Intent{Delivery: &contract.Delivery{Writes: b(true)}}, []string{errDefault, latDefault}, nil},
		{"writing with a mapped p99: only the error default", contract.Intent{SLAs: []contract.SLA{{Metric: "p99", Op: "<", Value: 0.2, Unit: "s"}},
			Delivery: &contract.Delivery{Writes: b(true)}}, []string{errDefault}, []string{latDefault, "not mapped"}},
		{"p50 SLA alone: default named, not reported as unmapped", contract.Intent{SLAs: []contract.SLA{{Metric: "p50 latency", Op: "<", Value: 20, Unit: "ms"}},
			Delivery: &contract.Delivery{Writes: b(true)}}, []string{latDefault}, []string{"not mapped"}},
		{"p99 lower bound is reported as unmapped and the default is named", contract.Intent{SLAs: []contract.SLA{{Metric: "p99", Op: ">", Value: 50, Unit: "ms"}},
			Delivery: &contract.Delivery{Writes: b(true)}}, []string{"p99 SLA not mapped", "p99 > 50 ms", latDefault}, nil},
		{"both thresholds declared: nothing to say", contract.Intent{Delivery: &contract.Delivery{Writes: b(true),
			Sentinel: &contract.Sentinel{ErrorRateDeltaMax: &errMax, LatencyP99MsMax: &lat}}}, nil, []string{errDefault, latDefault}},
	}
	for _, c := range cases {
		note := sentinelNote(c.in)
		for _, s := range c.contains {
			if !strings.Contains(note, s) {
				t.Fatalf("%s: note %q lacks %q", c.name, note, s)
			}
		}
		for _, s := range c.absent {
			if strings.Contains(note, s) {
				t.Fatalf("%s: note %q has %q", c.name, note, s)
			}
		}
		if c.contains == nil && note != "" {
			t.Fatalf("%s: want no note, got %q", c.name, note)
		}
	}
}

// A file the candidate deletes is a changed path too (stageDelivery judges changed plus deleted). Here a deleted file
// under the read-only glob escalates as protected_boundary, and a deleted write path lands in the canary.
func TestStage7DeletedPathsReachBoundaryAndCanary(t *testing.T) {
	a, dir := repoLite(t)
	write(t, dir, "legacy/old.go", "package legacy\n")
	write(t, dir, "src/gone.go", "package src\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "files the candidate deletes")
	for _, rel := range []string{"legacy/old.go", "src/gone.go"} {
		if err := os.Remove(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Fatal(err)
		}
	}
	intent := intentFile(t, func(in *contract.Intent) {
		in.SliceID = "report.v1"
		in.Delivery = &contract.Delivery{Writes: b(true), ReadOnlyPaths: []string{"legacy/**"},
			Compensation: &contract.Compensation{Strategy: contract.VersionedRecords, Description: "tag rows with slice version"}}
	})
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "report.v1", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["7"].Status != contract.StageEscalated || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", byStage["7"], *st.Outcome)
	}
	escalations := readEscalations(t, dir, "report.v1")
	if len(escalations.Items) != 1 || escalations.Items[0].Kind != "protected_boundary" || !strings.Contains(escalations.Items[0].Evidence.Message, "legacy/old.go") ||
		strings.Contains(escalations.Items[0].Evidence.Message, "src/gone.go") {
		t.Fatalf("%+v", escalations.Items)
	}
	plan := readDelivery(t, dir, "report.v1")
	if plan.Canary == nil || fmt.Sprint(plan.Canary.Paths) != "[src/gone.go]" {
		t.Fatalf("%+v", plan.Canary)
	}
}

func TestStage7ChangedReadOnlyPathEscalates(t *testing.T) {
	a, dir := repoLite(t)
	intent := intentFile(t, func(in *contract.Intent) {
		in.SliceID = "report.v1"
		in.Delivery = &contract.Delivery{Writes: b(false), ReadOnlyPaths: []string{"legacy/**"}}
	})
	// an uncommitted change inside the read-only glob is in the candidate tree (the working tree is judged)
	write(t, dir, "legacy/report.go", "package legacy\n")
	st, err := Run(context.Background(), a, &fakeLauncher{}, Options{Slice: "report.v1", Intent: intent})
	if err != nil {
		t.Fatal(err)
	}
	byStage := byID(st)
	if byStage["7"].Status != contract.StageEscalated || !strings.Contains(byStage["7"].Reason, "read-only") || *st.Outcome != "escalated" {
		t.Fatalf("%+v %v", byStage["7"], *st.Outcome)
	}
	escalations := readEscalations(t, dir, "report.v1")
	found := false
	for _, e := range escalations.Items {
		if e.Kind == "protected_boundary" && e.Target == "report.v1::delivery" && strings.Contains(e.Evidence.Message, "legacy/report.go") {
			found = true
		}
	}
	if !found {
		t.Fatalf("%+v", escalations.Items)
	}
}

// The delivery.json the binary writes is checked against contracts/delivery.schema.json, the contract the platform
// reads. The walker covers what the schema states for this file: unknown and missing keys, scalar types, enums, and
// the nested objects, arrays and oneOf of parity, canary, sentinel and rings. The allOf rule (writes true needs the
// sentinel numbers) is not walked; the sentinel tests above check it.
func TestStage7DeliveryJSONMatchesTheSchema(t *testing.T) {
	schemaBytes, err := os.ReadFile(filepath.Join("..", "..", "contracts", "delivery.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaBytes, &schema); err != nil {
		t.Fatal(err)
	}
	sha := "d165efc1a2b3c4d5e6f708192a3b4c5d6e7f8091"
	in := contract.Intent{SliceID: "core.v1", Invariants: []string{"INV-01 conservation"},
		SLAs: []contract.SLA{{Metric: "p99 latency", Op: "<", Value: 180.7, Unit: "ms"}},
		Delivery: &contract.Delivery{Writes: b(true), ReadOnlyPaths: []string{"legacy/**"},
			Compensation: &contract.Compensation{Strategy: contract.CompensatingEvents, Description: "reverse entry by tx_id"}}}
	plan := buildPlan(in, "core.v1", contract.ModeRefactor, &sha, []string{"core/ledger.go", "legacy/report.go"})
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if problems := schemaProblems(schema, doc, "delivery"); len(problems) != 0 {
		t.Fatalf("delivery.json does not match the schema: %v", problems)
	}
	// the walker itself must catch drift, or the check above proves nothing
	doc.(map[string]any)["bogus"] = true
	if problems := schemaProblems(schema, doc, "delivery"); len(problems) != 1 || !strings.Contains(problems[0], "bogus") {
		t.Fatalf("an unknown key went unnoticed: %v", problems)
	}
}

// schemaProblems walks a decoded JSON value against a JSON Schema node (the subset described on the test above).
func schemaProblems(s map[string]any, v any, at string) []string {
	if alts, ok := s["oneOf"].([]any); ok {
		if v == nil {
			return nil
		}
		var first []string
		for _, a := range alts {
			as, _ := a.(map[string]any)
			if t, _ := as["type"].(string); t == "null" {
				continue
			}
			p := schemaProblems(as, v, at)
			if len(p) == 0 {
				return nil
			}
			if first == nil {
				first = p
			}
		}
		return first
	}
	if v == nil {
		return nil
	}
	if enum, ok := s["enum"].([]any); ok {
		found := false
		for _, e := range enum {
			if e == v {
				found = true
			}
		}
		if !found {
			return []string{fmt.Sprintf("%s: %v is not in the enum", at, v)}
		}
	}
	var out []string
	switch t, _ := s["type"].(string); t {
	case "object":
		obj, ok := v.(map[string]any)
		if !ok {
			return []string{at + ": want an object"}
		}
		props, _ := s["properties"].(map[string]any)
		if s["additionalProperties"] == false {
			for k := range obj {
				if _, ok := props[k]; !ok {
					out = append(out, fmt.Sprintf("%s: unknown key %q", at, k))
				}
			}
		}
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if _, ok := obj[r.(string)]; !ok {
					out = append(out, fmt.Sprintf("%s: missing key %q", at, r))
				}
			}
		}
		for k, pv := range obj {
			if ps, ok := props[k].(map[string]any); ok {
				out = append(out, schemaProblems(ps, pv, at+"."+k)...)
			}
		}
	case "array":
		arr, ok := v.([]any)
		if !ok {
			return []string{at + ": want an array"}
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, e := range arr {
				out = append(out, schemaProblems(items, e, fmt.Sprintf("%s[%d]", at, i))...)
			}
		}
	case "string":
		if _, ok := v.(string); !ok {
			out = append(out, at+": want a string")
		}
	case "boolean":
		if _, ok := v.(bool); !ok {
			out = append(out, at+": want a boolean")
		}
	case "integer", "number":
		f, ok := v.(float64)
		if !ok || (t == "integer" && f != float64(int64(f))) {
			out = append(out, at+": want a "+t)
		}
	}
	return out
}
