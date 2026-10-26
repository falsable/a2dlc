package engine

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

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
	if !c.copyIn {
		args := append([]string{"run", "--rm"}, c.flags...)
		for _, t := range c.trees {
			v := t.dir + ":" + t.at
			if c.ro {
				v += ":ro"
			}
			args = append(args, "-v", v)
		}
		cmd := exec.CommandContext(ctx, c.bin, append(append(args, c.image), c.cmd...)...)
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	}
	var idOut, errOut bytes.Buffer
	// --rm as well as the deferred rm: if a2dlc itself is killed after start, the container still goes when it exits.
	create := exec.CommandContext(ctx, c.bin, append(append(append([]string{"create", "--rm"}, c.flags...), c.image), c.cmd...)...)
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
