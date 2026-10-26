package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// TestLister is implemented by runners that can ask an engine for the test functions of base test files read from their
// source (operation: list_tests). It is not part of Runner: a runner without it, or an engine without the operation, makes
// the NOP gate run the base to learn which tests already existed, which is what it did before the listing.
type TestLister interface {
	// ListTests lists the files of req.ChangedFiles, which exist under dir (a folder holding only those base files).
	ListTests(ctx context.Context, e contract.EngineConfig, req contract.Request, dir string) (contract.ListTestsResponse, error)
}

// ErrWithoutListTests marks an engine that does not do list_tests (an older image answers something else).
var ErrWithoutListTests = errors.New("the engine does not do list_tests")

func (d Docker) ListTests(ctx context.Context, e contract.EngineConfig, req contract.Request, dir string) (contract.ListTestsResponse, error) {
	var resp contract.ListTestsResponse
	req.Operation = "list_tests"
	// dir holds only the test files, so copying it in twice (base and candidate are the same here) costs nothing.
	io, output, errRun := d.engine(ctx, e, req, dir, dir)
	if io == "" {
		return resp, errRun
	}
	defer removeIO(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("%w (%s: response outside engine-list-tests-response: %v)", ErrWithoutListTests, e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not list tests: %s", *resp.Error)
	}
	return resp, ValidateListTests(req, resp)
}

// ValidateListTests checks that the engine answered about the files that were asked, once each, and that a file it could
// not list lists nothing and says why.
func ValidateListTests(req contract.Request, resp contract.ListTestsResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion {
		return fmt.Errorf("list_tests outside contract: schema_version %d", resp.SchemaVersion)
	}
	asked := map[string]bool{}
	for _, f := range req.ChangedFiles {
		asked[f] = true
	}
	seen := map[string]bool{}
	for _, f := range resp.Files {
		switch {
		case !asked[f.File]:
			return fmt.Errorf("list_tests of %q, which was not requested", f.File)
		case seen[f.File]:
			return fmt.Errorf("list_tests of %q twice", f.File)
		case !f.Listable && (f.Reason == "" || len(f.Tests) > 0):
			return fmt.Errorf("list_tests of %q: a file that is not listable gives a reason and no tests", f.File)
		}
		seen[f.File] = true
	}
	return nil
}
