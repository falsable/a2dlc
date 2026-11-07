package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ImageBuilder is what install asks of Docker to build the derived engine image. The real one is engine.DockerBuilder;
// tests inject a fake, so no test needs Docker. out is the build log, kept even when the build fails.
type ImageBuilder interface {
	Build(ctx context.Context, tag, dockerfile, contextDir string) (out string, err error)
}

// DeriveOptions are the flags of `a2dlc install` that concern the derived Python engine image (a2dlc-pe6.6).
type DeriveOptions struct {
	Skip bool // --no-derive-image
	Dev  bool // --dev-deps: also install the repo's dev requirements
}

const (
	pathEngineDockerfile = ".a2dlc/engine-py.Dockerfile"
	pathEngineContext    = ".a2dlc/engine-py"
	derivedPrefix        = "a2dlc-engine-py-"
)

// Requirement files install reads, relative to the repo root: the first production one that exists, and with --dev-deps
// every development one that exists. A name outside these lists (requirements-docs.txt) is never installed.
var (
	prodRequirements = []string{"requirements.txt", "requirements-prod.txt", "requirements/prod.txt", "requirements/base.txt", "requirements/production.txt"}
	devRequirements  = []string{"requirements-dev.txt", "requirements/dev.txt", "requirements-test.txt", "requirements/test.txt"}
)

// derivation is what the repo's dependency files turn into: the requirements file for the build context, the local files
// (vendored wheels, find-links folders) it needs next to it, and where it all came from.
type derivation struct {
	requirements string
	local        map[string][]byte // path under the context's local/ → content
	sources      []string
}

var (
	reqIncludeRe = regexp.MustCompile(`^(?:-r|--requirement)[ =]\s*(\S+)`)
	reqLinksRe   = regexp.MustCompile(`^(?:-f|--find-links)[ =]\s*(\S+)`)
)

// pythonDependencies reads requirements*.txt (or, with none, [project] dependencies of pyproject.toml). ok is false when
// the repo declares no dependency.
func (a App) pythonDependencies(dev bool) (derivation, bool, error) {
	d := derivation{local: map[string][]byte{}}
	var lines []string
	exists := func(rel string) bool { _, err := os.Stat(a.path(rel)); return err == nil }
	var files []string
	for _, rel := range prodRequirements {
		if exists(rel) {
			files = append(files, rel)
			break
		}
	}
	if dev {
		for _, rel := range devRequirements {
			if exists(rel) {
				files = append(files, rel)
			}
		}
	}
	for _, rel := range files {
		got, err := a.readRequirements(rel, &d, 0)
		if err != nil {
			return d, false, err
		}
		lines = append(lines, got...)
		d.sources = append(d.sources, rel)
	}
	if len(files) == 0 && exists("pyproject.toml") {
		b, err := os.ReadFile(a.path("pyproject.toml"))
		if err != nil {
			return d, false, err
		}
		if deps := pyprojectDependencies(string(b)); len(deps) > 0 {
			lines = append(lines, deps...)
			d.sources = append(d.sources, "pyproject.toml")
		}
	}
	if len(lines) == 0 {
		return d, false, nil
	}
	d.requirements = strings.Join(lines, "\n") + "\n"
	return d, true, nil
}

// readRequirements returns the effective lines of one requirements file: comments and blanks gone, `-r other.txt`
// inlined (depth-limited against cycles), and a local file or find-links folder copied into d.local with the line
// rewritten to where the Dockerfile puts it.
func (a App) readRequirements(rel string, d *derivation, depth int) ([]string, error) {
	if depth > 5 {
		return nil, fmt.Errorf("%s: -r includes nest deeper than 5", rel)
	}
	b, err := os.ReadFile(a.path(rel))
	if err != nil {
		return nil, err
	}
	var out []string
	for _, raw := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		dir := path.Dir(rel)
		switch {
		case reqIncludeRe.MatchString(line):
			inc := path.Join(dir, reqIncludeRe.FindStringSubmatch(line)[1])
			got, err := a.readRequirements(inc, d, depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, got...)
		case reqLinksRe.MatchString(line):
			target := reqLinksRe.FindStringSubmatch(line)[1]
			if dest, ok := a.vendor(path.Join(dir, target), d); ok {
				line = "--find-links " + dest
			}
			out = append(out, line)
		case !strings.HasPrefix(line, "-") && (strings.HasSuffix(line, ".whl") || strings.HasSuffix(line, ".tar.gz") || strings.HasSuffix(line, ".zip")):
			if dest, ok := a.vendor(path.Join(dir, line), d); ok {
				line = dest
			}
			out = append(out, line)
		default:
			out = append(out, line)
		}
	}
	return out, nil
}

