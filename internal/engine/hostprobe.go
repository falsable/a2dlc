package engine

import (
	"bytes"
	"context"
	"encoding/json"
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

// importsProbe runs inside the engine image, with the repo mounted read-only at /repo and the file list on stdin. It
// asks the engine's own resolver (engine.installed, engine.roots_local), so doctor and a check agree on what imports.
// Imports under try/except or `if TYPE_CHECKING:` are skipped: they are allowed to be missing.
const importsProbe = `
import ast, json, sys
sys.path.insert(0, "/engine")
import engine
from pathlib import Path
root = Path("/repo")
local = engine.roots_local(root)
std = set(sys.stdlib_module_names)
missing = {}
for f in json.load(sys.stdin)["files"]:
    if engine.is_test_code(f):
        continue
    try:
        tree = ast.parse((root / f).read_text(encoding="utf-8"))
    except Exception:
        continue
    guarded = set()
    for n in ast.walk(tree):
        if isinstance(n, ast.Try):
            for b in n.body:
                guarded.update(id(x) for x in ast.walk(b))
        elif isinstance(n, ast.If) and "TYPE_CHECKING" in ast.dump(n.test):
            for b in n.body:
                guarded.update(id(x) for x in ast.walk(b))
    for n in ast.walk(tree):
        if id(n) in guarded:
            continue
        names = [a.name for a in n.names] if isinstance(n, ast.Import) else [n.module] if isinstance(n, ast.ImportFrom) and not n.level and n.module else []
        for name in names:
            top = name.split(".")[0]
            if top in std or top in local or top == "__future__" or engine.installed(top):
                continue
            missing.setdefault(top, [])
            if f not in missing[top]:
                missing[top].append(f)
print(json.dumps(missing))
`

// UnresolvedImports lists, for the Python files, the top-level modules that image cannot import: module → files. The
// container has no network; the repo is read-only.
func (p HostProbe) UnresolvedImports(ctx context.Context, image, repoDir string, files []string) (map[string][]string, error) {
	in, err := json.Marshal(map[string][]string{"files": files})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, p.docker(), "run", "--rm", "-i", "--network", "none", "-v", repoDir+":/repo:ro", "--entrypoint", "python", image, "-c", importsProbe)
	var stdout, stderr bytes.Buffer
	cmd.Stdin, cmd.Stdout, cmd.Stderr = bytes.NewReader(in), &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s run %s: %v: %s", p.docker(), image, err, firstLine(strings.TrimSpace(stderr.String())))
	}
	missing := map[string][]string{}
	if err := json.Unmarshal(stdout.Bytes(), &missing); err != nil {
		return nil, fmt.Errorf("the import probe of %s answered something else than JSON: %v", image, err)
	}
	return missing, nil
}
