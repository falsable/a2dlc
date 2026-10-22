package run

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestEngineLanguage(t *testing.T) {
	py := contract.EngineConfig{Language: "python", Paths: []string{"src/**/*.py"}}
	ts := contract.EngineConfig{Language: "typescript", Paths: []string{"web/**/*.ts"}}
	cases := []struct {
		name    string
		engines []contract.EngineConfig
		files   []string
		want    string
	}{
		{"no engines", nil, []string{"src/a.py"}, ""},
		{"single engine, no file to match", []contract.EngineConfig{ts}, nil, "typescript"},
		{"single engine, file outside its paths", []contract.EngineConfig{py}, []string{"README.md"}, "python"},
		{"two engines, file of the second", []contract.EngineConfig{py, ts}, []string{"web/app.ts"}, "typescript"},
		{"two engines, file of the first", []contract.EngineConfig{py, ts}, []string{"src/calc.py"}, "python"},
		{"two engines, no match: schema default", []contract.EngineConfig{py, ts}, []string{"docs/x.md"}, ""},
	}
	for _, c := range cases {
		if got := engineLanguage(c.engines, c.files); got != c.want {
			t.Errorf("%s: engineLanguage = %q, want %q", c.name, got, c.want)
		}
	}
}
