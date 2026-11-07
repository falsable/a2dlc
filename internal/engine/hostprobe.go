package engine

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// HostProbe answers the questions of `a2dlc doctor` about the machine: is Docker up, is an image local, which build is a
// binary. It only reads; nothing is started, pulled or written. Bin is "docker" if empty.
type HostProbe struct{ Bin string }

const probeTimeout = 15 * time.Second

func (p HostProbe) docker() string {
	if p.Bin == "" {
		return "docker"
	}
	return p.Bin
}

func (p HostProbe) run(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// DockerReachable asks the daemon (not just the client) for its version.
func (p HostProbe) DockerReachable(ctx context.Context) error {
	out, err := p.run(ctx, p.docker(), "version", "--format", "{{.Server.Version}}")
	if err != nil {
		return fmt.Errorf("%s version: %v: %s", p.docker(), err, firstLine(out))
	}
	return nil
}

// ImagePresent is true when the image is in the local store. "No such image" is false, not an error; anything else
// (the daemon went away mid-probe) is.
func (p HostProbe) ImagePresent(ctx context.Context, ref string) (bool, error) {
	out, err := p.run(ctx, p.docker(), "image", "inspect", "--format", "{{.Id}}", ref)
	if err == nil {
		return true, nil
	}
	if strings.Contains(strings.ToLower(out), "no such image") {
		return false, nil
	}
	return false, fmt.Errorf("%s image inspect %s: %v: %s", p.docker(), ref, err, firstLine(out))
}

// BinaryVersion runs `<path> version`, which every a2dlc build answers without touching a repo.
func (p HostProbe) BinaryVersion(ctx context.Context, path string) (string, error) {
	out, err := p.run(ctx, path, "version")
	if err != nil {
		return "", fmt.Errorf("%s version: %v", path, err)
	}
	if out == "" {
		return "", errors.New(path + " version printed nothing")
	}
	return firstLine(out), nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
