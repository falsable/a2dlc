package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// TracesRunner is implemented by runners that run the base tests under the trace harness (operation: capture_traces,
// Stage 1 of refactor and legacy). It is optional, like the other operations: a runner without it leaves the traces
// uncaptured, and the stage says so.
type TracesRunner interface {
	Traces(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.TracesResponse, error)
}

// Traces runs the capture_traces operation and reads engine-traces-response. A harness that could not run at all comes
// back with its response and an error: the error field is the reason, and traces are absent. An answer outside the
// contract, or no answer, is an error of its own.
func (d Docker) Traces(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.TracesResponse, error) {
	var resp contract.TracesResponse
	req.Operation = "capture_traces"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer removeIO(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, noResponse(e.Image, errRun, output)
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("engine %s gives no traces (response outside engine-traces-response: %v)", e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not capture the traces: %s", *resp.Error)
	}
	return resp, ValidateTraces(resp)
}

// ValidateTraces applies the schema rules that matter to the core: the schema version, and the traces and skipped lists
// present (possibly empty) when the harness ran.
func ValidateTraces(resp contract.TracesResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Traces == nil || resp.Skipped == nil {
		return fmt.Errorf("traces outside contract: schema_version %d, traces and skipped required", resp.SchemaVersion)
	}
	return nil
}
