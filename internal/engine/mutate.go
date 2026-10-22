package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"

	"github.com/falsable/a2dlc/internal/contract"
)

// mutantID is the contract pattern: the id is the name of a folder in /io/mutants and cannot leave it.
var mutantID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// Mutate asks the engine for mutants of the changed lines (operation: mutate). req.Budget has to be a
// contract.MutateBudget: the core builds it, not the config.
func (d Docker) Mutate(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.MutateResponse, map[string][]byte, error) {
	var resp contract.MutateResponse
	req.Operation = "mutate"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, nil, errRun
	}
	defer os.RemoveAll(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, nil, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, nil, fmt.Errorf("engine %s does not mutate (response outside engine-mutate-response: %v)", e.Image, err)
	}
	if resp.Error != nil {
		return resp, nil, fmt.Errorf("the engine did not mutate: %s", *resp.Error)
	}
	files, err := MutantFiles(filepath.Join(io, "mutants"), req, resp)
	return resp, files, err
}

// MutantFiles reads an engine's mutants folder and enforces the contract: each id is safe as a folder and unique,
// each mutant is in a request file and in one of its changed lines, exactly that file in its folder, and nothing
// undeclared. A mutant outside the diff's lines does not test the suite against the change: the response is rejected.
func MutantFiles(root string, req contract.Request, resp contract.MutateResponse) (map[string][]byte, error) {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Mutants == nil || resp.NotGenerated == nil || resp.Pruned == nil {
		return nil, fmt.Errorf("mutate response incomplete (schema_version, mutants, not_generated and pruned are required)")
	}
	var b contract.MutateBudget
	if err := json.Unmarshal(req.Budget, &b); err != nil {
		return nil, fmt.Errorf("mutate budget: %v", err)
	}
	request := map[string]bool{}
	for _, f := range req.ChangedFiles {
		request[f] = true
	}
	for _, n := range resp.NotGenerated {
		if !request[n.File] {
			return nil, fmt.Errorf("not_generated of %q, which was not requested", n.File)
		}
	}
	for _, p := range resp.Pruned { // a pruned equivalent only counts on a requested changed line
		if !request[p.File] || !contains(b.ChangedLines[p.File], p.Line) {
			return nil, fmt.Errorf("pruned outside the requested changed lines: %s:%d", p.File, p.Line)
		}
	}
	declared := map[string]string{} // id → file
	for _, m := range resp.Mutants {
		switch {
		case !mutantID.MatchString(m.ID):
			return nil, fmt.Errorf("mutant id %q outside contract", m.ID)
		case declared[m.ID] != "":
			return nil, fmt.Errorf("mutant id %q repeated", m.ID)
		case !request[m.File]:
			return nil, fmt.Errorf("mutant %s of %q, which was not requested", m.ID, m.File)
		case !contains(b.ChangedLines[m.File], m.Line):
			return nil, fmt.Errorf("mutant %s at %s:%d, outside the changed lines", m.ID, m.File, m.Line)
		}
		declared[m.ID] = m.File
	}
	out := map[string][]byte{}
	err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
		if errors.Is(err, fs.ErrNotExist) && p == root {
			return nil
		}
		if err != nil || de.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		for id, f := range declared {
			if rel == id+"/"+f {
				b, err := os.ReadFile(p)
				out[id] = b
				return err
			}
		}
		return fmt.Errorf("file %q in /io/mutants not declared", rel)
	})
	if err != nil {
		return nil, err
	}
	for id, f := range declared {
		if _, ok := out[id]; !ok {
			return nil, fmt.Errorf("mutant %s declares %s and it is not in /io/mutants/%s/", id, f, id)
		}
	}
	return out, nil
}

func contains(lines []int, line int) bool {
	for _, x := range lines {
		if x == line {
			return true
		}
	}
	return false
}
