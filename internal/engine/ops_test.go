package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// responseFile writes body as the response.json the fake docker copies into /io.
func responseFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "response.json")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLemmasReadsTheResponse(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"lemmas":[{"name":"lem_add","status":"pass","message":"holds"}]}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	got, err := Docker{Bin: bin}.Lemmas(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if err != nil || len(got.Lemmas) != 1 || got.Lemmas[0].Status != "pass" {
		t.Fatalf("%+v %v", got, err)
	}
}

// An engine that answers something else (the 0.2 image falls back to the diff) is ErrWithoutLemmas, not a PASS.
func TestLemmasOutsideContractIsErrWithoutLemmas(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"files":[]}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	_, err := Docker{Bin: bin}.Lemmas(context.Background(), contract.EngineConfig{Image: "py:0.2"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if !errors.Is(err, ErrWithoutLemmas) {
		t.Fatalf("err %v", err)
	}
}

func TestLemmasWithoutResponseIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, "[]", "", "")
	if _, err := (Docker{Bin: bin}).Lemmas(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{}, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("an engine that wrote no response.json must be an error")
	}
}

func TestValidateLemmasRules(t *testing.T) {
	ok := contract.LemmasResponse{SchemaVersion: contract.SchemaVersion, Lemmas: []contract.LemmaResult{{Name: "a", Status: "pass"}}}
	if err := ValidateLemmas(ok); err != nil {
		t.Fatal(err)
	}
	cases := map[string]contract.LemmasResponse{
		"no lemmas array": {SchemaVersion: contract.SchemaVersion},
		"unnamed lemma":   {SchemaVersion: contract.SchemaVersion, Lemmas: []contract.LemmaResult{{Status: "pass"}}},
		"unknown status":  {SchemaVersion: contract.SchemaVersion, Lemmas: []contract.LemmaResult{{Name: "a", Status: "maybe"}}},
		"fail without counterexample": {SchemaVersion: contract.SchemaVersion,
			Lemmas: []contract.LemmaResult{{Name: "a", Status: "fail"}}},
	}
	for name, resp := range cases {
		if err := ValidateLemmas(resp); err == nil {
			t.Errorf("%s: accepted %+v", name, resp)
		}
	}
	fail := contract.LemmasResponse{SchemaVersion: contract.SchemaVersion,
		Lemmas: []contract.LemmaResult{{Name: "a", Status: "fail", Counterexample: map[string]any{"args": []any{1}, "returned": 2}}}}
	if err := ValidateLemmas(fail); err != nil {
		t.Fatal(err)
	}
}

func TestFuzzReadsFindingsAndCounts(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"findings":[{"function":"f","input":"{\"x\":-1}","error":"ZeroDivisionError","seed":7}],"runs":1200,"seconds":10}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	got, err := Docker{Bin: bin}.Fuzz(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if err != nil || len(got.Findings) != 1 || got.Findings[0].Seed != 7 || got.Runs != 1200 {
		t.Fatalf("%+v %v", got, err)
	}
}

// An empty findings list is a round that ran with no findings; a missing one is an engine that did not fuzz.
func TestFuzzNeedsFindingsPresent(t *testing.T) {
	if err := ValidateFuzz(contract.FuzzResponse{SchemaVersion: contract.SchemaVersion, Findings: []contract.FuzzFinding{}}); err != nil {
		t.Fatalf("an empty round must pass: %v", err)
	}
	if err := ValidateFuzz(contract.FuzzResponse{SchemaVersion: contract.SchemaVersion}); err == nil {
		t.Fatal("a response without findings must be rejected")
	}
	if err := ValidateFuzz(contract.FuzzResponse{SchemaVersion: contract.SchemaVersion, Findings: []contract.FuzzFinding{}, Runs: -1}); err == nil {
		t.Fatal("negative runs must be rejected")
	}
}

func TestDepsReadsTheEdgesBetweenRequestedFiles(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"edges":[{"from":"a.py","to":"b.py"}],"unresolved":[]}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	req := contract.Request{SchemaVersion: contract.SchemaVersion, ChangedFiles: []string{"a.py", "b.py"}}
	got, err := Docker{Bin: bin}.Deps(context.Background(), contract.EngineConfig{Image: "py:1"}, req, t.TempDir(), t.TempDir())
	if err != nil || len(got.Edges) != 1 {
		t.Fatalf("%+v %v", got, err)
	}
}

// An edge that names a file nobody requested is outside the contract: the core does not take it as the candidate's.
func TestDepsRejectsEdgeToUnrequestedFile(t *testing.T) {
	req := contract.Request{SchemaVersion: contract.SchemaVersion, ChangedFiles: []string{"a.py"}}
	resp := contract.DepsResponse{SchemaVersion: contract.SchemaVersion, Edges: []contract.DepsEdge{{From: "a.py", To: "secret.py"}}, Unresolved: []string{}}
	if err := ValidateDeps(req, resp); err == nil {
		t.Fatal("an edge to a file that was not requested must be rejected")
	}
	resp.Edges = []contract.DepsEdge{{From: "x.py", To: "a.py"}}
	if err := ValidateDeps(req, resp); err == nil {
		t.Fatal("an edge from a file that was not requested must be rejected")
	}
}

func TestDepsWithoutOperationIsAnError(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"files":[]}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	if _, err := (Docker{Bin: bin}).Deps(context.Background(), contract.EngineConfig{Image: "py:0.2"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("an engine without deps_graph must be an error, not an empty graph")
	}
}
