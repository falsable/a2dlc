package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

// Batch approval (RFC-0007 Part A). `a2dlc approve --pending` shows a person every waiver of the current receipt that
// waits for a signature, grouped by the change it declares, and signs them all with one unlock of the key. What it signs
// is exactly what `a2dlc approve <target>` signs, one approval per target (§6.4.7): the batch changes how a person is
// asked, not what an approval means.
//
// The same report has two readers. Text is for the person who signs: plain sentences, the old and the new result, and
// what the signature binds. JSON (`--json`) is for agents and UIs: compact, stable keys, nothing to parse out of prose, and
// it signs nothing.

// PendingItem is one target waiting for a signature.
type PendingItem struct {
	Target         string                   `json:"target"`
	Kind           string                   `json:"kind"` // function, test, check_finding, protected_path
	Counterexample *contract.Counterexample `json:"counterexample,omitempty"`
	Detail         string                   `json:"detail,omitempty"` // for a test or a finding: what the gate said
}

// PendingGroup is the targets one waiver text declares: the same decision for the same stated reason.
type PendingGroup struct {
	Decision string        `json:"decision"`
	Reason   string        `json:"reason"`
	Items    []PendingItem `json:"items"`
}

// PendingReport is what waits for a person on the current tree.
type PendingReport struct {
	Tree    string         `json:"tree"`
	Verdict string         `json:"verdict"`
	Groups  []PendingGroup `json:"groups"`
	Count   int            `json:"count"`
}

// Pending reads the receipt of the current tree and lists what waits for a signature. A receipt about another tree is an
// error: signing it would bind approvals to files the person has not been shown.
func (a App) Pending() (PendingReport, error) {
	var r PendingReport
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		return r, err
	}
	var v contract.Verdict
	if err := a.read("", pathVerdict, &v); err != nil {
		return r, fmt.Errorf("there is no %s for the current tree: run a2dlc check first", pathVerdict)
	}
	if bareTree(v.TreeHash) != tree {
		return r, fmt.Errorf("%s is about another tree than the one on disk: run a2dlc check first", pathVerdict)
	}
	waivers, err := a.waivers("")
	if err != nil {
		return r, err
	}
	r.Tree, r.Verdict = v.TreeHash, v.Verdict
	byTarget := map[string]PendingItem{}
	for _, t := range v.Targets {
		byTarget[t.Target] = PendingItem{Target: t.Target, Kind: "function", Counterexample: t.Counterexample}
	}
	for _, t := range v.Tests {
		byTarget[t.Target] = PendingItem{Target: t.Target, Kind: "test", Detail: t.Reason}
	}
	for _, c := range v.Checks {
		if c.Findings == nil {
			continue
		}
		for _, f := range *c.Findings {
			byTarget[f.Target] = PendingItem{Target: f.Target, Kind: "check_finding", Detail: c.ID + ": " + f.Message}
		}
	}
	groups := map[string]*PendingGroup{}
	targets := append([]string(nil), v.PendingWaivers...)
	sort.Strings(targets)
	for _, target := range targets {
		w, ok := waivers[target]
		if !ok {
			continue // the waiver was removed since the check: nothing to sign
		}
		it, ok := byTarget[target]
		if !ok {
			it = PendingItem{Target: target, Kind: "function"}
		}
		if strings.HasSuffix(target, protectedSuffix) {
			it.Kind = "protected_path"
		}
		key := w.Decision + "\x00" + w.Reason
		g := groups[key]
		if g == nil {
			g = &PendingGroup{Decision: w.Decision, Reason: w.Reason}
			groups[key] = g
		}
		g.Items = append(g.Items, it)
		r.Count++
	}
	r.Groups = []PendingGroup{}
	for _, g := range groups {
		r.Groups = append(r.Groups, *g)
	}
	sort.Slice(r.Groups, func(i, j int) bool { return r.Groups[i].Items[0].Target < r.Groups[j].Items[0].Target })
	return r, nil
}

