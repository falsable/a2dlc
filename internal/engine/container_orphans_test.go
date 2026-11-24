package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOrphansInPicksOnlyContainersCreatedMoreThanAnHourAgo(t *testing.T) {
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	ps := "old1 2026-11-25 10:49:00 +0000 UTC\n" +
		"new1 2026-11-25 11:30:00 +0000 UTC\n" +
		"old2 2026-11-24 15:51:00 +0100 CET\n" +
		"bad1 yesterday\n"
	got := strings.Join(orphansIn(ps, now), ",")
	if got != "old1,old2" {
		t.Fatalf("orphans %q, want old1,old2", got)
	}
	if ids := orphansIn("", now); len(ids) != 0 {
		t.Fatalf("empty ps gave %v", ids)
	}
}

// A copy-in run labels its container and, first, removes the labelled ones an earlier a2dlc left in Created.
func TestCopyInLabelsItsContainerAndSweepsOldOrphansOnce(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "log")
	bin := filepath.Join(dir, "docker")
	old := time.Now().Add(-2 * time.Hour).UTC().Format("2006-01-02 15:04:05 -0700 MST")
	recent := time.Now().Add(-time.Minute).UTC().Format("2006-01-02 15:04:05 -0700 MST")
	script := "#!/bin/sh\necho \"$@\" >> \"" + log + "\"\n" +
		"case \"$1\" in\n" +
		"ps) printf 'orphan1 " + old + "\nfresh1 " + recent + "\n' ;;\n" +
		"create) echo cid1 ;;\n" +
		"cp) cat > /dev/null ;;\n" +
		"esac\nexit 0\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	c := container{bin: bin, copyIn: true, image: "img:1", trees: []dirAt{{t.TempDir(), "/w"}}}
	for i := 0; i < 2; i++ {
		if err := c.run(context.Background(), &strings.Builder{}); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	b, _ := os.ReadFile(log)
	got := string(b)
	if n := strings.Count(got, "ps -a --filter label="+copyInLabel); n != 1 {
		t.Fatalf("%d sweeps, want 1:\n%s", n, got)
	}
	if !strings.Contains(got, "rm -f orphan1") || strings.Contains(got, "rm -f fresh1") {
		t.Fatalf("sweep removed the wrong containers:\n%s", got)
	}
	if !strings.Contains(got, "create --rm --label "+copyInLabel+" img:1") {
		t.Fatalf("create without the label:\n%s", got)
	}
}
