package app

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// Absent invalid_base_failures takes the defaults of the engines' languages, TypeScript included.
func TestInvalidBaseFailuresDefaultsPerLanguage(t *testing.T) {
	ts := contract.Config{Engines: []contract.EngineConfig{{Language: "typescript"}}, Tests: &contract.TestsConfig{}}
	got := strings.Join(invalidBaseFailures(ts), "|")
	for _, want := range []string{"is not a function", "Cannot find module", "ReferenceError", "does not provide an export named"} {
		if !strings.Contains(got, want) {
			t.Fatalf("typescript default %q missing from %q", want, got)
		}
	}
	both := contract.Config{Engines: []contract.EngineConfig{{Language: "python"}, {Language: "typescript"}}, Tests: &contract.TestsConfig{}}
	if n := len(invalidBaseFailures(both)); n != len(defaultInvalidBaseFailures["python"])+len(defaultInvalidBaseFailures["typescript"]) {
		t.Fatalf("two languages give %d patterns", n)
	}
}

// An explicit list replaces the defaults, and an explicit empty list means none.
func TestInvalidBaseFailuresExplicitListReplacesDefaults(t *testing.T) {
	explicit := contract.Config{Engines: []contract.EngineConfig{{Language: "typescript"}},
		Tests: &contract.TestsConfig{InvalidBaseFailures: []string{"ImportError"}}}
	if got := invalidBaseFailures(explicit); len(got) != 1 || got[0] != "ImportError" {
		t.Fatalf("explicit list: %v", got)
	}
	none := contract.Config{Engines: []contract.EngineConfig{{Language: "python"}},
		Tests: &contract.TestsConfig{InvalidBaseFailures: []string{}}}
	if got := invalidBaseFailures(none); len(got) != 0 {
		t.Fatalf("explicit empty list gave %v", got)
	}
}

// With no engine of a known language there is nothing to default to.
func TestInvalidBaseFailuresWithoutEngines(t *testing.T) {
	if got := invalidBaseFailures(contract.Config{Tests: &contract.TestsConfig{}}); len(got) != 0 {
		t.Fatalf("no engines gave %v", got)
	}
}
