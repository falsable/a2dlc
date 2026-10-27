package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

const (
	cfgNoCritical  = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}]}`
	cfgOtherCrit   = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"critical_code":["src/core/**"]}`
	cfgProtected   = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"protected_paths":["infra/**"]}`
	cfgSymbolCrit  = `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"critical_code":["src/lib/f.ts::g"]}`
	msgNoVerdict   = "no verdict for this tree: nothing examined"
	msgNotBlocking = "this does not block the push"
)

// setConfig commits cfg as the repo's config: the base of what the test changes next.
func setConfig(t *testing.T, dir, cfg string) {
	t.Helper()
	write(t, dir, ".a2dlc/config.json", cfg)
	git(t, dir, "commit", "-qam", "config")
}

// withUpstream pushes HEAD to a bare remote and follows it, so the range being pushed is what is committed afterwards.
func withUpstream(t *testing.T, dir string) {
	t.Helper()
	remote := t.TempDir()
	git(t, remote, "init", "-q", "--bare")
	git(t, dir, "remote", "add", "origin", remote)
	git(t, dir, "push", "-q", "-u", "origin", "HEAD")
}

func policy(t *testing.T, a App) VerifyReport {
	t.Helper()
	rep, err := a.VerifyPolicy("HEAD")
	if err != nil {
		t.Fatalf("VerifyPolicy: %v", err)
	}
	return rep
}

func has(lines []string, sub string) bool {
	for _, l := range lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func checkAndCommit(t *testing.T, a App, dir, base string) contract.Verdict {
	t.Helper()
	v, err := a.Check(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "change")
	return v
}

// Row 1.
func TestVerifyPolicyFreshPassLetsThrough(t *testing.T) {
	a, _, _ := newRepo(t)
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	if rep := policy(t, a); len(rep.Blocked)+len(rep.Warnings) != 0 {
		t.Fatalf("%+v", rep)
	}
}

// Row 2.
func TestVerifyPolicyFreshFailBlocks(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgNoCritical)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	// Not critical code: the divergence is a note and the verdict does not fail, so a FAIL needs a critical target.
	if v := checkAndCommit(t, a, dir, "HEAD"); v.Verdict != contract.PASS {
		t.Fatalf("a divergence on code not declared critical is a note: %s", v.Verdict)
	}
	setConfig(t, dir, `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"critical_code":["src/lib/**"]}`)
	write(t, dir, "src/lib/f.ts", "export const f = 3\n")
	if v := checkAndCommit(t, a, dir, "HEAD"); v.Verdict != contract.FAIL {
		t.Fatalf("critical divergence: %s", v.Verdict)
	}
	rep := policy(t, a)
	if !has(rep.Blocked, "the verdict is FAIL: src/lib/f.ts::f") {
		t.Fatalf("%+v", rep)
	}
	lines, blocked := rep.Lines()
	if !blocked || !strings.HasPrefix(lines[0], "a2dlc verify: blocked: ") {
		t.Fatalf("%v", lines)
	}
}

// Row 3: INCONCLUSIVE warns, unless what stayed unjudged includes critical or protected code.
func TestVerifyPolicyFreshInconclusiveWarnsWhenNothingCriticalIsUnjudged(t *testing.T) {
	for name, cfg := range map[string]string{"no critical_code": cfgNoCritical, "critical code elsewhere": cfgOtherCrit} {
		t.Run(name, func(t *testing.T) {
			a, _, dir := newRepo(t)
			setConfig(t, dir, cfg)
			write(t, dir, "src/lib/f.ts", "INCONCLUSIVE\n")
			if v := checkAndCommit(t, a, dir, "HEAD"); v.Verdict != contract.INCONCLUSIVE {
				t.Fatal(v.Verdict)
			}
			rep := policy(t, a)
			lines, blocked := rep.Lines()
			if blocked || !has(rep.Warnings, "the verdict is INCONCLUSIVE") || !strings.Contains(lines[len(lines)-1], msgNotBlocking) {
				t.Fatalf("%v", lines)
			}
		})
	}
}

func TestVerifyPolicyFreshInconclusiveOnCriticalCodeBlocks(t *testing.T) {
	a, _, dir := newRepo(t) // critical_code: src/lib/**
	write(t, dir, "src/lib/f.ts", "INCONCLUSIVE\n")
	if v := checkAndCommit(t, a, dir, "HEAD"); v.Verdict != contract.INCONCLUSIVE {
		t.Fatal(v.Verdict)
	}
	rep := policy(t, a)
	if !has(rep.Blocked, "INCONCLUSIVE and leaves critical or protected code unjudged (src/lib/f.ts, src/lib/f.ts::f)") {
		t.Fatalf("%+v", rep)
	}
}

// A symbol-level critical_code entry tells the symbols of a target apart; where the receipt names only the file, it counts
// for the whole file.
func TestVerifyPolicySymbolLevelCriticalCode(t *testing.T) {
	cfg := cfgOf(t, cfgSymbolCrit)
	byTarget := unjudgedOf(contract.Verdict{Targets: []contract.VerdictTarget{{TargetResult: contract.TargetResult{Target: "src/lib/f.ts::f", Verdict: contract.INCONCLUSIVE}}}})
	if hit := byTarget.guarded(cfg, nil); len(hit) != 0 {
		t.Fatalf("the receipt says the unjudged symbol is f, not g: %v", hit)
	}
	byFile := unjudgedOf(contract.Verdict{OutOfScope: []contract.OutOfScope{{File: "src/lib/f.ts", Reason: "no_engine"}}})
	if hit := byFile.guarded(cfg, nil); len(hit) != 1 {
		t.Fatalf("with no symbol to tell, the entry counts for the whole file: %v", hit)
	}
}

func cfgOf(t *testing.T, s string) contract.Config {
	t.Helper()
	var c contract.Config
	if err := contract.DecodeStrictBytes([]byte(s), &c); err != nil {
		t.Fatal(err)
	}
	return c
}

// The causes without a file (a test gate in error, an inconclusive test) fall back to any critical file among the changed.
func TestVerifyPolicyInconclusiveWithoutFileCauseUsesChangedFiles(t *testing.T) {
	cfg := cfgOf(t, cfgOtherCrit)
	v := contract.Verdict{Verdict: contract.INCONCLUSIVE, TestGates: &contract.TestGates{NOPOracle: contract.Gate{Status: contract.GateError}}}
	u := unjudgedOf(v)
	if !u.noFile {
		t.Fatal("a gate in error names no file")
	}
	if hit := u.guarded(cfg, []string{"src/lib/f.ts"}); len(hit) != 0 {
		t.Fatalf("%v", hit)
	}
	if hit := u.guarded(cfg, []string{"src/lib/f.ts", "src/core/x.ts"}); len(hit) != 1 || hit[0] != "src/core/x.ts" {
		t.Fatalf("%v", hit)
	}
}

// Entropos, 2026-10-27: every changed file out of scope and no critical_code. The check is INCONCLUSIVE and the push goes through.
func TestVerifyPolicyEntroposCaseEverythingOutOfScopeDoesNotBlock(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgNoCritical)
	for _, f := range []string{"app/a.ts", "app/b.ts", "docs/c.md", "README.md", "app/new.ts"} {
		write(t, dir, f, "changed "+f+"\n")
	}
	write(t, dir, "src/lib/f.ts", "NO_TARGET\n")
	v := checkAndCommit(t, a, dir, "HEAD")
	if v.Verdict != contract.INCONCLUSIVE || len(v.OutOfScope) < 6 {
		t.Fatalf("the receipt must be INCONCLUSIVE with every file out of scope: %s %+v", v.Verdict, v.OutOfScope)
	}
	rep := policy(t, a)
	lines, blocked := rep.Lines()
	if blocked || !has(rep.Warnings, "files out of scope") || !strings.Contains(lines[len(lines)-1], msgNotBlocking) {
		t.Fatalf("%v", lines)
	}
}

// Row 4, no receipt.
func TestVerifyPolicyNoVerdictWarnsWhenRangeTouchesNothingCritical(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgNoCritical)
	withUpstream(t, dir)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	git(t, dir, "commit", "-qam", "change")
	rep := policy(t, a)
	if len(rep.Blocked) != 0 || !has(rep.Warnings, msgNoVerdict) {
		t.Fatalf("%+v", rep)
	}
}

func TestVerifyPolicyNoVerdictBlocksWhenRangeTouchesCriticalCode(t *testing.T) {
	a, _, dir := newRepo(t) // critical_code: src/lib/**
	withUpstream(t, dir)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	git(t, dir, "commit", "-qam", "change")
	rep := policy(t, a)
	if !has(rep.Blocked, "changes critical or protected code (src/lib/f.ts) and there is no verdict for this tree: run a2dlc check") {
		t.Fatalf("%+v", rep)
	}
	// The same range touching nothing critical goes through.
	git(t, dir, "reset", "-q", "--hard", "@{upstream}")
	write(t, dir, "README.md", "other\n")
	git(t, dir, "commit", "-qam", "docs")
	if rep := policy(t, a); len(rep.Blocked) != 0 {
		t.Fatalf("%+v", rep)
	}
}

// Row 4, stale receipt; a FAIL one says so.
func TestVerifyPolicyStaleVerdictWarnsAndSaysItWasFail(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgOtherCrit)
	withUpstream(t, dir)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	// Fail by criticality: judge it with src/lib critical, then commit another config as the base of the pushed range.
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	git(t, dir, "commit", "-qam", "change")
	write(t, dir, "README.md", "after the check\n")
	git(t, dir, "commit", "-qam", "no check")
	rep := policy(t, a)
	if len(rep.Blocked) != 0 || !has(rep.Warnings, msgNoVerdict) || !has(rep.Warnings, "was issued over another tree; changed: README.md") {
		t.Fatalf("%+v", rep)
	}
	var v contract.Verdict
	if err := a.read("", pathVerdict, &v); err != nil {
		t.Fatal(err)
	}
	v.Verdict = contract.FAIL
	if err := a.write(pathVerdict, v); err != nil {
		t.Fatal(err)
	}
	if rep := policy(t, a); !has(rep.Warnings, "that verdict was FAIL") || len(rep.Blocked) != 0 {
		t.Fatalf("%+v", rep)
	}
}

// Row 4, a receipt whose base leaves unpushed commits out.
func TestVerifyPolicyReceiptBaseNotCoveringUnpushedCommitsIsNoVerdict(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgNoCritical)
	withUpstream(t, dir)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	git(t, dir, "commit", "-qam", "change")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil { // against HEAD: sees nothing
		t.Fatal(err)
	}
	rep := policy(t, a)
	if len(rep.Blocked) != 0 || !has(rep.Warnings, msgNoVerdict) || !has(rep.Warnings, "does not cover the unpushed commits") {
		t.Fatalf("%+v", rep)
	}
}

// Row 4, range that cannot be determined.
func TestVerifyPolicyUndeterminedRangeBlocksOnlyIfConfigDeclaresCritical(t *testing.T) {
	a, _, dir := newRepo(t) // no upstream, no origin/HEAD; critical_code declared
	if rep := policy(t, a); !has(rep.Blocked, "the range being pushed cannot be determined") {
		t.Fatalf("%+v", rep)
	}
	setConfig(t, dir, cfgNoCritical)
	rep := policy(t, a)
	if len(rep.Blocked) != 0 || !has(rep.Warnings, msgNoVerdict) {
		t.Fatalf("%+v", rep)
	}
}

// Row 5.
func TestVerifyPolicyProtectedPathWithoutApprovalBlocksWithOrWithoutVerdict(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgProtected)
	withUpstream(t, dir)
	write(t, dir, "infra/main.tf", "resource\n")
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "infra")
	rep := policy(t, a)
	if !has(rep.Blocked, "infra/main.tf matches protected_paths and has no valid approval") {
		t.Fatalf("no verdict: %+v", rep)
	}
	if _, err := a.Check(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if rep := policy(t, a); !has(rep.Blocked, "infra/main.tf matches protected_paths and has no valid approval") {
		t.Fatalf("with a verdict: %+v", rep)
	}
}

// Row 6: an unsigned waiver blocks where what it exempts would block.
func TestVerifyPolicyUnsignedWaiverBlocksWhereWhatItExemptsWouldBlock(t *testing.T) {
	cfg := cfgOf(t, `{"schema_version":1,"critical_code":["src/core/**"]}`)
	tgt := func(name, verdictOf string) contract.VerdictTarget {
		return contract.VerdictTarget{TargetResult: contract.TargetResult{Target: name, Verdict: verdictOf}, Waived: true}
	}
	for _, c := range []struct {
		name   string
		target contract.VerdictTarget
		blocks bool
	}{
		{"FAIL", tgt("src/lib/f.ts::f", contract.FAIL), true},
		{"INCONCLUSIVE on critical code", tgt("src/core/x.ts::x", contract.INCONCLUSIVE), true},
		{"INCONCLUSIVE elsewhere", tgt("src/lib/f.ts::f", contract.INCONCLUSIVE), false},
		{"PASS", tgt("src/lib/f.ts::f", contract.PASS), false},
	} {
		t.Run(c.name, func(t *testing.T) {
			ws := unsignedWaivers(contract.Verdict{Targets: []contract.VerdictTarget{c.target}}, cfg, map[string]bool{}, map[string]string{})
			if len(ws) != 1 || ws[0].blocks != c.blocks || !strings.Contains(ws[0].text, "unsigned") {
				t.Fatalf("%+v", ws)
			}
		})
	}
	// A signed one is not reported; a finding of a blocking check is a FAIL, an advisory one is not.
	f := []contract.VerdictCheckFinding{{Target: "a::b", Waived: true}}
	v := contract.Verdict{Checks: []contract.VerdictCheck{{Severity: contract.CheckBlocking, Findings: &f}}}
	if ws := unsignedWaivers(v, cfg, map[string]bool{"a::b": true}, nil); len(ws) != 0 {
		t.Fatalf("%+v", ws)
	}
	if ws := unsignedWaivers(v, cfg, map[string]bool{}, nil); len(ws) != 1 || !ws[0].blocks {
		t.Fatalf("%+v", ws)
	}
	v.Checks[0].Severity = contract.CheckAdvisory
	if ws := unsignedWaivers(v, cfg, map[string]bool{}, nil); len(ws) != 1 || ws[0].blocks {
		t.Fatalf("%+v", ws)
	}
}

// Row 6, through the whole flow: the waiver of a FAIL without a signature stops the push, as it did.
func TestVerifyPolicyUnsignedWaiverOfAFailBlocks(t *testing.T) {
	a, _, dir := newRepo(t)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"src/lib/f.ts::f","decision":"enforce_new_behavior","reason":"on purpose"}]}`)
	checkAndCommit(t, a, dir, "HEAD")
	if rep := policy(t, a); !has(rep.Blocked, "waiver for src/lib/f.ts::f: unsigned") {
		t.Fatalf("%+v", rep)
	}
}

