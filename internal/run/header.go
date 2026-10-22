package run

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// @a2dlc headers (§6.3): comments co-located with each source file that the slice creates or modifies. The core knows
// nothing about languages: a line counts with any comment token of up to three characters (#, //, --, …). Whether a
// header is missing is decided by the Tier 0 lint of the engine; the core only checks that, if the header declares one,
// it belongs to this run.

// reHeader matches a line «<comment> @a2dlc:<key> = <value>». The value is decoded separately.
var reHeader = regexp.MustCompile(`^\s*\S{1,3}\s*@a2dlc:([a-z_]+)\s*=\s*(.*)$`)

// reBareMode accepts mode = greenfield without quotes, as in the protocol's example.
var reBareMode = regexp.MustCompile(`^[a-z_]+$`)

// headerList are the keys whose value is a JSON array of strings; the others are a JSON string.
var headerList = map[string]bool{"protected_invariants": true, "rejected_alternatives": true, "do_not_refactor": true}

// headerError is an @a2dlc line that could not be read: its key and the reason, with the line number.
type headerError struct{ clave, reason string }

// readHeaders extracts the @a2dlc lines from a file. vals stores each key as a string or []string; a repeated key or a
// value that does not decode goes to errs and does not enter vals.
func readHeaders(text string) (map[string]any, []headerError) {
	vals := map[string]any{}
	var errs []headerError
	for i, line := range strings.Split(text, "\n") {
		m := reHeader.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		clave := m[1]
		if _, ok := vals[clave]; ok {
			errs = append(errs, headerError{clave, fmt.Sprintf("line %d: repeated key", i+1)})
			continue
		}
		v, err := headerValue(clave, strings.TrimSpace(m[2]))
		if err != nil {
			errs = append(errs, headerError{clave, fmt.Sprintf("line %d: %v", i+1, err)})
			continue
		}
		vals[clave] = v
	}
	return vals, errs
}

// headerValue decodes a line's value: a JSON array of strings for the lists, a JSON string for the rest, and mode also
// accepts the bare word.
func headerValue(clave, raw string) (any, error) {
	if headerList[clave] {
		if !strings.HasPrefix(raw, "[") {
			return nil, errors.New("expected a JSON array of strings")
		}
		var l []string
		if err := json.Unmarshal([]byte(raw), &l); err != nil {
			return nil, fmt.Errorf("invalid JSON array: %v", err)
		}
		return l, nil
	}
	if strings.HasPrefix(raw, `"`) {
		var s string
		if err := json.Unmarshal([]byte(raw), &s); err != nil {
			return nil, fmt.Errorf("invalid JSON string: %v", err)
		}
		return s, nil
	}
	if clave == "mode" && reBareMode.MatchString(raw) {
		return raw, nil
	}
	return nil, errors.New("expected a JSON string in quotes")
}

// requestHeader builds request.header with what the run has: slice and mode from the state, invariants and rejected
// alternatives from the intent, the sha256 of contract.json (nil without S*, lite mode) and do_not_refactor from
// intent.boundary (the same symbols that CB-04 watches in boundary.go).
func (m *Machine) requestHeader(intent, contractJSON json.RawMessage) (contract.BuilderHeader, error) {
	var it contract.Intent
	if len(intent) > 0 && string(intent) != "null" {
		if err := json.Unmarshal(intent, &it); err != nil {
			return contract.BuilderHeader{}, fmt.Errorf("intent.json does not decode for the @a2dlc header: %w", err)
		}
	}
	var doNotRefactor []string
	if it.Boundary != nil {
		doNotRefactor = it.Boundary.DoNotRefactor
	}
	c := contract.BuilderHeader{SliceID: m.state.SliceID, Mode: m.state.Mode,
		ProtectedInvariants: withoutNil(it.Invariants), RejectedAlternatives: withoutNil(it.RejectedAlternatives), DoNotRefactor: withoutNil(doNotRefactor)}
	if len(contractJSON) > 0 && string(contractJSON) != "null" {
		h, err := HashFile(filepath.Join(m.dir, ArtContract))
		if err != nil {
			return contract.BuilderHeader{}, err
		}
		c.ContractHash = &h
	}
	return c, nil
}

// withoutNil gives [] instead of nil so that the JSON says [] and not null.
func withoutNil(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

// headerReview is what Stage 3 reports about the headers of the changed files: mismatch are the files that declare
// another run (they fail the stage), faltan (missing) are those with no slice_id line (they do not fail), notes the rest.
type headerReview struct {
	mismatch []string
	faltan   []string
	notes    []string
}

// reviewHeaders reads each changed file of a builder's /work. A file with slice_id, mode or contract_hash different
// from the run's was written against another contract.json. Deleted files and binaries have no header to check.
func reviewHeaders(work string, files []string, request contract.BuilderHeader) (headerReview, error) {
	var r headerReview
	for _, rel := range files {
		b, err := os.ReadFile(filepath.Join(work, filepath.FromSlash(rel)))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return r, err
		}
		if bytes.IndexByte(b, 0) >= 0 {
			continue
		}
		vals, errs := readHeaders(string(b))
		if _, ok := vals["slice_id"]; !ok && !hasError(errs, "slice_id") {
			r.faltan = append(r.faltan, rel)
		}
		var diffs []string
		if s, ok := vals["slice_id"].(string); ok && s != request.SliceID {
			diffs = append(diffs, fmt.Sprintf("slice_id %q (the run has %q)", s, request.SliceID))
		}
		if s, ok := vals["mode"].(string); ok && s != request.Mode {
			diffs = append(diffs, fmt.Sprintf("mode %q (the run has %q)", s, request.Mode))
		}
		if s, ok := vals["contract_hash"].(string); ok && (request.ContractHash == nil || s != *request.ContractHash) {
			expected := "none (lite mode)"
			if request.ContractHash != nil {
				expected = fmt.Sprintf("%q", *request.ContractHash)
			}
			diffs = append(diffs, fmt.Sprintf("contract_hash %q (the run has %s)", s, expected))
		}
		if len(diffs) > 0 {
			r.mismatch = append(r.mismatch, fmt.Sprintf("@a2dlc header of %s does not match: %s", rel, strings.Join(diffs, ", ")))
		}
		if len(errs) > 0 {
			var reasons []string
			for _, e := range errs {
				reasons = append(reasons, "@a2dlc:"+e.clave+" "+e.reason)
			}
			r.notes = append(r.notes, "malformed @a2dlc header in "+rel+": "+strings.Join(reasons, "; "))
		}
	}
	if len(r.faltan) > 0 {
		r.notes = append(r.notes, "@a2dlc header missing in "+strings.Join(r.faltan, ", ")+" (the engine's Tier 0 lint decides whether it is an error)")
	}
	return r, nil
}

// hasError reports whether a line with the given key could not be read: that key is present, though malformed.
func hasError(errs []headerError, clave string) bool {
	for _, e := range errs {
		if e.clave == clave {
			return true
		}
	}
	return false
}
