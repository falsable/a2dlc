package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// exitingDocker is a docker that runs nothing and ends with the given status and message, as a container the kernel killed.
func exitingDocker(t *testing.T, status int, stderr string) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(bin, fmt.Appendf(nil, "#!/bin/sh\necho '%s' >&2\nexit %d\n", stderr, status), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

// A container killed for memory exits 137 with an empty log. The error has to say that, not "did not write response.json (exit status 137): ".
func TestKilledEngineSaysItRanOutOfMemory(t *testing.T) {
	bin := exitingDocker(t, 137, "")
	_, err := Docker{Bin: bin}.Run(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("an engine that was killed and wrote nothing must be an error")
	}
	for _, want := range []string{"py:1", "exit 137", "memory", "engines[].memory"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error %q does not mention %q", err, want)
		}
	}
}

// Any other failure keeps its exit status and what the container printed.
func TestOtherEngineFailureKeepsItsOutput(t *testing.T) {
	bin := exitingDocker(t, 2, "boom from the engine")
	_, err := Docker{Bin: bin}.Run(context.Background(), contract.EngineConfig{Image: "py:1"}, contract.Request{SchemaVersion: contract.SchemaVersion}, t.TempDir(), t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "exit status 2") || !strings.Contains(err.Error(), "boom from the engine") || strings.Contains(err.Error(), "137") {
		t.Fatalf("err %v", err)
	}
}
