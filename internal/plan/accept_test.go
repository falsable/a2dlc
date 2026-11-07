package plan

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

const testIntentSHA = "abababababababababababababababababababababababababababababababab"

// acceptAll is the acceptance of the tests that are about something else than who signed the plan.
func acceptAll(contract.Plan, string, string, string) (contract.PlanAcceptance, error) {
	return contract.PlanAcceptance{Kind: contract.AcceptedByApproval, At: time.Now().UTC().Format(time.RFC3339),
		Approval: &contract.PlanApproval{PlanSHA256: strings.Repeat("0", 64), BaseRef: strings.Repeat("0", 40), ExpiresAt: "2999-01-01T00:00:00Z", Principal: "test", Signature: "-----BEGIN SSH SIGNATURE-----\nx\n-----END SSH SIGNATURE-----\n"}}, nil
}

type signedPlan struct {
	t         *testing.T
	repoDir   string
	base      string
	key       string
	planBytes []byte
	run       func(planBytes []byte) (contract.PlanState, error)
	ran       func() int
}

// newSignedPlan is a repository with a plan of two slices, a key that the allowed signers list, and a runner that uses the
// real acceptance (DefaultAccept) over a scripted executor: nothing about who signed is faked.
func newSignedPlan(t *testing.T) signedPlan {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	repo, base := newRepo(t)
	p, f := graphPlan([][]int{{}, {0}})
	planBytes, _ := json.Marshal(p)
	keys := t.TempDir()
	key := filepath.Join(keys, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "me", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(keys, "allowed")
	os.WriteFile(signers, []byte("me@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600)
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	ex := &scripted{t: t, dir: repo.Dir, sawBases: map[string]string{}, outcome: map[string]string{"f.s0": OutPassed, "f.s1": OutPassed}}
	return signedPlan{t: t, repoDir: repo.Dir, base: base, key: key, planBytes: planBytes,
		run: func(b []byte) (contract.PlanState, error) {
			r := Runner{Repo: repo, Exec: ex, IntentSHA256: testIntentSHA, Final: func(context.Context, string, string) (bool, string, error) { return true, "", nil }}
			st, _, err := r.Run(context.Background(), b, f, base)
			return st, err
		},
		ran: func() int { return ex.n }}
}

func (s signedPlan) approve(t *testing.T) contract.PlanApproval {
	t.Helper()
	repo := gitxRepo(s.repoDir)
	ap, err := Approve(repo, s.planBytes, ApproveOptions{Key: s.key, Principal: "me@a2dlc", Expires: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ap
}

func (s signedPlan) authorize(t *testing.T, mutate func(*contract.Authorization)) {
	t.Helper()
	au := contract.Authorization{BaseRef: s.base, IntentSHA256: testIntentSHA, Targets: []string{PlanAcceptTarget("f")}, Decision: contract.PlanAccept,
		ExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339), Principal: "me@a2dlc"}
	if mutate != nil {
		mutate(&au)
	}
	sig, err := approval.SignAuthorization(s.key, approval.AuthorizationPayload(au.BaseRef, au.IntentSHA256, au.Decision, au.ExpiresAt, au.Targets))
	if err != nil {
		t.Fatal(err)
	}
	au.Signature = sig
	b, _ := json.MarshalIndent(contract.Authorizations{SchemaVersion: 1, Authorizations: []contract.Authorization{au}}, "", " ")
	if err := os.MkdirAll(filepath.Join(s.repoDir, ".a2dlc"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(s.repoDir, pathAuthorizations), b, 0o644)
}

// A plan nobody signed and no authorization covers does not run: no slice starts and no state is left.
func TestUnsignedPlanIsRefused(t *testing.T) {
	s := newSignedPlan(t)
	_, err := s.run(s.planBytes)
	if err == nil || !strings.Contains(err.Error(), "plan is not accepted") || s.ran() != 0 {
		t.Fatalf("err %v, %d runs", err, s.ran())
	}
	if _, err := os.Stat(filepath.Join(StateDir(s.repoDir, "f"), "state.json")); err == nil {
		t.Fatal("a refused plan left a state")
	}
}

// A plan signed by a person runs, and the state says it was accepted by that approval.
func TestSignedPlanIsAccepted(t *testing.T) {
	s := newSignedPlan(t)
	ap := s.approve(t)
	st, err := s.run(s.planBytes)
	if err != nil || st.Outcome == nil || *st.Outcome != "PASS" {
		t.Fatalf("outcome %v err %v", st.Outcome, err)
	}
	if st.Acceptance == nil || st.Acceptance.Kind != contract.AcceptedByApproval || st.Acceptance.Approval.Signature != ap.Signature {
		t.Fatalf("the state does not say what accepted the plan: %+v", st.Acceptance)
	}
	if problems, err := VerifyRun(s.repoDir, "f"); err != nil || len(problems) != 0 {
		t.Fatalf("verify-run of an accepted plan: %v %v", problems, err)
	}
}

// An authorization that covers plan.accept for this feature, intent and base stands in for the person's signature; one for
// another intent, another base or another feature, or one that expired, does not.
func TestAuthorizationThatCoversPlanAcceptIsAccepted(t *testing.T) {
	s := newSignedPlan(t)
	s.authorize(t, nil)
	st, err := s.run(s.planBytes)
	if err != nil || st.Acceptance == nil || st.Acceptance.Kind != contract.AcceptedByAuthorization {
		t.Fatalf("acceptance %+v err %v", st.Acceptance, err)
	}
	if problems, err := VerifyRun(s.repoDir, "f"); err != nil || len(problems) != 0 {
		t.Fatalf("verify-run: %v %v", problems, err)
	}
	for name, mutate := range map[string]func(*contract.Authorization){
		"another intent":  func(a *contract.Authorization) { a.IntentSHA256 = strings.Repeat("cd", 32) },
		"another base":    func(a *contract.Authorization) { a.BaseRef = strings.Repeat("1", 40) },
		"another feature": func(a *contract.Authorization) { a.Targets = []string{PlanAcceptTarget("other")} },
		"expired":         func(a *contract.Authorization) { a.ExpiresAt = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) },
		"a waiver decision": func(a *contract.Authorization) {
			a.Decision = contract.EnforceNewBehavior
			a.Targets = []string{"src/f.py::f"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			fresh := newSignedPlan(t)
			fresh.authorize(t, mutate)
			if _, err := fresh.run(fresh.planBytes); err == nil || !strings.Contains(err.Error(), "plan is not accepted") || fresh.ran() != 0 {
				t.Fatalf("err %v, %d runs", err, fresh.ran())
			}
		})
	}
}

// A plan edited after its approval, and an approval edited after signing, are refused.
func TestTamperedPlanIsRefused(t *testing.T) {
	s := newSignedPlan(t)
	s.approve(t)
	var p contract.Plan
	if err := json.Unmarshal(s.planBytes, &p); err != nil {
		t.Fatal(err)
	}
	p.Slices[0].Goal += " and also drop the table"
	edited, _ := json.Marshal(p)
	if _, err := s.run(edited); err == nil || !strings.Contains(err.Error(), "approves another plan") || s.ran() != 0 {
		t.Fatalf("an edited plan ran: err %v, %d runs", err, s.ran())
	}
	path := filepath.Join(StateDir(s.repoDir, "f"), approvalFile)
	b, _ := os.ReadFile(path)
	os.WriteFile(path, []byte(strings.Replace(string(b), `"expires_at": "`+time.Now().Add(24*time.Hour).UTC().Format("2006-01-02"), `"expires_at": "2099-01-01`, 1)), 0o644)
	if _, err := s.run(s.planBytes); err == nil || !strings.Contains(err.Error(), "plan is not accepted") || s.ran() != 0 {
		t.Fatalf("an approval edited after signing was accepted: err %v, %d runs", err, s.ran())
	}
}

// verify-run fails a plan run whose acceptance is missing, was altered, had expired when it was used, or is for another plan.
func TestVerifyRunRejectsAnAcceptanceThatIsMissingTamperedExpiredOrForeign(t *testing.T) {
	s := newSignedPlan(t)
	s.approve(t)
	if _, err := s.run(s.planBytes); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(StateDir(s.repoDir, "f"), "state.json")
	original, _ := os.ReadFile(statePath)
	acceptance := func(doc map[string]any) map[string]any { return doc["acceptance"].(map[string]any) }
	cases := map[string]struct {
		edit func(map[string]any)
		want string
	}{
		"missing": {func(d map[string]any) { delete(d, "acceptance") }, "records no acceptance"},
		"tampered": {func(d map[string]any) {
			acceptance(d)["approval"].(map[string]any)["expires_at"] = "2099-01-01T00:00:00Z"
		}, "acceptance: approval"},
		"expired when used": {func(d map[string]any) {
			acceptance(d)["at"] = time.Now().Add(10 * 24 * time.Hour).UTC().Format(time.RFC3339)
		}, "expired"},
		"for another plan": {func(d map[string]any) { d["plan_sha256"] = strings.Repeat("0", 64) }, "approves another plan"},
		"another base":     {func(d map[string]any) { d["base_sha"] = strings.Repeat("1", 40) }, "base"},
		"two records":      {func(d map[string]any) { acceptance(d)["authorization"] = map[string]any{} }, "does not match what the record holds"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			var doc map[string]any
			if err := json.Unmarshal(original, &doc); err != nil {
				t.Fatal(err)
			}
			c.edit(doc)
			b, _ := json.MarshalIndent(doc, "", " ")
			os.WriteFile(statePath, b, 0o644)
			problems, err := VerifyRun(s.repoDir, "f")
			if name == "two records" && err != nil { // an empty authorization does not even decode: also a rejection
				return
			}
			if err != nil || !strings.Contains(strings.Join(problems, "\n"), c.want) {
				t.Fatalf("want a problem with %q, got %v (err %v)", c.want, problems, err)
			}
		})
	}
}

func gitxRepo(dir string) gitx.Repo { return gitx.Repo{Dir: dir} }
