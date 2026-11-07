package app

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

func divergence(target, args, old, new string) contract.Escalation {
	return escalation("behavior_divergence", target, "differs", contract.Evidence{Counterexample: &contract.Counterexample{Args: args, Old: old, New: new}})
}

func writeEscalations(t *testing.T, dir, rel string, items ...contract.Escalation) {
	t.Helper()
	b, err := json.Marshal(contract.Escalations{SchemaVersion: 1, TreeHash: "git-tree:" + strings.Repeat("a", 40), CreatedAt: "2026-11-07T10:00:00Z", Items: items})
	if err != nil {
		t.Fatal(err)
	}
	write(t, dir, rel, string(b))
}

// The counterexample that a real check left in escalations.json becomes a property with its probe.
func TestProposeFromEscalationsReadsTheEscalationsOfACheck(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	doc, err := a.ProposeFromEscalations(a.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Proposals) != 1 {
		t.Fatalf("proposals: %+v skipped: %+v", doc.Proposals, doc.Skipped)
	}
	p := doc.Proposals[0]
	if p.Target != "src/lib/f.ts::f" || p.File != "src/lib/f.ts" || p.Source.Kind != "behavior_divergence" {
		t.Fatalf("proposal: %+v", p)
	}
	if p.Hash != ProposalHash("src/lib/f.ts::f", "[]", "export const f = 1\n") {
		t.Fatalf("hash %s is not the hash of target, args and old", p.Hash)
	}
	if p.Probe.Kind != "example" || p.Probe.Input != "[]" || p.Probe.Expected != "export const f = 1\n" || p.Probe.ID != "ESC-1" || p.Probe.Executable() {
		t.Fatalf("probe: %+v", p.Probe)
	}
	if !strings.Contains(p.Property, "export const f = 1") || !strings.Contains(p.Property, "export const f = 2") {
		t.Fatalf("property says neither the base's result nor the candidate's: %q", p.Property)
	}
}

// Dedup by content: a second run proposes nothing new, and the record stays outside the tree_hash.
func TestProposeFromEscalationsTwiceProposesNothingNew(t *testing.T) {
	a, _, dir := newRepo(t)
	writeEscalations(t, dir, ".a2dlc/escalations.json", divergence("m.py::f", "x=1", "2", "3"))
	treeBefore, err := a.Repo.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	first, err := a.ProposeFromEscalations(a.Now())
	if err != nil || len(first.Proposals) != 1 {
		t.Fatalf("first: %+v err=%v", first, err)
	}
	if err := a.RecordProposals(first); err != nil {
		t.Fatal(err)
	}
	second, err := a.ProposeFromEscalations(a.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Proposals) != 0 || len(second.Skipped) != 1 || !strings.Contains(second.Skipped[0].Reason, "already proposed") {
		t.Fatalf("second run: %+v", second)
	}
	if treeAfter, _ := a.Repo.WorktreeTree(); treeAfter != treeBefore {
		t.Fatalf("the record changed the tree_hash: %s -> %s", treeBefore, treeAfter)
	}
	// a new counterexample is still proposed
	writeEscalations(t, dir, ".a2dlc/escalations.json", divergence("m.py::f", "x=1", "2", "3"), divergence("m.py::f", "x=7", "8", "9"))
	third, err := a.ProposeFromEscalations(a.Now())
	if err != nil || len(third.Proposals) != 1 || third.Proposals[0].Probe.Input != "x=7" {
		t.Fatalf("third run: %+v err=%v", third, err)
	}
}

// The same counterexample in the last check and in a run, under different escalation ids or with another new output, is
// one proposal: only target, args and old are hashed.
func TestProposeFromEscalationsHashesTargetArgsAndOldOnly(t *testing.T) {
	a, _, dir := newRepo(t)
	writeEscalations(t, dir, ".a2dlc/escalations.json", divergence("m.py::f", "x=1", "2", "3"))
	writeEscalations(t, dir, ".a2dlc/run/s1/escalations.json", divergence("m.py::f", "x=1", "2", "99"), divergence("m.py::g", "x=1", "2", "3"))
	doc, err := a.ProposeFromEscalations(a.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Proposals) != 2 || len(doc.Skipped) != 1 {
		t.Fatalf("proposals %d skipped %+v", len(doc.Proposals), doc.Skipped)
	}
	if doc.Proposals[0].Probe.ID != "ESC-1" || doc.Proposals[1].Probe.ID != "ESC-2" || doc.Proposals[0].Hash >= doc.Proposals[1].Hash {
		t.Fatalf("proposals are not in hash order with ordered probe ids: %+v", doc.Proposals)
	}
	if ProposalHash("m.py::f", "x=1", "2") == ProposalHash("m.py::f", "x=1", "23") || ProposalHash("m.py::f", "x=12", "") == ProposalHash("m.py::f", "x=1", "2") {
		t.Fatal("fields run together in the hash")
	}
}

// What is not a counterexample to keep, or that a person declared intended, is read and left out with its reason.
func TestProposeFromEscalationsSkipsWhatItCannotOrShouldNotPropose(t *testing.T) {
	a, _, dir := newRepo(t)
	mutant := escalation("mutant_survived", "m.py::f", "alive", contract.Evidence{Mutant: &contract.Mutant{ID: "m1", File: "m.py", Line: 3, Operator: "flip", Description: "x"}})
	mutant.Needs = &contract.Needs{Role: contract.RoleSaboteur, Artifact: "test_files"}
	lemma := escalation("lemma_failed", "m.py::l", "bad", contract.Evidence{Message: "post fails", Counterexample: &contract.Counterexample{Args: "x=1", Old: "", New: "2"}})
	noOld := divergence("m.py::n", "x=1", "", "3")
	waived := divergence("m.py::w", "x=1", "2", "3")
	keep := divergence("m.py::k", "x=1", "2", "3")
	writeEscalations(t, dir, ".a2dlc/escalations.json", mutant, lemma, noOld, waived, keep)
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"m.py::w","decision":"enforce_new_behavior","reason":"intended"},{"target":"m.py::k","decision":"preserve_legacy","reason":"keep"}]}`)
	doc, err := a.ProposeFromEscalations(a.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Proposals) != 1 || doc.Proposals[0].Target != "m.py::k" {
		t.Fatalf("proposals: %+v", doc.Proposals)
	}
	why := map[string]string{}
	for _, s := range doc.Skipped {
		why[s.Target] = s.Reason
	}
	for target, want := range map[string]string{"m.py::f": "kind mutant_survived", "m.py::l": "kind lemma_failed", "m.py::n": "no counterexample", "m.py::w": "enforce_new_behavior"} {
		if !strings.Contains(why[target], want) {
			t.Errorf("%s: skipped for %q, want %q", target, why[target], want)
		}
	}
}

// With no escalations at all there is nothing to propose and nothing fails.
func TestProposeFromEscalationsWithoutEscalations(t *testing.T) {
	a, _, _ := newRepo(t)
	doc, err := a.ProposeFromEscalations(a.Now())
	if err != nil || len(doc.Proposals) != 0 || len(doc.Skipped) != 0 {
		t.Fatalf("doc=%+v err=%v", doc, err)
	}
}

// An escalations.json that does not match the contract is an error that names the file, never a silent skip.
func TestProposeFromEscalationsRefusesAnInvalidFile(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/run/s1/escalations.json", `{"schema_version":1,"items":[{"id":"x"}]}`)
	_, err := a.ProposeFromEscalations(a.Now())
	if err == nil || !strings.Contains(err.Error(), ".a2dlc/run/s1/escalations.json") {
		t.Fatalf("err=%v", err)
	}
}
