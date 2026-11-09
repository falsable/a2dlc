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

func TestInvalidBaseFailuresDefaultsForPHP(t *testing.T) {
	cfg := contract.Config{Engines: []contract.EngineConfig{{Language: "php"}}, Tests: &contract.TestsConfig{}}
	got := strings.Join(invalidBaseFailures(cfg), "|")
	for _, want := range []string{`Class "`, "not found", "Call to undefined function", "Call to undefined method", "Failed opening required"} {
		if !strings.Contains(got, want) {
			t.Errorf("php default %q missing from %q", want, got)
		}
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

// With no engine of a known language (a tests-only repo) the defaults are the union of every language's: an empty list
// would make any failed testcase on the base a valid failure, even a failed import (bead a2dlc-pef).
func TestInvalidBaseFailuresWithoutEnginesIsTheUnionOfTheLanguages(t *testing.T) {
	want := len(unique(append(append(append([]string{}, defaultInvalidBaseFailures["python"]...), defaultInvalidBaseFailures["typescript"]...), defaultInvalidBaseFailures["php"]...)))
	noEngines := contract.Config{Tests: &contract.TestsConfig{}}
	unknownLanguage := contract.Config{Engines: []contract.EngineConfig{{Language: "go"}}, Tests: &contract.TestsConfig{}}
	for _, cfg := range []contract.Config{noEngines, unknownLanguage} {
		if got := invalidBaseFailures(cfg); len(got) != want {
			t.Errorf("%+v gave %d patterns %v, want %d", cfg.Engines, len(got), got, want)
		}
	}
	if got := invalidBaseFailures(contract.Config{Tests: &contract.TestsConfig{InvalidBaseFailures: []string{}}}); len(got) != 0 {
		t.Fatalf("an explicit empty list still means none, got %v", got)
	}
}

// The language of the stub engine picks the defaults like an engine's, and it is not widened to the union.
func TestInvalidBaseFailuresTakeTheLanguageOfTheStubEngine(t *testing.T) {
	cfg := contract.Config{Tests: &contract.TestsConfig{StubEngine: &contract.EngineConfig{Language: "python"}}}
	if got := invalidBaseFailures(cfg); len(got) != len(unique(defaultInvalidBaseFailures["python"])) {
		t.Fatalf("got %v", got)
	}
}
