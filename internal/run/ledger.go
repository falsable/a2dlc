package run

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

// Isolation ledger (run-ledger.schema.json): one entry per launchRecord, chained by hash. The hash of an entry is
// sha256(prev_hash + JSON of the entry without hash, with its keys in schema order and without spaces); the first entry
// uses "" as prev_hash. verify-run recalculates it.
type Ledger struct {
	path string
	doc  contract.RunLedger
}

func openLedger(path, runID string) (*Ledger, error) {
	l := &Ledger{path: path, doc: contract.RunLedger{SchemaVersion: 1, RunID: runID}}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return l, nil
		}
		return nil, err
	}
	if err := contract.DecodeStrictBytes(b, &l.doc); err != nil {
		return nil, fmt.Errorf("%s does not match the contract: %w", path, err)
	}
	if l.doc.RunID != runID {
		return nil, fmt.Errorf("%s belongs to run %s, not %s", path, l.doc.RunID, runID)
	}
	if err := Verify(l.doc); err != nil {
		return nil, err
	}
	return l, nil
}

// Append fills in seq, prev_hash and hash of the entry and persists it.
func (l *Ledger) Append(e contract.LedgerEntry) (contract.LedgerEntry, error) {
	e.Seq = len(l.doc.Entries) + 1
	e.PrevHash = nil
	if n := len(l.doc.Entries); n > 0 {
		h := l.doc.Entries[n-1].Hash
		e.PrevHash = &h
	}
	if e.Inputs == nil {
		e.Inputs = []contract.HashedFile{}
	}
	if e.Outputs == nil {
		e.Outputs = []contract.HashedFile{}
	}
	if e.EnvKeys == nil {
		e.EnvKeys = []string{}
	}
	e.Hash = hashEntry(e)
	l.doc.Entries = append(l.doc.Entries, e)
	b, err := json.MarshalIndent(l.doc, "", " ")
	if err != nil {
		return e, err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o755); err != nil {
		return e, err
	}
	return e, os.WriteFile(l.path, append(b, '\n'), 0o644)
}

// AppendAuthorizations records the authorizations a check relied on (RFC-0007 Part B). A use already recorded for the same
// stage, target and signature is not recorded again: a Saboteur round checks the same tree several times. Each new use is
// recorded in authorizations_used and bound into the chain by an entry of role check, so a ledger cannot drop or edit it.
func (l *Ledger) AppendAuthorizations(uses []contract.LedgerAuthorization) error {
	for _, u := range uses {
		dup := false
		for _, old := range l.doc.AuthorizationsUsed {
			dup = dup || (old.Stage == u.Stage && old.Target == u.Target && old.Authorization.Signature == u.Authorization.Signature)
		}
		if dup {
			continue
		}
		l.doc.AuthorizationsUsed = append(l.doc.AuthorizationsUsed, u)
		if _, err := l.Append(authorizationEntry(u)); err != nil {
			return err
		}
	}
	return nil
}

// authorizationRef is what the chained entry commits to for one use: see LedgerAuthorizationRef.
func authorizationRef(u contract.LedgerAuthorization) contract.LedgerAuthorizationRef {
	au := u.Authorization
	b, _ := json.Marshal(au)
	return contract.LedgerAuthorizationRef{Target: u.Target, SHA256: hashBytes(b),
		ID: approval.AuthorizationID(approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets))}
}

// authorizationEntry is the entry that binds a use: it launches nothing, so it has no image, network or cost.
func authorizationEntry(u contract.LedgerAuthorization) contract.LedgerEntry {
	ref := authorizationRef(u)
	return contract.LedgerEntry{At: u.At, Stage: u.Stage, Role: RoleCheck, Attempt: 1, Family: "script", Image: "a2dlc-core",
		ArgvSHA256: hashBytes([]byte(RoleCheck)), Network: "none", Status: contract.LedgerOK, Authorization: &ref}
}

// RoleCheck is the role of the ledger entry that binds one use of an authorization.
const RoleCheck = "check"

// verifyAuthorizations checks that authorizations_used and the entries of role check say the same thing, one to one: a
// record whose entry was removed or edited, or an entry whose record was deleted, is a chain error.
func verifyAuthorizations(l contract.RunLedger) error {
	pool := append([]contract.LedgerAuthorization(nil), l.AuthorizationsUsed...)
	for _, e := range l.Entries {
		if e.Authorization == nil {
			if e.Role == RoleCheck {
				return fmt.Errorf("entry %d: a check entry binds no authorization", e.Seq)
			}
			continue
		}
		if e.Role != RoleCheck {
			return fmt.Errorf("entry %d: only an entry of role check binds an authorization", e.Seq)
		}
		found := -1
		for i, u := range pool {
			if u.Stage == e.Stage && u.At == e.At && authorizationRef(u) == *e.Authorization {
				found = i
				break
			}
		}
		if found < 0 {
			return fmt.Errorf("entry %d binds the authorization used for %s, but authorizations_used has no such record (deleted or edited)", e.Seq, e.Authorization.Target)
		}
		pool = append(pool[:found], pool[found+1:]...)
	}
	if len(pool) > 0 {
		return fmt.Errorf("authorizations_used records the use for %s (stage %s) that no chained entry binds (added or edited)", pool[0].Target, pool[0].Stage)
	}
	return nil
}

func hashEntry(e contract.LedgerEntry) string {
	e.Hash = ""
	b, _ := json.Marshal(e)
	h := sha256.New()
	if e.PrevHash != nil {
		h.Write([]byte(*e.PrevHash))
	}
	h.Write(b)
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Verify recalculates the chain: consecutive seq from 1, prev_hash equal to the previous hash and hash equal to the
// recalculated one, and that the entries of role check and authorizations_used match one to one.
func Verify(l contract.RunLedger) error {
	var prev *string
	for i, e := range l.Entries {
		if e.Seq != i+1 {
			return fmt.Errorf("entry %d: seq %d", i+1, e.Seq)
		}
		switch {
		case prev == nil && e.PrevHash != nil:
			return fmt.Errorf("entry 1: prev_hash must be null")
		case prev != nil && (e.PrevHash == nil || *e.PrevHash != *prev):
			return fmt.Errorf("entry %d: prev_hash is not the hash of entry %d", e.Seq, e.Seq-1)
		}
		if h := hashEntry(e); h != e.Hash {
			return fmt.Errorf("entry %d: hash %s does not match the recorded one (%s)", e.Seq, h[:19], e.Hash[:19])
		}
		h := e.Hash
		prev = &h
	}
	return verifyAuthorizations(l)
}

// HashFile is the sha256 of a file in the contractJSON format.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func hashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(s[:])
}

// hashFilesUnder lists all files under dir with their hash, with paths as the role sees them (prefix inside the container).
func hashFilesUnder(dir, prefix string) ([]contract.HashedFile, error) {
	var out []contract.HashedFile
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		h, err := HashFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		out = append(out, contract.HashedFile{Path: prefix + "/" + filepath.ToSlash(rel), SHA256: h})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	if out == nil {
		out = []contract.HashedFile{}
	}
	return out, err
}
