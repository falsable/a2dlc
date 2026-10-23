package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// ErrCheckTimeout is what a CheckRunner returns when the check ran past its timeout_s: the core records status timeout,
// which is never a pass.
var ErrCheckTimeout = errors.New("the check exceeded its timeout")

// CheckRun is what a pluggable check left: the response it wrote to $A2DLC_OUT and the tail of its output.
type CheckRun struct {
	Response contract.CheckResponse
	Log      string
}

// CheckRunner runs one pluggable check (config.checks, protocol §5 Checks) in its image, without network, with the
// base at /base:ro, the candidate at /cand:ro and the matched changed files in $A2DLC_CHANGED, and returns the
// response it wrote to $A2DLC_OUT. Anything that is not a valid response (missing image, non-zero exit, missing or
// invalid file) is an error; the core turns it into status error, never into a pass.
type CheckRunner interface {
	RunCheck(ctx context.Context, c contract.CheckConfig, files []string, baseDir, candDir string) (CheckRun, error)
}

// DockerChecks runs a check in Docker: docker run --rm --network none, with the resources of the check.
type DockerChecks struct{ Bin string }

func (d DockerChecks) RunCheck(ctx context.Context, c contract.CheckConfig, files []string, baseDir, candDir string) (CheckRun, error) {
	var r CheckRun
	io, err := os.MkdirTemp("", "a2dlc-cio-*")
	if err != nil {
		return r, err
	}
	defer os.RemoveAll(io)
	if err := os.WriteFile(filepath.Join(io, "changed.txt"), []byte(strings.Join(files, "\n")+"\n"), 0o644); err != nil {
		return r, err
	}
	bin, mem, cpus, limit := d.Bin, c.Memory, c.CPUs, time.Duration(c.TimeoutS)*time.Second
	if bin == "" {
		bin = "docker"
	}
	if mem == "" {
		mem = "1g"
	}
	if cpus == 0 {
		cpus = 1
	}
	if limit == 0 {
		limit = 120 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := []string{"run", "--rm", "--network", "none", "--memory", mem, "--cpus", fmt.Sprint(cpus),
		"-v", baseDir + ":/base:ro", "-v", candDir + ":/cand:ro", "-v", io + ":/io", "-w", "/cand",
		"-e", "A2DLC_CHECK=" + c.ID, "-e", "A2DLC_CHANGED=" + strings.Join(files, " "), "-e", "A2DLC_CHANGED_FILE=/io/changed.txt",
		"-e", "A2DLC_OUT=/io/response.json", "--entrypoint", "sh", c.Image, "-c", c.Command}
	var out bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr = &out, &out
	errRun := cmd.Run()
	r.Log = tail(out.String())
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return r, fmt.Errorf("%w (timeout_s=%d)", ErrCheckTimeout, int(limit.Seconds()))
	case errors.As(errRun, &ee):
		return r, fmt.Errorf("the check exited %d: %s", ee.ExitCode(), r.Log)
	case errRun != nil:
		return r, errRun
	}
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return r, fmt.Errorf("the check did not write $A2DLC_OUT: %s", r.Log)
	}
	if err := contract.DecodeStrictBytes(raw, &r.Response); err != nil {
		return r, fmt.Errorf("$A2DLC_OUT outside contract: %v", err)
	}
	return r, nil
}

var _ CheckRunner = DockerChecks{}
