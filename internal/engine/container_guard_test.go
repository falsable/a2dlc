// @a2dlc:slice_id = "beads-a2dlc-gkn.0269c1"
// @a2dlc:mode = "lite"
// @a2dlc:rationale = "Tests drive container.run against a fake docker to show a flag-like image is refused and a timed-out container is removed."
// @a2dlc:rejected_alternatives = []
// @a2dlc:protected_invariants = []
// @a2dlc:do_not_refactor = []

package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

func hangingDocker(t *testing.T) (bin, log string) {
	t.Helper()
	dir := t.TempDir()
	log = filepath.Join(dir, "log")
	bin = filepath.Join(dir, "docker")
	script := "#!/bin/sh\necho \"$@\" >> \"" + log + "\"\n" +
		"case \"$1\" in\ncreate) echo ctr1; exit 0;;\ncp) cat >/dev/null; exit 0;;\nrun|start) exec sleep 30;;\nesac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, log
}

func TestEngineRejectsAnImageThatIsAFlag(t *testing.T) {
	for _, copyIn := range []bool{false, true} {
		bin, log := hangingDocker(t)
		err := container{bin: bin, copyIn: copyIn, image: "--privileged", cmd: []string{"x"}}.run(context.Background(), &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "starts with -") {
			t.Fatalf("copyIn=%v: err %v", copyIn, err)
		}
		if b, _ := os.ReadFile(log); len(b) != 0 {
			t.Fatalf("copyIn=%v: docker ran: %s", copyIn, b)
		}
	}
}

func TestEngineRemovesItsContainerOnTimeout(t *testing.T) {
	for _, copyIn := range []bool{false, true} {
		bin, log := hangingDocker(t)
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		start := time.Now()
		err := container{bin: bin, copyIn: copyIn, image: "img:1", trees: []dirAt{{t.TempDir(), "/w"}}}.run(ctx, &strings.Builder{})
		cancel()
		if err == nil || time.Since(start) > 10*time.Second {
			t.Fatalf("copyIn=%v: err %v after %v", copyIn, err, time.Since(start))
		}
		b, _ := os.ReadFile(log)
		if !strings.Contains(string(b), "rm -f ") {
			t.Fatalf("copyIn=%v: no rm -f in log %s", copyIn, b)
		}
		if !copyIn && !strings.Contains(string(b), "--name a2dlc-") {
			t.Fatalf("no --name in log %s", b)
		}
	}
}

func TestChecksRejectsInvalidImageAndRemovesContainerOnTimeout(t *testing.T) {
	for _, img := range []string{"--volume=/:/host", "", "bad image"} {
		bin, log := hangingDocker(t)
		_, err := DockerChecks{Bin: bin}.RunCheck(context.Background(), contract.CheckConfig{ID: "c1", Image: img, Command: "true"}, []string{"a.go"}, t.TempDir(), t.TempDir())
		if err == nil {
			t.Fatalf("image %q: expected error", img)
		}
		if b, _ := os.ReadFile(log); len(b) != 0 {
			t.Fatalf("image %q: docker ran: %s", img, b)
		}
	}

	bin, log := hangingDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := DockerChecks{Bin: bin}.RunCheck(ctx, contract.CheckConfig{ID: "c1", Image: "chk:1", Command: "true"}, []string{"a.go"}, t.TempDir(), t.TempDir())
	if !errors.Is(err, ErrCheckTimeout) {
		t.Fatalf("expected ErrCheckTimeout, got %v", err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "--name a2dlc-") || !strings.Contains(string(b), "rm -f a2dlc-") {
		t.Fatalf("timed-out check container not named/removed: %s", b)
	}
}

func TestZ3RejectsInvalidImageAndRemovesContainerOnTimeout(t *testing.T) {
	for _, img := range []string{"--privileged", "", "bad image"} {
		bin, log := hangingDocker(t)
		_, err := Z3{Bin: bin, Image: img}.Solve(context.Background(), contract.Z3Request{SchemaVersion: 1})
		if err == nil {
			t.Fatalf("image %q: expected error", img)
		}
		if b, _ := os.ReadFile(log); len(b) != 0 {
			t.Fatalf("image %q: docker ran: %s", img, b)
		}
	}

	bin, log := hangingDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := Z3{Bin: bin, Image: "z3:1", Timeout: time.Second}.Solve(ctx, contract.Z3Request{SchemaVersion: 1})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "--name a2dlc-") || !strings.Contains(string(b), "rm -f a2dlc-") {
		t.Fatalf("timed-out z3 container not named/removed: %s", b)
	}
}
