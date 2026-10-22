package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// Dependency allowlist (sec-dependency-allowlist, protocol §7 supply chain). The dependency manifests and lockfiles that
// the candidate changes are diffed against the base, and every added or version-changed dependency must be listed in
// .a2dlc/deps.allow.json (or the file named in config.supply_chain.allowlist_file). The allowlist is read from the base
// commit, never from the candidate, so a builder that edits it changes nothing. Parsing is textual, one reader per
// ecosystem and no package manager: the core knows no languages, and nothing is installed.
//
// Fail closed. Two things escalate as "unparsed:<file>", which no allowlist entry can allow:
//   - a change to a manifest or lockfile of an ecosystem that is not parsed (unparsedFiles), and
//   - a change to a construct the reader cannot check in a parsed manifest: a go.mod replace or exclude, a requirements
//     index, find-links or include option, a pyproject package index or uv source (see the opaque lines of parsed).
//     Constructs that are already in the base and unchanged are not a change.
// The one thing the reader does not see is what a manifest resolves through outside the repo (a .npmrc registry, a
// pip.conf, a .cargo/config.toml): those files are not manifests and are not read.

// DepsAllowlistDefault is where the allowlist lives when config.supply_chain does not name another file.
const DepsAllowlistDefault = ".a2dlc/deps.allow.json"

// unparsedFiles are manifests and lockfiles of ecosystems this reader does not parse, by base name: a change to one of them
// cannot be checked against the allowlist, so it fails closed. The list is finite: a manifest of an ecosystem that is
// neither parsed nor listed here (environment.yml, renv.lock, build.sbt, Project.toml, stack.yaml, ...) is not a dependency
// change for this reader and passes; the README records that limit (unparsedManifest adds the suffixes).
var unparsedFiles = map[string]bool{
	"yarn.lock": true, "pnpm-lock.yaml": true, "poetry.lock": true, "uv.lock": true, "pdm.lock": true,
	"Pipfile.lock": true, "Gemfile.lock": true, "composer.lock": true, "bun.lock": true, "bun.lockb": true,
	"npm-shrinkwrap.json": true, "Gemfile": true, "Pipfile": true, "composer.json": true, "setup.py": true,
	"setup.cfg": true, "go.work": true,
	// Maven and Gradle
	"pom.xml": true, "build.gradle": true, "build.gradle.kts": true, "settings.gradle": true, "settings.gradle.kts": true,
	"gradle.lockfile": true,
	// Elixir, Swift, Dart, CocoaPods, Carthage, Deno
	"mix.exs": true, "mix.lock": true, "Package.swift": true, "Package.resolved": true, "pubspec.yaml": true,
	"pubspec.lock": true, "Podfile": true, "Podfile.lock": true, "Cartfile": true, "Cartfile.resolved": true,
	"deno.json": true, "deno.jsonc": true, "deno.lock": true,
	// .NET, C and C++, Conan, vcpkg, NuGet
	"Directory.Packages.props": true, "Directory.Build.props": true, "packages.lock.json": true, "packages.config": true,
	"conanfile.txt": true, "conanfile.py": true, "vcpkg.json": true,
}

// unparsedSuffixes are the file name endings of manifests of ecosystems this reader does not parse (fail closed).
var unparsedSuffixes = []string{".csproj", ".fsproj", ".vbproj", ".gemspec", ".cabal", ".podspec"}

// unparsedManifest reports whether file is a manifest or lockfile this reader does not parse, by its name.
func unparsedManifest(file string) bool {
	name := path.Base(file)
	if unparsedFiles[name] {
		return true
	}
	for _, s := range unparsedSuffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	return false
}

// sourceChangedVersion is the version a dependency gets when its version is the same in base and candidate but its
// locked source (npm resolved URL, integrity) is not. It is no real version, so an exact allowlist entry never matches it;
// a name-only entry does.
const sourceChangedVersion = "(source changed)"

