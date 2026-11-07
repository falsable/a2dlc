package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// recordApp is an App over a bare folder: the record needs no git, and Now moves one minute per call so events order.
func recordApp(t *testing.T) App {
	t.Helper()
	at := time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC)
	return App{Repo: gitx.Repo{Dir: t.TempDir()}, Now: func() time.Time { at = at.Add(time.Minute); return at }}
}

// lintVerdict is a verdict whose blocking check "lint" found each target (none: the check passed).
func lintVerdict(base string, targets ...string) contract.Verdict {
	pass, fs := len(targets) == 0, []contract.VerdictCheckFinding{}
	for _, tg := range targets {
		fs = append(fs, contract.VerdictCheckFinding{Target: tg, Message: "found " + tg})
	}
	return contract.Verdict{BaseRef: base, TreeHash: "git-tree:" + base, Verdict: contract.FAIL,
		Checks: []contract.VerdictCheck{{ID: "lint", Severity: contract.CheckBlocking, Status: contract.CheckRan, Pass: &pass, Findings: &fs}}}
}

func record(t *testing.T, a App, v contract.Verdict, in findingInput) {
	t.Helper()
	if err := a.recordFindings(v, in); err != nil {
		t.Fatal(err)
	}
}

func events(t *testing.T, a App) []contract.FindingEvent {
	t.Helper()
	evs, err := a.readFindingEvents()
	if err != nil {
		t.Fatal(err)
	}
	return evs
}

func eventNames(evs []contract.FindingEvent) string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Event)
	}
	return strings.Join(out, ",")
}

