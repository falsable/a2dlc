package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// warmFake keeps the "volume" in a folder. It lays the delta over a copy of it the way the container script does, and runs the
// fake test command over the result, so what the runs judge is the volume plus the delta and nothing else.
type warmFake struct {
	*fakeTests
	vol   string
	mu    sync.Mutex
	syncs []string // "full" or "delta N" per sync
	seen  []map[string]string
	broke bool // the volume claims a tree it does not hold
}

func (w *warmFake) Warm() bool { return true }

func (w *warmFake) WarmTree(context.Context, string, string) (string, error) {
	b, err := os.ReadFile(filepath.Join(w.vol, ".a2dlc-tree"))
	if err != nil {
		return "", nil
	}
	return string(b), nil
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil || p == from {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(to, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(to, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func applyDelta(t *testing.T, root, dir string, deleted []string) {
	t.Helper()
	for _, d := range deleted {
		os.RemoveAll(filepath.Join(root, filepath.FromSlash(d)))
	}
	copyTree(t, dir, root)
}

func (w *warmFake) SyncWarm(_ context.Context, _, _, tree string, dir string, deleted []string, full bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	os.Remove(filepath.Join(w.vol, ".a2dlc-tree"))
	if full {
		os.RemoveAll(w.vol)
		os.MkdirAll(w.vol, 0o755)
		w.syncs = append(w.syncs, "full")
	} else {
		w.syncs = append(w.syncs, "delta")
	}
	applyDelta(&testing.T{}, w.vol, dir, deleted)
	return os.WriteFile(filepath.Join(w.vol, ".a2dlc-tree"), []byte(tree), 0o644)
}

func listing(t *testing.T, root string) map[string]string {
	t.Helper()
	m := map[string]string{}
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, p)
			b, _ := os.ReadFile(p)
			m[filepath.ToSlash(rel)] = string(b)
		}
		return nil
	})
	return m
}

func (w *warmFake) RunTestsWarm(ctx context.Context, tc contract.TestsConfig, command string, env map[string]string, _, dir string, deleted []string) (engine.TestRun, error) {
	work, err := os.MkdirTemp("", "a2dlc-warmwork-*")
	if err != nil {
		return engine.TestRun{}, err
	}
	defer os.RemoveAll(work)
	copyTree(&testing.T{}, w.vol, work)
	os.Remove(filepath.Join(work, ".a2dlc-tree"))
	applyDelta(&testing.T{}, work, dir, deleted)
	w.mu.Lock()
	w.seen = append(w.seen, listing(&testing.T{}, work))
	w.mu.Unlock()
	return w.fakeTests.RunTests(ctx, tc, command, env, work)
}

func warmRepo(t *testing.T) (App, *warmFake, string) {
	t.Helper()
	t.Setenv(warmEnv, "1")
	a, fe, ft := staticRepo(t)
	dir := a.Repo.Dir
	a.Runner = &lister{fakeEngine: fe, listed: map[string][]contract.ListedTest{
		"tests/a.t": {{Name: "old"}, {Name: "previous"}}, "tests/b.t": {{Name: "oldb"}}}}
	wf := &warmFake{fakeTests: ft, vol: t.TempDir()}
	a.Tests = wf
	return a, wf, dir
}

// The tree a warm run judges is the tree a run over a full export judges, for the candidate and for the base with the stubs,
// and the verdict is the same.
func TestWarmTreeJudgesTheSameTreeAsAFullExport(t *testing.T) {
	a, wf, dir := warmRepo(t)
	warm := judged(t, a)
	if len(wf.syncs) != 1 || wf.syncs[0] != "full" {
		t.Fatalf("the first check pays the full copy: %v", wf.syncs)
	}
	if len(wf.seen) < 2 {
		t.Fatalf("runs over the warm tree: %d", len(wf.seen))
	}
	base := listing(t, wf.vol)
	delete(base, ".a2dlc-tree")
	cand := map[string]string{}
	baseWithStubs := map[string]string{}
	for _, tree := range wf.seen {
		switch {
		case strings.Contains(tree["src/lib/f.ts"], "IMPL added"):
			cand = tree
		case strings.Contains(tree["src/lib/f.ts"], "STUB"):
			baseWithStubs = tree
		}
	}
	if cand["tests/a.t"] != "old uses:old\nprevious failed\ntest_added uses:added\n" || cand["tests/b.t"] != "oldb uses:old\nnewb uses:added\n" || cand["README.md"] != base["README.md"] {
		t.Fatalf("candidate tree is not the working tree: %q", cand)
	}
	if !strings.Contains(baseWithStubs["src/lib/f.ts"], "STUB") || baseWithStubs["README.md"] != base["README.md"] {
		t.Fatalf("base with stubs: %q", baseWithStubs["src/lib/f.ts"])
	}
	// the same verdict as without the warm tree
	a.Tests = wf.fakeTests
	if plain := judged(t, a); !reflect.DeepEqual(warm, plain) {
		t.Fatalf("warm and cold judged differently:\n%+v\n%+v", warm, plain)
	}
	_ = dir
}

// A later check on a base the volume does not hold pays for the files that differ only; a volume with no marker is rebuilt.
func TestWarmTreeSyncsByDiffAndRebuildsWhenItClaimsNothing(t *testing.T) {
	a, wf, dir := warmRepo(t)
	git(t, dir, "stash", "-q", "-u") // back to the base commit, with the working changes aside
	git(t, dir, "stash", "drop", "-q")
	judged(t, a)
	write(t, dir, "src/lib/g.ts", "export const g = 1\nIMPL g\n")
	write(t, dir, "README.md", "second\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "second base")
	write(t, dir, "src/lib/g.ts", "export const g = 1\nIMPL g\nIMPL added\n")
	write(t, dir, "tests/a.t", "old uses:old\nprevious failed\nnew uses:added\n")
	judged(t, a)
	if got := strings.Join(wf.syncs, ","); got != "full,delta" {
		t.Fatalf("syncs: %s", got)
	}
	vol := listing(t, wf.vol)
	if vol["README.md"] != "second\n" || vol["src/lib/g.ts"] == "" {
		t.Fatalf("the volume was not brought to the new base: %v", sortedKeysOf(vol))
	}
	os.Remove(filepath.Join(wf.vol, ".a2dlc-tree"))
	write(t, dir, "tests/a.t", "old uses:old\nprevious failed\nnewer uses:added\n")
	judged(t, a)
	if got := strings.Join(wf.syncs, ","); got != "full,delta,full" {
		t.Fatalf("a volume without a marker must be rebuilt: %s", got)
	}
}

func sortedKeysOf(m map[string]string) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// Without A2DLC_WARM_TREE=1 the volume is never touched and the runs export the whole tree.
func TestWarmTreeIsOffByDefault(t *testing.T) {
	a, wf, _ := warmRepo(t)
	t.Setenv(warmEnv, "")
	judged(t, a)
	if len(wf.syncs) != 0 || len(wf.seen) != 0 {
		t.Fatalf("warm tree used while off: syncs %v, warm runs %d", wf.syncs, len(wf.seen))
	}
}
