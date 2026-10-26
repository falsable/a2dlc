package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// fakeDocker writes a shell stand-in for the docker binary. The Go tests run in the Linux container, so a shell script
// is enough. Every invocation appends its arguments to log; "image inspect" prints inspect (or fails with inspectErr);
// "run" copies response (if set) to the /io folder it is given and records whether /io/golden.json was present.
// The copy-in path: "create" keeps its arguments and prints the id ctr1, "cp" extracts its stdin into <dir>/ctr (or
// fails if <dir>/cp-fails exists), and "start" does what "run" does with the arguments "create" kept.
func fakeDocker(t *testing.T, inspect, inspectErr, response string) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	bin = filepath.Join(dir, "docker")
	script := `#!/bin/sh
echo "$@" >> "` + log + `"
case "$1" in
image)
  if [ -n "` + inspectErr + `" ]; then echo "` + inspectErr + `" >&2; exit 1; fi
  printf '%s' '` + inspect + `'
  exit 0;;
create)
  printf '%s\n' "$@" > "` + dir + `/create.args"
  echo ctr1
  exit 0;;
cp)
  if [ -f "` + dir + `/cp-fails" ]; then echo "cp refused" >&2; exit 1; fi
  mkdir -p "` + dir + `/ctr" && tar -x -C "` + dir + `/ctr"
  exit $?;;
run|start)
  if [ "$1" = start ]; then set -- $(cat "` + dir + `/create.args"); fi
  for a in "$@"; do case "$a" in *:/io) io="${a%:/io}";; esac; done
  if [ -f "$io/golden.json" ]; then echo golden-present >> "` + log + `"; fi
  if [ -n "` + response + `" ]; then cp "` + response + `" "$io/response.json"; fi
  exit 0;;
esac
exit 0
`
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestImageDigestReadsTheRepoDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("ab", 32)
	bin, _ := fakeDocker(t, `["registry.local/tests@`+digest+`"]`, "", "")
	got, err := DockerTests{Bin: bin}.ImageDigest(context.Background(), "tests:1")
	if err != nil || got != digest {
		t.Fatalf("got %q, %v", got, err)
	}
}

// A local build that was never pushed has no RepoDigests: the answer is empty, not an error (Stage −1 turns it into CB-05).
func TestImageDigestEmptyForLocalImage(t *testing.T) {
	bin, _ := fakeDocker(t, `[]`, "", "")
	got, err := DockerTests{Bin: bin}.ImageDigest(context.Background(), "tests:local")
	if err != nil || got != "" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestImageDigestMissingImageIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, "", "Error: No such image: tests:absent", "")
	if _, err := (DockerTests{Bin: bin}).ImageDigest(context.Background(), "tests:absent"); err == nil || !strings.Contains(err.Error(), "No such image") {
		t.Fatalf("err %v", err)
	}
}

// On a Linux host the test container runs as the host user, so the files it leaves in the tree are not root's.
func TestRunTestsRunsAsHostUserOnNonWindows(t *testing.T) {
	bin, log := fakeDocker(t, "[]", "", "")
	if _, err := (DockerTests{Bin: bin}).RunTests(context.Background(), contract.TestsConfig{Image: "tests:1"}, "true", nil, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	got := strings.Contains(string(b), "--user")
	if runtime.GOOS == "windows" {
		if got {
			t.Fatalf("windows host must not pass --user: %s", b)
		}
		return
	}
	if !got {
		t.Fatalf("no --user in %s", b)
	}
}

// With CopyIn the tree goes into the container through docker cp and is not mounted; the container is removed after.
func TestRunTestsCopiesTheTreeIn(t *testing.T) {
	bin, log := fakeDocker(t, "[]", "", "")
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "pkg", "a.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := (DockerTests{Bin: bin, CopyIn: true}).RunTests(context.Background(), contract.TestsConfig{Image: "tests:1"}, "true", nil, src); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(filepath.Dir(log), "ctr", "work", "pkg", "a.py")); err != nil || string(b) != "x = 1\n" {
		t.Fatalf("tree not copied into /work: %q, %v", b, err)
	}
	b, _ := os.ReadFile(log)
	for _, bad := range []string{"run --rm", ":/work", "--user"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("%q in %s", bad, b)
		}
	}
	if !strings.Contains(string(b), "start -a ctr1") || !strings.Contains(string(b), "rm -f ctr1") {
		t.Fatalf("no start or rm of the container: %s", b)
	}
}

