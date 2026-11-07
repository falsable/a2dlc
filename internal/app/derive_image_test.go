package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// fakeBuilder is the docker runner of these tests: it records the build and answers with the log and error it is given.
type fakeBuilder struct {
	out        string
	err        error
	tag        string
	dockerfile string
	ctxDir     string
	calls      int
}

func (b *fakeBuilder) Build(_ context.Context, tag, dockerfile, contextDir string) (string, error) {
	b.calls++
	b.tag, b.dockerfile, b.ctxDir = tag, dockerfile, contextDir
	return b.out, b.err
}

func deriveFix(t *testing.T, files map[string]string, b *fakeBuilder) *doctorFix {
	t.Helper()
	f := newDoctorFix(t, files)
	f.a.Builder = b
	return f
}

func pythonImage(t *testing.T, f *doctorFix) string {
	t.Helper()
	cfg, err := f.a.config()
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range cfg.Engines {
		if e.Language == "python" {
			return e.Image
		}
	}
	t.Fatal("no python engine in the config")
	return ""
}

func contextRequirements(f *doctorFix) string {
	b, _ := os.ReadFile(filepath.Join(f.dir, ".a2dlc", "engine-py", "requirements.txt"))
	return string(b)
}

func TestDeriveWritesDockerfileBuildsAndPointsConfig(t *testing.T) {
	b := &fakeBuilder{}
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "# web\nfastapi==0.115.0\n\nhttpx>=0.27  # client\n"}, b)
	done, warnings := f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if len(warnings) != 0 {
		t.Fatalf("warnings: %v", warnings)
	}
	if b.calls != 1 || !strings.HasPrefix(b.tag, "a2dlc-engine-py-") || !strings.Contains(b.tag, ":0.14-") {
		t.Fatalf("build: %+v", b)
	}
	if got := pythonImage(t, f); got != b.tag {
		t.Fatalf("config image = %q, want %q (done: %v)", got, b.tag, done)
	}
	df, err := os.ReadFile(filepath.Join(f.dir, ".a2dlc", "engine-py.Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"FROM " + enginePyImage + "\n", "USER root", "pip freeze > /opt/a2dlc-engine-constraints.txt", "-c /opt/a2dlc-engine-constraints.txt"} {
		if !strings.Contains(string(df), want) {
			t.Errorf("Dockerfile lacks %q:\n%s", want, df)
		}
	}
	if got := contextRequirements(f); got != "fastapi==0.115.0\nhttpx>=0.27\n" {
		t.Errorf("requirements.txt of the context = %q", got)
	}
	if filepath.ToSlash(b.dockerfile) != filepath.ToSlash(filepath.Join(f.dir, ".a2dlc", "engine-py.Dockerfile")) {
		t.Errorf("built with dockerfile %s", b.dockerfile)
	}
}

func TestDeriveTagFollowsTheDependencies(t *testing.T) {
	tagOf := func(reqs string) string {
		b := &fakeBuilder{}
		f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": reqs}, b)
		f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
		// the repo directory differs per call, so compare only the part after the slug
		return b.tag[strings.Index(b.tag, ":"):]
	}
	if a, b := tagOf("fastapi==0.115.0\n"), tagOf("fastapi==0.116.0\n"); a == b {
		t.Fatalf("two different requirements got the same tag %s", a)
	}
	if a, b := tagOf("fastapi==0.115.0\n"), tagOf("fastapi==0.115.0\n"); a != b {
		t.Fatalf("the same requirements got %s and %s", a, b)
	}
}

func TestDeriveBuildFailureKeepsBaseAndPrintsTheCommand(t *testing.T) {
	b := &fakeBuilder{out: "ERROR: Could not find a version that satisfies the requirement nothing", err: errors.New("docker build: exit status 1")}
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "nothing==9\n"}, b)
	_, warnings := f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if got := pythonImage(t, f); got != enginePyImage {
		t.Fatalf("the config image became %q after a failed build", got)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "docker build -f .a2dlc/engine-py.Dockerfile -t "+b.tag+" .a2dlc/engine-py") ||
		!strings.Contains(warnings[0], "keeps "+enginePyImage) {
		t.Fatalf("warnings: %v", warnings)
	}
}

