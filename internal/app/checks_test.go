package app

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// fakeChecks answers each check id with a canned response or error, and records what it received.
type fakeChecks struct {
	resp  map[string]contract.CheckResponse
	err   map[string]error
	files map[string][]string
}

func (f *fakeChecks) RunCheck(_ context.Context, c contract.CheckConfig, files []string, baseDir, candDir string) (engine.CheckRun, error) {
	if f.files == nil {
		f.files = map[string][]string{}
	}
	f.files[c.ID] = files
	if _, err := os.Stat(filepath.Join(candDir, "README.md")); err != nil {
		return engine.CheckRun{}, errors.New("the candidate was not exported")
	}
	if err := f.err[c.ID]; err != nil {
		return engine.CheckRun{}, err
	}
	return engine.CheckRun{Response: f.resp[c.ID]}, nil
}

func response(id string, pass bool, findings ...string) contract.CheckResponse {
	r := contract.CheckResponse{SchemaVersion: 1, ID: id, Status: contract.CheckRan, Pass: pass, Findings: []contract.CheckFinding{}}
	for _, f := range findings {
		r.Findings = append(r.Findings, contract.CheckFinding{Target: f, Message: "finding on " + f})
	}
	return r
}

// checksRepo is newRepo with config.checks committed on the base and README.md (no engine, no tests) changed.
func checksRepo(t *testing.T, checks string, fake *fakeChecks) (App, string) {
	t.Helper()
	a, _, dir := newRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"checks":`+checks+`}`)
	git(t, dir, "commit", "-qam", "checks")
	write(t, dir, "README.md", "changed\n")
	a.Checks = fake
	return a, dir
}

const blockingLint = `[{"id":"lint","image":"lint:1","command":"lint","severity":"blocking","matches":["**/*.md"]}]`

func findingsOf(v contract.Verdict, id string) []contract.VerdictCheckFinding {
	for _, c := range v.Checks {
		if c.ID == id {
			return c.FindingList()
		}
	}
	return nil
}

func kinds(items []contract.Escalation) string {
	var out []string
	for _, e := range items {
		out = append(out, e.Kind+"@"+e.Target)
	}
	return strings.Join(out, " ")
}

func TestChecksTruthTable(t *testing.T) {
	ok := response("lint", true)
	bad := response("lint", false, "README.md::L1")
	cases := []struct {
		name    string
		checks  string
		fake    *fakeChecks
		waivers string
		verdict string
		status  string
		esc     string
	}{
		{"blocking finding is FAIL", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": bad}}, "",
			contract.FAIL, contract.CheckRan, "check_failed@README.md::L1"},
		{"blocking finding with unsigned accept_finding is pending", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": bad}},
			`{"schema_version":1,"waivers":[{"target":"README.md::L1","decision":"accept_finding","reason":"on purpose"}]}`,
			contract.PASS, contract.CheckRan, "signature_pending@README.md::L1"},
		{"a waiver of another class does not cover the finding", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": bad}},
			`{"schema_version":1,"waivers":[{"target":"README.md::L1","decision":"enforce_new_behavior","reason":"wrong class"}]}`,
			contract.FAIL, contract.CheckRan, "check_failed@README.md::L1"},
		{"blocking pass is PASS", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": ok}}, "", contract.PASS, contract.CheckRan, ""},
		{"blocking error is INCONCLUSIVE", blockingLint, &fakeChecks{err: map[string]error{"lint": errors.New("the check exited 127")}}, "",
			contract.INCONCLUSIVE, contract.CheckError, "tooling@.a2dlc/config.json::checks.lint"},
		{"blocking timeout is INCONCLUSIVE", blockingLint, &fakeChecks{err: map[string]error{"lint": engine.ErrCheckTimeout}}, "",
			contract.INCONCLUSIVE, contract.CheckTimeout, "tooling@.a2dlc/config.json::checks.lint"},
		{"advisory finding is recorded only", strings.Replace(blockingLint, "blocking", "advisory", 1), &fakeChecks{resp: map[string]contract.CheckResponse{"lint": bad}}, "",
			contract.PASS, contract.CheckRan, ""},
		{"advisory error is recorded as error, never pass", strings.Replace(blockingLint, "blocking", "advisory", 1), &fakeChecks{err: map[string]error{"lint": errors.New("down")}}, "",
			contract.PASS, contract.CheckError, ""},
		{"no matching file is not_applicable", strings.Replace(blockingLint, "**/*.md", "**/*.py", 1), &fakeChecks{resp: map[string]contract.CheckResponse{"lint": bad}}, "",
			contract.PASS, contract.CheckNotApplicable, ""},
		{"a response for another id is an error", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("eslint", true)}}, "",
			contract.INCONCLUSIVE, contract.CheckError, "tooling@.a2dlc/config.json::checks.lint"},
		{"pass with findings is an error", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", true, "README.md::L1")}}, "",
			contract.INCONCLUSIVE, contract.CheckError, "tooling@.a2dlc/config.json::checks.lint"},
		{"a finding on a file the check did not receive is an error", blockingLint, &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "src/lib/f.ts::L1")}}, "",
			contract.INCONCLUSIVE, contract.CheckError, "tooling@.a2dlc/config.json::checks.lint"},
		{"without a runner every check is an error", blockingLint, nil, "", contract.INCONCLUSIVE, contract.CheckError, "tooling@.a2dlc/config.json::checks.lint"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var fake *fakeChecks
			if c.fake != nil {
				fake = c.fake
			}
			a, dir := checksRepo(t, c.checks, fake)
			if fake == nil {
				a.Checks = nil
			}
			if c.waivers != "" {
				write(t, dir, ".a2dlc/waivers.json", c.waivers)
			}
			v, err := a.Check(context.Background(), "HEAD")
			if err != nil {
				t.Fatal(err)
			}
			if v.Verdict != c.verdict || len(v.Checks) != 1 || v.Checks[0].Status != c.status {
				t.Fatalf("verdict=%s checks=%+v", v.Verdict, v.Checks)
			}
			if got := kinds(escalations(v)); got != c.esc {
				t.Fatalf("escalations %q, want %q", got, c.esc)
			}
			ran := c.status == contract.CheckRan
			if ran != (v.Checks[0].Pass != nil) || ran != (v.Checks[0].Findings != nil) {
				t.Fatalf("pass and findings are set exactly when the check ran: %+v", v.Checks[0])
			}
			if c.status == contract.CheckRan && fake != nil && strings.Join(fake.files["lint"], " ") != "README.md" {
				t.Fatalf("the check received %v", fake.files["lint"])
			}
		})
	}
}

// A check bound to a run stage does not fire on a2dlc check, and the reverse; a check with no fire_on fires on check.
func TestChecksFireOnBinding(t *testing.T) {
	checks := `[{"id":"lint","image":"lint:1","command":"lint","severity":"blocking","fire_on":["stage:4"]},
	            {"id":"policy","image":"policy:1","command":"policy","severity":"blocking"}]`
	fake := &fakeChecks{resp: map[string]contract.CheckResponse{"lint": response("lint", false, "README.md::lint"), "policy": response("policy", true)}}
	a, _ := checksRepo(t, checks, fake)
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.Checks) != 1 || v.Checks[0].ID != "policy" {
		t.Fatalf("check: %s %+v %v", v.Verdict, v.Checks, err)
	}
	v, err = a.CheckWith(context.Background(), "HEAD", CheckOptions{FireOn: "stage:4"})
	if err != nil || v.Verdict != contract.FAIL || len(v.Checks) != 1 || v.Checks[0].ID != "lint" {
		t.Fatalf("stage:4: %s %+v %v", v.Verdict, v.Checks, err)
	}
}

// A signed accept_finding turns the finding into a waived one that verify accepts; the same signature does not turn a
// blocking check that could not run into a PASS: an error is not a finding.
func TestSignedAcceptFindingCoversTheFindingNotAnError(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
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

	// Unsigned: check passes with the waiver pending and verify stops.
	v, err := a.Check(context.Background(), "HEAD")
	if err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 1 || !findingsOf(v, "lint")[0].Waived {
		t.Fatalf("unsigned: %s pending=%v checks=%+v err=%v", v.Verdict, v.PendingWaivers, v.Checks, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "unsigned")
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 1 || !strings.Contains(probs[0], "unsigned") {
		t.Fatalf("verify unsigned: %v %v", probs, err)
	}
	// Signed: nothing pending, verify passes.
	if err := a.Approve("README.md::lint", key, "me@a2dlc"); err != nil {
		t.Fatal(err)
	}
	if v, err = a.Check(context.Background(), "HEAD~1"); err != nil || v.Verdict != contract.PASS || len(v.PendingWaivers) != 0 {
		t.Fatalf("signed: %s pending=%v err=%v", v.Verdict, v.PendingWaivers, err)
	}
	git(t, dir, "add", "-A")
	git(t, dir, "commit", "-qm", "signed")
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 0 {
		t.Fatalf("verify signed: %v %v", probs, err)
	}
	// The same signed waiver, with the check down: INCONCLUSIVE, and verify refuses the verdict.
	fake.err = map[string]error{"lint": errors.New("image lint:1 not found")}
	if v, err = a.Check(context.Background(), "HEAD~2"); err != nil || v.Verdict != contract.INCONCLUSIVE || v.Checks[0].Status != contract.CheckError {
		t.Fatalf("check down: %s %+v err=%v", v.Verdict, v.Checks, err)
	}
	if probs, err := a.Verify("HEAD"); err != nil || len(probs) != 1 || !strings.Contains(probs[0], "INCONCLUSIVE") {
		t.Fatalf("verify check down: %v %v", probs, err)
	}
}
