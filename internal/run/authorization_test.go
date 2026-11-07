package run

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

const (
	authTarget  = "src/f.py::f"
	authIntent  = "{\"change\":\"f now adds one\"}\n"
	authExpires = "2026-11-12T00:00:00Z"
)

// divergingEngine reports a behavior change in every changed file.
type divergingEngine struct{ engine.Runner }

func (divergingEngine) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.Response, error) {
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{}}
	for _, f := range req.ChangedFiles {
		r.Targets = append(r.Targets, contract.TargetResult{Target: f + "::f", Engine: "hypothesis", EngineVersion: "fake", Verdict: contract.FAIL,
			SearchBudget: []byte(`{"examples":1,"seed":1}`), Generators: "typed_default", Equivalence: "strict",
			Counterexample: &contract.Counterexample{Args: "[1]", Old: "1", New: "2"}})
	}
	return r, nil
}

type authRun struct {
	a       app.App
	dir     string
	key     string
	signers string
	m       *Machine
}

// newAuthRun is a repo with a python engine that reports a divergence in src/f.py, a waiver for it, a key the allowed
// signers list, and a machine whose slice folder holds the intent the authorization is signed against.
func newAuthRun(t *testing.T, authorize bool) authRun {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a, dir := repoLite(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"python","image":"x","paths":["src/**"],"budget":{}}]}`)
	// repoLite leaves the change of src/f.py in the work tree: commit the config only, so the change stays the candidate
	git(t, dir, "add", ".a2dlc/config.json")
	git(t, dir, "commit", "-qm", "engine")
	a.Runner = divergingEngine{}
	write(t, dir, ".a2dlc/waivers.json", `{"schema_version":1,"waivers":[{"target":"`+authTarget+`","decision":"enforce_new_behavior","reason":"on purpose"}]}`)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "me", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	m, err := open(a, &fakeLauncher{}, Options{Slice: "auth"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir, ArtIntent), []byte(authIntent), 0o644); err != nil {
		t.Fatal(err)
	}
	if authorize {
		base := git(t, dir, "rev-parse", "HEAD")
		au := contract.Authorization{BaseRef: base, IntentSHA256: app.IntentHash([]byte(authIntent)), Targets: []string{authTarget},
			Decision: "enforce_new_behavior", ExpiresAt: authExpires, Principal: "me@a2dlc"}
		sig, err := approval.SignAuthorization(key, approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets))
		if err != nil {
			t.Fatal(err)
		}
		au.Signature = sig
		b, _ := jsonIndent(contract.Authorizations{SchemaVersion: 1, Authorizations: []contract.Authorization{au}})
		write(t, dir, ".a2dlc/authorizations.json", string(b))
	}
	return authRun{a: a, dir: dir, key: key, signers: signers, m: m}
}

func (r authRun) check(t *testing.T) contract.Verdict {
	t.Helper()
	res, err := r.m.checkWith(context.Background(), "4", app.CheckOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

// A waived divergence that a signed authorization covers does not stop the run: no escalation, and the ledger says which
// authorization covered which target at which stage.
func TestRunDoesNotStopForAnAuthorizedWaiver(t *testing.T) {
	r := newAuthRun(t, true)
	res := r.check(t)
	if res.Verdict != contract.PASS || len(res.PendingWaivers) != 0 || len(app.Escalations(res)) != 0 {
		t.Fatalf("verdict %s pending %v escalations %v", res.Verdict, res.PendingWaivers, app.Escalations(res))
	}
	used := ledgerOf(t, r.m.dir).AuthorizationsUsed
	if len(used) != 1 || used[0].Target != authTarget || used[0].Stage != "4" || used[0].Authorization.Principal != "me@a2dlc" {
		t.Fatalf("the ledger does not say what covered the step: %+v", used)
	}
	// the same check again (a Saboteur round) records the same use once
	r.check(t)
	if used := ledgerOf(t, r.m.dir).AuthorizationsUsed; len(used) != 1 {
		t.Fatalf("a second check duplicated the record: %+v", used)
	}
}

// Without an authorization the step escalates as it always did, and the ledger records nothing.
func TestRunStopsForAWaiverNoAuthorizationCovers(t *testing.T) {
	r := newAuthRun(t, false)
	res := r.check(t)
	esc := app.Escalations(res)
	if len(res.PendingWaivers) != 1 || len(esc) != 1 || esc[0].Kind != "signature_pending" {
		t.Fatalf("pending %v escalations %+v", res.PendingWaivers, esc)
	}
	if _, err := os.Stat(filepath.Join(r.m.dir, "ledger.json")); err == nil {
		t.Fatalf("a check that relied on nothing wrote a ledger: %+v", ledgerOf(t, r.m.dir))
	}
}

// verify-run checks again every authorization a run relied on, against the allowed signers and the time of use.
func TestVerifyRunRejectsAnAuthorizationThatIsNoLongerValid(t *testing.T) {
	r := newAuthRun(t, true)
	r.check(t)
	if err := r.m.save(); err != nil {
		t.Fatal(err)
	}
	problems, err := VerifyRun(r.a, "auth")
	if err != nil || len(problems) != 0 {
		t.Fatalf("a valid run: %v %v", problems, err)
	}
	ledgerPath := filepath.Join(r.m.dir, "ledger.json")
	original, _ := os.ReadFile(ledgerPath)
	edit := func(from, to string) {
		t.Helper()
		if !strings.Contains(string(original), from) {
			t.Fatalf("the ledger has no %q", from)
		}
		os.WriteFile(ledgerPath, []byte(strings.Replace(string(original), from, to, 1)), 0o644)
	}
	cases := map[string]func(){
		"a text edited after signing": func() { edit(authExpires, "2027-01-01T00:00:00Z") },
		"an expiry before the use": func() {
			// the time of use in the record, the entry that binds it keeps the old one
			i := strings.Index(string(original), `"authorizations_used"`)
			day := r.m.now().UTC().Format("2006-01-02")
			os.WriteFile(ledgerPath, []byte(string(original)[:i]+strings.Replace(string(original)[i:], day, "2030-01-01", 1)), 0o644)
		},
		"a signer no longer allowed": func() {
			os.WriteFile(ledgerPath, original, 0o644)
			empty := filepath.Join(t.TempDir(), "allowed")
			os.WriteFile(empty, []byte(""), 0o600)
			t.Setenv("A2DLC_ALLOWED_SIGNERS", empty)
		},
		"no allowed signers at all": func() {
			os.WriteFile(ledgerPath, original, 0o644)
			t.Setenv("A2DLC_ALLOWED_SIGNERS", filepath.Join(t.TempDir(), "missing"))
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			defer t.Setenv("A2DLC_ALLOWED_SIGNERS", r.signers)
			setup()
			problems, err := VerifyRun(r.a, "auth")
			if err != nil || len(problems) == 0 || !strings.Contains(strings.Join(problems, "\n"), "authorization") {
				t.Fatalf("problems %v err %v", problems, err)
			}
		})
	}
}

// The authorization that covered a step is bound into the chained entry of that step: a ledger that deletes or edits the
// record of its use no longer verifies, with a chain error and not only with an authorization one.
func TestVerifyRunRejectsAHiddenOrEditedAuthorizationUse(t *testing.T) {
	r := newAuthRun(t, true)
	r.check(t)
	// a launch after the check, so the use is in the middle of the chain as in a real run
	if _, err := r.m.ledger.Append(contract.LedgerEntry{At: "2026-11-07T10:00:00Z", Stage: "4", Role: "saboteur", Attempt: 1, Family: "claude",
		Image: "x", ArgvSHA256: hashBytes([]byte("argv")), Network: "none", Status: contract.LedgerOK}); err != nil {
		t.Fatal(err)
	}
	if err := r.m.save(); err != nil {
		t.Fatal(err)
	}
	if problems, err := VerifyRun(r.a, "auth"); err != nil || len(problems) != 0 {
		t.Fatalf("a valid run: %v %v", problems, err)
	}
	ledgerPath := filepath.Join(r.m.dir, "ledger.json")
	original, _ := os.ReadFile(ledgerPath)
	cases := map[string]func(map[string]any){
		"the records deleted": func(doc map[string]any) { delete(doc, "authorizations_used") },
		"the records emptied": func(doc map[string]any) { doc["authorizations_used"] = []any{} },
		"the time of use edited": func(doc map[string]any) {
			doc["authorizations_used"].([]any)[0].(map[string]any)["at"] = "2026-11-07T09:00:00Z"
		},
		"the stage edited": func(doc map[string]any) {
			doc["authorizations_used"].([]any)[0].(map[string]any)["stage"] = "5"
		},
		"the binding of the entry edited": func(doc map[string]any) {
			for _, e := range doc["entries"].([]any) {
				if bound, ok := e.(map[string]any)["authorization"].(map[string]any); ok {
					bound["id"] = "sha256:" + strings.Repeat("0", 64)
				}
			}
		},
		"the binding of the entry deleted": func(doc map[string]any) {
			for _, e := range doc["entries"].([]any) {
				delete(e.(map[string]any), "authorization")
			}
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(original, &doc); err != nil {
				t.Fatal(err)
			}
			edit(doc)
			b, _ := json.MarshalIndent(doc, "", " ")
			os.WriteFile(ledgerPath, b, 0o644)
			problems, err := VerifyRun(r.a, "auth")
			if err != nil || !strings.Contains(strings.Join(problems, "\n"), "ledger: ") {
				t.Fatalf("a ledger that hides or edits the use of an authorization must fail the chain: %v %v", problems, err)
			}
		})
	}
}
