package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// ErrWithoutLemmas marks an engine that does not do the lemmas operation (it answers something else): the lemmas of the
// slice stay unchecked and are listed as such; an unchecked lemma is never a PASS.
var ErrWithoutLemmas = errors.New("the engine does not do lemmas")

// LemmasRunner is implemented by runners that check the declared lemmas of Critical slices (operation: lemmas, Stage 2).
// It is optional on purpose: a runner without it leaves the lemmas unchecked.
type LemmasRunner interface {
	Lemmas(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.LemmasResponse, error)
}

// Lemmas runs the lemmas operation and reads engine-lemmas-response. An answer outside the contract wraps
// ErrWithoutLemmas; an engine that reports an error is an error of its own.
func (d Docker) Lemmas(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.LemmasResponse, error) {
	var resp contract.LemmasResponse
	req.Operation = "lemmas"
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
		return resp, fmt.Errorf("%w (%s: response outside engine-lemmas-response: %v)", ErrWithoutLemmas, e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not check the lemmas: %s", *resp.Error)
	}
	return resp, ValidateLemmas(resp)
}

// lemmaStatus are the statuses of engine-lemmas-response.schema.json.
var lemmaStatus = map[string]bool{"pass": true, "fail": true, "inconclusive": true, "no_pair": true}

// ValidateLemmas applies the schema rules that matter to the core: the schema version, a name and a known status per
// lemma, and a counterexample for every fail (a failure without its input cannot be fed back to the builder).
func ValidateLemmas(resp contract.LemmasResponse) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Lemmas == nil {
		return fmt.Errorf("lemmas outside contract: schema_version %d and lemmas required", resp.SchemaVersion)
	}
	for _, l := range resp.Lemmas {
		switch {
		case l.Name == "":
			return fmt.Errorf("lemmas outside contract: a lemma without name")
		case !lemmaStatus[l.Status]:
			return fmt.Errorf("lemmas outside contract: lemma %q with status %q", l.Name, l.Status)
		case l.Status == "fail" && l.Counterexample == nil:
			return fmt.Errorf("lemmas outside contract: lemma %q failed without counterexample", l.Name)
		}
	}
	return nil
}