func report(t *testing.T, a App) FindingReport {
	t.Helper()
	r, err := a.FindingReport()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func row(r FindingReport, source string) SourceCount {
	for _, s := range r.Sources {
		if s.Source == source {
			return s
		}
	}
	return SourceCount{}
}

// Criterion 1: a finding that is gone in the next check, with no waiver, is fixed. Nothing here calls a model.
func TestFindingGoneNextRunWithNoWaiverIsFixed(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	r := report(t, a)
	if c := row(r, "check:lint"); c.Raised != 1 || c.Open != 1 || c.Parked != 1 || c.Fixed != 0 {
		t.Fatalf("after the first run: %+v", c)
	}
	record(t, a, lintVerdict("b1"), findingInput{})
	if got := eventNames(events(t, a)); got != "raised,fixed" {
		t.Fatalf("events %s, want raised,fixed", got)
	}
	if c := row(report(t, a), "check:lint"); c.Raised != 1 || c.Fixed != 1 || c.Open != 0 {
		t.Fatalf("after it was gone: %+v", c)
	}
}

// A second check on the same tree changes nothing: events are transitions, not observations.
func TestRecordIsIdempotentForTheSameFindings(t *testing.T) {
	a := recordApp(t)
	for i := 0; i < 3; i++ {
		record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	}
	if got := eventNames(events(t, a)); got != "raised" {
		t.Fatalf("events %s, want a single raised", got)
	}
}

// A finding that comes back after it was fixed is a new occurrence: raised counts it again.
func TestFindingThatReturnsIsRaisedAgain(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	record(t, a, lintVerdict("b1"), findingInput{})
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	if c := row(report(t, a), "check:lint"); c.Raised != 2 || c.Fixed != 1 || c.Open != 1 {
		t.Fatalf("%+v", c)
	}
}

// Findings are followed only between checks against the same base: a check against another base says nothing about them.
func TestOtherBaseDoesNotFixAnything(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	record(t, a, lintVerdict("b2"), findingInput{})
	if c := row(report(t, a), "check:lint"); c.Fixed != 0 || c.Open != 1 {
		t.Fatalf("a check on another base fixed a finding: %+v", c)
	}
}

// A run that could not look at everything (a check down, an engine that did not answer) cannot say that a finding is gone.
func TestIncompleteRunFixesNothing(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	down := lintVerdict("b1")
	down.Checks[0].Status, down.Checks[0].Findings, down.Checks[0].Pass = contract.CheckError, nil, nil
	record(t, a, down, findingInput{})
	engineDown := lintVerdict("b1")
	engineDown.EngineErrors = []contract.EngineError{{Language: "typescript", Files: []string{"src/a.ts"}, Error: "no answer"}}
	record(t, a, engineDown, findingInput{})
	// The check being down is itself a (tooling) finding; the lint finding must still be open.
	list, err := a.FindingList(true)
	if err != nil {
		t.Fatal(err)
	}
	open := map[string]bool{}
	for _, f := range list {
		open[f.Target] = true
	}
	if c := row(report(t, a), "check:lint"); c.Fixed != 0 || !open["README.md::lint"] {
		t.Fatalf("an incomplete run fixed a finding: %+v %v", c, list)
	}
}

// A target that carries a waiver is not "gone without a waiver": nothing is derived for it when it disappears.
func TestWaiverPresentFixesNothingWhenFindingDisappears(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	record(t, a, lintVerdict("b1"), findingInput{waivers: map[string]contract.Waiver{"README.md::lint": {Target: "README.md::lint", Decision: contract.PreserveLegacy}}})
	if c := row(report(t, a), "check:lint"); c.Fixed != 0 {
		t.Fatalf("%+v", c)
	}
}

// Criterion 2: a signed accept_finding is intentional; an unsigned one is not, it is parked waiting for the signature.
func TestSignedAcceptFindingIsIntentional(t *testing.T) {
	a := recordApp(t)
	v := lintVerdict("b1", "README.md::lint")
	v.Checks[0].Findings = &[]contract.VerdictCheckFinding{{Target: "README.md::lint", Message: "m", Waived: true}}
	waivers := map[string]contract.Waiver{"README.md::lint": {Target: "README.md::lint", Decision: contract.AcceptFinding, Reason: "ok"}}

	unsigned := v
	unsigned.PendingWaivers = []string{"README.md::lint"}
	record(t, a, unsigned, findingInput{waivers: waivers, approved: map[string]bool{}})
	if c := row(report(t, a), "check:lint"); c.Intentional != 0 || c.Parked != 1 {
		t.Fatalf("unsigned: %+v", c)
	}
	record(t, a, v, findingInput{waivers: waivers, approved: map[string]bool{"README.md::lint": true}})
	evs := events(t, a)
	if eventNames(evs) != "raised,intentional" || evs[1].Decision != contract.AcceptFinding {
		t.Fatalf("events %s %+v", eventNames(evs), evs)
	}
	if c := row(report(t, a), "check:lint"); c.Raised != 1 || c.Intentional != 1 || c.Open != 0 {
		t.Fatalf("signed: %+v", c)
	}
	// It is still waived on the next runs: nothing new, and it never becomes "fixed" while the waiver covers it.
	record(t, a, v, findingInput{waivers: waivers, approved: map[string]bool{"README.md::lint": true}})
	record(t, a, lintVerdict("b1"), findingInput{waivers: waivers, approved: map[string]bool{"README.md::lint": true}})
	if got := eventNames(events(t, a)); got != "raised,intentional" {
		t.Fatalf("events %s", got)
	}
}

// A signed waiver that is not an acceptance of the finding (preserve_legacy asks the legacy back) is not "intentional".
func TestSignedPreserveLegacyIsNotIntentional(t *testing.T) {
	a := recordApp(t)
	v := lintVerdict("b1", "README.md::lint")
	v.Checks[0].Findings = &[]contract.VerdictCheckFinding{{Target: "README.md::lint", Message: "m", Waived: true}}
	record(t, a, v, findingInput{waivers: map[string]contract.Waiver{"README.md::lint": {Decision: contract.PreserveLegacy}}, approved: map[string]bool{"README.md::lint": true}})
	if c := row(report(t, a), "check:lint"); c.Intentional != 0 {
		t.Fatalf("%+v", c)
	}
}

// Criterion 3: false and irrelevant exist only through a label, and the label says who.
func TestFalseAndIrrelevantNeedALabel(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint", "README.md::L3"), findingInput{})
	record(t, a, lintVerdict("b1", "README.md::L3"), findingInput{}) // lint is fixed; L3 stays
	c := row(report(t, a), "check:lint")
	if c.False != 0 || c.Irrelevant != 0 {
		t.Fatalf("derived without a label: %+v", c)
	}
	list, err := a.FindingList(false)
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %+v", err, list)
	}
	var open string
	for _, f := range list {
		if f.Outcome == "parked" {
			open = f.ID
		}
	}
	if _, err := a.LabelFinding(open, "false", "", contract.ViaCLI, ""); err == nil {
		t.Fatal("a label without who set it was accepted")
	}
	if _, err := a.LabelFinding(open, "fixed", "ana", contract.ViaCLI, ""); err == nil {
		t.Fatal("a label may only say false or irrelevant")
	}
	if _, err := a.LabelFinding("fnd-000000000000", "false", "ana", contract.ViaCLI, ""); !errors.Is(err, ErrUnknownFinding) {
		t.Fatalf("unknown id: %v", err)
	}
	if _, err := a.LabelFinding(open, "false", "ana", "model", ""); err == nil {
		t.Fatal("a surface other than cli or mcp was accepted")
	}
	e, err := a.LabelFinding(open, "false", "ana", contract.ViaMCP, "never reaches that input")
	if err != nil || e.By != "ana" || e.Via != contract.ViaMCP || e.Outcome != "false" {
		t.Fatalf("%v %+v", err, e)
	}
	c = row(report(t, a), "check:lint")
	if c.Raised != 2 || c.Fixed != 1 || c.False != 1 || c.Open != 0 || c.Parked != 0 {
		t.Fatalf("%+v", c)
	}
	// The label stays when the finding later disappears: a derived outcome never overrides a person's.
	record(t, a, lintVerdict("b1"), findingInput{})
	if c = row(report(t, a), "check:lint"); c.False != 1 || c.Fixed != 1 {
		t.Fatalf("a label was overridden: %+v", c)
	}
	// A later label replaces the earlier one; both stay in the record.
	if _, err := a.LabelFinding(open, "irrelevant", "ana", contract.ViaCLI, ""); err != nil {
		t.Fatal(err)
	}
	if c = row(report(t, a), "check:lint"); c.False != 0 || c.Irrelevant != 1 {
		t.Fatalf("%+v", c)
	}
}

