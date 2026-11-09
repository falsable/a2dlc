package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

const validExamples = "../../contracts/examples/valid/"

func copyExample(t *testing.T, a App, example, rel string) []byte {
	t.Helper()
	b, err := os.ReadFile(validExamples + example)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(a.path(rel)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(rel), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return b
}

func journalHTML(t *testing.T, a App) string {
	t.Helper()
	j, err := a.ReadJournal()
	if err != nil {
		t.Fatal(err)
	}
	b, err := j.HTML()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJournalWithNothingRecordedSaysSo(t *testing.T) {
	html := journalHTML(t, recordApp(t))
	for _, want := range []string{"No check has written a receipt yet", "<p>Nothing.</p>", "missing"} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
}

// A waived behavior change shows its decision and who signed it, the escalations wait on a person, and every file read
// is listed with the sha256 of its bytes. The same files render the same bytes.
func TestJournalJoinsReceiptWaiverApprovalAndEscalations(t *testing.T) {
	a := recordApp(t)
	verdict := copyExample(t, a, "verdict__pending.json", pathVerdict)
	copyExample(t, a, "approvals__signed.json", pathApprovals)
	copyExample(t, a, "escalation__check.json", pathEscalations)
	b, _ := encode(contract.Waivers{SchemaVersion: 1, Waivers: []contract.Waiver{{Target: "src/lib/routes.ts::tabToPath",
		Decision: "enforce_new_behavior", Reason: "playground moved to its own route"}}})
	if err := os.WriteFile(a.path(pathWaivers), b, 0o644); err != nil {
		t.Fatal(err)
	}
	record(t, a, lintVerdict("b1", "README.md"), findingInput{})

	html := journalHTML(t, a)
	sum := sha256.Sum256(verdict)
	for _, want := range []string{
		"enforce_new_behavior: playground moved to its own route", "signed by aldemar",
		"Waiver for <code>src/lib/routes.ts::tabToPath</code> needs a signature",
		"The function returns another path for playground.",
		"check_finding on <code>README.md</code>",
		hex.EncodeToString(sum[:]),
	} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if again := journalHTML(t, a); again != html {
		t.Error("the same files rendered different bytes")
	}
}

func TestJournalRefusesASourceOutOfContract(t *testing.T) {
	a := recordApp(t)
	if err := os.MkdirAll(filepath.Dir(a.path(pathVerdict)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(pathVerdict), []byte(`{"verdict":"PASS"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ReadJournal(); err == nil || !strings.Contains(err.Error(), "out of contract") {
		t.Fatalf("want an out-of-contract error, got %v", err)
	}
}

// Text from engines and checks reaches the page escaped: a summary cannot inject markup into what an auditor opens.
func TestJournalEscapesRecordedText(t *testing.T) {
	a := recordApp(t)
	b, _ := encode(contract.Escalations{SchemaVersion: 1, TreeHash: "git-tree:x", CreatedAt: "2026-11-09T10:00:00Z",
		Items: []contract.Escalation{{ID: "esc-1", Kind: "tooling", Target: "a.py::f", Summary: "<script>alert(1)</script>",
			Evidence: contract.Evidence{}, ResolveWith: []string{"fix_tooling"}}}})
	if err := os.MkdirAll(filepath.Dir(a.path(pathEscalations)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.path(pathEscalations), b, 0o644); err != nil {
		t.Fatal(err)
	}
	j, err := a.ReadJournal()
	if err != nil {
		t.Fatal(err)
	}
	page, _ := j.HTML()
	if bytes.Contains(page, []byte("<script>alert")) {
		t.Error("recorded text reached the page unescaped")
	}
}