func TestDeriveWithoutBuilderKeepsBaseAndSaysSo(t *testing.T) {
	f := newDoctorFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "fastapi\n"})
	_, warnings := f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if pythonImage(t, f) != enginePyImage || len(warnings) != 1 || !strings.Contains(warnings[0], "docker build") {
		t.Fatalf("image %s, warnings %v", pythonImage(t, f), warnings)
	}
}

// The engine's packages win: the Dockerfile pins them as constraints, so a repo requirement that conflicts fails the build,
// and install says which requirement and keeps the base image instead of shipping an image with the engine's pins replaced.
func TestDeriveReportsARepoPinThatConflictsWithTheEngine(t *testing.T) {
	log := "ERROR: Cannot install hypothesis==5.0.0 and hypothesis==6.112.0 because these package versions have conflicting dependencies.\n" +
		"The conflict is caused by:\n    The user requested hypothesis==5.0.0\n    The user requested (constraint) hypothesis==6.112.0\n" +
		"ERROR: ResolutionImpossible: for help visit https://pip.pypa.io/\n"
	b := &fakeBuilder{out: log, err: errors.New("docker build: exit status 1")}
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "hypothesis==5.0.0\n"}, b)
	_, warnings := f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if pythonImage(t, f) != enginePyImage {
		t.Fatal("a conflicting pin changed the engine image")
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "conflict with the packages the engine pins") || !strings.Contains(warnings[0], "hypothesis==5.0.0") ||
		!strings.Contains(warnings[0], "the engine's win") {
		t.Fatalf("warnings: %v", warnings)
	}
	df, _ := os.ReadFile(filepath.Join(f.dir, ".a2dlc", "engine-py.Dockerfile"))
	if !strings.Contains(string(df), "-c /opt/a2dlc-engine-constraints.txt") {
		t.Fatalf("the install line does not use the engine's constraints:\n%s", df)
	}
}

func TestDeriveDevRequirementsOnlyWithTheFlag(t *testing.T) {
	files := map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "fastapi\n", "requirements-dev.txt": "pytest\n", "requirements-docs.txt": "sphinx\n"}
	reqs := func(opt DeriveOptions) string {
		f := deriveFix(t, files, &fakeBuilder{})
		f.a.DeriveEngineImage(context.Background(), opt)
		return contextRequirements(f)
	}
	if got := reqs(DeriveOptions{}); got != "fastapi\n" {
		t.Errorf("without the flag: %q", got)
	}
	if got := reqs(DeriveOptions{Dev: true}); got != "fastapi\npytest\n" {
		t.Errorf("with --dev-deps: %q", got)
	}
}

func TestDeriveInlinesRequirementIncludes(t *testing.T) {
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "-r requirements/base.txt\nuvicorn\n", "requirements/base.txt": "fastapi\n"}, &fakeBuilder{})
	f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if got := contextRequirements(f); got != "fastapi\nuvicorn\n" {
		t.Fatalf("requirements: %q", got)
	}
}

func TestDeriveFromPyprojectDependencies(t *testing.T) {
	toml := "[build-system]\nrequires = [\"setuptools\"]\n\n[project]\nname = \"x\"\ndependencies = [\n  \"fastapi>=0.100\",  # web\n  'httpx',\n]\n\n[project.optional-dependencies]\ndev = [\"pytest\"]\n"
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "pyproject.toml": toml}, &fakeBuilder{})
	f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if got := contextRequirements(f); got != "fastapi>=0.100\nhttpx\n" {
		t.Fatalf("requirements: %q", got)
	}
	if got := pyprojectDependencies("[project]\nname=\"x\"\ndependencies = [\"a\", \"b>=1\"]\n"); strings.Join(got, ",") != "a,b>=1" {
		t.Fatalf("one-line array: %v", got)
	}
	if got := pyprojectDependencies("[project]\nname=\"x\"\n"); len(got) != 0 {
		t.Fatalf("no dependencies key: %v", got)
	}
}

