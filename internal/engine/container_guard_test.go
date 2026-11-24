// @a2dlc:slice_id = "beads-a2dlc-gkn.0269c1"
// @a2dlc:mode = "lite"
// @a2dlc:rationale = "Tests drive container.run against a fake docker to show a flag-like image is refused and a timed-out container is removed."
// @a2dlc:rejected_alternatives = []
// @a2dlc:protected_invariants = []
// @a2dlc:do_not_refactor = []

package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
