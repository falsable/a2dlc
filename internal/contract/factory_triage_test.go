package contract

import (
	"strings"
	"testing"
)

// The triage's spend is part of the report's usage, so the run total, and with it cost per accepted change, counts it.
func TestReportUsageIncludesTheTriage(t *testing.T) {
	const name = "factory-report__triage.json"
	if err := readFactory[FactoryReport](t, name).Validate(); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*FactoryReport){
		"usage is not the sum of the lanes and the triage": func(r *FactoryReport) { r.Usage.USD -= 0.03 }, // as if the triage were outside
		"triage usage is not the sum of its issues":        func(r *FactoryReport) { r.Triage.Usage.Calls++; r.Usage.Calls++ },
		"choice":      func(r *FactoryReport) { r.Triage.Issues[0].Choice = "maybe" },
		"its lane is": func(r *FactoryReport) { r.Lanes[2].Status = LaneRejected },
	}
	for want, breaks := range cases {
		r := readFactory[FactoryReport](t, name)
		breaks(&r)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

// Arm minimal pins the triage model; arm control triages with its own, so it names none.
func TestTriageModelBelongsToTheMinimalArmOnly(t *testing.T) {
	for _, name := range []string{"factory-config__triage-minimal.json", "factory-config__triage-control.json"} {
		if err := readFactory[FactoryConfig](t, name).Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	m := readFactory[FactoryConfig](t, "factory-config__triage-minimal.json")
	m.Triage.Model = ""
	if err := m.Validate(); err == nil || !strings.Contains(err.Error(), "required for arm minimal") {
		t.Errorf("err = %v", err)
	}
	c := readFactory[FactoryConfig](t, "factory-config__triage-control.json")
	c.Triage.Model = "haiku"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "arm control") {
		t.Errorf("err = %v", err)
	}
}
