package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// FuzzRunner is implemented by runners that fuzz the changed public functions (operation: fuzz, Stage 4). It is optional:
// a runner without it does not fuzz, and the stage says so in its reason.
type FuzzRunner interface {
	Fuzz(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.FuzzResponse, error)
}

// Fuzz runs the fuzz operation and reads engine-fuzz-response. An answer outside the contract, or an engine that reports
// an error, is an error: a fuzzing round that did not run is not a round with no findings.
func (d Docker) Fuzz(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.FuzzResponse, error) {
	var resp contract.FuzzResponse
	req.Operation = "fuzz"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer removeIO(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("engine %s gives no fuzz response (response outside engine-fuzz-response: %v)", e.Image, err)
	}
	return resp, ValidateFuzz(resp)
}

// ValidateFuzz applies the schema rules that matter to the core: the schema version, findings present (possibly empty),
// non-negative counts, and a function for every finding.
func ValidateFuzz(resp contract.FuzzResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Findings == nil {
		return fmt.Errorf("fuzz outside contract: schema_version %d and findings required", resp.SchemaVersion)
	}
	if resp.Runs < 0 || resp.Seconds < 0 {
		return fmt.Errorf("fuzz outside contract: runs %d, seconds %v", resp.Runs, resp.Seconds)
	}
	for _, f := range resp.Findings {
		if f.Function == "" {
			return fmt.Errorf("fuzz outside contract: finding without function: %+v", f)
		}
	}
	return nil
}

// removeIO removes an /io folder and reports a failed removal on stderr instead of dropping it.
func removeIO(dir string) {
	if err := os.RemoveAll(dir); err != nil {
		fmt.Fprintf(os.Stderr, "a2dlc: cannot remove the engine folder %s: %v\n", dir, err)
	}
}
