package engine

import (
	"context"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func TestTracesReadsTheResponse(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"runner":"pytest","test_exit":0,"traces":[],"branch_coverage":0.75,"skipped":[],"error":null}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	got, err := Docker{Bin: bin}.Traces(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if err != nil || got.Runner == nil || *got.Runner != "pytest" || got.BranchCoverage == nil || *got.BranchCoverage != 0.75 {
		t.Fatalf("%+v %v", got, err)
	}
}

// A harness that could not run comes back with its reason: the response is returned with the error, so the stage can
// name the reason instead of losing it.
func TestTracesHarnessErrorComesBackWithItsReason(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"runner":null,"test_exit":null,"skipped":[],"error":"no tests discovered"}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	got, err := Docker{Bin: bin}.Traces(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if err == nil || got.Error == nil || *got.Error != "no tests discovered" {
		t.Fatalf("err %v, response %+v", err, got)
	}
}

// An engine that answers outside engine-traces-response (one without the operation) is an error, not an empty capture.
func TestTracesOutsideContractIsAnError(t *testing.T) {
	resp := responseFile(t, `{"schema_version":1,"targets":[]}`)
	bin, _ := fakeDocker(t, "[]", "", resp)
	if _, err := (Docker{Bin: bin}).Traces(context.Background(), contract.EngineConfig{Image: "py:0.2"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("a response outside engine-traces-response must be an error, not an empty capture")
	}
}

func TestTracesWithoutResponseIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, "[]", "", "")
	if _, err := (Docker{Bin: bin}).Traces(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{}, t.TempDir(), t.TempDir()); err == nil {
		t.Fatal("an engine that wrote no response.json must be an error")
	}
}

func TestValidateTracesNeedsTracesAndSkipped(t *testing.T) {
	ok := contract.TracesResponse{SchemaVersion: contract.SchemaVersion, Traces: []contract.CaptureTrace{}, Skipped: []contract.TraceSkip{}}
	if err := ValidateTraces(ok); err != nil {
		t.Fatal(err)
	}
	if err := ValidateTraces(contract.TracesResponse{SchemaVersion: contract.SchemaVersion, Skipped: []contract.TraceSkip{}}); err == nil {
		t.Fatal("a response without traces must be rejected")
	}
	if err := ValidateTraces(contract.TracesResponse{SchemaVersion: contract.SchemaVersion, Traces: []contract.CaptureTrace{}}); err == nil {
		t.Fatal("a response without skipped must be rejected")
	}
}
