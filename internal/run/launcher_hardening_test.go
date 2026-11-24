package run

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// A role container runs with no capabilities, no privilege escalation and a process cap (a2dlc-zden).
func TestLauncherHardensRoleContainers(t *testing.T) {
	bin, recorded := dockerFake(t)
	r := contract.Role{Family: "x", Image: "a2dlc-roles:0.1", Argv: []string{"python3", "/a2dlc/adapter.py"}}
	if _, err := launch(t, bin, r, LaunchSpec{}); err != nil {
		t.Fatal(err)
	}
	args := readRecordedArgs(t, recorded)
	for _, pair := range [][2]string{
		{"--cap-drop", "ALL"},
		{"--security-opt", "no-new-privileges"},
		{"--pids-limit", "256"},
	} {
		i := slices.Index(args, pair[0])
		if i < 0 || i+1 >= len(args) || args[i+1] != pair[1] {
			t.Fatalf("missing %s %s in %v", pair[0], pair[1], args)
		}
	}
}

func TestRolesDockerfileUsesNonRootUser(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "roles", "Dockerfile"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "USER a2dlc") {
		t.Fatalf("roles/Dockerfile must run as non-root USER a2dlc:\n%s", b)
	}
}