// DepsAllowlistFile is the allowlist file the config names (config.supply_chain.allowlist_file), or the default.
func (a App) DepsAllowlistFile() (string, error) {
	cfg, err := a.config()
	if err != nil {
		return "", err
	}
	if cfg.SupplyChain != nil && cfg.SupplyChain.AllowlistFile != "" {
		return cfg.SupplyChain.AllowlistFile, nil
	}
	return DepsAllowlistDefault, nil
}

// DepsAllowlist reads the allowlist from base. A missing file is an empty allowlist: every dependency is then new.
func (a App) DepsAllowlist(base string) (contract.DepsAllow, error) {
	file, err := a.DepsAllowlistFile()
	if err != nil {
		return contract.DepsAllow{}, err
	}
	b, err := a.Repo.Show(base, file)
	if errors.Is(err, os.ErrNotExist) {
		return contract.DepsAllow{SchemaVersion: contract.SchemaVersion}, nil
	}
	if err != nil {
		return contract.DepsAllow{}, err
	}
	var allow contract.DepsAllow
	if err := contract.DecodeStrictBytes(b, &allow); err != nil {
		return contract.DepsAllow{}, fmt.Errorf("%s out of contract (contracts/deps-allow.schema.json): %w", file, err)
	}
	return allow, nil
}

