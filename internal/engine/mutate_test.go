package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestMutantFilesEnforcesContract(t *testing.T) {
	req := contract.Request{ChangedFiles: []string{"src/f.py"}, Budget: json.RawMessage(`{"changed_lines":{"src/f.py":[2]},"max_mutants":30}`)}
	base := func() contract.MutateResponse {
		return contract.MutateResponse{SchemaVersion: 1, NotGenerated: []contract.FileReason{}, Pruned: []contract.PrunedMutant{},
			Mutants: []contract.Mutant{{ID: "m001", File: "src/f.py", Line: 2, Operator: contract.OpArithmetic, Description: "x"}}}
	}
	folder := func(t *testing.T, files map[string]string) string {
		d := t.TempDir()
		for rel, s := range files {
			p := filepath.Join(d, filepath.FromSlash(rel))
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte(s), 0o644)
		}
		return d
	}
	got, err := MutantFiles(folder(t, map[string]string{"m001/src/f.py": "mutated"}), req, base())
	if err != nil || string(got["m001"]) != "mutated" {
		t.Fatalf("%v %v", got, err)
	}
	cases := map[string]struct {
		breaks func(*contract.MutateResponse)
		files  map[string]string
	}{
		"outside the changed lines": {func(r *contract.MutateResponse) { r.Mutants[0].Line = 1 }, map[string]string{"m001/src/f.py": "x"}},
		"which was not requested":   {func(r *contract.MutateResponse) { r.Mutants[0].File = "src/g.py" }, map[string]string{"m001/src/g.py": "x"}},
		"outside contract":          {func(r *contract.MutateResponse) { r.Mutants[0].ID = "../x" }, nil},
		"repeated":                  {func(r *contract.MutateResponse) { r.Mutants = append(r.Mutants, r.Mutants[0]) }, map[string]string{"m001/src/f.py": "x"}},
		"not declared":              {func(*contract.MutateResponse) {}, map[string]string{"m001/src/f.py": "x", "m002/src/f.py": "y"}},
		"is not in /io/mutants":     {func(*contract.MutateResponse) {}, nil},
		"incomplete":                {func(r *contract.MutateResponse) { r.NotGenerated = nil }, map[string]string{"m001/src/f.py": "x"}},
		"pruned are required":       {func(r *contract.MutateResponse) { r.Pruned = nil }, map[string]string{"m001/src/f.py": "x"}},
		"pruned outside the requested changed lines": {func(r *contract.MutateResponse) {
			r.Pruned = []contract.PrunedMutant{{File: "src/f.py", Line: 1, Reason: contract.PruneAssert}}
		}, map[string]string{"m001/src/f.py": "x"}},
	}
	for want, c := range cases {
		r := base()
		c.breaks(&r)
		if _, err := MutantFiles(folder(t, c.files), req, r); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v", want, err)
		}
	}
}
