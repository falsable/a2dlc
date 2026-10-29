package factory

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

// HoldoutRunner runs the sealed holdout's files against a checkout and returns the command's exit code and output. It is
// only ever called by Integrate, with the seal already verified, and never for a builder.
type HoldoutRunner interface {
	Run(ctx context.Context, spec contract.FactoryHoldout, tree string, files []string) (exit int, log string, err error)
}

// DockerHoldout is the production runner: the checkout and the holdout are mounted read-only at /src and /holdout, with
// no network, for the one run. The command copies what it needs into the container's own writable folder.
type DockerHoldout struct{ Bin string }

func (d DockerHoldout) Run(ctx context.Context, h contract.FactoryHoldout, tree string, files []string) (int, string, error) {
	bin, mem, cpus, limit := d.Bin, h.Memory, h.CPUs, time.Duration(h.TimeoutS)*time.Second
	if bin == "" {
		bin = "docker"
	}
	if mem == "" {
		mem = "2g"
	}
	if cpus == 0 {
		cpus = 2
	}
	if limit == 0 {
		limit = 10 * time.Minute
	}
	hostTree, err := filepath.Abs(tree)
	if err != nil {
		return 0, "", err
	}
	hostHoldout, err := filepath.Abs(h.Dir)
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	args := []string{"run", "--rm", "--network", "none", "--memory", mem, "--cpus", fmt.Sprint(cpus),
		"-v", hostTree + ":/src:ro", "-v", hostHoldout + ":/holdout:ro", "-w", "/tmp",
		"-e", "HOLDOUT_FILES=" + strings.Join(files, " "), "--entrypoint", "sh", h.Image, "-c", h.Command}
	var out bytes.Buffer
	c := exec.CommandContext(ctx, bin, args...)
	c.Stdout, c.Stderr = &out, &out
	err = c.Run()
	var ee *exec.ExitError
	switch {
	case ctx.Err() != nil:
		return 0, out.String(), fmt.Errorf("the holdout exceeded timeout_s=%d", int(limit.Seconds()))
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String(), nil
	case err != nil:
		return 0, "", err
	}
	return 0, out.String(), nil
}

// holdoutFiles is what runs to validate a lane: the files of the issues integrated so far and the lane's own, in the
// order of the spec so the command line does not depend on the queue.
func holdoutFiles(h contract.FactoryHoldout, issues map[string]bool) []string {
	var files []string
	for _, u := range h.Units {
		if issues[u.IssueID] {
			files = append(files, u.Files...)
		}
	}
	return files
}

func holdoutUnit(h contract.FactoryHoldout, issue string) bool {
	for _, u := range h.Units {
		if u.IssueID == issue {
			return true
		}
	}
	return false
}

// mountHoldout is the only way the holdout reaches a tree: the seal is checked right before every mount, the spec must
// name that seal, and the folder must not be inside the checkout it is mounted next to (a lane's tree would then
// carry it). A failure here is an error of the integration, not a verdict on the lane.
func mountHoldout(ctx context.Context, r HoldoutRunner, h contract.FactoryHoldout, tree string, files []string) (int, string, error) {
	seal, err := VerifySeal(h.Dir)
	if err != nil {
		return 0, "", err
	}
	if seal != h.Seal {
		return 0, "", fmt.Errorf("the holdout in %s is sealed as %s and the spec names %s", h.Dir, seal, h.Seal)
	}
	if inside(h.Dir, tree) {
		return 0, "", fmt.Errorf("the holdout %s is inside the checkout %s", h.Dir, tree)
	}
	return r.Run(ctx, h, tree, files)
}

func inside(dir, parent string) bool {
	a, err1 := filepath.Abs(dir)
	b, err2 := filepath.Abs(parent)
	if err1 != nil || err2 != nil {
		return true // cannot tell: refuse
	}
	rel, err := filepath.Rel(b, a)
	return err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