// DependencyChanges returns the dependencies that candidate adds or changes in the manifests and lockfiles among changed
// (the paths the diff from base touches), with Allowed set from allow. A dependency is allowed when an entry names it in
// its ecosystem and, if the entry has versions, every version the candidate has is exactly that string (ranges are not
// evaluated, so they fail closed). A dependency whose version is unchanged but whose npm source changed is a change with
// no version, so only a name-only entry allows it. Removed dependencies are not changes. Sorted by ecosystem and name.
func (a App) DependencyChanges(base, candidate string, changed []string, allow contract.DepsAllow) ([]contract.DependencyChange, error) {
	type key struct{ eco, name string }
	found := map[key]depVersions{}
	var order []key
	note := func(k key, vs depVersions) {
		if _, ok := found[k]; !ok {
			order = append(order, k)
		}
		if found[k] == nil {
			found[k] = depVersions{}
		}
		for v := range vs {
			found[k][v] = true
		}
	}
	for _, file := range changed {
		if unparsedManifest(file) {
			note(key{"unparsed", file}, depVersions{"": true})
			continue
		}
		eco := ecosystemOf(file)
		if eco == "" {
			continue
		}
		before, okBefore, err := a.manifestDeps(base, file)
		if err != nil {
			return nil, err
		}
		after, okAfter, err := a.manifestDeps(candidate, file)
		if err != nil {
			return nil, err
		}
		if !okBefore || !okAfter || !sameSet(before.opaque, after.opaque) {
			// a manifest the reader cannot parse (the builder broke it, or the base did), or a construct it cannot check
			// changed: not checked, so it fails closed
			note(key{"unparsed", file}, depVersions{"": true})
			continue
		}
		for dep, vs := range after.deps {
			old, existed := before.deps[dep]
			versionChanged := !existed || !sameVersions(old, vs)
			sourceChanged := existed && !sameSet(before.sources[dep], after.sources[dep])
			switch {
			case versionChanged:
				note(key{eco, dep}, vs)
			case sourceChanged:
				note(key{eco, dep}, depVersions{sourceChangedVersion: true})
			}
		}
	}
	out := []contract.DependencyChange{}
	for _, k := range order {
		allowed := false
		for _, e := range allow.Allow {
			if e.Ecosystem != k.eco || normalizeName(k.eco, e.Name) != normalizeName(k.eco, k.name) {
				continue
			}
			if e.Versions == "" || onlyVersion(found[k], e.Versions) {
				allowed = true
				break
			}
		}
		out = append(out, contract.DependencyChange{Ecosystem: k.eco, Name: k.name, Allowed: allowed})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Ecosystem != out[j].Ecosystem {
			return out[i].Ecosystem < out[j].Ecosystem
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

// manifestDeps parses one manifest at a treeish; a file that does not exist there has nothing. ok is false when the file is
// there but the reader cannot parse it; err is only for failures to read the repo.
func (a App) manifestDeps(treeish, file string) (p *parsed, ok bool, err error) {
	b, err := a.Repo.Show(treeish, file)
	if errors.Is(err, os.ErrNotExist) {
		return newParsed(), true, nil
	}
	if err != nil {
		return nil, false, err
	}
	out, perr := parseDeps(file, b)
	if perr != nil {
		return nil, false, nil
	}
	return out, true, nil
}

// depVersions is the set of versions (or ranges, for npm manifests) that a dependency has in one file.
type depVersions map[string]bool

// parsed is what one reader finds in a manifest: the dependencies with their versions, the locked source of each package
// where the format has one (npm: resolved URL and integrity, by name), and the constructs the reader cannot check.
type parsed struct {
	deps    map[string]depVersions
	sources map[string][]string
	opaque  []string
}

func newParsed() *parsed {
	return &parsed{deps: map[string]depVersions{}, sources: map[string][]string{}}
}

func sameVersions(a, b depVersions) bool {
	if len(a) != len(b) {
		return false
	}
	for v := range a {
		if !b[v] {
			return false
		}
	}
	return true
}

// sameSet reports whether two lists hold the same elements, in any order.
func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func onlyVersion(vs depVersions, want string) bool {
	for v := range vs {
		if v != want {
			return false
		}
	}
	return len(vs) > 0
}

// ecosystemOf names the ecosystem of a manifest or lockfile by its path, or "" if the file is not one this reader parses.
func ecosystemOf(file string) string {
	name := path.Base(file)
	switch {
	case name == "go.mod" || name == "go.sum":
		return "go"
	case name == "package.json" || name == "package-lock.json":
		return "npm"
	case name == "Cargo.toml" || name == "Cargo.lock":
		return "cargo"
	case name == "pyproject.toml", isRequirementsFile(file):
		return "pypi"
	}
	return ""
}

// isRequirementsFile reports whether file is a pip requirements file: a requirements*.txt, requirements*.in or
// requirements*.lock anywhere in the repo (dev-requirements.txt, requirements.in, pip-tools output), or any .txt or .in
// kept in a requirements directory (requirements/prod.txt).
func isRequirementsFile(file string) bool {
	name := path.Base(file)
	switch ext := path.Ext(name); {
	case strings.Contains(name, "requirements") && (ext == ".txt" || ext == ".in" || ext == ".lock"):
		return true
	case ext == ".txt" || ext == ".in":
		for _, dir := range strings.Split(path.Dir(file), "/") {
			if dir == "requirements" {
				return true
			}
		}
	}
	return false
}

// parseDeps reads the dependencies of one manifest or lockfile. An empty file has none.
func parseDeps(file string, b []byte) (*parsed, error) {
	p := newParsed()
	if len(b) == 0 {
		return p, nil
	}
	switch name := path.Base(file); {
	case name == "go.mod":
		parseGoMod(b, p)
	case name == "go.sum":
		parseGoSum(b, p)
	case name == "package.json":
		return p, parseNpmManifest(b, p)
	case name == "package-lock.json":
		return p, parseNpmLock(b, p)
	case name == "Cargo.toml":
		parseCargoManifest(b, p)
	case name == "Cargo.lock":
		parseCargoLock(b, p)
	case name == "pyproject.toml":
		parsePyproject(b, p)
	default:
		parseRequirements(b, p)
	}
	return p, nil
}

func add(out map[string]depVersions, name, version string) {
	if name == "" {
		return
	}
	if out[name] == nil {
		out[name] = depVersions{}
	}
	out[name][version] = true
}

// goBlock opens a parenthesized go.mod block that holds dependency or dependency-changing directives.
var goBlock = regexp.MustCompile(`^(require|replace|exclude)\s*\(\s*$`)

// goComment is a // comment: it starts a token (a space or the line start before it), so a URL in a directive is kept.
var goComment = regexp.MustCompile(`(^|\s)//.*$`)

// parseGoMod reads the require directives. replace and exclude change what a require resolves to (a replace can point a
// module at local code), so each one is opaque: a change of any of them fails closed.
func parseGoMod(b []byte, p *parsed) {
	block := ""
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(goComment.ReplaceAllString(l, ""))
		if block != "" {
			switch {
			case t == ")":
				block = ""
			case block == "require":
				if f := strings.Fields(t); len(f) >= 2 {
					add(p.deps, f[0], f[1])
				}
			case t != "":
				p.opaque = append(p.opaque, block+" "+t)
			}
			continue
		}
		if m := goBlock.FindStringSubmatch(t); m != nil {
			block = m[1]
			continue
		}
		switch {
		case strings.HasPrefix(t, "require "):
			if f := strings.Fields(t); len(f) >= 3 {
				add(p.deps, f[1], f[2])
			}
		case strings.HasPrefix(t, "replace "), strings.HasPrefix(t, "exclude "):
			p.opaque = append(p.opaque, t)
		}
	}
}

func parseGoSum(b []byte, p *parsed) {
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 {
			add(p.deps, f[0], strings.TrimSuffix(f[1], "/go.mod"))
		}
	}
}

func parseNpmManifest(b []byte, p *parsed) error {
	var doc struct {
		Dependencies         map[string]string `json:"dependencies"`
		DevDependencies      map[string]string `json:"devDependencies"`
		OptionalDependencies map[string]string `json:"optionalDependencies"`
		PeerDependencies     map[string]string `json:"peerDependencies"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	for _, m := range []map[string]string{doc.Dependencies, doc.DevDependencies, doc.OptionalDependencies, doc.PeerDependencies} {
		for name, v := range m {
			add(p.deps, name, v)
		}
	}
	return nil
}

// npmLockEntry is a package of package-lock.json: versions 2 and 3 list packages by path, version 1 nests dependencies.
type npmLockEntry struct {
	Version      string                  `json:"version"`
	Resolved     string                  `json:"resolved"`
	Integrity    string                  `json:"integrity"`
	Dependencies map[string]npmLockEntry `json:"dependencies"`
}

func parseNpmLock(b []byte, p *parsed) error {
	var doc struct {
		Packages     map[string]npmLockEntry `json:"packages"`
		Dependencies map[string]npmLockEntry `json:"dependencies"` // lockfile version 1
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return err
	}
	for key, e := range doc.Packages {
		if i := strings.LastIndex(key, "node_modules/"); i >= 0 {
			p.addLocked(key[i+len("node_modules/"):], e)
		}
	}
	p.addLockedV1(doc.Dependencies)
	return nil
}

// addLockedV1 walks the nested dependencies of lockfile version 1, where a package's own dependencies sit under it.
func (p *parsed) addLockedV1(entries map[string]npmLockEntry) {
	for name, e := range entries {
		p.addLocked(name, e)
		p.addLockedV1(e.Dependencies)
	}
}

func (p *parsed) addLocked(name string, e npmLockEntry) {
	add(p.deps, name, e.Version)
	if e.Resolved != "" || e.Integrity != "" {
		p.sources[name] = append(p.sources[name], e.Version+" "+e.Resolved+" "+e.Integrity)
	}
}

// cargoOpaque is a [patch] or [replace] table: it points a crate at another source, so its lines fail closed.
func cargoOpaque(section string) bool {
	return section == "patch" || strings.HasPrefix(section, "patch.") || section == "replace"
}

func parseCargoManifest(b []byte, p *parsed) {
	section, table := "", ""
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(stripHash(l))
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "[") {
			section, table = strings.Trim(t, "[] "), ""
			if m := cargoDepTable.FindStringSubmatch(section); m != nil {
				table = m[1]
				add(p.deps, table, "")
			}
			if cargoOpaque(section) {
				p.opaque = append(p.opaque, section)
			}
			continue
		}
		if cargoOpaque(section) {
			p.opaque = append(p.opaque, section+" "+t)
			continue
		}
		key, value, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		key, value = strings.Trim(strings.TrimSpace(key), `"`), strings.TrimSpace(value)
		switch {
		case table != "" && key == "version":
			p.deps[table] = depVersions{}
			add(p.deps, table, strings.Trim(value, `"`))
		case table == "" && strings.HasSuffix(section, "dependencies"):
			add(p.deps, key, strings.Trim(value, `"`))
		}
	}
}

var cargoDepTable = regexp.MustCompile(`(?:^|\.)(?:dev-|build-)?dependencies\.([A-Za-z0-9_-]+)$`)

func parseCargoLock(b []byte, p *parsed) {
	var name, version string
	registry := false
	flush := func() {
		if name != "" && registry {
			add(p.deps, name, version)
		}
		name, version, registry = "", "", false
	}
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case t == "[[package]]":
			flush()
		case strings.HasPrefix(t, "name = "):
			name = strings.Trim(strings.TrimPrefix(t, "name = "), `"`)
		case strings.HasPrefix(t, "version = "):
			version = strings.Trim(strings.TrimPrefix(t, "version = "), `"`)
		case strings.HasPrefix(t, "source = "):
			registry = true // path and workspace packages have no source: they are not dependencies
		}
	}
	flush()
}

