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
run)
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
