package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/junit"
)

// Static listing of the base's tests (bead a2dlc-uca). The NOP gate needs to know which of the candidate's testcases
// already existed on the base, and it used to learn it by running the base's test files. The engine can read the same
// answer off the base blob, so the base run leaves the critical path. The match is at function level, as the engine lists:
// the parametrize suffix of a JUnit name is stripped, so a new parameter case of an existing test counts as the existing
// test (the old run-based check called it new). A file the engine cannot list statically goes back to the base run, for that
// file only.

// listingVersion is part of the cache key of a listing: bump it when what an engine answers for the same blob changes.
const listingVersion = "list1"

// listings are the static listings of the base's test files that the engines could read, by file.
type listings map[string]map[contract.ListedTest]bool

// listBase asks the engines for the test functions of the base blobs of inBase. A file that comes back in the result is
// listed; every other one needs the base run. The listing of a blob is cached forever, keyed by blob and engine image: a
// base blob is immutable. Nothing here can fail the gate: any problem leaves the file to the base run.
func (a App) listBase(ctx context.Context, in gateInput, inBase []string) listings {
	out := listings{}
	lister, ok := a.Runner.(engine.TestLister)
	if !ok || len(in.engines) == 0 || len(inBase) == 0 {
		return out
	}
	blobs := map[string]string{}
	for _, f := range inBase {
		if b, err := a.Repo.Blob(in.baseSha, f); err == nil {
			blobs[f] = b
		}
	}
	for _, e := range in.engines {
		var misses []string
		for _, f := range inBase {
			if _, done := out[f]; done || blobs[f] == "" {
				continue
			}
			if lf, hit := a.readListing(e, blobs[f]); hit {
				if lf.Listable {
					out[f] = setOf(lf.Tests)
				}
				continue
			}
			misses = append(misses, f)
		}
		if len(misses) == 0 {
			continue
		}
		dir, err := os.MkdirTemp("", "a2dlc-list-*")
		if err != nil {
			continue
		}
		okDir := true
		for _, f := range misses {
			b, err := a.Repo.Show(in.baseSha, f)
			if err != nil {
				okDir = false
				break
			}
			p := filepath.Join(dir, filepath.FromSlash(f))
			if os.MkdirAll(filepath.Dir(p), 0o755) != nil || os.WriteFile(p, b, 0o644) != nil {
				okDir = false
				break
			}
		}
		if okDir {
			req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: in.baseSha,
				BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: misses, Budget: json.RawMessage("{}")}
			t0 := time.Now()
			resp, err := lister.ListTests(ctx, e, req, dir)
			timerOf(ctx).engineOp("list_tests", time.Since(t0))
			if err == nil {
				for _, lf := range resp.Files {
					a.saveListing(e, blobs[lf.File], lf)
					if lf.Listable {
						out[lf.File] = setOf(lf.Tests)
					}
				}
			}
		}
		if err := os.RemoveAll(dir); err != nil {
			os.Stderr.WriteString("a2dlc: cannot remove the listing folder " + dir + ": " + err.Error() + "\n")
		}
	}
	return out
}

func setOf(ts []contract.ListedTest) map[contract.ListedTest]bool {
	m := make(map[contract.ListedTest]bool, len(ts))
	for _, t := range ts {
		m[t] = true
	}
	return m
}

func (a App) listingPath(e contract.EngineConfig, blob string) string {
	dir := a.cacheDir()
	if dir == "" || blob == "" {
		return ""
	}
	h := sha256.Sum256([]byte(listingVersion + "\x00" + e.Language + "\x00" + e.Image + "\x00" + blob))
	return filepath.Join(dir, "listing-"+hex.EncodeToString(h[:])[:24]+".json")
}

func (a App) readListing(e contract.EngineConfig, blob string) (contract.ListedFile, bool) {
	var lf contract.ListedFile
	p := a.listingPath(e, blob)
	if p == "" {
		return lf, false
	}
	b, err := os.ReadFile(p)
	if err != nil || json.Unmarshal(b, &lf) != nil {
		return contract.ListedFile{}, false
	}
	return lf, true
}

func (a App) saveListing(e contract.EngineConfig, blob string, lf contract.ListedFile) {
	p := a.listingPath(e, blob)
	if p == "" || os.MkdirAll(filepath.Dir(p), 0o755) != nil {
		return
	}
	b, _ := json.Marshal(lf)
	os.WriteFile(p, b, 0o644)
}

// moduleOf is the dotted module name a runner puts in the classname of the testcases of file: "tests/test_a.py" gives
// "tests.test_a".
func moduleOf(file string) string {
	return strings.ReplaceAll(strings.TrimSuffix(file, ".py"), "/", ".")
}

// existing says which of the candidate's testcases already existed on the base, from the listings. A testcase belongs to a
// base file when its file attribute names it, or, when the runner does not give one, when its classname starts with the
// file's module. It existed when its class path and its name without the parametrize suffix are in the listing of that
// file. fallback are the base files whose testcases cannot be matched to the listing (the classname does not start with the
// file's module: a different rootdir, for instance); they go back to the base run. Testcases of files that are not in the
// base are new.
func existing(candidate map[string]junit.Case, inBase []string, ls listings) (own map[string]junit.Case, fallback map[string]bool) {
	own, fallback = map[string]junit.Case{}, map[string]bool{}
	for id, c := range candidate {
		file := ""
		for _, f := range inBase {
			if c.File != "" {
				if c.File == f {
					file = f
				}
				continue
			}
			if m := moduleOf(f); (c.Classname == m || strings.HasPrefix(c.Classname, m+".")) && len(m) > len(moduleOf(file)) {
				file = f
			}
		}
		set, listed := ls[file]
		if file == "" || !listed {
			continue
		}
		m := moduleOf(file)
		var class string
		switch {
		case c.Classname == m:
		case strings.HasPrefix(c.Classname, m+"."):
			class = strings.TrimPrefix(c.Classname, m+".")
		default:
			fallback[file] = true
			continue
		}
		name := c.Name
		if i := strings.Index(name, "["); i >= 0 {
			name = name[:i]
		}
		if set[contract.ListedTest{Class: class, Name: name}] {
			own[id] = c
		}
	}
	return own, fallback
}