// The report's columns add up: every raised finding has exactly one state.
func TestReportColumnsAddUp(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "a.md::lint", "b.md::lint", "c.md::lint"), findingInput{})
	record(t, a, lintVerdict("b1", "b.md::lint"), findingInput{})
	r := report(t, a)
	c := r.Total
	if c.Raised != c.Fixed+c.Intentional+c.False+c.Irrelevant+c.Open || c.Raised != 3 {
		t.Fatalf("%+v", c)
	}
	if row(r, "check:lint").Raised != 3 {
		t.Fatalf("%+v", r)
	}
}

// Where a finding came from: the engine, the check or the gate that raised it.
func TestSourceNamesTheEngineTheCheckOrTheGate(t *testing.T) {
	fail := contract.VerdictTarget{TargetResult: contract.TargetResult{Target: "src/a.ts::f", Engine: "typescript", Verdict: contract.FAIL,
		Counterexample: &contract.Counterexample{Args: "[0]", Old: "1", New: "2"}}}
	v := lintVerdict("b1", "README.md::lint")
	v.Targets = []contract.VerdictTarget{fail}
	v.Tests = []contract.TestResult{{Target: "tests/t.py::test_x", Gate: contract.GateNOP, Verdict: contract.FAIL}}
	v.Coverage = &contract.Coverage{Status: contract.CoverageRan, Files: []contract.CoverageFile{{File: "src/b.ts", Uncovered: []int{3}}}}
	v.EngineErrors = []contract.EngineError{{Language: "python", Files: []string{"x.py"}, Error: "down"}}
	a := recordApp(t)
	record(t, a, v, findingInput{})
	got := map[string]string{}
	for _, e := range events(t, a) {
		got[e.Target] = e.Source + "|" + e.Kind
	}
	want := map[string]string{
		"src/a.ts::f":        "engine:typescript|behavior_divergence",
		"README.md::lint":    "check:lint|check_finding",
		"tests/t.py::test_x": "gate:nop_oracle|test_nop_oracle",
		"src/b.ts::*":        "gate:coverage|uncovered_change",
		"x.py::*":            "engine:python|tooling",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %q, want %q (all: %v)", k, got[k], w, got)
		}
	}
}

// The id is the same in every run for the same kind, source and target, and it differs when any of the three differs.
func TestFindingIDIsStable(t *testing.T) {
	id := findingID("k", "check:a", "t")
	if again := findingID("k", "check:a", "t"); id != again {
		t.Fatal("not stable")
	}
	for _, other := range []string{findingID("j", "check:a", "t"), findingID("k", "check:b", "t"), findingID("k", "check:a", "u")} {
		if other == id {
			t.Fatal("collision")
		}
	}
	if !strings.HasPrefix(id, "fnd-") || len(id) != 16 {
		t.Fatal(id)
	}
}

