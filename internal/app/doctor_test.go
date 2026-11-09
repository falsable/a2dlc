package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// fakeProbe stands in for the host: no test of doctor runs Docker or another a2dlc binary.
type fakeProbe struct {
	dockerErr  error
	images     map[string]bool
	versions   map[string]string
	asked      []string
	unresolved map[string][]string
	importsErr error
}

func (f *fakeProbe) DockerReachable(context.Context) error {
	f.asked = append(f.asked, "docker")
	return f.dockerErr
}
func (f *fakeProbe) ImagePresent(_ context.Context, ref string) (bool, error) {
	f.asked = append(f.asked, ref)
	return f.images[ref], nil
}
func (f *fakeProbe) BinaryVersion(_ context.Context, path string) (string, error) {
	if v, ok := f.versions[path]; ok {
		return v, nil
	}
	return "", errors.New("cannot run " + path)
}

func (f *fakeProbe) UnresolvedImports(_ context.Context, image, _ string, files []string) (map[string][]string, error) {
	f.asked = append(f.asked, "imports:"+image)
	return f.unresolved, f.importsErr
}

type doctorFix struct {
	a     App
	dir   string
	exe   string // this binary: the one the hook names too, until a test says otherwise
	probe *fakeProbe
}

// newDoctorFix makes a repo with the given files, runs install over it with a binary that exists on disk, and keeps the
// host out of the signers lookup (env and home) so the cases do not depend on the machine.
func newDoctorFix(t *testing.T, files map[string]string) *doctorFix {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", "")
	t.Setenv("A2DLC_ALLOWED_SIGNERS_DATA", "")
	dir := t.TempDir()
	git(t, dir, "init", "-q")
	for rel, s := range files {
		write(t, dir, rel, s)
	}
	repo, err := gitx.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(t.TempDir(), "a2dlc-test")
	if err := os.WriteFile(exe, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	probe := &fakeProbe{images: map[string]bool{enginePyImage: true, engineTSImage: true}, versions: map[string]string{}}
	a := App{Repo: repo, Version: "1.2.3", Now: func() time.Time { return time.Unix(0, 0) }, Probe: probe}
	if _, err := a.Install(exe, nil); err != nil {
		t.Fatal(err)
	}
	return &doctorFix{a: a, dir: dir, exe: exe, probe: probe}
}

func pyRepo(t *testing.T) *doctorFix {
	return newDoctorFix(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "src/a.py": "x = 1\n"})
}