func TestDeriveVendorsLocalWheelsAndFindLinksFolders(t *testing.T) {
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "--no-index\n--find-links vendor\ntinydep==0.1\n./extra/other-1.0-py3-none-any.whl\n",
		"vendor/tinydep-0.1-py3-none-any.whl": "wheel-bytes", "extra/other-1.0-py3-none-any.whl": "other-bytes"}, &fakeBuilder{})
	f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if got := contextRequirements(f); got != "--no-index\n--find-links /local/vendor\ntinydep==0.1\n/local/extra/other-1.0-py3-none-any.whl\n" {
		t.Fatalf("requirements: %q", got)
	}
	for _, rel := range []string{"vendor/tinydep-0.1-py3-none-any.whl", "extra/other-1.0-py3-none-any.whl"} {
		if _, err := os.Stat(filepath.Join(f.dir, ".a2dlc", "engine-py", "local", filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s was not copied into the build context: %v", rel, err)
		}
	}
}

func TestDeriveSkipsWhenAskedWithNoDependenciesOrWithACustomImage(t *testing.T) {
	b := &fakeBuilder{}
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "fastapi\n"}, b)
	if done, w := f.a.DeriveEngineImage(context.Background(), DeriveOptions{Skip: true}); len(done)+len(w) != 0 || b.calls != 0 {
		t.Fatalf("--no-derive-image still did something: %v %v", done, w)
	}
	if _, err := os.Stat(filepath.Join(f.dir, ".a2dlc", "engine-py.Dockerfile")); err == nil {
		t.Fatal("--no-derive-image wrote a Dockerfile")
	}
	none := deriveFix(t, map[string]string{"src/a.py": "x = 1\n"}, b)
	none.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if b.calls != 0 {
		t.Fatal("a repo with no dependencies was built")
	}
	custom := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "fastapi\n"}, b)
	cfg, _ := custom.a.config()
	cfg.Engines[0].Image = "registry.example/my-engine:1"
	if err := custom.a.write(pathConfig, cfg); err != nil {
		t.Fatal(err)
	}
	done, _ := custom.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if b.calls != 0 || pythonImage(t, custom) != "registry.example/my-engine:1" || len(done) != 1 || !strings.Contains(done[0], "kept the python engine image") {
		t.Fatalf("a custom image was touched: calls=%d image=%s done=%v", b.calls, pythonImage(t, custom), done)
	}
}

func TestDeriveIsIdempotentAndSkipsTheBuildWhenTheImageIsThere(t *testing.T) {
	b := &fakeBuilder{}
	f := deriveFix(t, map[string]string{"src/a.py": "x = 1\n", "requirements.txt": "fastapi\n"}, b)
	f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	tag := b.tag
	f.probe.images[tag] = true // what `docker build` then left behind
	b.calls = 0
	f.a.DeriveEngineImage(context.Background(), DeriveOptions{})
	if b.calls != 0 || pythonImage(t, f) != tag {
		t.Fatalf("second install: builds=%d image=%s want %s", b.calls, pythonImage(t, f), tag)
	}
}

