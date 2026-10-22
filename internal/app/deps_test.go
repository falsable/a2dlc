package app

import (
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// depsOf flattens the dependencies a reader finds in one file into "name@v1|v2" pairs, sorted.
func depsOf(t *testing.T, file, content string) string {
	t.Helper()
	p, err := parseDeps(file, []byte(content))
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	var out []string
	for name, vs := range p.deps {
		var v []string
		for x := range vs {
			v = append(v, x)
		}
		sort.Strings(v)
		out = append(out, name+"@"+strings.Join(v, "|"))
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

func TestDepsReadGoModAndGoSum(t *testing.T) {
	gomod := "module example.com/x\n\ngo 1.23\n\nrequire example.com/a v1.2.3 // indirect\n\nrequire (\n\texample.com/b v0.1.0\n\texample.com/c v2.0.0 // why\n)\n\nreplace example.com/b => ../b\n"
	if got := depsOf(t, "go.mod", gomod); got != "example.com/a@v1.2.3 example.com/b@v0.1.0 example.com/c@v2.0.0" {
		t.Fatalf("go.mod: %s", got)
	}
	// go.sum lists the module twice (the source and its go.mod): one dependency, one version
	gosum := "example.com/a v1.2.3 h1:x=\nexample.com/a v1.2.3/go.mod h1:y=\n"
	if got := depsOf(t, "sub/go.sum", gosum); got != "example.com/a@v1.2.3" {
		t.Fatalf("go.sum: %s", got)
	}
}

func TestDepsReadNpmManifestAndLockfile(t *testing.T) {
	pkg := `{"dependencies":{"left-pad":"^1.3.0"},"devDependencies":{"jest":"29.0.0"}}`
	if got := depsOf(t, "package.json", pkg); got != "jest@29.0.0 left-pad@^1.3.0" {
		t.Fatalf("package.json: %s", got)
	}
	// the root package has no node_modules/ key, and a nested copy is named by its last node_modules/ segment
	lock := `{"packages":{"":{"name":"x"},"node_modules/left-pad":{"version":"1.3.0"},"node_modules/a/node_modules/b":{"version":"2.0.0"}}}`
	if got := depsOf(t, "package-lock.json", lock); got != "b@2.0.0 left-pad@1.3.0" {
		t.Fatalf("package-lock.json: %s", got)
	}
}

func TestDepsReadRequirementsAndPyproject(t *testing.T) {
	req := "# comment\n-r other.txt\nRequests[socks]>=2.31 ; python_version > '3'\nSome_Pkg==1.0\n\n"
	if got := depsOf(t, "requirements-dev.txt", req); got != "requests@>=2.31 some-pkg@==1.0" {
		t.Fatalf("requirements: %s", got)
	}
	pyproject := "[project]\nname = \"x\"\ndependencies = [\n  \"httpx>=0.27\",\n  \"pydantic==2.8.0\",\n]\n\n" +
		"[project.optional-dependencies]\ndev = [\"pytest>=8\"]\n\n[tool.poetry.dependencies]\npython = \"^3.11\"\nrich = \"^13.7\"\n"
	if got := depsOf(t, "pyproject.toml", pyproject); got != "httpx@>=0.27 pydantic@==2.8.0 pytest@>=8 rich@^13.7" {
		t.Fatalf("pyproject.toml: %s", got)
	}
}

func TestDepsReadCargoManifestAndLockfile(t *testing.T) {
	cargo := "[package]\nname = \"x\"\nversion = \"0.1.0\"\n\n[dependencies]\nserde = \"1.0\"\ntokio = \"1\"\n\n" +
		"[dev-dependencies.criterion]\nversion = \"0.5\"\n\n[target.'cfg(unix)'.dependencies]\nlibc = \"0.2\"\n"
	if got := depsOf(t, "Cargo.toml", cargo); got != "criterion@0.5 libc@0.2 serde@1.0 tokio@1" {
		t.Fatalf("Cargo.toml: %s", got)
	}
	// a package without source is the crate itself or a path dependency of the workspace: not a registry dependency
	lock := "[[package]]\nname = \"x\"\nversion = \"0.1.0\"\n\n[[package]]\nname = \"serde\"\nversion = \"1.0.200\"\n" +
		"source = \"registry+https://github.com/rust-lang/crates.io-index\"\n"
	if got := depsOf(t, "Cargo.lock", lock); got != "serde@1.0.200" {
		t.Fatalf("Cargo.lock: %s", got)
	}
}

// commitOn commits files over base (not over HEAD, as candidateOf does), so a test can have a base with manifests.
func commitOn(t *testing.T, a App, base string, files map[string]string) string {
	t.Helper()
	base, err := a.Repo.ResolveCommit(base)
	if err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	if err := a.Repo.Export(base, work); err != nil {
		t.Fatal(err)
	}
	for rel, s := range files {
		write(t, work, rel, s)
	}
	tree, err := a.Repo.TreeFromDir(base, work)
	if err != nil {
		t.Fatal(err)
	}
	c, err := a.Repo.CommitOn(tree, base, "a2dlc test", time.Unix(0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func changesOf(t *testing.T, cs []contract.DependencyChange) string {
	t.Helper()
	var out []string
	for _, c := range cs {
		s := c.Ecosystem + ":" + c.Name
		if c.Allowed {
			s += "(allowed)"
		}
		out = append(out, s)
	}
	return strings.Join(out, " ")
}

func TestDependencyNotInAllowlistIsReported(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"go.mod": "module x\n\nrequire example.com/a v1.0.0\n"})
	cand := commitOn(t, a, base, map[string]string{"go.mod": "module x\n\nrequire (\n\texample.com/a v1.0.0\n\texample.com/new v1.0.0\n)\n"})
	allow := contract.DepsAllow{Allow: []contract.AllowEntry{{Ecosystem: "go", Name: "example.com/a"}}}
	cs, err := a.DependencyChanges(base, cand, []string{"go.mod"}, allow)
	if err != nil {
		t.Fatal(err)
	}
	// the untouched dependency is no change; the added one is, and it is not allowed
	if got := changesOf(t, cs); got != "go:example.com/new" {
		t.Fatalf("%s", got)
	}
	allow.Allow = append(allow.Allow, contract.AllowEntry{Ecosystem: "go", Name: "example.com/new"})
	if cs, _ = a.DependencyChanges(base, cand, []string{"go.mod"}, allow); changesOf(t, cs) != "go:example.com/new(allowed)" {
		t.Fatalf("%s", changesOf(t, cs))
	}
	// an allowlist entry in another ecosystem does not count
	allow.Allow = []contract.AllowEntry{{Ecosystem: "npm", Name: "example.com/new"}}
	if cs, _ = a.DependencyChanges(base, cand, []string{"go.mod"}, allow); changesOf(t, cs) != "go:example.com/new" {
		t.Fatalf("%s", changesOf(t, cs))
	}
}

func TestDependencyVersionChangeNeedsTheAllowedVersion(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"requirements.txt": "httpx==0.27.0\n"})
	bumped := commitOn(t, a, base, map[string]string{"requirements.txt": "httpx==0.28.0\n"})
	exact := contract.DepsAllow{Allow: []contract.AllowEntry{{Ecosystem: "pypi", Name: "httpx", Versions: "==0.27.0"}}}
	cs, err := a.DependencyChanges(base, bumped, []string{"requirements.txt"}, exact)
	if err != nil || changesOf(t, cs) != "pypi:httpx" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// the same version the allowlist names is no change at all
	same := commitOn(t, a, base, map[string]string{"requirements.txt": "HTTPX==0.27.0\n"})
	if cs, err = a.DependencyChanges(base, same, []string{"requirements.txt"}, exact); err != nil || len(cs) != 0 {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// the name without versions allows any version of it
	any := contract.DepsAllow{Allow: []contract.AllowEntry{{Ecosystem: "pypi", Name: "httpx"}}}
	if cs, _ = a.DependencyChanges(base, bumped, []string{"requirements.txt"}, any); changesOf(t, cs) != "pypi:httpx(allowed)" {
		t.Fatalf("%s", changesOf(t, cs))
	}
}

func TestDependencyUnparsableOrUnparsedLockfileFailsClosed(t *testing.T) {
	a, _, _ := newRepo(t)
	base := commitOn(t, a, "HEAD", map[string]string{"package.json": `{"dependencies":{}}`})
	broken := commitOn(t, a, base, map[string]string{"package.json": `{"dependencies": `})
	cs, err := a.DependencyChanges(base, broken, []string{"package.json"}, contract.DepsAllow{})
	if err != nil || changesOf(t, cs) != "unparsed:package.json" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	yarn := commitOn(t, a, base, map[string]string{"yarn.lock": "# new\n"})
	if cs, err = a.DependencyChanges(base, yarn, []string{"yarn.lock"}, contract.DepsAllow{}); err != nil || changesOf(t, cs) != "unparsed:yarn.lock" {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
	// a changed file that is not a manifest is not a dependency change
	readme := commitOn(t, a, base, map[string]string{"README.md": "changed\n"})
	if cs, err = a.DependencyChanges(base, readme, []string{"README.md"}, contract.DepsAllow{}); err != nil || len(cs) != 0 {
		t.Fatalf("%v %s", err, changesOf(t, cs))
	}
}

func TestDepsAllowlistIsReadFromTheBaseAndStrict(t *testing.T) {
	a, _, _ := newRepo(t)
	// no file in the base: an empty allowlist, so every dependency is new
	allow, err := a.DepsAllowlist("HEAD")
	if err != nil || len(allow.Allow) != 0 {
		t.Fatalf("%v %+v", err, allow)
	}
	ok := commitOn(t, a, "HEAD", map[string]string{".a2dlc/deps.allow.json": `{"schema_version":1,"allow":[{"ecosystem":"go","name":"example.com/a"}]}`})
	if allow, err = a.DepsAllowlist(ok); err != nil || len(allow.Allow) != 1 || allow.Allow[0].Name != "example.com/a" {
		t.Fatalf("%v %+v", err, allow)
	}
	// a field the schema does not have is an error, not a silent empty list
	bad := commitOn(t, a, "HEAD", map[string]string{".a2dlc/deps.allow.json": `{"schema_version":1,"allowed":[]}`})
	if _, err = a.DepsAllowlist(bad); err == nil {
		t.Fatal("an allowlist out of contract was accepted")
	}
}
