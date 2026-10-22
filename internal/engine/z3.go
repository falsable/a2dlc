package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// Z3 runs the satisfiability engine (engines/z3) of Stage 1.5. It is not a language engine: it does not see the repo,
// only /io with the request (the smt clauses of S*), with no network. a2dlc run uses it; check does not call it.
type Z3 struct {
	Bin     string        // "docker" if empty
	Image   string        // e.g. a2dlc-engine-z3:0.1
	Timeout time.Duration // 5 min if empty
}

// Solve sends the request and returns the response validated against this one. An error means the engine did not give
// a usable response (missing image, crashed container, response outside contract), not an unsat.
func (z Z3) Solve(ctx context.Context, req contract.Z3Request) (contract.Z3Response, error) {
	var resp contract.Z3Response
	io, err := os.MkdirTemp("", "a2dlc-z3-*")
	if err != nil {
		return resp, err
	}
	defer os.RemoveAll(io)
	b, _ := json.MarshalIndent(req, "", " ")
	if err := os.WriteFile(filepath.Join(io, "request.json"), b, 0o644); err != nil {
		return resp, err
	}
	bin, limit := z.Bin, z.Timeout
	if bin == "" {
		bin = "docker"
	}
	if limit == 0 {
		limit = 5 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var out bytes.Buffer
	c := exec.CommandContext(ctx, bin, "run", "--rm", "--network", "none", "--memory", "1g", "--cpus", "1", "-v", io+":/io", z.Image)
	c.Stdout, c.Stderr = &out, &out
	errRun := c.Run()
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", z.Image, errRun, tail(out.String()))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("response of %s outside z3-response: %v", z.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the z3 engine reported an error: %s", *resp.Error)
	}
	if err := resp.Validate(req); err != nil {
		return resp, fmt.Errorf("response of %s outside contract: %v", z.Image, err)
	}
	return resp, nil
}