// vendor copies a local file or folder (a vendored wheel, a find-links folder) into d.local and returns where the image
// sees it. A path outside the repo or one that does not exist is left as written: the build fails and says so.
func (a App) vendor(rel string, d *derivation) (string, bool) {
	rel = path.Clean(rel)
	if strings.HasPrefix(rel, "../") || path.IsAbs(rel) {
		return "", false
	}
	info, err := os.Stat(a.path(rel))
	if err != nil {
		return "", false
	}
	add := func(file string) {
		if b, err := os.ReadFile(a.path(file)); err == nil {
			d.local[file] = b
		}
	}
	if !info.IsDir() {
		add(rel)
		return "/local/" + rel, true
	}
	entries, _ := os.ReadDir(a.path(rel))
	for _, e := range entries {
		if !e.IsDir() {
			add(path.Join(rel, e.Name()))
		}
	}
	return "/local/" + rel, true
}

// pyprojectDependencies reads the strings of `dependencies = [...]` in the [project] table. It is a line reader, not a TOML
// parser: it covers the arrays that PEP 621 tools write, one or several strings per line.
func pyprojectDependencies(toml string) []string {
	var deps []string
	inProject, inArray := false, false
	quoted := regexp.MustCompile(`"((?:[^"\\]|\\.)*)"|'([^']*)'`)
	for _, raw := range strings.Split(strings.ReplaceAll(toml, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "[") && !inArray {
			inProject = line == "[project]"
			continue
		}
		if !inProject {
			continue
		}
		if !inArray {
			rest, ok := strings.CutPrefix(line, "dependencies")
			rest = strings.TrimSpace(rest)
			rest, ok2 := strings.CutPrefix(rest, "=")
			if !ok || !ok2 {
				continue
			}
			line, inArray = strings.TrimSpace(rest), true
		}
		for _, m := range quoted.FindAllStringSubmatch(line, -1) {
			deps = append(deps, m[1]+m[2])
		}
		if strings.Contains(line, "]") {
			return deps
		}
	}
	return deps
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// engineTag is the part of the derived image's tag that names the engine it derives from: the tag of the image, or the
// first 12 hex digits of the digest when the image is name@sha256:... without a tag.
func engineTag(base string) string {
	ref, digest, _ := strings.Cut(base, "@")
	if i := strings.LastIndex(ref, ":"); i > strings.LastIndex(ref, "/") {
		return ref[i+1:]
	}
	if _, hex, ok := strings.Cut(digest, ":"); ok && len(hex) >= 12 {
		return "sha256-" + hex[:12]
	}
	return "latest"
}

func derivedDockerfile(base string) string {
	return fmt.Sprintf(`# Written by a2dlc install: the engine image plus the repo's dependencies, so the engine can import the code it judges.
# Build with network once; a2dlc check runs the image with --network none.
FROM %s
USER root
# The engine's own packages are pinned in the base image. As constraints they win: a repo requirement that conflicts with
# one fails this build with pip's message instead of replacing the engine's version.
RUN pip freeze > /opt/a2dlc-engine-constraints.txt
COPY requirements.txt /tmp/repo-requirements.txt
COPY local /local
RUN pip install --no-cache-dir -r /tmp/repo-requirements.txt -c /opt/a2dlc-engine-constraints.txt && rm /tmp/repo-requirements.txt
`, base)
}

// derivedTag: a2dlc-engine-py-<repo slug>:<engine tag>-<hash of everything the image is built from>.
func derivedTag(repoDir, base, dockerfile string, d derivation) string {
	slug := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(filepath.Base(repoDir)), "-"), "-")
	if slug == "" {
		slug = "repo"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	h := sha256.New()
	h.Write([]byte(base + "\x00" + dockerfile + "\x00" + d.requirements))
	var names []string
	for n := range d.local {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		h.Write([]byte("\x00" + n + "\x00"))
		h.Write(d.local[n])
	}
	return derivedPrefix + slug + ":" + engineTag(base) + "-" + hex.EncodeToString(h.Sum(nil))[:12]
}