// pyprojectOpaque is a table that names where packages come from (a uv source or index, a poetry or pdm source): a line
// of it fails closed.
func pyprojectOpaque(section string) bool {
	for _, t := range []string{"tool.uv.sources", "tool.uv.index", "tool.poetry.source", "tool.pdm.source"} {
		if section == t || strings.HasPrefix(section, t+".") {
			return true
		}
	}
	return false
}

// poetryTable is a Poetry dependency given as its own table: [tool.poetry.dependencies.<name>] (also group and dev forms).
var poetryTable = regexp.MustCompile(`^tool\.poetry\.(?:group\.[^.]+\.)?(?:dev-)?dependencies\.(.+)$`)

// tomlString matches a TOML basic or literal string on a line.
var tomlString = regexp.MustCompile(`"([^"]+)"|'([^']+)'`)

// parsePyproject reads [project] dependencies, optional and group arrays, the [build-system] requires (build-time code
// runs from them), Poetry's dependency tables in both forms, and the tables that name other sources (opaque).
func parsePyproject(b []byte, p *parsed) {
	section, inArray := "", false
	for _, l := range strings.Split(string(b), "\n") {
		t := strings.TrimSpace(stripHash(l))
		if t == "" {
			continue
		}
		if inArray {
			addRequirementStrings(p, t)
			inArray = !strings.Contains(t, "]")
			continue
		}
		if strings.HasPrefix(t, "[") {
			section = strings.Trim(t, "[] ")
			if pyprojectOpaque(section) {
				p.opaque = append(p.opaque, section)
			}
			continue
		}
		key, value, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		key, value = strings.Trim(strings.TrimSpace(key), `"'`), strings.TrimSpace(value)
		if pyprojectOpaque(section) {
			p.opaque = append(p.opaque, section+" "+t)
			continue
		}
		if m := poetryTable.FindStringSubmatch(section); m != nil {
			name := strings.Trim(m[1], `"'`)
			switch key {
			case "version":
				add(p.deps, name, unquoteTOML(value))
			case "git", "path", "url":
				add(p.deps, name, key+"="+unquoteTOML(value))
			}
			continue
		}
		arrayHere := key == "dependencies" && section == "project" || section == "project.optional-dependencies" ||
			section == "dependency-groups" || key == "requires" && section == "build-system"
		if strings.HasPrefix(value, "[") && arrayHere {
			inArray = !strings.Contains(value, "]")
			addRequirementStrings(p, value)
			continue
		}
		if strings.HasPrefix(section, "tool.poetry.") && strings.HasSuffix(section, "dependencies") && key != "python" {
			add(p.deps, key, unquoteTOML(value))
		}
	}
}

