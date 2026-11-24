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
)

type av2Keys struct {
	key       string
	otherKey  string
	signers   string
	principal string
	baseSha   string
	repoDir   string
	app       app.App
	launcher  *fakeLauncher
	opts      Options
	runDir    string
}

func setupAV2(t *testing.T, slice string) av2Keys {
	t.Helper()
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	a, launcherFake, opts, runDir := prepareGreenfield(t, slice)
	keysDir := t.TempDir()
	key := filepath.Join(keysDir, "trusted")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "human", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen trusted: %v %s", err, out)
	}
	otherKey := filepath.Join(keysDir, "untrusted")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "intruder", "-f", otherKey).CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen untrusted: %v %s", err, out)
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	signers := filepath.Join(keysDir, "allowed_signers")
	if err := os.WriteFile(signers, []byte("human@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("A2DLC_ALLOWED_SIGNERS", signers)
	baseSha, err := a.Repo.ResolveCommit(a.DefaultBase())
	if err != nil {
		t.Fatal(err)
	}
	return av2Keys{
		key:       key,
		otherKey:  otherKey,
		signers:   signers,
		principal: "human@a2dlc",
		baseSha:   baseSha,
		repoDir:   a.Repo.Dir,
		app:       a,
		launcher:  launcherFake,
		opts:      opts,
		runDir:    runDir,
	}
}

func writeSignedContractEnvelope(t *testing.T, dir, name string, s contract.SliceContract, key, principal, sliceID, baseSha string) (string, string) {
	t.Helper()
	raw, err := json.MarshalIndent(s, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	cPath := filepath.Join(dir, name)
	if err := os.WriteFile(cPath, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := sha256Hex(raw)
	payload := approval.ContractPayload(sha, sliceID, baseSha)
	sig, err := approval.SignContract(key, payload)
	if err != nil {
		t.Fatal(err)
	}
	acc := ContractAcceptance{
		SliceID:        sliceID,
		BaseRef:        baseSha,
		ContractSHA256: sha,
		Principal:      principal,
		Signature:      sig,
	}
	sb, err := json.MarshalIndent(acc, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	sigPath := cPath + ".sig"
	if err := os.WriteFile(sigPath, append(sb, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	return cPath, sigPath
}

func TestApprovalContractNamespaceAndPayload(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("no ssh-keygen")
	}
	if approval.ContractNamespace != "a2dlc-contract" {
		t.Fatalf("ContractNamespace = %q, want a2dlc-contract", approval.ContractNamespace)
	}
	dir := t.TempDir()
	key := filepath.Join(dir, "k")
	if out, err := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-C", "tester", "-f", key).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	pub, _ := os.ReadFile(key + ".pub")
	signers := filepath.Join(dir, "allowed")
	if err := os.WriteFile(signers, []byte("tester@a2dlc "+strings.TrimSpace(string(pub))+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("a", 64)
	base := strings.Repeat("b", 40)
	p := approval.ContractPayload(sha, "calc.media.v1", base)
	want := "a2dlc-contract/1\n" + sha + "\ncalc.media.v1\n" + base + "\n"
	if string(p) != want {
		t.Fatalf("payload = %q, want %q", string(p), want)
	}
	sig, err := approval.SignContract(key, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := approval.VerifyContract(signers, "tester@a2dlc", p, sig); err != nil {
		t.Fatalf("valid contract signature rejected: %v", err)
	}
	if err := approval.VerifyContract(signers, "tester@a2dlc", approval.ContractPayload(sha, "other.slice", base), sig); err == nil {
		t.Fatal("contract signature verified for different slice_id")
	}
	if err := approval.VerifyContract(signers, "tester@a2dlc", approval.ContractPayload(sha, "calc.media.v1", strings.Repeat("c", 40)), sig); err == nil {
		t.Fatal("contract signature verified for different base_ref")
	}
	// Cross-namespace signatures must be rejected.
	if err := approval.VerifyPlan(signers, "tester@a2dlc", p, sig); err == nil {
		t.Fatal("contract signature verified as plan")
	}
	if err := approval.Verify(signers, "tester@a2dlc", p, sig); err == nil {
		t.Fatal("contract signature verified as approval")
	}
	planSig, err := approval.SignPlan(key, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := approval.VerifyContract(signers, "tester@a2dlc", p, planSig); err == nil {
		t.Fatal("plan signature verified as contract")
	}
	verdictSig, err := approval.SignVerdict(key, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := approval.VerifyContract(signers, "tester@a2dlc", p, verdictSig); err == nil {
		t.Fatal("verdict signature verified as contract")
	}
}

func TestAcceptContractResolvesSpecDivergenceAndPreservesOnResume(t *testing.T) {
	env := setupAV2(t, "calc.media.v1")
	env.launcher.script = scriptSpec(t, 0) // Stage 1 diverges persistently
	opts := env.opts
	opts.MaxSpecRounds = 1
	st, err := Run(context.Background(), env.app, env.launcher, opts)
	if err != nil {
		t.Fatal(err)
	}
	if st.Stages[2].Status != contract.StageEscalated || len(env.launcher.seen) != 3 {
		t.Fatalf("expected Stage 1 escalation after 3 role calls, got %+v seen=%d", st.Stages[2], len(env.launcher.seen))
	}

	// Sign an accepted contract outside the repo (originally source "consensus", converted to "human_accepted" on acceptance).
	accepted := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
	cPath, _ := writeSignedContractEnvelope(t, t.TempDir(), "accepted.json", accepted, env.key, env.principal, "calc.media.v1", env.baseSha)

	env.launcher.seen = nil
	opts.AcceptContract = cPath
	st, err = Run(context.Background(), env.app, env.launcher, opts)
	if err != nil {
		t.Fatal(err)
	}
	s1 := byID(st)["1"]
	if s1.Status != contract.StageDone || s1.Artifact == nil || s1.Artifact.Path != ArtContract {
		t.Fatalf("Stage 1 did not close with accepted contract: %+v", s1)
	}
	if len(env.launcher.seen) != 0 {
		t.Fatalf("Stage 1 launched roles despite --accept-contract: %v", env.launcher.seen)
	}
	if st.Outcome == nil || *st.Outcome != contract.PASS {
		t.Fatalf("expected PASS outcome, got %v", st.Outcome)
	}

	// Verify contract.json has source "human_accepted" and probes.json matches its hash.
	var saved contract.SliceContract
	cb, err := os.ReadFile(filepath.Join(env.runDir, ArtContract))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(cb, &saved); err != nil || saved.Validate() != nil || saved.Source != contract.SourceHumanAccepted {
		t.Fatalf("saved contract invalid or wrong source: err=%v contract=%+v", err, saved)
	}
	contractHash, err := HashFile(filepath.Join(env.runDir, ArtContract))
	if err != nil {
		t.Fatal(err)
	}
	if contractHash != s1.Artifact.SHA256 {
		t.Fatalf("Stage 1 artifact hash %s != contract.json hash %s", s1.Artifact.SHA256, contractHash)
	}
	var probes contract.Probes
	pb, err := os.ReadFile(filepath.Join(env.runDir, ArtProbes))
	if err != nil {
		t.Fatal(err)
	}
	if err := contract.DecodeStrictBytes(pb, &probes); err != nil || probes.ContractHash != contractHash || len(probes.Probes) != 1 {
		t.Fatalf("probes.json mismatch: err=%v probes=%+v wantHash=%s", err, probes, contractHash)
	}
	if probs, err := VerifyRun(env.app, "calc.media.v1"); err != nil || len(probs) != 0 {
		t.Fatalf("VerifyRun failed: err=%v probs=%v", err, probs)
	}

	// Resume without --accept-contract: Stage 1 must NOT regenerate contract.json or launch specifiers.
	opts.AcceptContract = ""
	st2, err := Run(context.Background(), env.app, env.launcher, opts)
	if err != nil {
		t.Fatal(err)
	}
	if len(env.launcher.seen) != 0 {
		t.Fatalf("resume launched roles: %v", env.launcher.seen)
	}
	if *byID(st2)["1"].StartedAt != *s1.StartedAt || byID(st2)["1"].Artifact.SHA256 != contractHash {
		t.Fatalf("resume re-ran Stage 1: before=%+v after=%+v", s1, byID(st2)["1"])
	}
	afterBytes, _ := os.ReadFile(filepath.Join(env.runDir, ArtContract))
	if string(afterBytes) != string(cb) {
		t.Fatal("contract.json bytes changed on resume")
	}
}

func TestAcceptContractResolvesBacktranslationAndUnsatAndPreservesOnRedo(t *testing.T) {
	t.Run("backtranslation_mismatch", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		write(t, env.repoDir, ".a2dlc/roles.json", strings.Replace(rolesSpec, `"inquisitor"`, `"back_translator":{"family":"mistral","image":"bt:1","argv":["x"],"retries":0},"inquisitor"`, 1))
		var bt contract.InquisitorBTRequest
		matches := false
		env.launcher.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
			if s.Stage == "1.5" && s.Role == contract.RoleInquisitor {
				payloadOf(t, s, &bt)
				r := contract.InquisitorBTResponse{Matches: matches, Discrepancies: []contract.BTDiscrepancy{}}
				if !matches {
					r.Discrepancies = []contract.BTDiscrepancy{{Clause: "NaN", IntentSays: "reject NaN", ContractImplies: "unspecified"}}
				}
				return ok(t, s, r), 0, false
			}
			return scriptBT(t, matches, &bt)(s)
		}
		st, err := Run(context.Background(), env.app, env.launcher, env.opts)
		if err != nil {
			t.Fatal(err)
		}
		if byID(st)["1"].Status != contract.StageDone || byID(st)["1.5"].Status != contract.StageEscalated {
			t.Fatalf("expected Stage 1 done and Stage 1.5 escalated, got %+v %+v", byID(st)["1"], byID(st)["1.5"])
		}

		// Fix the contract, sign it with source: human_accepted, and re-run with AcceptContract.
		fixed := contractOf(contract.SourceHumanAccepted, "raises ValueError with empty xs or NaN")
		cPath, sigPath := writeSignedContractEnvelope(t, t.TempDir(), "fixed.json", fixed, env.key, env.principal, "calc.media.v1", env.baseSha)
		matches = true
		env.launcher.seen = nil
		opts := env.opts
		opts.AcceptContract = cPath
		opts.AcceptContractSig = sigPath
		st, err = Run(context.Background(), env.app, env.launcher, opts)
		if err != nil {
			t.Fatal(err)
		}
		// Only Stage 1.5 back_translator + inquisitor should launch (no Stage 1 specifiers).
		if len(env.launcher.seen) != 2 {
			t.Fatalf("expected 2 Stage 1.5 role calls (no Stage 1 specifiers), got %d: %v", len(env.launcher.seen), env.launcher.seen)
		}
		if byID(st)["1"].Status != contract.StageDone || byID(st)["1.5"].Status != contract.StageDone || *st.Outcome != contract.PASS {
			t.Fatalf("unexpected state: s1=%+v s1.5=%+v outcome=%v", byID(st)["1"], byID(st)["1.5"], st.Outcome)
		}
		var saved contract.SliceContract
		b, _ := os.ReadFile(filepath.Join(env.runDir, ArtContract))
		if err := contract.DecodeStrictBytes(b, &saved); err != nil || saved.Source != contract.SourceHumanAccepted || saved.Functions[0].Post[0] != "raises ValueError with empty xs or NaN" {
			t.Fatalf("contract not updated cleanly: %v %+v", err, saved)
		}
	})

	t.Run("unsat_spec_and_stage_minus1_redo_preservation", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		env.opts.Mode = contract.ModeBrownfield
		env.opts.Intent = intentFile(t, func(in *contract.Intent) {
			in.SliceID, in.Mode, in.Goal = "calc.media.v1", contract.ModeBrownfield, "mean"
		})
		unsatContract := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
		unsatContract.Functions[0].SMT = []string{"(declare-const x Int)", "(assert (> x 0))", "(assert (< x 0))"}
		env.launcher.script = func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
			if s.Role == contract.RoleInquisitor {
				return ok(t, s, contract.InquisitorResponse{Contract: unsatContract, Discrepancies: []contract.SpecDiscrepancy{}, OpenAssumptions: []contract.Assumption{}}), 0, false
			}
			return scriptSpec(t, 1)(s)
		}
		z := &fakeSolver{states: map[string]string{"mean": contract.Z3Unsat}}
		opts := env.opts
		opts.Z3 = z
		st, err := Run(context.Background(), env.app, env.launcher, opts)
		if err != nil {
			t.Fatal(err)
		}
		if byID(st)["1.5"].Status != contract.StageEscalated || !strings.Contains(byID(st)["1.5"].Reason, "CB-02a") {
			t.Fatalf("expected CB-02a escalation, got %+v", byID(st)["1.5"])
		}

		// Fix SMT, sign with raw .sig sidecar via SignContract + --accept-contract-sig.
		satContract := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
		satContract.Functions[0].SMT = []string{"(declare-const x Int)", "(assert (> x 0))"}
		raw, _ := json.MarshalIndent(satContract, "", " ")
		raw = append(raw, '\n')
		tmpDir := t.TempDir()
		cPath := filepath.Join(tmpDir, "sat.json")
		sigPath := filepath.Join(tmpDir, "detached.sig")
		if err := os.WriteFile(cPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		rawSig, err := approval.SignContract(env.key, approval.ContractPayload(sha256Hex(raw), "calc.media.v1", env.baseSha))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sigPath, []byte(rawSig), 0o644); err != nil {
			t.Fatal(err)
		}

		z.states["mean"] = contract.Z3Sat
		env.launcher.seen = nil
		opts.AcceptContract = cPath
		opts.AcceptContractSig = sigPath
		st, err = Run(context.Background(), env.app, env.launcher, opts)
		if err != nil {
			t.Fatal(err)
		}
		if byID(st)["1"].Status != contract.StageDone || byID(st)["1.5"].Status != contract.StageDone || *st.Outcome != contract.PASS || len(env.launcher.seen) != 0 {
			t.Fatalf("expected PASS with 0 role launches, got s1=%+v s1.5=%+v outcome=%v seen=%v", byID(st)["1"], byID(st)["1.5"], st.Outcome, env.launcher.seen)
		}

		// Force Stage -1 redo (making Stage -1 skip stale by adding baseline_command and a fake test runner) WITHOUT --accept-contract:
		// Stage 1 must preserve the human-accepted contract without launching Stage 1 roles.
		write(t, env.repoDir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"out_of_scope_paths":["src/**"],"tests":{"image":"x","paths":["tests/**"],"command":"run","baseline_command":"run"}}`)
		write(t, env.repoDir, "tests/test_base.t", "base passed\n")
		env.app.Tests = &fakeRunner{}
		opts.AcceptContract = ""
		opts.AcceptContractSig = ""
		st, err = Run(context.Background(), env.app, env.launcher, opts)
		if err != nil {
			t.Fatal(err)
		}
		if len(env.launcher.seen) != 0 || byID(st)["1"].Status != contract.StageDone {
			t.Fatalf("Stage 1 redo launched roles or failed: seen=%v s1=%+v", env.launcher.seen, byID(st)["1"])
		}
		var saved contract.SliceContract
		b, _ := os.ReadFile(filepath.Join(env.runDir, ArtContract))
		if err := contract.DecodeStrictBytes(b, &saved); err != nil || saved.Source != contract.SourceHumanAccepted {
			t.Fatalf("preserved contract lost human_accepted source: %v %+v", err, saved)
		}
		if probs, err := VerifyRun(env.app, "calc.media.v1"); err != nil || len(probs) != 0 {
			t.Fatalf("VerifyRun failed after redo preservation: %v %v", err, probs)
		}
	})
}

func TestAcceptContractSelfContainedEnvelope(t *testing.T) {
	env := setupAV2(t, "calc.media.v1")
	accepted := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
	rawC, _ := json.Marshal(accepted)
	sha := sha256Hex(rawC)
	sig, err := approval.SignContract(env.key, approval.ContractPayload(sha, "calc.media.v1", env.baseSha))
	if err != nil {
		t.Fatal(err)
	}
	bundle := contractEnvelope{
		SliceID:        "calc.media.v1",
		BaseRef:        env.baseSha,
		ContractSHA256: sha,
		Principal:      env.principal,
		Signature:      sig,
		Contract:       rawC,
	}
	b, _ := json.MarshalIndent(bundle, "", " ")
	envPath := filepath.Join(t.TempDir(), "bundle.json")
	if err := os.WriteFile(envPath, append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := env.opts
	opts.AcceptContract = envPath
	st, err := Run(context.Background(), env.app, env.launcher, opts)
	if err != nil {
		t.Fatal(err)
	}
	if byID(st)["1"].Status != contract.StageDone || *st.Outcome != contract.PASS || len(env.launcher.seen) != 0 {
		t.Fatalf("envelope run failed: s1=%+v outcome=%v seen=%v", byID(st)["1"], st.Outcome, env.launcher.seen)
	}
}

func TestAcceptContractFailsClosed(t *testing.T) {
	baseContract := func(slice string) contract.SliceContract {
		c := contractOf(contract.SourceConsensus, "raises ValueError with empty xs")
		c.SliceID = slice
		return c
	}

	t.Run("unsigned_missing_sig", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		raw, _ := json.MarshalIndent(baseContract("calc.media.v1"), "", " ")
		cPath := filepath.Join(t.TempDir(), "unsigned.json")
		if err := os.WriteFile(cPath, raw, 0o644); err != nil {
			t.Fatal(err)
		}
		opts := env.opts
		opts.AcceptContract = cPath
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("unsigned contract was accepted")
		}
		if _, err := os.Stat(filepath.Join(env.runDir, ArtContract)); err == nil {
			t.Fatal("contract.json written on rejected acceptance")
		}
	})

	t.Run("tampered_contract_bytes", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		cPath, _ := writeSignedContractEnvelope(t, t.TempDir(), "c.json", baseContract("calc.media.v1"), env.key, env.principal, "calc.media.v1", env.baseSha)
		b, _ := os.ReadFile(cPath)
		tampered := strings.Replace(string(b), "raises ValueError with empty xs", "returns 0.0", 1)
		if err := os.WriteFile(cPath, []byte(tampered), 0o644); err != nil {
			t.Fatal(err)
		}
		opts := env.opts
		opts.AcceptContract = cPath
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("tampered contract was accepted")
		}
	})

	t.Run("untrusted_key_and_empty_allowed_signers", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		cPath, _ := writeSignedContractEnvelope(t, t.TempDir(), "c.json", baseContract("calc.media.v1"), env.otherKey, "intruder@a2dlc", "calc.media.v1", env.baseSha)
		opts := env.opts
		opts.AcceptContract = cPath
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("contract signed by untrusted key was accepted")
		}
		// Trusted key, but empty allowed_signers.
		cPath2, _ := writeSignedContractEnvelope(t, t.TempDir(), "c2.json", baseContract("calc.media.v1"), env.key, env.principal, "calc.media.v1", env.baseSha)
		emptySigners := filepath.Join(t.TempDir(), "empty_allowed")
		_ = os.WriteFile(emptySigners, []byte(""), 0o600)
		t.Setenv("A2DLC_ALLOWED_SIGNERS", emptySigners)
		opts.AcceptContract = cPath2
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("contract accepted with empty allowed_signers")
		}
		t.Setenv("A2DLC_ALLOWED_SIGNERS", filepath.Join(t.TempDir(), "nonexistent"))
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("contract accepted with missing allowed_signers")
		}
	})

	t.Run("wrong_base_ref", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		wrongBase := strings.Repeat("f", 40)
		cPath, _ := writeSignedContractEnvelope(t, t.TempDir(), "c.json", baseContract("calc.media.v1"), env.key, env.principal, "calc.media.v1", wrongBase)
		opts := env.opts
		opts.AcceptContract = cPath
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil || !strings.Contains(err.Error(), "base_ref") {
			t.Fatalf("expected base_ref refusal, got %v", err)
		}
		// Also test raw .sig signed against another base commit.
		raw, _ := json.Marshal(baseContract("calc.media.v1"))
		c2 := filepath.Join(t.TempDir(), "raw.json")
		_ = os.WriteFile(c2, raw, 0o644)
		sig, err := approval.SignContract(env.key, approval.ContractPayload(sha256Hex(raw), "calc.media.v1", wrongBase))
		if err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(c2+".sig", []byte(sig), 0o644)
		opts.AcceptContract = c2
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("raw signature for wrong base_ref was accepted")
		}
	})

	t.Run("wrong_slice_id", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		cPath, _ := writeSignedContractEnvelope(t, t.TempDir(), "c.json", baseContract("other.slice.v1"), env.key, env.principal, "other.slice.v1", env.baseSha)
		opts := env.opts
		opts.AcceptContract = cPath
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil || !strings.Contains(err.Error(), "slice_id") {
			t.Fatalf("expected slice_id refusal, got %v", err)
		}
		// Contract has matching slice_id, but signature envelope was signed for another slice.
		cPath2, _ := writeSignedContractEnvelope(t, t.TempDir(), "c2.json", baseContract("calc.media.v1"), env.key, env.principal, "other.slice.v1", env.baseSha)
		opts.AcceptContract = cPath2
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil || !strings.Contains(err.Error(), "slice_id") {
			t.Fatalf("expected signature slice_id refusal, got %v", err)
		}
	})

	t.Run("cross_namespace_and_spoofed_on_disk", func(t *testing.T) {
		env := setupAV2(t, "calc.media.v1")
		raw, _ := json.Marshal(baseContract("calc.media.v1"))
		cPath := filepath.Join(t.TempDir(), "c.json")
		_ = os.WriteFile(cPath, raw, 0o644)
		wrongNsSig, err := approval.SignPlan(env.key, approval.ContractPayload(sha256Hex(raw), "calc.media.v1", env.baseSha))
		if err != nil {
			t.Fatal(err)
		}
		acc := ContractAcceptance{SliceID: "calc.media.v1", BaseRef: env.baseSha, ContractSHA256: sha256Hex(raw), Principal: env.principal, Signature: wrongNsSig}
		sb, _ := json.Marshal(acc)
		_ = os.WriteFile(cPath+".sig", sb, 0o644)
		opts := env.opts
		opts.AcceptContract = cPath
		if _, err := Run(context.Background(), env.app, env.launcher, opts); err == nil {
			t.Fatal("plan-namespace signature was accepted for contract")
		}

		// Unsigned spoofed contract.json with source "human_accepted" dropped directly into the run dir must fail VerifyRun and preserveAcceptedContract.
		spoofed := baseContract("calc.media.v1")
		spoofed.Source = contract.SourceHumanAccepted
		m, err := open(env.app, env.launcher, env.opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.closeContract(spoofed); err != nil {
			t.Fatal(err)
		}
		if _, _, _, _, err := m.preserveAcceptedContract(); err == nil {
			t.Fatal("preserveAcceptedContract preserved unsigned human_accepted contract")
		}
		probs := acceptedContractProblems(m.dir, m.state)
		if len(probs) == 0 {
			t.Fatal("acceptedContractProblems did not reject unsigned human_accepted contract")
		}
	})
}
