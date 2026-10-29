package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// ErrWithoutTier0 marks an engine that does not do the tier0 operation (it answers something else): what it was to
// check stays unreviewed in Tier 0 and is listed; it is not taken as a Builder failure.
var ErrWithoutTier0 = errors.New("the engine does not do tier0")

func (d Docker) Tier0(ctx context.Context, e contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Tier0Response, error) {
	var resp contract.Tier0Response
	req.Operation = "tier0"
	io, output, errRun := d.engine(ctx, e, req, baseDir, candDir)
	if io == "" {
		return resp, errRun
	}
	defer os.RemoveAll(io)
	raw, err := os.ReadFile(filepath.Join(io, "response.json"))
	if err != nil {
		return resp, fmt.Errorf("engine %s did not write /io/response.json (%v): %s", e.Image, errRun, tail(output))
	}
	if err := contract.DecodeStrictBytes(raw, &resp); err != nil {
		return resp, fmt.Errorf("%w (%s: response outside engine-tier0-response: %v)", ErrWithoutTier0, e.Image, err)
	}
	if resp.Error != nil {
		return resp, fmt.Errorf("the engine did not do tier0: %s", *resp.Error)
	}
	return resp, ValidateTier0(req, resp)
}

// kindTier0Known are the kinds of engine-tier0-response.schema.json.
func kindTier0Known(k string) bool {
	switch k {
	case contract.Tier0Compile, contract.Tier0Typecheck, contract.Tier0Lint, contract.Tier0Probe,
		contract.Tier0Policy, contract.Tier0Header, contract.Tier0Tool, contract.Tier0Lemma, contract.Tier0Test, contract.Tier0Coverage:
		return true
	}
	return false
}

// ValidateTier0 applies to a tier0 response the schema rules that matter to the core: ok if and only if there are no
// findings, known kinds, and each finding is about a requested file (a changed one or one from a probe).
func ValidateTier0(req contract.Request, resp contract.Tier0Response) error {
	if resp.SchemaVersion != contract.SchemaVersion || resp.Findings == nil {
		return fmt.Errorf("tier0 outside contract: schema_version %d and findings required", resp.SchemaVersion)
	}
	if resp.OK != (len(resp.Findings) == 0) {
		return fmt.Errorf("tier0 outside contract: ok=%v with %d findings", resp.OK, len(resp.Findings))
	}
	request := map[string]bool{}
	for _, f := range req.ChangedFiles {
		request[f] = true
	}
	var b contract.Tier0Budget
	if len(req.Budget) > 0 {
		if err := json.Unmarshal(req.Budget, &b); err != nil {
			return err
		}
	}
	for _, p := range b.Probes {
		if p.File != nil {
			request[*p.File] = true
		}
	}
	for _, f := range resp.Findings {
		switch {
		case !kindTier0Known(f.Kind):
			return fmt.Errorf("tier0 outside contract: kind %q", f.Kind)
		case !request[f.File]:
			return fmt.Errorf("tier0: finding about %q, which was not requested", f.File)
		case f.Message == "" || f.Line != nil && *f.Line < 1:
			return fmt.Errorf("tier0 outside contract: finding %+v", f)
		}
	}
	return nil
}
