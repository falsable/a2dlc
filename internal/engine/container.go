// @a2dlc:slice_id = "beads-a2dlc-gkn.0269c1"
// @a2dlc:mode = "lite"
// @a2dlc:rationale = "An image that starts with - would be read by docker as a flag, and a container that outlives its context keeps running, so both container paths check the image first and remove the container by name."
// @a2dlc:rejected_alternatives = []
// @a2dlc:protected_invariants = []
// @a2dlc:do_not_refactor = []

package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// refImage is the image pattern in roles.schema.json: the image goes as the positional argument of `docker run`, where
// docker still reads flags, so one that starts with "-" (--volume=/:/host) or carries whitespace/control characters is refused.
var refImage = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/:@-]*$`)

// validateImage rejects anything that is not an image reference.
func validateImage(image string) error {
	if strings.HasPrefix(image, "-") {
		return fmt.Errorf("image %q starts with - and docker would read it as a flag", image)
	}
	if !refImage.MatchString(image) {
		return fmt.Errorf("image %q is not an image reference (must match %s)", image, refImage)
	}
	return nil
}

// CopyInDefault says whether the trees go into the container as a copy instead of a bind mount: on every host but Linux,
// where Docker runs in a VM and a bind mount crosses into it file by file. Measured on Docker Desktop for Windows
// (a2dlc-d9d, 2026-10-26): the same 10 pytest tests took 83-87 s over a bind-mounted folder and 28 s from inside Docker.
var CopyInDefault = runtime.GOOS != "linux"

// dirAt is a host folder the container sees at at (an absolute path: "/cand", "/work").
type dirAt struct{ dir, at string }

// container is one docker run. Without copyIn the trees are bind-mounted (read-only if ro). With copyIn they go into the
// container before it starts: docker create, one tar of every tree on stdin to docker cp, docker start -a. The copy is the
// container's own, so a write to it never reaches the host folder, which is what ro guards in the mounted case.
type container struct {
	bin    string
	copyIn bool
	flags  []string // everything between "run" and the image except the trees: limits, -v io:/io, -w, -e, --entrypoint
	trees  []dirAt
	ro     bool
	image  string
	cmd    []string
}

// run returns what docker run would: an *exec.ExitError carries the container's exit code. A failure to create the
// container or to copy the trees in is any other error, so it is never read as the exit code of the command.
func (c container) run(ctx context.Context, out io.Writer) error {
	if err := validateImage(c.image); err != nil {
		return err
	}
	if !c.copyIn {
		name := containerName()
		args := append([]string{"run", "--rm", "--name", name}, c.flags...)
		for _, t := range c.trees {
			v := t.dir + ":" + t.at
			if c.ro {
				v += ":ro"
			}
			args = append(args, "-v", v)
		}
		cmd := exec.CommandContext(ctx, c.bin, append(append(args, c.image), c.cmd...)...)
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		if ctx.Err() != nil {
			// Killing the docker client leaves the container running: remove it by its name.
			rm, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			exec.CommandContext(rm, c.bin, "rm", "-f", name).Run()
		}
		return err
	}
	sweepOrphans(c.bin)
	var idOut, errOut bytes.Buffer
	// --rm as well as the deferred rm: if a2dlc itself is killed after start, the container still goes when it exits.
	// Killed between create and start, neither runs: the label lets a later a2dlc remove it (sweepOrphans).
	create := exec.CommandContext(ctx, c.bin, append(append(append([]string{"create", "--rm", "--label", copyInLabel}, c.flags...), c.image), c.cmd...)...)
	create.Stdout, create.Stderr = &idOut, &errOut
	if err := create.Run(); err != nil {
		return fmt.Errorf("docker create %s: %v: %s", c.image, err, tail(errOut.String()))
	}
	id := strings.TrimSpace(idOut.String())
	defer func() {
		// Not ctx: after a timeout it is already done, and the container would be left behind.
		rm, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		exec.CommandContext(rm, c.bin, "rm", "-f", id).Run()
	}()
	errOut.Reset()
	cp := exec.CommandContext(ctx, c.bin, "cp", "-", id+":/")
	cp.Stdout, cp.Stderr = &errOut, &errOut
	stdin, err := cp.StdinPipe()
	if err != nil {
		return err
	}
	if err := cp.Start(); err != nil {
		return fmt.Errorf("docker cp: %v", err)
	}
	errTar := writeTrees(stdin, c.trees)
	stdin.Close()
	if err := cp.Wait(); err != nil {
		return fmt.Errorf("docker cp into %s: %v: %s", c.image, err, tail(errOut.String()))
	}
	if errTar != nil {
		return fmt.Errorf("tar of the trees for %s: %v", c.image, errTar)
	}
	start := exec.CommandContext(ctx, c.bin, "start", "-a", id)
	start.Stdout, start.Stderr = out, out
	return start.Run()
}

// copyInLabel marks the containers the copy-in path creates, so the ones an a2dlc killed before docker start left in
// Created can be found and removed (a2dlc-ltk).
const copyInLabel = "a2dlc.copyin=1"

// orphanAge is how long a labelled container may sit in Created before a sweep removes it. create, cp and start take
// seconds, so a container that old belongs to no a2dlc still running.
const orphanAge = time.Hour

var swept sync.Map // docker binary -> struct{}: one sweep per binary and process

// sweepOrphans removes the copy-in containers left in Created for longer than orphanAge, once per process. It is best
// effort: a docker that cannot list or remove them does not stop the run.
func sweepOrphans(bin string) {
	if _, done := swept.LoadOrStore(bin, struct{}{}); done {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "ps", "-a", "--filter", "label="+copyInLabel, "--filter", "status=created",
		"--format", "{{.ID}} {{.CreatedAt}}").Output()
	if err != nil {
		return
	}
	for _, id := range orphansIn(string(out), time.Now()) {
		exec.CommandContext(ctx, bin, "rm", "-f", id).Run()
	}
}

// orphansIn picks, from `docker ps --format "{{.ID}} {{.CreatedAt}}"` lines, the ids created more than orphanAge before
// now. A line whose time does not parse is left alone.
func orphansIn(ps string, now time.Time) []string {
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(ps), "\n") {
		id, at, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok {
			continue
		}
		created, err := time.Parse("2006-01-02 15:04:05 -0700 MST", at)
		if err == nil && now.Sub(created) > orphanAge {
			ids = append(ids, id)
		}
	}
	return ids
}

// containerName is a unique --name for a container, so it can be removed when its context ends.
func containerName() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "a2dlc-" + hex.EncodeToString(b)
}

// writeTrees writes one tar with each tree under its path. Like gitx.Export, it carries directories and regular files
// only: symlinks are skipped.
func writeTrees(w io.Writer, trees []dirAt) error {
	tw := tar.NewWriter(w)
	for _, t := range trees {
		prefix := strings.TrimPrefix(t.at, "/")
		err := filepath.WalkDir(t.dir, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(t.dir, p)
			if err != nil {
				return err
			}
			name := prefix
			if rel != "." {
				name += "/" + filepath.ToSlash(rel)
			}
			info, err := de.Info()
			if err != nil {
				return err
			}
			switch {
			case de.IsDir():
				return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: info.ModTime()})
			case !de.Type().IsRegular():
				return nil
			}
			if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: int64(info.Mode().Perm()),
				Size: info.Size(), ModTime: info.ModTime()}); err != nil {
				return err
			}
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			return err
		}
	}
	return tw.Close()
}
