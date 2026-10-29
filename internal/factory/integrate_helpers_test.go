package factory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// sealedDir writes a small holdout of the kind VerifySeal checks and seals it. It never reads the real holdout.
func sealedDir(t *testing.T, files map[string]string) (dir, seal string) {
	t.Helper()
	dir = t.TempDir()
	var names []string
	for name, body := range files {
		write(t, dir, name, body)
		names = append(names, name)
	}
	sort.Strings(names)
	var manifest strings.Builder
	for _, n := range names {
		sum := sha256.Sum256([]byte(files[n]))
		fmt.Fprintf(&manifest, "%s *%s\n", hex.EncodeToString(sum[:]), n)
	}
	write(t, dir, "MANIFEST.sha256", manifest.String())
	s := sha256.Sum256([]byte(manifest.String()))
	seal = hex.EncodeToString(s[:])
	write(t, dir, "SEAL", seal+"  MANIFEST.sha256\n")
	return dir, seal
}

// fakeHoldout stands for the container: it records what it was asked to run and answers with the exit code its rule
// gives for the checkout.
type fakeHoldout struct {
	mu    sync.Mutex
	runs  [][]string
	trees []string
	exit  func(tree string, files []string) int
}

func (f *fakeHoldout) Run(_ context.Context, _ contract.FactoryHoldout, tree string, files []string) (int, string, error) {
	f.mu.Lock()
	f.runs = append(f.runs, append([]string(nil), files...))
	f.trees = append(f.trees, tree)
	f.mu.Unlock()
	code := 0
	if f.exit != nil {
		code = f.exit(tree, files)
	}
	return code, "HOLDOUT-CANARY output", nil
}

// failWhenPresent makes the holdout fail for a checkout that has the file.
func failWhenPresent(rel string) func(string, []string) int {
	return func(tree string, _ []string) int {
		if _, err := os.Stat(filepath.Join(tree, filepath.FromSlash(rel))); err == nil {
			return 1
		}
		return 0
	}
}

// candidate commits files on top of base and points a run ref at the commit, as a2dlc run leaves a builder's candidate:
// a commit whose parent is the lane's base. It returns the ref.
func candidate(t *testing.T, d Deps, base, issue string, files map[string]string) string {
	t.Helper()
	tmp := filepath.Join(t.TempDir(), "cand")
	git(t, d.Repo.Dir, "worktree", "add", "-q", "--detach", tmp, base)
	for rel, body := range files {
		write(t, tmp, rel, body)
	}
	git(t, tmp, "add", "-A")
	git(t, tmp, "commit", "-qm", "candidate "+issue)
	sha := git(t, tmp, "rev-parse", "HEAD")
	ref := "refs/a2dlc/run/" + issue + ".abcdef/builder_primary"
	git(t, d.Repo.Dir, "update-ref", ref, sha)
	git(t, d.Repo.Dir, "worktree", "remove", "--force", tmp)
	return ref
}

func acceptedLane(issue, ref string) contract.FactoryLane {
	pass := contract.PASS
	return contract.FactoryLane{IssueID: issue, Status: contract.LaneAccepted, RunOutcome: &pass, CandidateRef: &ref, Stages: []contract.StageUsage{}}
}

func reportOf(d Deps, lanes ...contract.FactoryLane) contract.FactoryReport {
	base := git2(d.Repo.Dir, "rev-parse", "HEAD")
	return contract.FactoryReport{SchemaVersion: 1, RunID: "run-0123456789ab", BatchID: "test.1", BaseSha: base, Outcome: contract.RunCompleted, Lanes: lanes}
}

func git2(dir string, args ...string) string {
	out, err := gitIn(dir, args...)
	if err != nil {
		panic(err)
	}
	return out
}

func holdoutSpec(dir, seal string, units ...string) contract.FactoryHoldout {
	h := contract.FactoryHoldout{SchemaVersion: 1, Dir: dir, Seal: seal, Image: "x", Command: "true"}
	for _, u := range units {
		h.Units = append(h.Units, contract.FactoryHoldoutUnit{IssueID: u, Files: []string{u + ".test"}})
	}
	return h
}
