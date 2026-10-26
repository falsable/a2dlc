package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/falsable/a2dlc/internal/contract"
)

// LineClassifier is implemented by runners that can ask an engine which unexamined changed lines are defensive
// exception handlers or declaration-only code (operation: classify_lines). It is not part of Runner: a runner without
// it, or an engine without the operation, leaves every line unexamined, which is what the gate did before the notes.
type LineClassifier interface {
	ClassifyLines(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.ClassifyResponse, error)
}

// ErrWithoutClassify marks an engine that does not do classify_lines (an older image answers something else): its lines
// stay unexamined and block as before.
var ErrWithoutClassify = errors.New("the engine does not do classify_lines")

func (d Docker) ClassifyLines(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.ClassifyResponse, error) {
	var resp contract.ClassifyResponse
	req.Operation = "classify_lines"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer removeIO(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("%w (%s: response outside engine-classify-lines-response: %v)", ErrWithoutClassify, e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not classify lines: %s", *resp.Error)
	}
	return resp, ValidateClassify(req, resp)
}

// ValidateClassify applies the rules of the schema that matter to the core: the files are requested ones, once each,
// and every noted line is one the request asked about, in at most one note. A response that breaks this is not partly
// trusted: the caller drops it whole.
func ValidateClassify(req contract.Request, resp contract.ClassifyResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Files == nil {
		return fmt.Errorf("classify_lines outside contract: schema_version %d and files required", resp.SchemaVersion)
	}
	var budget contract.ClassifyBudget
	if err := contract.DecodeStrictBytes(req.Budget, &budget); err != nil {
		return fmt.Errorf("classify_lines request outside contract: %v", err)
	}
	requested := map[string]bool{}
	for _, f := range req.ChangedFiles {
		requested[f] = true
	}
	seen := map[string]bool{}
	for _, f := range resp.Files {
		if !requested[f.File] {
			return fmt.Errorf("classify_lines of %q, which was not requested", f.File)
		}
		if seen[f.File] {
			return fmt.Errorf("classify_lines of %q twice", f.File)
		}
		seen[f.File] = true
		asked := map[int]bool{}
		for _, l := range budget.Lines[f.File] {
			asked[l] = true
		}
		taken := map[int]bool{}
		for _, n := range f.Notes {
			if !contract.IsNoteCategory(n.Category) || len(n.Lines) == 0 {
				return fmt.Errorf("classify_lines of %q outside contract: note %+v", f.File, n)
			}
			for _, l := range n.Lines {
				if !asked[l] || taken[l] {
					return fmt.Errorf("classify_lines of %q: line %d was not asked about or is in two notes", f.File, l)
				}
				taken[l] = true
			}
		}
	}
	return nil
}

// SortedNotes orders notes by category and their lines ascending, so that the receipt does not depend on the engine.
func SortedNotes(notes []contract.LineNote) []contract.LineNote {
	out := make([]contract.LineNote, 0, len(notes))
	for _, n := range notes {
		lines := append([]int{}, n.Lines...)
		sort.Ints(lines)
		out = append(out, contract.LineNote{Category: n.Category, Lines: lines})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Category < out[j].Category })
	return out
}
