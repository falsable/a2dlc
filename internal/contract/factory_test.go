package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readFactory[T any](t *testing.T, name string) T {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(validDir, name))
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := DecodeStrictBytes(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// The checks that JSON Schema cannot express are in Validate; the valid examples pass them and each rule has a case
// that breaks it.
func TestFactoryConfigValidate(t *testing.T) {
	for _, name := range []string{"factory-config__claude-lite.json", "factory-config__no-network.json"} {
		if err := readFactory[FactoryConfig](t, name).Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	cases := map[string]func(*FactoryConfig){
		"caps.lane_usd":          func(c *FactoryConfig) { c.Caps.LaneUSD = 7 },
		"caps.run_stop_new_usd":  func(c *FactoryConfig) { c.Caps.RunStopNewUSD = 6.5 },
		"caps.run_kill_usd":      func(c *FactoryConfig) { c.Caps.RunKillUSD = 30 },
		"network provider needs": func(c *FactoryConfig) { c.EgressAllow = nil },
		"caps.lane_turns":        func(c *FactoryConfig) { c.Caps.LaneTurns = 0 }, // a cap left out decodes as zero
		"caps.total_usd":         func(c *FactoryConfig) { c.Caps.TotalUSD = 0 },
		"concurrency":            func(c *FactoryConfig) { c.Concurrency = 0 },
		"roles_image, argv":      func(c *FactoryConfig) { c.Tools = nil },
	}
	for want, breaks := range cases {
		c := readFactory[FactoryConfig](t, "factory-config__claude-lite.json")
		breaks(&c)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}

func TestFactoryBatchValidateRejectsDuplicateIssueIDs(t *testing.T) {
	b := readFactory[FactoryBatch](t, "factory-batch__three-issues.json")
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	b.Issues[1].ID = b.Issues[0].ID
	if err := b.Validate(); err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("err = %v", err)
	}
}

// The report's totals are arithmetic over its lanes, and cost per accepted change is total over accepted.
func TestFactoryReportValidate(t *testing.T) {
	if err := readFactory[FactoryReport](t, "factory-report__three-lanes.json").Validate(); err != nil {
		t.Fatal(err)
	}
	half := 0.1
	cases := map[string]func(*FactoryReport){
		"sum of the lanes":             func(r *FactoryReport) { r.Usage.CacheReadInputTokens++ },
		"accepted is":                  func(r *FactoryReport) { r.Accepted = 2 },
		"is not usage.usd":             func(r *FactoryReport) { r.CostPerAcceptedUSD = &half },
		"without an accepted lane":     func(r *FactoryReport) { r.Lanes[0].Status, r.Accepted = LaneRejected, 0 },
		"cost_per_accepted_usd is not": func(r *FactoryReport) { r.CostPerAcceptedUSD = nil },
	}
	for want, breaks := range cases {
		r := readFactory[FactoryReport](t, "factory-report__three-lanes.json")
		breaks(&r)
		if err := r.Validate(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}
