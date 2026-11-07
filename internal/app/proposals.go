package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

const pathProposals = pathReports + "/proposals.jsonl"

// proposedLine is one line of .a2dlc/reports/proposals.jsonl: a proposal that a2dlc propose --from-escalations handed out.
// The record is what makes a second run propose nothing new. It lives in .a2dlc/reports, outside the tree_hash.
type proposedLine struct {
	SchemaVersion int    `json:"schema_version"`
	At            string `json:"at"`
	Hash          string `json:"hash"`
	Target        string `json:"target"`
}

// ProposalHash is the content hash of a counterexample: sha256 of target, args and old, each followed by a NUL byte, with
// args and old as the engine wrote them. It leaves out the escalation id (kind + target, so it would merge different
// counterexamples of one target), the kind and the new output (check and run name the same input with different kinds).
func ProposalHash(target, args, old string) string {
	h := sha256.Sum256([]byte(target + "\x00" + args + "\x00" + old + "\x00"))
	return "sha256:" + hex.EncodeToString(h[:])
}

// ProposeFromEscalations turns the counterexamples of earlier runs into properties for the next intent. It reads
// .a2dlc/escalations.json (the last check) and .a2dlc/run/*/escalations.json (each run's), keeps the behavior_divergence
// items that carry a counterexample with arguments and an old output, and proposes that the function keeps returning
// the old output for those arguments. It leaves out: any other kind (a surviving mutant carries a mutant, not a
// counterexample; a lemma has no base output to keep), a target that .a2dlc/waivers.json waives with enforce_new_behavior
// (the person said the change is intended), and a hash already in .a2dlc/reports/proposals.jsonl. It calls no role and
// writes nothing: RecordProposals appends the hashes once the caller has delivered the document. The findings record
// (.a2dlc/reports/findings.jsonl) has no counterexample, so it is not read.
func (a App) ProposeFromEscalations(now time.Time) (contract.Proposals, error) {
	doc := contract.Proposals{SchemaVersion: contract.SchemaVersion, CreatedAt: now.UTC().Format(time.RFC3339), Proposals: []contract.Proposal{}, Skipped: []contract.ProposalSkip{}}
	files := []string{a.path(pathEscalations)}
	runs, err := filepath.Glob(a.path(".a2dlc/run/*/escalations.json"))
	if err != nil {
		return doc, err
	}
	sort.Strings(runs)
	files = append(files, runs...)
	var items []contract.Escalation
	for _, f := range files {
		b, err := os.ReadFile(f)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return doc, err
		}
		var es contract.Escalations
		if err := contract.DecodeStrictBytes(b, &es); err != nil {
			rel, _ := filepath.Rel(a.Repo.Dir, f)
			return doc, fmt.Errorf("%s does not match the contract (contracts/escalation.schema.json): %w", filepath.ToSlash(rel), err)
		}
		items = append(items, es.Items...)
	}
	waived, err := a.enforcedTargets()
	if err != nil {
		return doc, err
	}
	seen, err := a.proposedHashes()
	if err != nil {
		return doc, err
	}
	skip := func(e contract.Escalation, why string) {
		doc.Skipped = append(doc.Skipped, contract.ProposalSkip{EscalationID: e.ID, Target: e.Target, Reason: why})
	}
	for _, e := range items {
		ce := e.Evidence.Counterexample
		switch {
		case e.Kind != "behavior_divergence":
			skip(e, "kind "+e.Kind+": only a behavior_divergence counterexample has the base's output to keep")
		case ce == nil || ce.Args == "" || ce.Old == "":
			skip(e, "no counterexample with arguments and the base's output")
		case waived[e.Target]:
			skip(e, "a waiver enforce_new_behavior covers the target: the change is intended")
		default:
			h := ProposalHash(e.Target, ce.Args, ce.Old)
			if seen[h] {
				skip(e, "already proposed or in an earlier proposal: "+h)
				continue
			}
			seen[h] = true // two runs that found the same counterexample give one proposal
			file, symbol, _ := strings.Cut(e.Target, "::")
			doc.Proposals = append(doc.Proposals, contract.Proposal{
				Hash: h, Target: e.Target, File: file, Source: contract.ProposalSource{EscalationID: e.ID, Kind: e.Kind},
				Property: fmt.Sprintf("For %s(%s) the function returns %s, as on the base; a candidate that returns %s there was found by an earlier run.",
					symbol, ce.Args, ce.Old, ce.New),
				Probe: contract.Probe{Description: symbol + " keeps the base's result for " + ce.Args,
					Input: ce.Args, Expected: ce.Old, Kind: "example"},
			})
		}
	}
	// probe ids follow the hash order, so the document does not depend on which file listed an escalation first
	sort.SliceStable(doc.Proposals, func(i, j int) bool { return doc.Proposals[i].Hash < doc.Proposals[j].Hash })
	for i := range doc.Proposals {
		doc.Proposals[i].Probe.ID = fmt.Sprintf("ESC-%d", i+1)
	}
	return doc, nil
}

// enforcedTargets are the targets that waivers.json waives with enforce_new_behavior, signed or not.
func (a App) enforcedTargets() (map[string]bool, error) {
	out := map[string]bool{}
	b, err := os.ReadFile(a.path(pathWaivers))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var w contract.Waivers
	if err := contract.DecodeStrictBytes(b, &w); err != nil {
		return nil, fmt.Errorf("%s does not match the contract: %w", pathWaivers, err)
	}
	for _, x := range w.Waivers {
		if x.Decision == contract.EnforceNewBehavior {
			out[x.Target] = true
		}
	}
	return out, nil
}

func (a App) proposedHashes() (map[string]bool, error) {
	out := map[string]bool{}
	f, err := os.Open(a.path(pathProposals))
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if s := strings.TrimSpace(sc.Text()); s != "" {
			var l proposedLine
			if err := contract.DecodeStrictBytes([]byte(s), &l); err != nil || l.Hash == "" {
				return nil, fmt.Errorf("%s line %d is not a proposal record", pathProposals, n)
			}
			out[l.Hash] = true
		}
	}
	return out, sc.Err()
}

// RecordProposals appends the hashes of what was proposed, in one write, to .a2dlc/reports/proposals.jsonl.
func (a App) RecordProposals(doc contract.Proposals) error {
	if len(doc.Proposals) == 0 {
		return nil
	}
	var b strings.Builder
	for _, p := range doc.Proposals {
		line, err := json.Marshal(proposedLine{SchemaVersion: contract.SchemaVersion, At: doc.CreatedAt, Hash: p.Hash, Target: p.Target})
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(a.path(pathProposals)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(a.path(pathProposals), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
