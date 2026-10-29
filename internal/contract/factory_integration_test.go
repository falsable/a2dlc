package contract

import (
	"strings"
	"testing"
)

func TestFactoryHoldoutValidate(t *testing.T) {
	if err := readFactory[FactoryHoldout](t, "factory-holdout__tinylinks.json").Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*FactoryHoldout){
		"has two units": func(h *FactoryHoldout) { h.Units[1].IssueID = h.Units[0].IssueID },
		"is a unit of":  func(h *FactoryHoldout) { h.Units[1].Files = h.Units[0].Files },
	}
	for want, breaks := range cases {
		h := readFactory[FactoryHoldout](t, "factory-holdout__tinylinks.json")
		breaks(&h)
		if err := h.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

// An integration record cannot say main moved, count a lane that is not integrated, or integrate a lane without a PASS
// check and a passing holdout.
func TestFactoryIntegrationValidate(t *testing.T) {
	const name = "factory-integration__two-lanes.json"
	if err := readFactory[FactoryIntegration](t, name).Validate(); err != nil {
		t.Fatal(err)
	}
	fail, zero, sha := "FAIL", 1, strings.Repeat("3", 40)
	cases := map[string]func(*FactoryIntegration){
		"main moved":                                 func(r *FactoryIntegration) { r.MainShaAfter = sha },
		"integrated is":                              func(r *FactoryIntegration) { r.Integrated = 2 },
		"status holdout_failed and commit":           func(r *FactoryIntegration) { r.Lanes[1].Commit = &sha },
		"integrated without a PASS check":            func(r *FactoryIntegration) { r.Lanes[0].CheckVerdict = &fail },
		"without a PASS check and a passing holdout": func(r *FactoryIntegration) { r.Lanes[0].HoldoutExit = &zero },
		"queue_position":                             func(r *FactoryIntegration) { r.Lanes[2].QueuePosition = &zero },
	}
	for want, breaks := range cases {
		r := readFactory[FactoryIntegration](t, name)
		breaks(&r)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

// A merge queue record has usage exactly when a lane was rebuilt, and it is the sum of the rebuilds; the reference
// integration never rebuilds.
func TestIntegrationRebuildUsageIsTheSumOfTheRebuilds(t *testing.T) {
	const name = "factory-integration__rebuilt.json"
	if err := readFactory[FactoryIntegration](t, name).Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*FactoryIntegration){
		"usage is not the sum of the rebuilds": func(r *FactoryIntegration) { r.Usage.OutputTokens++ },
		"exactly when a lane was rebuilt":      func(r *FactoryIntegration) { r.Usage = nil },
		"rebuild outcome":                      func(r *FactoryIntegration) { r.Lanes[1].Rebuild.CandidateRef = nil },
		"only the merge queue rebuilds": func(r *FactoryIntegration) {
			r.Integrator, r.IntegrationBranch = IntegratorReference, r.IntegrationBranch+"-reference"
		},
	}
	for want, breaks := range cases {
		r := readFactory[FactoryIntegration](t, name)
		breaks(&r)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
	// a queue record with no rebuild and no usage stays valid
	if err := readFactory[FactoryIntegration](t, "factory-integration__two-lanes.json").Validate(); err != nil {
		t.Error(err)
	}
}