// decisionWords says what a decision asks the person to accept, in plain words.
func decisionWords(d string) string {
	switch d {
	case contract.EnforceNewBehavior:
		return "keep the new behavior"
	case contract.PreserveLegacy:
		return "keep the legacy behavior"
	case contract.QuarantineFlaky:
		return "quarantine an intermittent test so it does not block"
	case contract.CharacterizationTest:
		return "accept a test that passes on the base on purpose"
	case contract.AcceptFinding:
		return "accept a check finding"
	default:
		return d
	}
}

// Text is the report for the person who signs.
func (r PendingReport) Text() string {
	if r.Count == 0 {
		return "Nothing waits for your signature on this tree."
	}
	var b strings.Builder
	noun := "change waits"
	if r.Count > 1 {
		noun = "changes wait"
	}
	fmt.Fprintf(&b, "%d %s for your signature, in %d group(s). Each group is one reason the agent gave.\n", r.Count, noun, len(r.Groups))
	for i, g := range r.Groups {
		fmt.Fprintf(&b, "\n%d. %q\n   Asks you to: %s.\n", i+1, g.Reason, decisionWords(g.Decision))
		for _, it := range g.Items {
			switch it.Kind {
			case "protected_path":
				fmt.Fprintf(&b, "   - %s: a protected file was changed.\n", strings.TrimSuffix(it.Target, protectedSuffix))
			case "function":
				if c := it.Counterexample; c != nil {
					fmt.Fprintf(&b, "   - %s: with input %s it returned %s before, and now returns %s.\n", it.Target, c.Args, c.Old, c.New)
				} else {
					fmt.Fprintf(&b, "   - %s\n", it.Target)
				}
			default:
				if it.Detail != "" {
					fmt.Fprintf(&b, "   - %s: %s\n", it.Target, it.Detail)
				} else {
					fmt.Fprintf(&b, "   - %s\n", it.Target)
				}
			}
		}
	}
	b.WriteString("\nSigning approves these files exactly as they are now: any later edit to one of them needs a new signature.")
	return b.String()
}

// ApprovePending signs one approval per target of r with a single unlock of key and writes them to approvals.json,
// replacing any older approval of the same targets. Each signature is checked against the allowed signers before
// anything is written: a batch is all or nothing.
func (a App) ApprovePending(r PendingReport, key, principal string) error {
	if r.Count == 0 {
		return nil
	}
	waivers, err := a.waivers("")
	if err != nil {
		return err
	}
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		return err
	}
	if "git-tree:"+tree != r.Tree && tree != bareTree(r.Tree) {
		return fmt.Errorf("the tree changed while you read the list: run a2dlc approve --pending again")
	}
	var aps []contract.Approval
	var payloads [][]byte
	for _, g := range r.Groups {
		for _, it := range g.Items {
			w, ok := waivers[it.Target]
			if !ok {
				return fmt.Errorf("the waiver for %s is gone: run a2dlc approve --pending again", it.Target)
			}
			blob, err := a.Repo.Blob(tree, contract.TargetFile(it.Target))
			if err != nil {
				return fmt.Errorf("the file for %s is not in the tree: %w", it.Target, err)
			}
			ap := contract.Approval{Target: it.Target, WaiverSHA256: approval.WaiverSHA(w), FileBlob: blob, Principal: principal}
			aps = append(aps, ap)
			payloads = append(payloads, approval.Payload(ap.Target, ap.WaiverSHA256, ap.FileBlob))
		}
	}
	sigs, err := approval.SignMany(key, payloads)
	if err != nil {
		return err
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return err
	}
	signed := map[string]bool{}
	for i := range aps {
		aps[i].Signature = sigs[i]
		if err := approval.Verify(signers, principal, payloads[i], sigs[i]); err != nil {
			return fmt.Errorf("signed, but the signature for %s does not pass against the allowed signers (is your key missing from allowed_signers?): %w", aps[i].Target, err)
		}
		signed[aps[i].Target] = true
	}
	current, err := a.approvals("")
	if err != nil {
		return err
	}
	out := contract.Approvals{SchemaVersion: contract.SchemaVersion, Approvals: []contract.Approval{}}
	for _, x := range current {
		if !signed[x.Target] {
			out.Approvals = append(out.Approvals, x)
		}
	}
	out.Approvals = append(out.Approvals, aps...)
	return a.write(pathApprovals, out)
}
