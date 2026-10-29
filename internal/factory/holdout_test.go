package factory

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// The Go verifier and e2e/factory/check_holdout.sh must agree on the factory's own sealed holdout. This hashes its files
// and compares the result with SEAL; it opens nothing else of it.
func TestVerifySealAgreesWithTheSealOfTheReposHoldout(t *testing.T) {
	dir := filepath.Join("..", "..", "e2e", "testdata", "factory-holdout")
	want, err := os.ReadFile(filepath.Join(dir, "SEAL"))
	if err != nil {
		t.Skip("the repo's holdout is not here:", err)
	}
	got, err := VerifySeal(dir)
	if err != nil || got != strings.Fields(string(want))[0] {
		t.Fatalf("%q %v", got, err)
	}
}

// DockerHoldout builds one run without network, with the checkout and the holdout read-only, the files in the
// environment, and gives back the command's exit code. A script stands for docker and records what it was called with.
func TestDockerHoldoutRunsWithoutNetworkAndReadOnlyMounts(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in for docker is a shell script")
	}
	bin := t.TempDir()
	argsFile := filepath.Join(bin, "args")
	script := "#!/bin/sh\nfor a in \"$@\"; do echo \"$a\"; done > " + argsFile + "\necho tests output\nexit 3\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := contract.FactoryHoldout{SchemaVersion: 1, Dir: t.TempDir(), Image: "img:1", Command: "run-the-tests"}
	code, log, err := DockerHoldout{Bin: filepath.Join(bin, "docker")}.Run(context.Background(), spec, t.TempDir(), []string{"h01.test.ts", "h02.test.ts"})
	if err != nil || code != 3 || !strings.Contains(log, "tests output") {
		t.Fatalf("%d %q %v", code, log, err)
	}
	b, _ := os.ReadFile(argsFile)
	args := strings.Split(strings.TrimSpace(string(b)), "\n")
	have := func(s string) bool {
		for _, a := range args {
			if strings.Contains(a, s) {
				return true
			}
		}
		return false
	}
	for _, s := range []string{"--network", "none", ":/src:ro", ":/holdout:ro", "HOLDOUT_FILES=h01.test.ts h02.test.ts", "img:1", "run-the-tests"} {
		if !have(s) {
			t.Errorf("%q is not in %v", s, args)
		}
	}
	if args[0] != "run" || args[1] != "--rm" {
		t.Errorf("%v", args)
	}
}