func (f *doctorFix) doctor(t *testing.T) contract.Doctor {
	t.Helper()
	d, err := f.a.Doctor(context.Background(), f.exe)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func (f *doctorFix) setConfig(t *testing.T, raw string) {
	t.Helper()
	write(t, f.dir, ".a2dlc/config.json", raw)
}

func finding(d contract.Doctor, id string) *contract.DoctorFinding {
	for i := range d.Findings {
		if d.Findings[i].ID == id {
			return &d.Findings[i]
		}
	}
	return nil
}

func wantFinding(t *testing.T, d contract.Doctor, id, severity string) contract.DoctorFinding {
	t.Helper()
	f := finding(d, id)
	if f == nil {
		t.Fatalf("no finding %s in %+v", id, d.Findings)
	}
	if f.Severity != severity {
		t.Fatalf("%s is %s, want %s: %+v", id, f.Severity, severity, *f)
	}
	if severity != contract.DoctorInfo && f.Fix == "" {
		t.Fatalf("%s has no fix hint", id)
	}
	return *f
}

func TestDoctorHealthyRepo(t *testing.T) {
	f := pyRepo(t)
	before, _ := f.a.Repo.Visible()
	d := f.doctor(t)
	for _, id := range []string{"config.ok", "hook.ok", "engines.ok", "docker.ok", "images.ok", "signers.not_configured"} {
		wantFinding(t, d, id, contract.DoctorInfo)
	}
	for _, fd := range d.Findings {
		if fd.Severity != contract.DoctorInfo {
			t.Errorf("healthy repo, finding %+v", fd)
		}
	}
	if !d.OK || d.Blocking() || d.Binary.Path != filepath.ToSlash(f.exe) || d.Binary.Version != "1.2.3" {
		t.Errorf("ok=%v binary=%+v", d.OK, d.Binary)
	}
	// the report is the contract: it decodes strictly into its own type and writes nothing into the repo
	b, _ := json.Marshal(d)
	if err := contract.DecodeStrictBytes(b, &contract.Doctor{}); err != nil {
		t.Errorf("report does not decode strictly: %v", err)
	}
	if after, _ := f.a.Repo.Visible(); !reflect.DeepEqual(before, after) {
		t.Errorf("doctor changed the repo: %v -> %v", before, after)
	}
}

func TestDoctorConfig(t *testing.T) {
	f := pyRepo(t)
	os.Remove(filepath.Join(f.dir, ".a2dlc/config.json"))
	d := f.doctor(t)
	wantFinding(t, d, "config.missing", contract.DoctorBlocking)
	if d.OK {
		t.Error("ok with a blocking finding")
	}

	f = pyRepo(t)
	f.setConfig(t, `{"schema_version": 1, "engines": [`)
	wantFinding(t, f.doctor(t), "config.invalid", contract.DoctorBlocking)

	f = pyRepo(t)
	f.setConfig(t, `{"schema_version": 2, "engines": []}`)
	sk := wantFinding(t, f.doctor(t), "config.schema_skew", contract.DoctorBlocking)
	if !strings.Contains(sk.Message, "2") || !strings.Contains(sk.Message, "1.2.3") {
		t.Errorf("skew message names neither the config's schema nor the binary: %s", sk.Message)
	}
}

func TestDoctorHook(t *testing.T) {
	f := pyRepo(t)
	dir, _ := f.a.Repo.HooksDir()
	os.Remove(filepath.Join(dir, "pre-push"))
	wantFinding(t, f.doctor(t), "hook.not_installed", contract.DoctorBlocking)

	// the hook names a binary that is gone
	f = pyRepo(t)
	os.Remove(f.exe)
	other := filepath.Join(t.TempDir(), "this-binary")
	os.WriteFile(other, []byte("x"), 0o755)
	f.exe = other
	gone := wantFinding(t, f.doctor(t), "hook.binary_missing", contract.DoctorBlocking)
	if gone.Subject == "" {
		t.Error("binary_missing does not name the hook's path")
	}

	// the hook names another build that exists: warning with both paths and versions
	f = pyRepo(t)
	hookExe := f.exe
	f.exe = filepath.Join(t.TempDir(), "this-binary")
	os.WriteFile(f.exe, []byte("x"), 0o755)
	f.probe.versions[hookExe] = "0.9.0"
	d := f.doctor(t)
	mm := wantFinding(t, d, "hook.binary_mismatch", contract.DoctorWarning)
	for _, want := range []string{filepath.ToSlash(hookExe), filepath.ToSlash(f.exe), "0.9.0", "1.2.3"} {
		if !strings.Contains(mm.Message, want) {
			t.Errorf("mismatch message lacks %q: %s", want, mm.Message)
		}
	}
	if finding(d, "hook.ok") != nil {
		t.Error("hook.ok next to a mismatch")
	}
}

func TestDoctorEngines(t *testing.T) {
	// engines: [] while the tree has a language with an engine: the check would PASS with 0 targets (a2dlc-fjl)
	f := pyRepo(t)
	f.setConfig(t, `{"schema_version": 1, "engines": []}`)
	none := wantFinding(t, f.doctor(t), "engines.none_declared", contract.DoctorBlocking)
	if none.Subject != "python" {
		t.Errorf("subject %q", none.Subject)
	}

	// one language declared, another detected and left out
	f = newDoctorFix(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "src/a.py": "x = 1\n", "package.json": "{}\n", "src/b.ts": "export const b = 1\n"})
	f.setConfig(t, `{"schema_version": 1, "engines": [{"language":"python","image":"`+enginePyImage+`","paths":["src/**/*.py"],"budget":{"per_condition_timeout_s":10}}]}`)
	ts := wantFinding(t, f.doctor(t), "engines.language_not_declared", contract.DoctorWarning)
	if ts.Subject != "typescript" {
		t.Errorf("subject %q", ts.Subject)
	}

	// source no engine can examine (Go has none): warning, once per language
	f = newDoctorFix(t, map[string]string{"pyproject.toml": "[project]\nname = \"x\"\n", "src/a.py": "x = 1\n", "cmd/main.go": "package main\n", "cmd/b.go": "package main\n"})
	un := wantFinding(t, f.doctor(t), "engines.sources_uncovered", contract.DoctorWarning)
	if un.Subject != "Go" || !strings.Contains(un.Message, "2 Go files") {
		t.Errorf("uncovered finding: %+v", un)
	}
}

