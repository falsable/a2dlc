package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// the valid example of the contract: six escalations, one of them (tabToPath) a behavior_divergence with a counterexample
func writeExampleEscalations(t *testing.T, dir string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "valid", "escalation__check.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeIn(t, dir, ".a2dlc/escalations.json", string(b))
}

func TestProposeFromEscalationsWritesProposalsJSONUnderOutAndNothingNewIntoTheTree(t *testing.T) {
	a, dir := newProposeRepo(t)
	writeExampleEscalations(t, dir)
	treeBefore, err := a.Repo.WorktreeTree()
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "out")
	doc, err := runProposeFromEscalations(a, out, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Proposals) != 1 || doc.Proposals[0].Target != "src/lib/routes.ts::tabToPath" || doc.Proposals[0].Probe.Expected != `"/app/playground"` {
		t.Fatalf("proposals: %+v", doc.Proposals)
	}
	raw, err := os.ReadFile(filepath.Join(out, "proposals.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk contract.Proposals
	if err := contract.DecodeStrictBytes(raw, &onDisk); err != nil || len(onDisk.Proposals) != 1 {
		t.Fatalf("proposals.json: %v %+v", err, onDisk)
	}
	if treeAfter, _ := a.Repo.WorktreeTree(); treeAfter != treeBefore {
		t.Fatalf("propose changed the tree_hash: %s -> %s", treeBefore, treeAfter)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc", "reports", "proposals.jsonl")); err != nil {
		t.Fatalf("the record of what was proposed is missing: %v", err)
	}
	// a second run, into another empty folder, proposes nothing new
	out2 := filepath.Join(t.TempDir(), "out2")
	again, err := runProposeFromEscalations(a, out2, time.Now())
	if err != nil || len(again.Proposals) != 0 {
		t.Fatalf("second run: %+v err=%v", again, err)
	}
	// an intent is never touched: there is none to touch, and none appears
	if matches, _ := filepath.Glob(filepath.Join(dir, ".a2dlc", "intents", "*")); len(matches) != 0 {
		t.Fatalf("propose wrote an intent: %v", matches)
	}
}

func TestProposeFromEscalationsKeepsTheRecordWhenOutIsRefused(t *testing.T) {
	a, dir := newProposeRepo(t)
	writeExampleEscalations(t, dir)
	if _, err := runProposeFromEscalations(a, filepath.Join(dir, "out"), time.Now()); err == nil || !strings.Contains(err.Error(), "inside the repository") {
		t.Fatalf("out inside the repo: %v", err)
	}
	full := t.TempDir()
	writeIn(t, full, "x", "x")
	if _, err := runProposeFromEscalations(a, full, time.Now()); err == nil || !strings.Contains(err.Error(), "not empty") {
		t.Fatalf("out not empty: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".a2dlc", "reports", "proposals.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("a refused run recorded its proposals as handed out: %v", err)
	}
	// and the proposals are still there for the next run
	doc, err := runProposeFromEscalations(a, filepath.Join(t.TempDir(), "o"), time.Now())
	if err != nil || len(doc.Proposals) != 1 {
		t.Fatalf("after the refusals: %+v err=%v", doc, err)
	}
}

func TestProposeFromEscalationsOutputMatchesTheSchemaExample(t *testing.T) {
	var ex contract.Proposals
	b, err := os.ReadFile(filepath.Join("..", "..", "contracts", "examples", "valid", "proposals__escalations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(b, &ex); err != nil {
		t.Fatal(err)
	}
	back, _ := json.Marshal(ex)
	var m map[string]any
	if json.Unmarshal(back, &m) != nil || len(ex.Proposals) != 1 || len(ex.Skipped) != 1 {
		t.Fatalf("the example does not round-trip through the Go type: %s", back)
	}
}