// The record is out of the tree_hash: its path lies under an excluded one.
func TestRecordPathIsExcludedFromTheTreeHash(t *testing.T) {
	for _, ex := range gitx.Excluded {
		if pathFindings == ex || strings.HasPrefix(pathFindings, ex+"/") {
			return
		}
	}
	t.Fatalf("%s is not under gitx.Excluded %v", pathFindings, gitx.Excluded)
}

// Criterion 4: with the record on and off the verdict, its escalations and the tree_hash are the same. One repo, the
// record on first and off second, so the second check runs with findings.jsonl already in the working tree and over the
// same commits. The verdict's wall-clock timings are the only thing stripped: they change from run to run either way.
func TestVerdictAndTreeHashAreTheSameWithTheRecordOnAndOff(t *testing.T) {
	fake := &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::lint")}}
	a, dir := checksRepo(t, blockingLint, fake)
	a.Now = func() time.Time { return time.Date(2026, 11, 7, 10, 0, 0, 0, time.UTC) }
	run := func(flag string) (verdict, esc []byte, hash string) {
		t.Setenv("A2DLC_FINDINGS", flag)
		v, err := a.Check(context.Background(), "HEAD")
		if err != nil {
			t.Fatal(err)
		}
		verdict, _ = os.ReadFile(filepath.Join(dir, ".a2dlc/verdict.json"))
		esc, _ = os.ReadFile(filepath.Join(dir, ".a2dlc/escalations.json"))
		return stripTimings(t, verdict), esc, v.TreeHash
	}
	vOn, eOn, hOn := run("")
	if evs := events(t, a); eventNames(evs) != "raised" {
		t.Fatalf("on: events %s", eventNames(evs))
	}
	vOff, eOff, hOff := run("off")
	if evs := events(t, a); eventNames(evs) != "raised" {
		t.Fatalf("A2DLC_FINDINGS=off still wrote to the record: %s", eventNames(evs))
	}
	if string(vOff) != string(vOn) || string(eOff) != string(eOn) || hOff != hOn {
		t.Fatalf("the record changed the receipt: on %s off %s, tree %s vs %s", vOn, vOff, hOn, hOff)
	}
	// The record file is in the working tree now and the tree_hash is still the one without it.
	if tree, err := a.Repo.WorktreeTree(); err != nil || "git-tree:"+tree != hOn {
		t.Fatalf("the record moved the tree_hash: %s vs %s (%v)", tree, hOn, err)
	}
}

func stripTimings(t *testing.T, b []byte) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	delete(m, "timings")
	out, _ := json.Marshal(m)
	return out
}

// Over the real check: a finding that goes away between two checks of the same base is fixed, and the second check
// does not rewrite the first one's events.
func TestCheckRecordsAcrossSuccessiveRuns(t *testing.T) {
	fake := &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::lint")}}
	a, _ := checksRepo(t, blockingLint, fake)
	t.Setenv("A2DLC_FINDINGS", "")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	fake.resp["lint"] = response("lint", true)
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	evs := events(t, a)
	if eventNames(evs) != "raised,fixed" || evs[0].Source != "check:lint" || evs[0].State != contract.FindingParked {
		t.Fatalf("%s %+v", eventNames(evs), evs)
	}
}

// Signed accept_finding through the real check: the same flow as TestSignedAcceptFindingCoversTheFindingNotAnError.
func TestCheckRecordsSignedAcceptFindingAsIntentional(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	t.Setenv("A2DLC_FINDINGS", "")
	fake := &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::lint")}}
	a, dir := checksRepo(t, blockingLint, fake)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"README.md::lint","decision":"accept_finding","reason":"accepted"}]}`)
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	if err := a.Approve("README.md::lint", key, "me@a2dlc"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	if got := eventNames(events(t, a)); got != "raised,intentional" {
		t.Fatalf("events %s", got)
	}
}

// The record is append-only and strict: a corrupt line is an error the reader names, never a silent skip.
func TestCorruptRecordIsAnErrorNotASkip(t *testing.T) {
	a := recordApp(t)
	record(t, a, lintVerdict("b1", "README.md::lint"), findingInput{})
	f, _ := os.OpenFile(a.path(pathFindings), os.O_APPEND|os.O_WRONLY, 0o644)
	f.WriteString("{not json}\n")
	f.Close()
	if _, err := a.FindingReport(); err == nil || !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("%v", err)
	}
}