// addRequirementStrings adds the PEP 508 requirements quoted on one TOML line (double or single quotes).
func addRequirementStrings(p *parsed, s string) {
	for _, q := range tomlString.FindAllStringSubmatch(s, -1) {
		v := q[1]
		if v == "" {
			v = q[2]
		}
		name, version := parseRequirement(v)
		add(p.deps, name, version)
	}
}

func unquoteTOML(v string) string {
	return strings.Trim(strings.TrimSpace(v), `"'`)
}

// logicalLines joins the lines a requirements file continues with a trailing backslash.
func logicalLines(b []byte) []string {
	var out []string
	cur := ""
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimRight(l, "\r")
		if s, ok := strings.CutSuffix(strings.TrimRight(l, " \t"), "\\"); ok {
			cur += s + " "
			continue
		}
		out = append(out, cur+l)
		cur = ""
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// reqOptionStart is where a requirement's own options begin (pip's --hash lines of a locked file, for instance).
var reqOptionStart = regexp.MustCompile(`\s--`)

// parseRequirements reads a requirements*.txt file: one requirement per logical line, comments skipped. -e and --editable
// name their target: the #egg name, or the URL or path. Other options change where packages come from (an index,
// find-links, an included or constraints file) and are opaque. A --hash on a requirement belongs to the lock, not to the
// dependency, so it is skipped.
func parseRequirements(b []byte, p *parsed) {
	for _, l := range logicalLines(b) {
		raw := strings.TrimSpace(l)
		if target, ok := editableTarget(raw); ok {
			name, version := editable(target)
			add(p.deps, name, version)
			continue
		}
		t := strings.TrimSpace(stripHash(l))
		if t == "" {
			continue
		}
		if strings.HasPrefix(t, "-") {
			p.opaque = append(p.opaque, t)
			continue
		}
		spec, opts := t, ""
		if loc := reqOptionStart.FindStringIndex(t); loc != nil {
			spec, opts = t[:loc[0]], t[loc[0]:]
		}
		for _, o := range strings.Fields(opts) {
			if name, _, _ := strings.Cut(o, "="); strings.HasPrefix(name, "--") && name != "--hash" {
				p.opaque = append(p.opaque, t)
				break
			}
		}
		name, version := parseRequirement(spec)
		add(p.deps, name, version)
	}
}

// editableTarget returns what -e or --editable names on a requirements line, if the line is one.
func editableTarget(t string) (string, bool) {
	for _, flag := range []string{"--editable", "-e"} {
		if rest, ok := strings.CutPrefix(t, flag); ok && (rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '=') {
			return strings.TrimSpace(strings.TrimPrefix(rest, "=")), true
		}
	}
	return "", false
}

// editable names the dependency of an editable target: its #egg name if it has one (normalized, a PyPI name), else the
// URL or path itself. Its version is "url" or "path", which no exact version entry allows.
func editable(target string) (name, version string) {
	if i := strings.Index(target, "#egg="); i >= 0 {
		egg := target[i+len("#egg="):]
		if j := strings.Index(egg, "&"); j >= 0 {
			egg = egg[:j]
		}
		return normalizeName("pypi", egg), "url"
	}
	if strings.Contains(target, "://") {
		return target, "url"
	}
	return target, "path"
}

var pyRequirement = regexp.MustCompile(`^([A-Za-z0-9][A-Za-z0-9._-]*)(?:\[[^\]]*\])?\s*(.*)$`)

// parseRequirement splits a PEP 508 requirement into its normalized name and the rest (version and markers). A direct URL
// is its own name, so it is always a new dependency unless listed as one.
func parseRequirement(spec string) (name, version string) {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, ";"); i >= 0 {
		spec = strings.TrimSpace(spec[:i])
	}
	if strings.Contains(spec, "://") || strings.Contains(spec, " @ ") {
		return spec, "url"
	}
	m := pyRequirement.FindStringSubmatch(spec)
	if m == nil {
		return spec, ""
	}
	return normalizeName("pypi", m[1]), strings.TrimSpace(m[2])
}

// normalizeName is the ecosystem's name comparison: PyPI names are case-insensitive and ignore _ and . against -.
func normalizeName(eco, name string) string {
	if eco == "pypi" {
		return strings.NewReplacer("_", "-", ".", "-").Replace(strings.ToLower(name))
	}
	return name
}

// stripHash drops a # comment (manifests here have no # inside values except in URLs, which are rare enough to fail
// closed through the name).
func stripHash(l string) string {
	if i := strings.Index(l, "#"); i >= 0 {
		return l[:i]
	}
	return l
}
