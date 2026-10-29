package factory

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// VerifySeal checks a sealed holdout folder the way e2e/factory/check_holdout.sh does for its integrity part: SEAL holds
// the sha256 of MANIFEST.sha256, the manifest lists the sha256 of every other file, and no file is missing, changed or
// unlisted. It returns the seal. The canary and CR checks of the script are not repeated: they say how the folder was
// written, not whether it was edited since. A change is a new holdout with a new seal, never a patched one.
func VerifySeal(dir string) (string, error) {
	manifest, err := os.ReadFile(filepath.Join(dir, "MANIFEST.sha256"))
	if err != nil {
		return "", fmt.Errorf("holdout not sealed: %v", err)
	}
	sealBytes, err := os.ReadFile(filepath.Join(dir, "SEAL"))
	if err != nil {
		return "", fmt.Errorf("holdout not sealed: %v", err)
	}
	seal := strings.Fields(string(sealBytes))
	if len(seal) == 0 {
		return "", fmt.Errorf("holdout not sealed: SEAL is empty")
	}
	if sum := sha256.Sum256(manifest); hex.EncodeToString(sum[:]) != seal[0] {
		return "", fmt.Errorf("holdout changed: MANIFEST.sha256 does not match SEAL")
	}
	listed := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(manifest)))
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if line == "" {
			continue
		}
		// "<sha256> *<path>", as sha256sum writes it in binary mode
		sum, path, ok := strings.Cut(line, " *")
		if !ok || len(sum) != 64 {
			return "", fmt.Errorf("holdout changed: manifest line %q", line)
		}
		listed[path] = sum
	}
	for path, want := range listed {
		got, err := fileSHA256(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil {
			return "", fmt.Errorf("holdout changed: %s: %v", path, err)
		}
		if got != want {
			return "", fmt.Errorf("holdout changed: %s differs from the manifest", path)
		}
	}
	var extra []string
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if _, ok := listed[rel]; !ok && rel != "SEAL" && rel != "MANIFEST.sha256" {
			extra = append(extra, rel)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return "", fmt.Errorf("holdout changed: files that are not in the manifest: %s", strings.Join(extra, ", "))
	}
	return seal[0], nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