// Row 7.
func TestVerifyPolicyUnreadableVerdictIsNoVerdict(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgNoCritical)
	withUpstream(t, dir)
	write(t, dir, "README.md", "other\n")
	git(t, dir, "commit", "-qam", "docs")
	for name, content := range map[string]string{"corrupt": "{not json", "empty": "", "out of contract": `{"verdict":"PASS","unknown":1}`} {
		write(t, dir, pathVerdict, content)
		rep, err := a.VerifyPolicy("HEAD")
		if err != nil || len(rep.Blocked) != 0 || !has(rep.Warnings, msgNoVerdict) {
			t.Fatalf("%s: %+v %v", name, rep, err)
		}
	}
	os.Remove(filepath.Join(dir, pathVerdict))
}

func TestVerifyPolicyGitErrorIsAnErrorNotAVerdict(t *testing.T) {
	a, _, _ := newRepo(t)
	if rep, err := a.VerifyPolicy("no-such-ref"); err == nil || len(rep.Blocked) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
}

// Row 8: Verify, which --strict calls, still refuses what the policy lets through.
func TestVerifyStrictStillBlocksWhatThePolicyOnlyWarnsAbout(t *testing.T) {
	a, _, dir := newRepo(t)
	setConfig(t, dir, cfgNoCritical)
	if probs, _ := a.Verify("HEAD"); len(probs) != 1 || !strings.Contains(probs[0], "there is no .a2dlc/verdict.json") {
		t.Fatalf("no verdict: %v", probs)
	}
	if rep := policy(t, a); len(rep.Blocked) != 0 {
		t.Fatalf("%+v", rep)
	}
	write(t, dir, "src/lib/f.ts", "INCONCLUSIVE\n")
	checkAndCommit(t, a, dir, "HEAD")
	if probs, _ := a.Verify("HEAD"); len(probs) != 1 || !strings.Contains(probs[0], "the verdict is INCONCLUSIVE") {
		t.Fatalf("INCONCLUSIVE: %v", probs)
	}
	if rep := policy(t, a); len(rep.Blocked) != 0 {
		t.Fatalf("%+v", rep)
	}
}