// DeriveEngineImage gives the Python engine the repo's dependencies (a2dlc-pe6.6). Without them the engine cannot import
// the code it judges and every target is INCONCLUSIVE "could not import". It writes .a2dlc/engine-py.Dockerfile and its
// build context, builds a2dlc-engine-py-<repo>:<engine tag>-<hash>, and points the python engine of the config at it.
// A failed build or no network is not a failure of install: the config keeps the base image and a message says what to run.
// The messages are for stdout; warnings go to stderr.
func (a App) DeriveEngineImage(ctx context.Context, opt DeriveOptions) (done, warnings []string) {
	if opt.Skip {
		return nil, nil
	}
	cfg, err := a.config()
	if err != nil {
		return nil, nil
	}
	idx := -1
	for i, e := range cfg.Engines {
		if strings.EqualFold(e.Language, "python") {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, nil
	}
	current := cfg.Engines[idx].Image
	if current != enginePyImage && !strings.HasPrefix(current, derivedPrefix) {
		return []string{"kept the python engine image " + current + ": it is not the a2dlc engine image, so no image is derived from the repo's dependencies"}, nil
	}
	d, ok, err := a.pythonDependencies(opt.Dev)
	if err != nil {
		return nil, []string{"could not read the repo's Python dependencies, so the engine image was not derived: " + err.Error()}
	}
	if !ok {
		return nil, nil
	}
	dockerfile := derivedDockerfile(enginePyImage)
	tag := derivedTag(a.Repo.Dir, enginePyImage, dockerfile, d)
	if err := a.writeDerivationContext(dockerfile, d); err != nil {
		return nil, []string{"could not write " + pathEngineDockerfile + ": " + err.Error()}
	}
	done = append(done, fmt.Sprintf("wrote %s from %s", pathEngineDockerfile, strings.Join(d.sources, ", ")))
	later := fmt.Sprintf("docker build -f %s -t %s %s && a2dlc install", pathEngineDockerfile, tag, pathEngineContext)
	keep := fmt.Sprintf("the config keeps %s, so a check on code that imports third-party packages is INCONCLUSIVE (could not import); to build the image later run: %s", current, later)
	present := false
	if a.Probe != nil {
		present, _ = a.Probe.ImagePresent(ctx, tag)
	}
	if !present {
		if a.Builder == nil {
			return done, []string{"no Docker builder available: " + keep}
		}
		out, err := a.Builder.Build(ctx, tag, a.path(pathEngineDockerfile), a.path(pathEngineContext))
		if err != nil {
			if why := conflictLines(out); why != "" {
				return done, []string{"the repo's requirements conflict with the packages the engine pins (the engine's win, so no image was built): " + why + "; " + keep}
			}
			return done, []string{"could not build " + tag + " (" + err.Error() + "; the network is needed once): " + keep}
		}
	}
	cfg.Engines[idx].Image = tag
	if err := a.write(pathConfig, cfg); err != nil {
		return done, []string{"built " + tag + " but could not point " + pathConfig + " at it: " + err.Error()}
	}
	return append(done, fmt.Sprintf("python engine image is now %s (the repo's dependencies on top of %s)", tag, enginePyImage)), nil
}

func (a App) writeDerivationContext(dockerfile string, d derivation) error {
	ctxDir := a.path(pathEngineContext)
	if err := os.RemoveAll(ctxDir); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(ctxDir, "local"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ctxDir, "requirements.txt"), []byte(d.requirements), 0o644); err != nil {
		return err
	}
	for rel, b := range d.local {
		dest := filepath.Join(ctxDir, "local", filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(dest, b, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(a.path(pathEngineDockerfile), []byte(dockerfile), 0o644)
}

// buildLogPrefix is what BuildKit puts before each line of a step's output ("#9 2.011 ").
var buildLogPrefix = regexp.MustCompile(`^#\d+ [\d.]+ `)

// conflictLines picks pip's account of a resolution conflict out of a build log: "" when the log has none. It keeps the
// line that names the packages, the engine's pin and what depends on what, and drops the rest of the log.
func conflictLines(out string) string {
	if !strings.Contains(out, "conflicting dependencies") && !strings.Contains(out, "ResolutionImpossible") {
		return ""
	}
	var keep []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(buildLogPrefix.ReplaceAllString(strings.TrimSpace(l), ""))
		if strings.HasPrefix(l, "ERROR: Cannot install") || strings.HasPrefix(l, "The user requested") || strings.Contains(l, " depends on ") {
			keep = append(keep, strings.TrimPrefix(l, "ERROR: "))
		}
		if len(keep) == 6 {
			break
		}
	}
	return strings.Join(keep, " | ")
}
