package engine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// DockerBuilder builds the engine image that `a2dlc install` derives from the repo's dependencies (a2dlc-pe6.6). It is
// the only place a2dlc builds an image, and it needs the network once. Bin is "docker" if empty.
type DockerBuilder struct{ Bin string }

const buildTimeout = 15 * time.Minute

// Build runs docker build -f dockerfile -t tag contextDir and returns the log, also when the build fails.
func (b DockerBuilder) Build(ctx context.Context, tag, dockerfile, contextDir string) (string, error) {
	bin := b.Bin
	if bin == "" {
		bin = "docker"
	}
	ctx, cancel := context.WithTimeout(ctx, buildTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "build", "--progress=plain", "-f", dockerfile, "-t", tag, contextDir).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("docker build: %v: %s", err, lastLine(string(out)))
	}
	return string(out), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
