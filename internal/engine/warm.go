package engine

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// Warm tree (bead a2dlc-uca). Where the trees go into the container as a copy (CopyIn: every host but Linux), the cost of a
// test run is dominated by copying the repository in, once per run (about 32 s for 78 MB on Docker Desktop for Windows). A
// Docker volume per (repo, test image) holds the base tree, so a run copies in only what differs from it. The volume holds
// no state a verdict depends on: a run lays the exact difference between the volume's tree and the tree it judges over a
// copy of the volume, and a volume that cannot be shown to hold the tree it says (no marker, a sync that stopped halfway)
// is rebuilt from scratch.

// WarmTests is implemented by test runners that can run over a warm tree. A runner without it, or one that does not copy
// trees in, is run the usual way.
type WarmTests interface {
	TestRunner
	// Warm says whether warm trees apply to this runner at all.
	Warm() bool
	// WarmTree reads the git tree hash the volume holds ("" when it holds none).
	WarmTree(ctx context.Context, image, volume string) (string, error)
	// SyncWarm turns the volume into the tree tree: dir holds the files that differ (all of them when full), deleted the
	// paths to remove first. The marker is written last, so an interrupted sync leaves a volume that claims nothing.
	SyncWarm(ctx context.Context, image, volume, tree string, dir string, deleted []string, full bool) error
	// RunTestsWarm is RunTests over a copy of the volume with dir laid over it and deleted removed.
	RunTestsWarm(ctx context.Context, t contract.TestsConfig, command string, env map[string]string, volume, dir string, deleted []string) (TestRun, error)
}

const deletedFile = ".a2dlc-deleted"
const markerFile = ".a2dlc-tree"

func (d DockerTests) Warm() bool { return d.CopyIn }

func (d DockerTests) bin() string {
	if d.Bin == "" {
		return "docker"
	}
	return d.Bin
}

func (d DockerTests) WarmTree(ctx context.Context, image, volume string) (string, error) {
	var out, errOut bytes.Buffer
	c := exec.CommandContext(ctx, d.bin(), "run", "--rm", "--network", "none", "-v", volume+":/warm:ro", "--entrypoint", "sh", image,
		"-c", "cat /warm/"+markerFile+" 2>/dev/null; true")
	c.Stdout, c.Stderr = &out, &errOut
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("docker run %s (marker of %s): %v: %s", image, volume, err, tail(errOut.String()))
	}
	return strings.TrimSpace(out.String()), nil
}

func withDeleted(dir string, deleted []string) error {
	sort.Strings(deleted)
	// every line ends in a newline: sh's read does not run the loop body for an unterminated last line
	list := ""
	if len(deleted) > 0 {
		list = strings.Join(deleted, "\n") + "\n"
	}
	return os.WriteFile(filepath.Join(dir, deletedFile), []byte(list), 0o644)
}

func (d DockerTests) SyncWarm(ctx context.Context, image, volume, tree string, dir string, deleted []string, full bool) error {
	if err := withDeleted(dir, deleted); err != nil {
		return err
	}
	defer os.Remove(filepath.Join(dir, deletedFile))
	script := "set -e; rm -f /warm/" + markerFile + "; "
	if full {
		script += "find /warm -mindepth 1 -delete; "
	}
	script += `while IFS= read -r f; do [ -n "$f" ] && rm -rf "/warm/$f"; done < /delta/` + deletedFile + `; ` +
		`rm -f /delta/` + deletedFile + `; cp -a /delta/. /warm/; printf %s '` + tree + `' > /warm/` + markerFile
	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	err := container{bin: d.bin(), copyIn: true, image: image, flags: []string{"--network", "none", "-v", volume + ":/warm", "--entrypoint", "sh"},
		trees: []dirAt{{dir, "/delta"}}, cmd: []string{"-c", script}}.run(ctx, &out)
	if err != nil {
		return fmt.Errorf("sync of the warm tree %s: %v: %s", volume, err, tail(out.String()))
	}
	return nil
}

func (d DockerTests) RunTestsWarm(ctx context.Context, t contract.TestsConfig, command string, env map[string]string, volume, dir string, deleted []string) (TestRun, error) {
	if err := withDeleted(dir, deleted); err != nil {
		return TestRun{}, err
	}
	defer os.Remove(filepath.Join(dir, deletedFile))
	script := `set -e; cp -a /warm/. /work/; rm -f /work/` + markerFile + `; ` +
		`while IFS= read -r f; do [ -n "$f" ] && rm -rf "/work/$f"; done < /delta/` + deletedFile + `; ` +
		`rm -f /delta/` + deletedFile + `; cp -a /delta/. /work/; cd /work; exec sh -c "$A2DLC_CMD"`
	env = withCommand(env, command)
	return d.runIn(ctx, t, script, env, []string{"-v", volume + ":/warm:ro"}, []dirAt{{dir, "/delta"}})
}

func withCommand(env map[string]string, command string) map[string]string {
	out := map[string]string{"A2DLC_CMD": command}
	for k, v := range env {
		out[k] = v
	}
	return out
}