// The log of a real `docker build` (BuildKit) with the vendored conflicting pin of the manual run on 2026-11-07.
func TestConflictLinesFromARealBuildKitLog(t *testing.T) {
	log := "#6 [2/5] RUN pip freeze > /opt/a2dlc-engine-constraints.txt\n#9 [5/5] RUN pip install --no-cache-dir -r /tmp/repo-requirements.txt -c /opt/a2dlc-engine-constraints.txt\n" +
		"#9 2.011 ERROR: Cannot install attrs==0.0.1 because these package versions have conflicting dependencies.\n#9 2.013 The conflict is caused by:\n" +
		"#9 2.013     The user requested (constraint) attrs==26.1.0\n#9 2.478 ERROR: ResolutionImpossible: for help visit https://pip.pypa.io/\n"
	want := "Cannot install attrs==0.0.1 because these package versions have conflicting dependencies. | The user requested (constraint) attrs==26.1.0"
	if got := conflictLines(log); got != want {
		t.Fatalf("conflictLines = %q, want %q", got, want)
	}
	if conflictLines("ERROR: Could not find a version that satisfies the requirement nothing") != "" {
		t.Fatal("a missing package was reported as a conflict")
	}
}

func TestEngineTag(t *testing.T) {
	for in, want := range map[string]string{
		"a2dlc-engine-py:0.14": "0.14",
		"ghcr.io/example/a2dlc-engine-py@sha256:aaaaaaaaaaaaaaaaaaaaaaaa": "sha256-aaaaaaaaaaaa",
		"localhost:5000/a2dlc-engine-py:0.15":                             "0.15",
	} {
		if got := engineTag(in); got != want {
			t.Errorf("engineTag(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDoctorWarnsWhenTheEngineImageCannotImportWhatTheRepoImports(t *testing.T) {
	f := pyRepo(t)
	f.probe.unresolved = map[string][]string{"fastapi": {"src/a.py"}, "pydantic": {"src/a.py"}}
	d := f.doctor(t)
	w := wantFinding(t, d, "engines.imports_unresolved", contract.DoctorWarning)
	if !strings.Contains(w.Message, "fastapi, pydantic") || !strings.Contains(w.Message, "could not import") || !strings.Contains(w.Fix, "a2dlc install") {
		t.Fatalf("finding: %+v", w)
	}
	if !d.OK {
		t.Fatal("a warning must not make doctor blocking")
	}
}

func TestDoctorImportsOkAndProbeFailure(t *testing.T) {
	f := pyRepo(t)
	wantFinding(t, f.doctor(t), "engines.imports_ok", contract.DoctorInfo)
	f.probe.importsErr = errors.New("boom")
	wantFinding(t, f.doctor(t), "engines.imports_not_checked", contract.DoctorWarning)
	f.probe.importsErr, f.probe.dockerErr = nil, errors.New("down")
	if d := f.doctor(t); finding(d, "engines.imports_ok") != nil || finding(d, "engines.imports_unresolved") != nil {
		t.Fatalf("the imports were asked with Docker down: %+v", d.Findings)
	}
}

func TestCouldNotImportIsCountedApartFromOtherInconclusive(t *testing.T) {
	target := func(reason string) contract.VerdictTarget {
		return contract.VerdictTarget{TargetResult: contract.TargetResult{Verdict: contract.INCONCLUSIVE, Reason: reason}}
	}
	v := contract.Verdict{Targets: []contract.VerdictTarget{
		target("could not import: ModuleNotFoundError: No module named 'fastapi'"),
		target("could not import: ModuleNotFoundError: No module named 'pydantic.fields'"),
		target("could not import: ModuleNotFoundError: No module named 'fastapi'"),
		target("no type annotations on the parameters: CrossHair has no domain"),
		{TargetResult: contract.TargetResult{Verdict: contract.PASS}},
	}}
	n, mods := CouldNotImport(v)
	if n != 3 || strings.Join(mods, ",") != "fastapi,pydantic" {
		t.Fatalf("n=%d mods=%v", n, mods)
	}
	note := CouldNotImportNote(v)
	if !strings.Contains(note, "3 target(s) could not import") || !strings.Contains(note, "fastapi, pydantic") || !strings.Contains(note, "not code the engine cannot judge") {
		t.Fatalf("note: %s", note)
	}
	if CouldNotImportNote(contract.Verdict{Targets: v.Targets[3:]}) != "" {
		t.Fatal("a note without any could-not-import target")
	}
}
