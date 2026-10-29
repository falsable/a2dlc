package factory

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifySealAcceptsASealedFolderAndRefusesEveryChange(t *testing.T) {
	files := map[string]string{"tests/a.test.ts": "A", "tests/b.test.ts": "B", "CANARY": "c"}
	dir, seal := sealedDir(t, files)
	if got, err := VerifySeal(dir); err != nil || got != seal {
		t.Fatalf("%q %v", got, err)
	}
	cases := map[string]struct {
		edit func(dir string)
		want string
	}{
		"a listed file edited":  {func(d string) { write(t, d, "tests/a.test.ts", "A!") }, "tests/a.test.ts differs"},
		"a listed file deleted": {func(d string) { os.Remove(filepath.Join(d, "tests", "b.test.ts")) }, "tests/b.test.ts"},
		"a file added":          {func(d string) { write(t, d, "tests/c.test.ts", "C") }, "not in the manifest: tests/c.test.ts"},
		"the manifest edited":   {func(d string) { write(t, d, "MANIFEST.sha256", strings.Repeat("0", 64)+" *x\n") }, "does not match SEAL"},
		"the seal replaced":     {func(d string) { write(t, d, "SEAL", strings.Repeat("1", 64)+"\n") }, "does not match SEAL"},
		"the seal missing":      {func(d string) { os.Remove(filepath.Join(d, "SEAL")) }, "not sealed"},
	}
	for name, c := range cases {
		d, _ := sealedDir(t, files)
		c.edit(d)
		if _, err := VerifySeal(d); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

// The folder of a spec may not be inside the checkout the holdout is mounted next to: a lane's tree would carry it.
func TestMountHoldoutRefusesAFolderInsideTheCheckout(t *testing.T) {
	tree := t.TempDir()
	dir, seal := sealedDir(t, map[string]string{"tests/a.test.ts": "A"})
	inner := filepath.Join(tree, "holdout")
	if err := os.Rename(dir, inner); err != nil {
		t.Fatal(err)
	}
	f := &fakeHoldout{}
	_, _, err := mountHoldout(context.Background(), f, holdoutSpec(inner, seal, "i1"), tree, []string{"i1.test"})
	if err == nil || !strings.Contains(err.Error(), "inside the checkout") || len(f.runs) != 0 {
		t.Fatalf("err = %v, runs = %v", err, f.runs)
	}
	if !inside(inner, tree) || inside(dir, tree) {
		t.Fatal("inside() misjudges the folders")
	}
}