func TestDoctorDocker(t *testing.T) {
	f := pyRepo(t)
	f.probe.dockerErr = errors.New("cannot connect to the Docker daemon")
	d := f.doctor(t)
	dk := wantFinding(t, d, "docker.unreachable", contract.DoctorBlocking)
	if !strings.Contains(dk.Message, "cannot connect") {
		t.Errorf("message hides the cause: %s", dk.Message)
	}
	wantFinding(t, d, "images.not_checked", contract.DoctorInfo)
	if finding(d, "images.missing") != nil {
		t.Error("image findings with the daemon down")
	}

	// nothing configured runs in a container: Docker is not asked
	f = newDoctorFix(t, map[string]string{"cmd/main.go": "package main\n"})
	d = f.doctor(t)
	wantFinding(t, d, "docker.not_needed", contract.DoctorInfo)
	if len(f.probe.asked) != 0 {
		t.Errorf("asked the host: %v", f.probe.asked)
	}

	// no probe injected: said, not a pass
	f = pyRepo(t)
	f.a.Probe = nil
	wantFinding(t, f.doctor(t), "docker.not_checked", contract.DoctorInfo)
}

func TestDoctorSuggestsBuildingThePHPEngineImage(t *testing.T) {
	f := newDoctorFix(t, map[string]string{"composer.json": "{}\n", "src/A.php": "<?php\n"})
	f.probe.images[enginePHPImage] = false
	miss := wantFinding(t, f.doctor(t), "images.missing", contract.DoctorBlocking)
	if miss.Subject != enginePHPImage || !strings.Contains(miss.Fix, "engines/php") {
		t.Errorf("missing finding: %+v", miss)
	}
}

func TestDoctorImages(t *testing.T) {
	f := pyRepo(t)
	f.probe.images[enginePyImage] = false
	miss := wantFinding(t, f.doctor(t), "images.missing", contract.DoctorBlocking)
	if miss.Subject != enginePyImage || !strings.Contains(miss.Fix, "engines/py") {
		t.Errorf("missing finding: %+v", miss)
	}

	// an image of a registry is pulled on first use: a warning
	f = pyRepo(t)
	ref := "ghcr.io/falsable/a2dlc-engine-py@sha256:" + strings.Repeat("a", 64)
	f.setConfig(t, `{"schema_version": 1, "engines": [{"language":"python","image":"`+ref+`","paths":["src/**/*.py"],"budget":{"per_condition_timeout_s":10}}]}`)
	pull := wantFinding(t, f.doctor(t), "images.will_pull", contract.DoctorWarning)
	if pull.Subject != ref {
		t.Errorf("subject %q", pull.Subject)
	}

	// the tests image and the checks' images are looked at too, each once
	f = pyRepo(t)
	b, _ := os.ReadFile("../../contracts/examples/valid/config__tests-py.json")
	cfg := strings.Replace(string(b), "a2dlc-engine-py:0.1", enginePyImage, 1)
	cfg = strings.TrimSuffix(strings.TrimSpace(cfg), "}") + `, "checks": [{"id": "ruff", "image": "a2dlc-check-ruff:0.1", "command": "ruff", "severity": "advisory"}]}`
	f.setConfig(t, cfg)
	subjects := map[string]bool{}
	for _, fd := range f.doctor(t).Findings {
		if fd.ID == "images.missing" {
			subjects[fd.Subject] = true
		}
	}
	if !subjects["a2dlc-tests-py:0.1"] || !subjects["a2dlc-check-ruff:0.1"] || len(subjects) != 2 {
		t.Errorf("missing images reported: %v", subjects)
	}
}

func TestDoctorSigners(t *testing.T) {
	approvals, _ := os.ReadFile("../../contracts/examples/valid/approvals__signed.json")

	// signed documents in use and no allowed signers: nobody can approve, so a waiver can never be applied
	f := pyRepo(t)
	write(t, f.dir, ".a2dlc/approvals.json", string(approvals))
	wantFinding(t, f.doctor(t), "signers.missing", contract.DoctorBlocking)

	// only the config declares code that needs signatures: a risk, not yet a stop
	f = pyRepo(t)
	f.setConfig(t, `{"schema_version": 1, "engines": [], "protected_paths": ["migrations/**"]}`)
	wantFinding(t, f.doctor(t), "signers.missing", contract.DoctorWarning)

	// a signers file with a key
	f = pyRepo(t)
	write(t, f.dir, ".a2dlc/approvals.json", string(approvals))
	signers := filepath.Join(t.TempDir(), "allowed_signers")
	os.WriteFile(signers, []byte("alice@example.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExample\n"), 0o644)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	wantFinding(t, f.doctor(t), "signers.ok", contract.DoctorInfo)

	// the variable points at nothing
	t.Setenv("A2DLC_ALLOWED_SIGNERS", filepath.Join(t.TempDir(), "nope"))
	wantFinding(t, f.doctor(t), "signers.file_missing", contract.DoctorBlocking)

	// a file with no key
	empty := filepath.Join(t.TempDir(), "empty")
	os.WriteFile(empty, []byte("# nobody yet\n"), 0o644)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", empty)
	wantFinding(t, f.doctor(t), "signers.empty", contract.DoctorBlocking)
}