func TestEngineCopiesBaseAndCandidateIn(t *testing.T) {
	resp := filepath.Join(t.TempDir(), "response.json")
	if err := os.WriteFile(resp, []byte(`{"schema_version":1,"cases":[],"diffs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, log := fakeDocker(t, "[]", "", resp)
	base, cand := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(base, "f.go"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cand, "f.go"), []byte("cand"), 0o644); err != nil {
		t.Fatal(err)
	}
	req := contract.Request{SchemaVersion: contract.SchemaVersion, Operation: "golden_record", Budget: []byte(`{"cases":["c/*"]}`)}
	if _, err := (Docker{Bin: bin, CopyIn: true}).Golden(context.Background(), contract.EngineConfig{Image: "bb:1"}, req, base, cand, nil); err != nil {
		t.Fatal(err)
	}
	for at, want := range map[string]string{"base": "base", "cand": "cand"} {
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(log), "ctr", at, "f.go")); err != nil || string(b) != want {
			t.Fatalf("/%s/f.go: %q, %v", at, b, err)
		}
	}
	if b, _ := os.ReadFile(log); strings.Contains(string(b), ":/cand") {
		t.Fatalf("candidate mounted: %s", b)
	}
}

// A copy that fails is an error, not the exit code of the tests, and the container it created is still removed.
func TestRunTestsCopyFailureIsAnErrorAndRemovesTheContainer(t *testing.T) {
	bin, log := fakeDocker(t, "[]", "", "")
	if err := os.WriteFile(filepath.Join(filepath.Dir(log), "cp-fails"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := (DockerTests{Bin: bin, CopyIn: true}).RunTests(context.Background(), contract.TestsConfig{Image: "tests:1"}, "true", nil, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "cp refused") {
		t.Fatalf("err %v", err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "start") || !strings.Contains(string(b), "rm -f ctr1") {
		t.Fatalf("log %s", b)
	}
}

func TestGoldenRecordReadsTheResponse(t *testing.T) {
	hash := "sha256:" + strings.Repeat("0", 64)
	resp := filepath.Join(t.TempDir(), "response.json")
	body := `{"schema_version":1,"cases":[{"id":"c1","input_hash":"` + hash + `","output_hash":"` + hash + `","exit_code":0}],"diffs":[]}`
	if err := os.WriteFile(resp, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, _ := fakeDocker(t, "[]", "", resp)
	req := contract.Request{SchemaVersion: contract.SchemaVersion, Operation: "golden_record", Budget: []byte(`{"cases":["cases/*.json"]}`)}
	got, err := Docker{Bin: bin}.Golden(context.Background(), contract.EngineConfig{Language: "blackbox", Image: "bb:1"}, req, t.TempDir(), t.TempDir(), nil)
	if err != nil || len(got.Cases) != 1 || got.Cases[0].ID != "c1" {
		t.Fatalf("%+v %v", got, err)
	}
}

// golden_replay writes the recorded golden.json into /io before the engine runs, and an engine that does not answer
// the golden contract is an error, not an empty replay.
func TestGoldenReplayWritesTheRecord(t *testing.T) {
	resp := filepath.Join(t.TempDir(), "response.json")
	if err := os.WriteFile(resp, []byte(`{"schema_version":1,"cases":[],"diffs":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	bin, log := fakeDocker(t, "[]", "", resp)
	req := contract.Request{SchemaVersion: contract.SchemaVersion, Operation: "golden_replay", Budget: []byte(`{"cases":["cases/*.json"]}`)}
	if _, err := (Docker{Bin: bin}).Golden(context.Background(), contract.EngineConfig{Image: "bb:1"}, req, t.TempDir(), t.TempDir(), []byte(`{"cases":[]}`)); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(log); !strings.Contains(string(b), "golden-present") {
		t.Fatalf("golden.json not in /io: %s", b)
	}
}

func TestGoldenWithoutAnswerIsAnError(t *testing.T) {
	bin, _ := fakeDocker(t, "[]", "", "")
	req := contract.Request{SchemaVersion: contract.SchemaVersion, Operation: "golden_record", Budget: []byte(`{"cases":["c/*"]}`)}
	if _, err := (Docker{Bin: bin}).Golden(context.Background(), contract.EngineConfig{Image: "bb:1"}, req, t.TempDir(), t.TempDir(), nil); err == nil {
		t.Fatal("an engine that wrote no response.json must be an error")
	}
}
