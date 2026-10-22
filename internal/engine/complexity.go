package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// ErrWithoutComplexity marks an engine that does not do the complexity operation (it answers something else, like the
// 0.2 image of the Python engine, which falls back to the diff): its files stay unmeasured and the selection says so;
// they are not taken as zero complexity.
var ErrWithoutComplexity = errors.New("the engine does not do complexity")

func (d Docker) Complexity(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.ComplexityResponse, error) {
	var resp contract.ComplexityResponse
	req.Operation = "complexity"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer os.RemoveAll(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("%w (%s: response outside engine-complexity-response: %v)", ErrWithoutComplexity, e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not measure complexity: %s", *resp.Error)
	}
	return resp, ValidateComplexity(req, resp)
}

// ValidateComplexity applies the schema rules that matter to the core: each measured file is one that was requested,
// only once, and no figure is negative.
func ValidateComplexity(req contract.Request, resp contract.ComplexityResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Files == nil {
		return fmt.Errorf("complexity outside contract: schema_version %d and files required", resp.SchemaVersion)
	}
	request, seen := map[string]bool{}, map[string]bool{}
	for _, f := range req.ChangedFiles {
		request[f] = true
	}
	for _, f := range resp.Files {
		switch {
		case !request[f.File]:
			return fmt.Errorf("complexity of %q, which was not requested", f.File)
		case seen[f.File]:
			return fmt.Errorf("complexity of %q twice", f.File)
		case f.LocAdded < 0 || f.LocRemoved < 0 || f.Cyclomatic != nil && *f.Cyclomatic < 0 || f.ASTNodes != nil && *f.ASTNodes < 0:
			return fmt.Errorf("complexity outside contract: %+v", f)
		}
		seen[f.File] = true
	}
	return nil
}
