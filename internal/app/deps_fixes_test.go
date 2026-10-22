package app

import (
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// Each manifest that the reader does not parse fails closed when it changes (sec-dependency-allowlist, one case per file).
func TestDependencyUnparsedManifestsFailClosed(t *testing.T) {
	a, _, _ := newRepo(t)
	for _, file := range []string{"Gemfile", "Gemfile.lock", "Pipfile", "Pipfile.lock", "composer.json", "composer.lock", "setup.py",
		"setup.cfg", "go.work", "pom.xml", "build.gradle", "build.gradle.kts", "settings.gradle.kts", "gradle.lockfile",
		"mix.exs", "Package.swift", "deno.json", "app/Service.csproj"} {
		t.Run(file, func(t *testing.T) {
			base := commitOn(t, a, "HEAD", map[string]string{file: "# base\n"})
			cand := commitOn(t, a, base, map[string]string{file: "# base\ngem 'evil'\nevil = \"*\"\n\"evil/pkg\"\n"})
			cs, err := a.DependencyChanges(base, cand, []string{file}, contract.DepsAllow{})
			if err != nil || changesOf(t, cs) != "unparsed:"+file {
				t.Fatalf("%v %s", err, changesOf(t, cs))
			}
		})
	}
}

// requirements.txt: an editable VCS target is a dependency, an index option fails closed, a --hash belongs to the lock.
func TestRequirementsEditableIndexAndHashes(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"requirements.txt": "httpx==0.27.0\n"})
	editable := commitOn(t, a, base, map[string]string{"requirements.txt": "httpx==0.27.0\n-e git+https://evil.example/x.git#egg=x\n"})
	cs, err := a.DependencyChanges(base, editable, []string{"requirements.txt"}, contract.DepsAllow{})
	if err != nil || changesOf(t, cs) != "pypi:x" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	index := commitOn(t, a, base, map[string]string{"requirements.txt": "httpx==0.27.0\n--extra-index-url https://evil.example/simple\n"})
	if cs, err = a.DependencyChanges(base, index, []string{"requirements.txt"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "unparsed:requirements.txt" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// a hashed requirement (pip-compile --generate-hashes) has the version the allowlist names, not the backslash
	hashed := commitOn(t, a, base, map[string]string{"requirements.txt": "httpx==0.27.0\nrequests==2.31.0 \\\n    --hash=sha256:aaa \\\n    --hash=sha256:bbb\n"})
	exact := contract.DepsAllow{Allow: []contract.AllowEntry{{Ecosystem: "pypi", Name: "requests", Versions: "==2.31.0"}}}
	if cs, err = a.DependencyChanges(base, hashed, []string{"requirements.txt"}, exact); err != nil || changesOf(t, cs) != "pypi:requests(allowed)" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// an option already in the base is no change
	withIndex := commitOn(t, a, "HEAD", map[string]string{"requirements.txt": "--index-url https://mirror.example/simple\nhttpx==0.27.0\n"})
	bumped := commitOn(t, a, withIndex, map[string]string{"requirements.txt": "--index-url https://mirror.example/simple\nhttpx==0.28.0\n"})
	if cs, err = a.DependencyChanges(withIndex, bumped, []string{"requirements.txt"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:httpx" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}

// go.mod: a replace or exclude that changes fails closed, one already in the base does not, and a replace target is read
// whole (its URL is not cut at the //).
func TestGoModReplaceAndExcludeFailClosed(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"go.mod": "module x\n\nrequire example.com/a v1.0.0\n"})
	replaced := commitOn(t, a, base, map[string]string{"go.mod": "module x\n\nrequire example.com/a v1.0.0\n\nreplace example.com/a => ./evil\n"})
	if cs, err := a.DependencyChanges(base, replaced, []string{"go.mod"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "unparsed:go.mod" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	excluded := commitOn(t, a, base, map[string]string{"go.mod": "module x\n\nrequire example.com/a v1.0.0\n\nexclude (\n\texample.com/a v0.9.0\n)\n"})
	if cs, err := a.DependencyChanges(base, excluded, []string{"go.mod"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "unparsed:go.mod" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	forked := commitOn(t, a, "HEAD", map[string]string{"go.mod": "module x\n\nreplace example.com/a => https://fork.example/a v1.0.0\n"})
	swapped := commitOn(t, a, forked, map[string]string{"go.mod": "module x\n\nreplace example.com/a => https://evil.example/a v1.0.0\n"})
	if cs, err := a.DependencyChanges(forked, swapped, []string{"go.mod"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "unparsed:go.mod" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// the base already has a replace; a require bump alone is read as a dependency change
	bumped := commitOn(t, a, forked, map[string]string{"go.mod": "module x\n\nrequire example.com/b v1.1.0\n\nreplace example.com/a => https://fork.example/a v1.0.0\n"})
	if cs, err := a.DependencyChanges(forked, bumped, []string{"go.mod"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "go:example.com/b" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}

// pyproject.toml: build-system requires run code at build time; Poetry's table form and single-quoted arrays are read;
// a uv source fails closed.
func TestPyprojectBuildSystemPoetryTablesAndQuotes(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"pyproject.toml": "[build-system]\nrequires = [\"setuptools>=61\"]\n\n[tool.poetry.dependencies]\npython = \"^3.11\"\n"})
	build := commitOn(t, a, base, map[string]string{"pyproject.toml": "[build-system]\nrequires = [\"setuptools>=61\", \"evil==1\"]\n\n[tool.poetry.dependencies]\npython = \"^3.11\"\n"})
	if cs, err := a.DependencyChanges(base, build, []string{"pyproject.toml"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	table := commitOn(t, a, base, map[string]string{"pyproject.toml": "[build-system]\nrequires = [\"setuptools>=61\"]\n\n[tool.poetry.dependencies]\npython = \"^3.11\"\n\n[tool.poetry.dependencies.evil]\nversion = \"^1.0\"\n"})
	if cs, err := a.DependencyChanges(base, table, []string{"pyproject.toml"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	single := commitOn(t, a, base, map[string]string{"pyproject.toml": "[build-system]\nrequires = [\"setuptools>=61\"]\n\n[project]\nname = 'x'\ndependencies = ['evil==1']\n"})
	if cs, err := a.DependencyChanges(base, single, []string{"pyproject.toml"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	source := commitOn(t, a, base, map[string]string{"pyproject.toml": "[build-system]\nrequires = [\"setuptools>=61\"]\n\n[tool.uv.sources]\nevil = { git = \"https://evil.example/evil\" }\n"})
	if cs, err := a.DependencyChanges(base, source, []string{"pyproject.toml"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "unparsed:pyproject.toml" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}

// dev-requirements.txt is a requirements file too.
func TestRequirementsFileNamesWithPrefixAndSuffix(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"dev-requirements.txt": "httpx==0.27.0\n"})
	cand := commitOn(t, a, base, map[string]string{"dev-requirements.txt": "httpx==0.27.0\nevil==1\n"})
	if cs, err := a.DependencyChanges(base, cand, []string{"dev-requirements.txt"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}

// A requirements file in a subdirectory, and requirements.in (the pip-tools input), are read as pypi dependencies: adding
// one to either is a change the allowlist judges, not a silent pass.
func TestRequirementsInSubdirectoryAndRequirementsIn(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"requirements/prod.txt": "httpx==0.27.0\n"})
	cand := commitOn(t, a, base, map[string]string{"requirements/prod.txt": "httpx==0.27.0\nevil==1\n"})
	if cs, err := a.DependencyChanges(base, cand, []string{"requirements/prod.txt"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	inBase := commitOn(t, a, "HEAD", map[string]string{"requirements.in": "httpx\n"})
	inCand := commitOn(t, a, inBase, map[string]string{"requirements.in": "httpx\nevil\n"})
	if cs, err := a.DependencyChanges(inBase, inCand, []string{"requirements.in"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "pypi:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}

// npm: lockfile version 1 nests the dependencies of a package, and a resolved or integrity change with the same version is
// a change that an exact version entry does not allow but a name-only entry does.
func TestNpmLockNestedAndSourceSwap(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"package-lock.json": `{"dependencies":{"a":{"version":"1.0.0","dependencies":{"b":{"version":"1.0.0"}}}}}`})
	nested := commitOn(t, a, base, map[string]string{"package-lock.json": `{"dependencies":{"a":{"version":"1.0.0","dependencies":{"b":{"version":"1.0.0"},"evil":{"version":"6.6.6"}}}}}`})
	if cs, err := a.DependencyChanges(base, nested, []string{"package-lock.json"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "npm:evil" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	lock := func(resolved, integrity string) string {
		return `{"packages":{"node_modules/x":{"version":"1.0.0","resolved":"` + resolved + `","integrity":"` + integrity + `"}}}`
	}
	pinned := commitOn(t, a, "HEAD", map[string]string{"package-lock.json": lock("https://registry.npmjs.org/x/-/x-1.0.0.tgz", "sha512-A")})
	swapped := commitOn(t, a, pinned, map[string]string{"package-lock.json": lock("https://evil.example/x-1.0.0.tgz", "sha512-B")})
	exact := contract.DepsAllow{Allow: []contract.AllowEntry{{Ecosystem: "npm", Name: "x", Versions: "1.0.0"}}}
	if cs, err := a.DependencyChanges(pinned, swapped, []string{"package-lock.json"}, exact); err != nil || changesOf(t, cs) != "npm:x" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	nameOnly := contract.DepsAllow{Allow: []contract.AllowEntry{{Ecosystem: "npm", Name: "x"}}}
	if cs, err := a.DependencyChanges(pinned, swapped, []string{"package-lock.json"}, nameOnly); err != nil || changesOf(t, cs) != "npm:x(allowed)" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// the same source is no change
	same := commitOn(t, a, pinned, map[string]string{"package-lock.json": lock("https://registry.npmjs.org/x/-/x-1.0.0.tgz", "sha512-A")})
	if cs, err := a.DependencyChanges(pinned, same, []string{"package-lock.json"}, exact); err != nil || len(cs) != 0 {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}
