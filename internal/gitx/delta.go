package gitx

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Delta lists what Export(to) holds that Export(from) does not, and what it lacks that Export(from) holds. It sees what Export
// sees: regular files, nothing under .a2dlc/run, symlinks and submodules left out, so a path that turns into one of those is
// deleted. from and to are trees or commits.
func (r Repo) Delta(from, to string) (changed, deleted []string, err error) {
	out, err := r.git(nil, "diff", "--raw", "--no-renames", "-z", "--no-abbrev", from, to, "--", ":(exclude).a2dlc/run")
	if err != nil {
		return nil, nil, err
	}
	fields := strings.Split(strings.TrimRight(out, "\x00"), "\x00")
	changed, deleted = []string{}, []string{}
	for i := 0; i+1 < len(fields); i += 2 {
		meta, path := strings.Fields(strings.TrimPrefix(fields[i], ":")), fields[i+1]
		if len(meta) < 5 {
			return nil, nil, fmt.Errorf("git diff --raw: unreadable entry %q", fields[i])
		}
		newMode, state := meta[1], meta[4]
		switch {
		case state == "D" || newMode == "120000" || newMode == "160000":
			deleted = append(deleted, path)
		default:
			changed = append(changed, path)
		}
	}
	return changed, deleted, nil
}

// HasTree says whether the repo has the tree object, so that a Delta from it can be computed.
func (r Repo) HasTree(tree string) bool {
	_, err := r.git(nil, "cat-file", "-e", tree+"^{tree}")
	return err == nil
}

// ExportPaths writes only paths of treeish into dir, the way Export writes them (git archive: export-ignore and export-subst
// apply, symlinks are skipped).
func (r Repo) ExportPaths(treeish, dir string, paths []string) error {
	const chunk = 100
	for i := 0; i < len(paths); i += chunk {
		j := min(i+chunk, len(paths))
		args := []string{"archive", "--format=tar", treeish, "--"}
		for _, p := range paths[i:j] {
			args = append(args, ":(literal)"+p)
		}
		c := exec.Command("git", args...)
		c.Dir = r.Dir
		var errb bytes.Buffer
		c.Stderr = &errb
		pipe, err := c.StdoutPipe()
		if err != nil {
			return err
		}
		if err := c.Start(); err != nil {
			return err
		}
		errTar := extract(pipe, dir)
		io.Copy(io.Discard, pipe)
		if err := c.Wait(); err != nil {
			return fmt.Errorf("git archive %s: %v: %s", treeish, err, strings.TrimSpace(errb.String()))
		}
		if errTar != nil {
			return errTar
		}
	}
	return nil
}
