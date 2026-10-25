package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// DepsRunner is implemented by runners that list the import edges between the candidate's files (operation: deps_graph,
// Stage 6). It is optional: a runner without it leaves the import edges not measured.
type DepsRunner interface {
	Deps(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.DepsResponse, error)
}

// Deps runs the deps_graph operation over req.ChangedFiles and reads engine-deps-response. An engine without the
// operation replies with an error, which is returned: the edges are not guessed.
func (d Docker) Deps(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.DepsResponse, error) {
	var resp contract.DepsResponse
	req.Operation = "deps_graph"
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
		return resp, fmt.Errorf("engine %s gives no deps graph (response outside engine-deps-response: %v)", e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not give the deps graph: %s", *resp.Error)
	}
	return resp, ValidateDeps(req, resp)
}

// ValidateDeps applies the schema rules that matter to the core: the schema version, a non-null unresolved list, and
// edges only between files that were requested (both ends: an edge to a file nobody asked about is not the candidate's).
func ValidateDeps(req contract.Request, resp contract.DepsResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Edges == nil || resp.Unresolved == nil {
		return fmt.Errorf("deps outside contract: schema_version %d, edges and unresolved required", resp.SchemaVersion)
	}
	request := map[string]bool{}
	for _, f := range req.ChangedFiles {
		request[f] = true
	}
	for _, e := range resp.Edges {
		if !request[e.From] || !request[e.To] {
			return fmt.Errorf("deps: edge %q -> %q names a file that was not requested", e.From, e.To)
		}
	}
	return nil
}
