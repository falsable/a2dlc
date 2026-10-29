package factory

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/verdict"
)

// vetoPrefix starts the reason of a builder response the factory turned into a refusal.
const vetoPrefix = "a2dlc-factory: "

// testGuard watches a lane's builder for edits to tests that already exist on the base. The test files are the ones
// the repo declares in config.tests.paths, so the factory knows no language. Judging an old test's body takes the
// language (internal/app/gates.go judgeNOP leaves it out) and the gate can be satisfied by weakening the test, so the
// check is on the files themselves: a base test file that is modified or missing in the builder's /work rejects the
// lane. Adding test files is free.
type testGuard struct {
	repo    gitx.Repo
	base    string
	globs   []string // config.tests.paths at the base; empty when the repo declares no test files
	files   []string // base files that match the globs
	mu      sync.Mutex
	touched []contract.TestTouch
}

// newTestGuard reads the globs from the config committed at base. A repo without config.tests has no declared test
// files, and the guard then has nothing to protect.
func newTestGuard(repo gitx.Repo, base string) (*testGuard, error) {
	g := &testGuard{repo: repo, base: base}
	b, err := repo.Show(base, ".a2dlc/config.json")
	if errors.Is(err, os.ErrNotExist) {
		return g, nil
	}
	if err != nil {
		return nil, err
	}
	var cfg struct {
		Tests *struct {
			Paths []string `json:"paths"`
		} `json:"tests"`
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		return nil, fmt.Errorf(".a2dlc/config.json at %s: %w", base, err)
	}
	if cfg.Tests == nil || len(cfg.Tests.Paths) == 0 {
		return g, nil
	}
	g.globs = cfg.Tests.Paths
	all, err := repo.Files(base)
	if err != nil {
		return nil, err
	}
	for _, f := range all {
		for _, glob := range cfg.Tests.Paths {
			if verdict.Match(glob, f) {
				g.files = append(g.files, f)
				break
			}
		}
	}
	return g, nil
}

// check compares the builder's /work with the base and records what it found; the first call that finds something
// fixes the list.
func (g *testGuard) check(workDir string) []contract.TestTouch {
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.touched) > 0 {
		return g.touched
	}
	for _, f := range g.files {
		want, err := g.repo.Show(g.base, f)
		if err != nil {
			continue
		}
		got, err := os.ReadFile(filepath.Join(workDir, filepath.FromSlash(f)))
		switch {
		case errors.Is(err, os.ErrNotExist):
			g.touched = append(g.touched, contract.TestTouch{Path: f, Change: contract.TestDeleted})
		case err != nil || !bytes.Equal(got, want):
			g.touched = append(g.touched, contract.TestTouch{Path: f, Change: contract.TestModified})
		}
	}
	return g.touched
}

func (g *testGuard) result() []contract.TestTouch {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]contract.TestTouch(nil), g.touched...)
}

func touchReason(t []contract.TestTouch) string {
	parts := make([]string, len(t))
	for i, x := range t {
		parts[i] = x.Path + " (" + x.Change + ")"
	}
	return fmt.Sprintf("modified or deleted %d base test files: %s", len(t), strings.Join(parts, ", "))
}

// veto rewrites the builder's response.json as a refusal with the factory's reason and keeps its cost: Stage 3
// escalates on a refusal before Tier 0 and the oracle run, and the call still reaches the lane's ledger with what it
// spent. The refusal says it is the factory's, not the model's.
func veto(outDir string, t []contract.TestTouch) error {
	p := filepath.Join(outDir, "response.json")
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	var resp contract.RoleResponse
	if err := contract.DecodeStrictBytes(b, &resp); err != nil {
		return err
	}
	resp.Status, resp.Reason, resp.Payload = contract.RoleRefused, vetoPrefix+touchReason(t), nil
	out, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	return os.WriteFile(p, out, 0o644)
}
