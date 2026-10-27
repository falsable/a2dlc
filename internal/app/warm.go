package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/engine"
)

// Warm tree (bead a2dlc-uca; the volume itself is internal/engine/warm.go). On a host that copies trees into containers, a
// Docker volume per (repo, test image) holds the base tree. A check syncs it to the base by the files that differ, and each
// test run lays only the difference between the volume's tree and the tree it judges over a copy of the volume. The tree
// judged is still the git tree: the delta comes from git, never from the volume, and the receipt binds to the tree hash the
// check computed. Two checks of the same repo and image at the same time share the volume; the second one syncs it to its own
// base and the first one's runs would see a tree that is not its base, so concurrent checks of one repo are not supported.

// warmEnv turns the warm tree on. It is opt-in until the volume scripts have run against a real Docker host: the unit tests
// cover the delta and the sync logic with a fake volume, not the shell the container runs.
const warmEnv = "A2DLC_WARM_TREE"

// warmDeltaMax is how many files a sync or a run may lay before copying the whole tree is the better trade.
const warmDeltaMax = 500

type warmTree struct {
	volume string
	tree   string // git tree hash the volume holds
}

func warmVolume(repoDir, image string) string {
	abs, _ := filepath.Abs(repoDir)
	h := sha256.Sum256([]byte(abs + "\x00" + image))
	return "a2dlc-warm-" + hex.EncodeToString(h[:])[:16]
}

func plainPaths(paths []string) bool {
	for _, p := range paths {
		if strings.ContainsAny(p, "\n\r") {
			return false
		}
	}
	return true
}

// syncWarm brings the warm volume to the base tree and returns it, or nil when the runner has no warm trees or anything goes
// wrong: the runs then export the whole tree as they always did.
func (a App) syncWarm(ctx context.Context, in gateInput) *warmTree {
	if os.Getenv(warmEnv) != "1" {
		return nil
	}
	w, ok := a.Tests.(engine.WarmTests)
	if !ok || !w.Warm() || in.cfg.Image == "" {
		return nil
	}
	defer func(t0 time.Time) { timerOf(ctx).sync(time.Since(t0)) }(time.Now())
	want, err := a.Repo.CommitTree(in.baseSha)
	if err != nil {
		return nil
	}
	vol := warmVolume(a.Repo.Dir, in.cfg.Image)
	have, err := w.WarmTree(ctx, in.cfg.Image, vol)
	if err != nil {
		return nil
	}
	if have == want {
		return &warmTree{vol, want}
	}
	dir, err := os.MkdirTemp("", "a2dlc-warm-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	var deleted []string
	full := true
	if have != "" && a.Repo.HasTree(have) {
		if changed, del, err := a.Repo.Delta(have, want); err == nil && len(changed) <= warmDeltaMax && plainPaths(changed) && plainPaths(del) {
			if a.Repo.ExportPaths(want, dir, changed) == nil {
				full, deleted = false, del
			}
		}
	}
	if full {
		os.RemoveAll(dir)
		if os.MkdirAll(dir, 0o755) != nil || a.Repo.Export(want, dir) != nil {
			return nil
		}
	}
	if err := w.SyncWarm(ctx, in.cfg.Image, vol, want, dir, deleted, full); err != nil {
		fmt.Fprintf(os.Stderr, "a2dlc: warm tree not used: %v\n", err)
		return nil
	}
	return &warmTree{vol, want}
}

// warmDelta fills dir with the files of treeish that differ from the volume's tree and returns the paths to remove. false
// means the difference cannot be laid that way and the caller exports the whole tree.
func (a App) warmDelta(in gateInput, treeish, dir string) ([]string, bool) {
	changed, deleted, err := a.Repo.Delta(in.warm.tree, treeish)
	if err != nil || len(changed) > warmDeltaMax || !plainPaths(changed) || !plainPaths(deleted) {
		return nil, false
	}
	if a.Repo.ExportPaths(treeish, dir, changed) != nil {
		return nil, false
	}
	return deleted, true
}
