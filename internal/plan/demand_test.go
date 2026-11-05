package plan

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The peak of a slice is the largest thing it holds at once: one role at a time, the engines side by side.
func TestSliceDemandIsThePeakOfWhatRunsAtOnce(t *testing.T) {
	cfg := contract.Config{Engines: []contract.EngineConfig{{Memory: "3g", CPUs: 1}, {Memory: "2g", CPUs: 2}},
		Tests: &contract.TestsConfig{Memory: "4g", CPUs: 1}}
	roles := contract.Roles{Roles: map[string]contract.Role{
		"builder_primary": {Memory: "1g", CPUs: 1, TierUp: &contract.Role{Memory: "6g", CPUs: 4}},
	}}
	d := SliceDemand(cfg, roles, "")
	if d.Docker.CPUs != 4 || d.Docker.MemBytes != 6<<30 {
		t.Fatalf("%+v: want the tier_up's 4 CPU / 6 GiB", d.Docker)
	}
	roles.Roles["builder_primary"] = contract.Role{Memory: "1g", CPUs: 1}
	if d := SliceDemand(cfg, roles, ""); d.Docker.MemBytes != 5<<30 || d.Docker.CPUs != 3 {
		t.Fatalf("%+v: want both engines side by side, 3 CPU / 5 GiB", d.Docker)
	}
	if d := SliceDemand(cfg, roles, "1"); d.Docker.MemBytes != 4<<30 {
		t.Fatalf("%+v: with one engine at a time the tests' 4 GiB is the peak", d.Docker)
	}
	if d := SliceDemand(contract.Config{}, contract.Roles{}, ""); d.Docker.CPUs != 2 || d.Docker.MemBytes != 2<<30 {
		t.Fatalf("%+v: a repository with no limits takes the launcher's defaults", d.Docker)
	}
}