func TestVerifyReportLinesFormat(t *testing.T) {
	lines, blocked := VerifyReport{Warnings: []string{"w"}}.Lines()
	if blocked || len(lines) != 2 || lines[0] != "a2dlc verify: warning: w" || !strings.HasPrefix(lines[1], "a2dlc verify: warning: "+msgNotBlocking) {
		t.Fatalf("%v", lines)
	}
	lines, blocked = VerifyReport{Blocked: []string{"b"}, Warnings: []string{"w"}}.Lines()
	if !blocked || len(lines) != 2 || lines[0] != "a2dlc verify: blocked: b" {
		t.Fatalf("%v", lines)
	}
	if lines, blocked = (VerifyReport{}).Lines(); blocked || len(lines) != 0 {
		t.Fatalf("%v", lines)
	}
}

// More than 10 files are summarized with "and N more".
func TestVerifyPolicyWarningCapsTheFileList(t *testing.T) {
	var oos []contract.OutOfScope
	for i := range 13 {
		oos = append(oos, contract.OutOfScope{File: "f" + string(rune('a'+i)) + ".md", Reason: "no_engine"})
	}
	s := unjudgedOf(contract.Verdict{OutOfScope: oos}).summary()
	if !strings.Contains(s, "13 files out of scope") || !strings.Contains(s, "and 3 more") {
		t.Fatal(s)
	}
}

// The default policy still refuses what the two "does not launder committed changes" tests are about: critical code
// pushed over a receipt that does not cover it.
func TestVerifyPolicyDoesNotWashCommittedChangesOnCriticalCode(t *testing.T) {
	a, _, dir := newRepo(t)
	withUpstream(t, dir)
	write(t, dir, "src/lib/f.ts", "export const f = 2\n")
	git(t, dir, "commit", "-qam", "change")
	if _, err := a.Check(context.Background(), "HEAD"); err != nil {
		t.Fatal(err)
	}
	rep := policy(t, a)
	if !has(rep.Blocked, "changes critical or protected code (src/lib/f.ts)") || !has(rep.Warnings, "does not cover the unpushed commits") {
		t.Fatalf("%+v", rep)
	}
}
