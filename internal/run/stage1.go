package run

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/approval"
	"github.com/falsable/a2dlc/internal/contract"
)

// Stage 1: specification (protocol §5). It leaves S* in contract.json and its probes bound to the hash of S* in probes.json.
//   - lite: there is no S*.
//   - refactor: the legacy is the oracle. The capture is deterministic and done by the core, without LLM: the tests of the
//     base that name the changed sources and the public signatures (capture.json); S* is the minimal contractJSON that comes
//     out of the signatures with strict π_S.
//   - greenfield and brownfield_facade: dual-specifier divergence. α and β each translate the intent on their own side
//     (they do not see the other's contractJSON, policy.go); the Inquisitor sees both candidates and the catalogue of
//     assumptions and returns the consensus S*, the discrepancies and the open assumptions. Whatever remains unresolved
//     goes back to α and β up to --max-spec-rounds; if it persists, CB-01 (Stage 1 divergence, §7) with a spec_divergence
//     per point.

const (
	MaxSpecRoundsDefault  = 2
	ArtContractAcceptance = "contract_acceptance.json"
	catalogPath           = ".a2dlc/catalogue.json"
	emptyTree             = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // git's empty tree (sha1)
	reasonWithoutDualSpec = "no dual-spec"                             // the reason of the skipped Stage 1 without the three roles; skipStale recognizes it
	selectionCapture      = "files of the base under tests.paths whose text names as a whole word the name (without extension) of a changed source"
)

func stageSpec(ctx context.Context, m *Machine) (string, string, string, error) {
	if m.acceptedContract != nil {
		return m.acceptHumanContract()
	}
	if handled, status, reason, artifact, err := m.preserveAcceptedContract(); err != nil || handled {
		return status, reason, artifact, err
	}
	switch m.state.Mode {
	case contract.ModeLite:
		return contract.StageSkipped, "lite mode: no S* contract", "", nil
	case contract.ModeRefactor:
		return m.specRefactor(ctx)
	}
	return m.specDual(ctx)
}

// ContractAcceptance is the signed acceptance envelope for a human-accepted slice contract (S*, namespace a2dlc-contract).
type ContractAcceptance struct {
	SliceID        string `json:"slice_id"`
	BaseRef        string `json:"base_ref"`
	ContractSHA256 string `json:"contract_sha256"`
	Principal      string `json:"principal"`
	Signature      string `json:"signature"`
}

type contractEnvelope struct {
	SliceID        string          `json:"slice_id"`
	BaseRef        string          `json:"base_ref"`
	ContractSHA256 string          `json:"contract_sha256"`
	Principal      string          `json:"principal"`
	Signature      string          `json:"signature"`
	Contract       json.RawMessage `json:"contract"`
}

// ContractAcceptanceRecord persists the signed acceptance alongside the exact signed contract bytes in the run folder,
// allowing VerifyRun and Stage 1 resume to verify the signature without trusting unverified edits to contract.json.
type ContractAcceptanceRecord struct {
	SchemaVersion  int    `json:"schema_version"`
	SliceID        string `json:"slice_id"`
	BaseRef        string `json:"base_ref"`
	ContractSHA256 string `json:"contract_sha256"`
	Principal      string `json:"principal"`
	Signature      string `json:"signature"`
	SignedContract []byte `json:"signed_contract"`
}

type verifiedContract struct {
	contract       contract.SliceContract
	rawContract    []byte
	contractSHA256 string
	principal      string
	signature      string
}

func sha256Hex(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// SignContractFile validates a slice contract file against slice-contract.schema.json, signs its bytes under
// approval.ContractNamespace bound to sliceID and baseSha, and verifies the signature against allowed_signers.
func SignContractFile(contractPath, sliceID, baseSha, keyPath, principal string) (ContractAcceptance, error) {
	var acc ContractAcceptance
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		return acc, err
	}
	var s contract.SliceContract
	if err := contract.DecodeStrictBytes(raw, &s); err != nil {
		return acc, fmt.Errorf("%s does not match slice-contract: %w", contractPath, err)
	}
	if err := s.Validate(); err != nil {
		return acc, fmt.Errorf("%s is not valid: %w", contractPath, err)
	}
	if sliceID == "" {
		sliceID = s.SliceID
	} else if s.SliceID != sliceID {
		return acc, fmt.Errorf("%s has slice_id %q, not %q", contractPath, s.SliceID, sliceID)
	}
	contractSHA := sha256Hex(raw)
	payload := approval.ContractPayload(contractSHA, sliceID, baseSha)
	sig, err := approval.SignContract(keyPath, payload)
	if err != nil {
		return acc, err
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return acc, err
	}
	if err := approval.VerifyContract(signers, principal, payload, sig); err != nil {
		return acc, fmt.Errorf("signed, but the signature does not pass against the allowed signers (is your key missing from allowed_signers?): %w", err)
	}
	return ContractAcceptance{SliceID: sliceID, BaseRef: baseSha, ContractSHA256: contractSHA, Principal: principal, Signature: sig}, nil
}

func verifySignedContractBytes(rawContract []byte, sliceID, baseSha, signers, principal, sig, declaredSHA, declaredSlice, declaredBase string) (contract.SliceContract, string, error) {
	var s contract.SliceContract
	if err := contract.DecodeStrictBytes(rawContract, &s); err != nil {
		return s, "", fmt.Errorf("accepted contract does not match slice-contract: %w", err)
	}
	if err := s.Validate(); err != nil {
		return s, "", fmt.Errorf("accepted contract is invalid: %w", err)
	}
	if s.SliceID != sliceID {
		return s, "", fmt.Errorf("accepted contract slice_id %q does not match run slice %q", s.SliceID, sliceID)
	}
	actualSHA := sha256Hex(rawContract)
	if declaredSHA != "" && declaredSHA != actualSHA {
		return s, "", fmt.Errorf("contract signature sha256 %q does not match contract %q", declaredSHA, actualSHA)
	}
	if declaredSlice != "" && declaredSlice != sliceID {
		return s, "", fmt.Errorf("contract signature slice_id %q does not match run slice %q", declaredSlice, sliceID)
	}
	if declaredBase != "" && declaredBase != baseSha {
		return s, "", fmt.Errorf("contract signature base_ref %q does not match run base %q", declaredBase, baseSha)
	}
	normSig := strings.ReplaceAll(sig, "\r\n", "\n")
	payload := approval.ContractPayload(actualSHA, sliceID, baseSha)
	if principal != "" {
		if err := approval.VerifyContract(signers, principal, payload, normSig); err != nil {
			return s, "", err
		}
		return s, principal, nil
	}
	p, err := approval.VerifyContractAnyPrincipal(signers, payload, normSig)
	if err != nil {
		return s, "", err
	}
	return s, p, nil
}

func (m *Machine) verifyAcceptedContract(contractPath, sigPath string) (*verifiedContract, error) {
	rawInput, err := os.ReadFile(contractPath)
	if err != nil {
		return nil, fmt.Errorf("--accept-contract: %w", err)
	}
	var top map[string]json.RawMessage
	if json.Unmarshal(rawInput, &top) != nil || top == nil {
		return nil, fmt.Errorf("--accept-contract %s: not a JSON object", contractPath)
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil {
		return nil, fmt.Errorf("--accept-contract: allowed signers: %w", err)
	}
	if signers == "" {
		return nil, fmt.Errorf("--accept-contract: no allowed signers configured (A2DLC_ALLOWED_SIGNERS, A2DLC_ALLOWED_SIGNERS_DATA or ~/.config/a2dlc/allowed_signers)")
	}
	if _, hasContract := top["contract"]; hasContract {
		if sigPath != "" {
			return nil, fmt.Errorf("--accept-contract-sig cannot be combined with a self-contained signed contract envelope")
		}
		var env contractEnvelope
		if err := contract.DecodeStrictBytes(rawInput, &env); err != nil {
			return nil, fmt.Errorf("--accept-contract envelope %s: %w", contractPath, err)
		}
		if env.SliceID == "" || env.BaseRef == "" || env.ContractSHA256 == "" || env.Principal == "" || env.Signature == "" || len(env.Contract) == 0 {
			return nil, fmt.Errorf("--accept-contract envelope %s: slice_id, base_ref, contract_sha256, principal, signature and contract are required", contractPath)
		}
		// the signature covers the compact contract: an embedded RawMessage is re-indented by whoever pretty-prints the
		// envelope, and whitespace is not content
		var compact bytes.Buffer
		if err := json.Compact(&compact, env.Contract); err != nil {
			return nil, fmt.Errorf("--accept-contract envelope %s: contract: %w", contractPath, err)
		}
		env.Contract = compact.Bytes()
		s, p, err := verifySignedContractBytes(env.Contract, m.state.SliceID, m.baseSha, signers, env.Principal, env.Signature, env.ContractSHA256, env.SliceID, env.BaseRef)
		if err != nil {
			return nil, fmt.Errorf("--accept-contract %s: %w", contractPath, err)
		}
		return &verifiedContract{contract: s, rawContract: env.Contract, contractSHA256: sha256Hex(env.Contract), principal: p, signature: strings.ReplaceAll(env.Signature, "\r\n", "\n")}, nil
	}
	resolvedSig := sigPath
	if resolvedSig == "" {
		resolvedSig = contractPath + ".sig"
	}
	sigBytes, err := os.ReadFile(resolvedSig)
	if err != nil {
		return nil, fmt.Errorf("--accept-contract signature %s: %w", resolvedSig, err)
	}
	sigTrim := bytes.TrimSpace(sigBytes)
	if len(sigTrim) == 0 {
		return nil, fmt.Errorf("--accept-contract signature %s is empty", resolvedSig)
	}
	if sigTrim[0] == '{' {
		var acc ContractAcceptance
		if err := contract.DecodeStrictBytes(sigBytes, &acc); err != nil {
			return nil, fmt.Errorf("--accept-contract signature %s: %w", resolvedSig, err)
		}
		if acc.SliceID == "" || acc.BaseRef == "" || acc.ContractSHA256 == "" || acc.Principal == "" || acc.Signature == "" {
			return nil, fmt.Errorf("--accept-contract signature %s: slice_id, base_ref, contract_sha256, principal and signature are required", resolvedSig)
		}
		s, p, err := verifySignedContractBytes(rawInput, m.state.SliceID, m.baseSha, signers, acc.Principal, acc.Signature, acc.ContractSHA256, acc.SliceID, acc.BaseRef)
		if err != nil {
			return nil, fmt.Errorf("--accept-contract %s: %w", contractPath, err)
		}
		return &verifiedContract{contract: s, rawContract: rawInput, contractSHA256: sha256Hex(rawInput), principal: p, signature: strings.ReplaceAll(acc.Signature, "\r\n", "\n")}, nil
	}
	s, p, err := verifySignedContractBytes(rawInput, m.state.SliceID, m.baseSha, signers, "", string(sigBytes), "", "", "")
	if err != nil {
		return nil, fmt.Errorf("--accept-contract %s: %w", contractPath, err)
	}
	return &verifiedContract{contract: s, rawContract: rawInput, contractSHA256: sha256Hex(rawInput), principal: p, signature: strings.ReplaceAll(string(sigBytes), "\r\n", "\n")}, nil
}

func (m *Machine) acceptHumanContract() (string, string, string, error) {
	vc := m.acceptedContract
	rec := ContractAcceptanceRecord{
		SchemaVersion:  contract.SchemaVersion,
		SliceID:        m.state.SliceID,
		BaseRef:        m.baseSha,
		ContractSHA256: vc.contractSHA256,
		Principal:      vc.principal,
		Signature:      vc.signature,
		SignedContract: vc.rawContract,
	}
	if _, err := m.writeArtifact(ArtContractAcceptance, rec); err != nil {
		return "", "", "", err
	}
	s := vc.contract
	s.Source = contract.SourceHumanAccepted
	artifact, err := m.closeContract(s)
	if err != nil {
		return "", "", "", err
	}
	return contract.StageDone, fmt.Sprintf("human-accepted contract signed by %s", vc.principal), artifact, nil
}

func verifyAcceptanceRecord(dir, sliceID, baseSha string, onDisk []byte) (contract.SliceContract, ContractAcceptanceRecord, error) {
	var rec ContractAcceptanceRecord
	b, err := os.ReadFile(filepath.Join(dir, ArtContractAcceptance))
	if err != nil {
		return contract.SliceContract{}, rec, fmt.Errorf("missing or unreadable %s: %w", ArtContractAcceptance, err)
	}
	if err := contract.DecodeStrictBytes(b, &rec); err != nil {
		return contract.SliceContract{}, rec, fmt.Errorf("%s is out of contract: %w", ArtContractAcceptance, err)
	}
	if rec.SchemaVersion != contract.SchemaVersion || rec.SliceID == "" || rec.BaseRef == "" || rec.ContractSHA256 == "" || rec.Principal == "" || rec.Signature == "" || len(rec.SignedContract) == 0 {
		return contract.SliceContract{}, rec, fmt.Errorf("%s is incomplete", ArtContractAcceptance)
	}
	signers, cleanup, err := approval.AllowedSigners()
	defer cleanup()
	if err != nil || signers == "" {
		return contract.SliceContract{}, rec, fmt.Errorf("no allowed signers configured to verify %s", ArtContractAcceptance)
	}
	s, _, err := verifySignedContractBytes(rec.SignedContract, sliceID, baseSha, signers, rec.Principal, rec.Signature, rec.ContractSHA256, rec.SliceID, rec.BaseRef)
	if err != nil {
		return contract.SliceContract{}, rec, err
	}
	s.Source = contract.SourceHumanAccepted
	expected, err := marshalArtifact(s)
	if err != nil {
		return contract.SliceContract{}, rec, err
	}
	if !bytes.Equal(expected, onDisk) {
		return contract.SliceContract{}, rec, fmt.Errorf("%s on disk does not match the signed contract acceptance", ArtContract)
	}
	return s, rec, nil
}

func (m *Machine) preserveAcceptedContract() (bool, string, string, string, error) {
	onDisk, err := os.ReadFile(filepath.Join(m.dir, ArtContract))
	if errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(filepath.Join(m.dir, ArtContractAcceptance))
		return false, "", "", "", nil
	}
	if err != nil {
		return false, "", "", "", err
	}
	var diskS contract.SliceContract
	if err := contract.DecodeStrictBytes(onDisk, &diskS); err != nil || diskS.Source != contract.SourceHumanAccepted {
		return false, "", "", "", nil
	}
	s, rec, err := verifyAcceptanceRecord(m.dir, m.state.SliceID, m.baseSha, onDisk)
	if err != nil {
		return true, "", "", "", fmt.Errorf("cannot preserve %s with source %q: %w", ArtContract, contract.SourceHumanAccepted, err)
	}
	artifact, err := m.closeContract(s)
	if err != nil {
		return true, "", "", "", err
	}
	return true, contract.StageDone, fmt.Sprintf("preserved human-accepted contract signed by %s", rec.Principal), artifact, nil
}

func acceptedContractProblems(dir string, st contract.RunState) []string {
	onDisk, err := os.ReadFile(filepath.Join(dir, ArtContract))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return []string{ArtContract + ": " + err.Error()}
	}
	var s contract.SliceContract
	if err := contract.DecodeStrictBytes(onDisk, &s); err != nil || s.Source != contract.SourceHumanAccepted {
		return nil
	}
	var problems []string
	if _, _, err := verifyAcceptanceRecord(dir, st.SliceID, st.BaseSha, onDisk); err != nil {
		problems = append(problems, "human-accepted contract: "+err.Error())
	}
	pb, err := os.ReadFile(filepath.Join(dir, ArtProbes))
	if err != nil {
		problems = append(problems, "human-accepted contract: "+ArtProbes+": "+err.Error())
		return problems
	}
	var probes contract.Probes
	if err := contract.DecodeStrictBytes(pb, &probes); err != nil {
		problems = append(problems, "human-accepted contract: "+ArtProbes+" is out of contract: "+err.Error())
		return problems
	}
	if probes.ContractHash != hashBytes(onDisk) {
		problems = append(problems, "human-accepted contract: "+ArtProbes+" contract_hash does not match "+ArtContract)
	}
	return problems
}

// specRefactor captures and derives S* without launching any role.
func (m *Machine) specRefactor(ctx context.Context) (string, string, string, error) {
	cfg, err := m.app.Config()
	if err != nil {
		return "", "", "", err
	}
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return "", "", "", err
	}
	sources, _, err := m.app.Repo.Changed(m.baseSha, tree)
	if err != nil {
		return "", "", "", err
	}
	capture := contract.Capture{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID, BaseSha: m.baseSha,
		Selection: "no config.tests: there are no tests to capture", Tests: []string{}}
	if cfg.Tests != nil {
		_, sources = app.Partition(cfg.Tests.Paths, sources)
		all, err := m.app.Repo.DiffNames(emptyTree, m.baseSha)
		if err != nil {
			return "", "", "", err
		}
		inTests, _ := app.Partition(cfg.Tests.Paths, all)
		if capture.Tests, err = m.testsThatName(inTests, sources); err != nil {
			return "", "", "", err
		}
		capture.Selection = selectionCapture
	}
	if sources == nil {
		sources = []string{}
	}
	capture.ChangedFiles = sources
	if capture.Signatures, capture.EngineErrors, err = m.app.Signatures(ctx, m.baseSha); err != nil {
		return "", "", "", err
	}
	traceWhy, err := m.captureTraces(ctx, cfg, &capture)
	if err != nil {
		return "", "", "", err
	}
	if _, err := m.writeArtifact(ArtCapture, capture); err != nil {
		return "", "", "", err
	}
	s := contract.SliceContract{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID, Source: contract.SourceConsensus,
		Functions: []contract.SpecFunction{}, Probes: []contract.Probe{}, Assumptions: []contract.Assumption{},
		PiS: contract.PiS{Equivalence: contract.EquivalenceStrict, Relaxations: []string{}}}
	post := "same observable behavior as in base " + m.baseSha[:12] + " (refactor: the legacy is the oracle)"
	for _, f := range capture.Signatures {
		for _, sym := range f.Symbols {
			signature := sym.Signature
			s.Functions = append(s.Functions, contract.SpecFunction{Name: f.File + "::" + sym.Name, Signature: &signature,
				Pre: []string{}, Post: []string{post}, Invariants: []string{}, Errors: []contract.SpecError{}})
		}
	}
	if err := s.Validate(); err != nil {
		return "", "", "", fmt.Errorf("S* derived from the signatures out of contract: %w", err)
	}
	artifact, err := m.closeContract(s)
	if err != nil {
		return "", "", "", err
	}
	var notes []string
	if len(s.Functions) == 0 {
		notes = append(notes, "no engine signatures for the changed sources: S* without functions (Stage 4 compares against the base anyway)")
	}
	for _, e := range capture.EngineErrors {
		notes = append(notes, "engine "+e.Language+" gave no signatures: "+e.Error)
	}
	if traceWhy != "" {
		notes = append(notes, "traces not captured, the Stage 1.5 trace gate cannot pass: "+traceWhy)
	}
	return contract.StageDone, strings.Join(notes, "; "), artifact, nil
}

// testsThatName picks, from the base's tests, those that name as a whole word some changed source. It is a textual
// heuristic declared in capture.selection: the core does not know about imports. A short name picks too much, which is
// the safe side for a capture.
func (m *Machine) testsThatName(tests, sources []string) ([]string, error) {
	var res []*regexp.Regexp
	for _, f := range sources {
		if root := strings.TrimSuffix(path.Base(f), path.Ext(f)); root != "" {
			res = append(res, regexp.MustCompile(`\b`+regexp.QuoteMeta(root)+`\b`))
		}
	}
	out := []string{}
	for _, t := range tests {
		if len(res) == 0 {
			break
		}
		b, err := m.app.Repo.Show(m.baseSha, t)
		if err != nil {
			return nil, err
		}
		for _, re := range res {
			if re.Match(b) {
				out = append(out, t)
				break
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// closeContract writes S* and, with the hash of contract.json as it stayed on disk, the locked probes.
func (m *Machine) closeContract(s contract.SliceContract) (string, error) {
	artifact, err := m.writeArtifact(ArtContract, s)
	if err != nil {
		return "", err
	}
	h, err := HashFile(filepath.Join(m.dir, artifact))
	if err != nil {
		return "", err
	}
	_, err = m.writeArtifact(ArtProbes, contract.Probes{SchemaVersion: contract.SchemaVersion, SliceID: s.SliceID, ContractHash: h, Probes: s.Probes})
	return artifact, err
}

func (m *Machine) specDual(ctx context.Context) (string, string, string, error) {
	intent, err := os.ReadFile(filepath.Join(m.dir, ArtIntent))
	if errors.Is(err, os.ErrNotExist) {
		return contract.StageSkipped, "mode " + m.state.Mode + " without intent.json: the specifiers have nothing to translate (Stage 0)", "", nil
	} else if err != nil {
		return "", "", "", err
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(intent, &obj) != nil || obj == nil {
		return contract.StageFailed, "intent.json is not a JSON object", "", nil
	}
	if cfg, err := m.app.Config(); err == nil && cfg.Rung == contract.RungCriticalCode {
		if rt, err := m.readRoute(); err == nil && !rt.DualSpecRequired && rt.CriticalityEffective == contract.CriticalityLow && rt.Track == contract.TrackN1 {
			return contract.StageSkipped, contract.RungCriticalCodeNoDualSpec, "", nil
		}
	}
	var missing []string
	for _, r := range []string{contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta, contract.RoleInquisitor} {
		if _, ok := m.roles.Roles[r]; !ok {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		rt, err := m.readRoute()
		if err != nil {
			return "", "", "", err
		}
		if rt.DualSpecRequired {
			return contract.StageEscalated, "dual-spec mandatory (greenfield with criticality " + rt.CriticalityEffective + ", Router §5) and missing " + strings.Join(missing, ", ") + " in " + pathRoles + "; not built without S*", "", nil
		}
		return contract.StageSkipped, reasonWithoutDualSpec + ": missing " + strings.Join(missing, ", ") + " in " + pathRoles + "; the following stages run without S*", "", nil
	}
	// §5 asks for different families for α and β; the core only sees the one declared in roles.json, so it notes it, as n2.go does
	noteFamily := ""
	if fa, fb := m.roles.Roles[contract.RoleSpecifierAlpha].Family, m.roles.Roles[contract.RoleSpecifierBeta].Family; fa == fb {
		noteFamily = "α and β are of the same declared family (" + fa + "): they share bias and the consensus is worth less (§5 asks for different families)"
		if fe := m.roles.FamilyException; fe != nil {
			noteFamily += "; roles.json family_exception: " + fe.Reason
		}
	}
	lang, err := m.engineLanguageOf()
	if err != nil {
		return "", "", "", err
	}
	var capture *contract.Capture
	if b, err := os.ReadFile(filepath.Join(m.dir, ArtCapture)); err == nil {
		var c contract.Capture
		if err := contract.DecodeStrictBytes(b, &c); err != nil {
			return contract.StageFailed, "capture.json out of contract: " + err.Error(), "", nil
		}
		capture = &c
	}
	catalogue, reason, err := m.catalogue()
	if err != nil {
		return "", "", "", err
	}
	if reason != "" {
		return contract.StageFailed, reason, "", nil
	}
	rounds := m.maxSpec
	if rounds <= 0 {
		rounds = MaxSpecRoundsDefault
	}
	pending := []contract.SpecDiscrepancy{} // unresolved by the Inquisitor and by a person
	carry := []contract.SpecDiscrepancy{}   // what the specifiers see next round: the unresolved ones and the person's decisions
	var decided []string                    // topics a person decided through answers.json (decision 2)
	var stale []string                      // answers that no longer apply, and why
	extra := false                          // one more round so the specifiers write S* knowing the person's decision
	for round := 1; round <= rounds; round++ {
		m.iter = round
		var candidates [2]contract.SliceContract
		for i, role := range []string{contract.RoleSpecifierAlpha, contract.RoleSpecifierBeta} {
			c, st, reason, err := m.specify(ctx, role, contract.SpecifierRequest{Intent: intent, Capture: capture, Round: round, Discrepancies: carry, EngineLanguage: lang})
			if err != nil || st != "" {
				return st, reason, "", err
			}
			candidates[i] = c
		}
		resp, st, reason, err := m.inquire(ctx, candidates, catalogue, round, lang)
		if err != nil || st != "" {
			return st, reason, "", err
		}
		pending, carry = []contract.SpecDiscrepancy{}, []contract.SpecDiscrepancy{}
		decidedNow := 0
		for _, d := range resp.Discrepancies {
			if d.Resolution != nil {
				continue
			}
			// a person may already have decided this divergence on an earlier attempt: `a2dlc answer --probe <escalation id>`
			id, _ := divergenceID(m.state.SliceID, d.Topic)
			ans, ok, why, err := m.answerFor(id, ArtEscalations)
			if err != nil {
				return "", "", "", err
			}
			if ok {
				r := "a person decided (" + ans.By + "): " + ans.Choice + ". " + ans.Reason
				d.Resolution = &r
				carry = append(carry, d)
				decidedNow++
				if !contains(decided, d.Topic) {
					decided = append(decided, d.Topic)
				}
				continue
			}
			if why != "" && !contains(stale, why) {
				stale = append(stale, why)
			}
			pending = append(pending, d)
			carry = append(carry, d)
		}
		if len(pending) > 0 {
			continue
		}
		if decidedNow > 0 && !extra {
			// the specifiers have not seen the decision yet: S* is written in one more round that carries it
			extra = true
			if round == rounds {
				rounds++
			}
			continue
		}
		s := resp.Contract
		note := incorporateOpen(&s, resp.OpenAssumptions, catalogue)
		if len(decided) > 0 {
			note = strings.TrimSuffix(fmt.Sprintf("%d discrepancies decided by a person (%s: %s); %s", len(decided), ArtAnswers, strings.Join(decided, ", "), note), "; ")
		}
		if noteFamily != "" {
			note = strings.TrimSuffix(noteFamily+"; "+note, "; ")
		}
		artifact, err := m.closeContract(s)
		return contract.StageDone, note, artifact, err
	}
	st, reason, artifact, err := m.escalateDivergence(pending, rounds)
	if len(stale) > 0 {
		reason += "; " + strings.Join(stale, "; ")
	}
	return st, reason, artifact, err
}

// contains says whether xs has x.
func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// divergenceID is the escalation id (and target) of a spec_divergence on topic: it is what `a2dlc answer --probe` names,
// so an answer given on one attempt applies on the next one as long as the topic is the same.
func divergenceID(sliceID, topic string) (id, target string) {
	target = sliceID + "::" + strings.ReplaceAll(topic, `\`, "/")
	h := sha256.Sum256([]byte("spec_divergence\x00" + target))
	return "esc-" + hex.EncodeToString(h[:])[:12], target
}

// roleWithoutResponse translates an invocation without a valid response: after the retries, nil is failed (there was
// no one to believe: INCONCLUSIVE), refused is escalated (the role says the intent is not enough for it: a person resolves it).
func roleWithoutResponse(role string, invocation Invocation) (string, string) {
	if invocation.Response == nil {
		return contract.StageFailed, fmt.Sprintf("%s did not give a valid answer in %d attempts", role, len(invocation.Entries))
	}
	return contract.StageEscalated, role + " refused: " + invocation.Response.Reason
}

// rejectedAttempts bounds the launches of a Stage 1 role (a specifier or the Inquisitor) in one round: an answer the core
// rejects is retried once with the reason in request.rejection, so a model that skips a rule it was told gets to read
// why before the stage fails. A refusal or a launch without a valid envelope is not retried here (Invoke already did).
const rejectedAttempts = 2

// specify launches a specifier, validates its candidate and saves it as contract_alpha.json or contract_beta.json.
// A status other than "" is the end of the stage, with its reason.
func (m *Machine) specify(ctx context.Context, role string, req contract.SpecifierRequest) (contract.SliceContract, string, string, error) {
	for attempt := 1; ; attempt++ {
		var resp contract.SpecifierResponse
		invocation, err := m.Invoke(ctx, role, "1", req, "")
		if err != nil {
			return resp.Contract, "", "", err
		}
		if invocation.Response == nil || invocation.Response.Status != contract.RoleOK {
			st, reason := roleWithoutResponse(role, invocation)
			return resp.Contract, st, reason, nil
		}
		rejection := m.rejectSpec(role, invocation.Response.Payload, &resp)
		if rejection == "" {
			name := ArtContractAlpha
			if role == contract.RoleSpecifierBeta {
				name = ArtContractBeta
			}
			_, err = m.writeArtifact(name, resp.Contract)
			return resp.Contract, "", "", err
		}
		if attempt == rejectedAttempts {
			return resp.Contract, contract.StageFailed, fmt.Sprintf("%s (after %d attempts, the last one told why)", rejection, rejectedAttempts), nil
		}
		req.Rejection = rejection
	}
}

// rejectSpec decodes a specifier's payload into resp and returns why the core rejects it, or "" if it is accepted.
func (m *Machine) rejectSpec(role string, payload json.RawMessage, resp *contract.SpecifierResponse) string {
	if err := contract.DecodeStrictBytes(payload, resp); err != nil {
		return "payload of " + role + " out of contract: " + err.Error()
	}
	c := resp.Contract
	if err := c.Validate(); err != nil {
		return "the contract of " + role + " does not meet slice-contract: " + err.Error()
	}
	if c.Source != role || c.SliceID != m.state.SliceID {
		return fmt.Sprintf("the contract of %s says source %q and slice_id %q: it has to be %s and %s", role, c.Source, c.SliceID, role, m.state.SliceID)
	}
	return ""
}

// inquire launches the Inquisitor with the two candidates and the catalogue, and validates what it returns; a rejected
// answer is retried up to rejectedAttempts.
func (m *Machine) inquire(ctx context.Context, candidates [2]contract.SliceContract, cat *contract.AssumptionCatalogue, round int, lang string) (contract.InquisitorResponse, string, string, error) {
	req := contract.InquisitorRequest{ContractAlpha: candidates[0], ContractBeta: candidates[1], Catalogue: cat, Round: round, EngineLanguage: lang}
	for attempt := 1; ; attempt++ {
		var resp contract.InquisitorResponse
		invocation, err := m.Invoke(ctx, contract.RoleInquisitor, "1", req, "")
		if err != nil {
			return resp, "", "", err
		}
		if invocation.Response == nil || invocation.Response.Status != contract.RoleOK {
			st, reason := roleWithoutResponse(contract.RoleInquisitor, invocation)
			return resp, st, reason, nil
		}
		rejection := m.rejectInquiry(invocation.Response.Payload, &resp, cat)
		if rejection == "" {
			return resp, "", "", nil
		}
		if attempt == rejectedAttempts {
			return resp, contract.StageFailed, fmt.Sprintf("%s (after %d attempts, the last one told why)", rejection, rejectedAttempts), nil
		}
		req.Rejection = rejection
	}
}

// rejectInquiry decodes the Inquisitor's payload into resp and returns why the core rejects it, or "" if it is accepted.
func (m *Machine) rejectInquiry(payload json.RawMessage, resp *contract.InquisitorResponse, cat *contract.AssumptionCatalogue) string {
	if err := contract.DecodeStrictBytes(payload, resp); err != nil {
		return "payload of the inquisitor out of contract: " + err.Error()
	}
	if err := resp.Validate(); err != nil {
		return "the inquisitor's answer does not meet its contract: " + err.Error()
	}
	if resp.Contract.SliceID != m.state.SliceID {
		return fmt.Sprintf("S* of the inquisitor belongs to another slice (%s)", resp.Contract.SliceID)
	}
	// The catalogue governs (§5 Stage 1): an assumption "from catalogue" that is not in it is a category the LLM invented.
	inCatalogue := map[string]bool{}
	if cat != nil {
		for _, e := range cat.Entries {
			inCatalogue[e.ID] = true
		}
	}
	for _, a := range append(append([]contract.Assumption{}, resp.Contract.Assumptions...), resp.OpenAssumptions...) {
		if a.Source == contract.FromCatalogue && !inCatalogue[a.ID] {
			return "the inquisitor cites catalogue entry " + a.ID + ", which is not in " + catalogPath + " of the base"
		}
	}
	// Consensus probes are mandatory (§5 Stage 1 step 4): agreement between families is not correctness, so a catalogue
	// assumption that consensus accepts must come with a probe that cites it; without a catalogue there is no rule.
	if cat != nil {
		withProbe := map[string]bool{}
		for _, p := range resp.Contract.Probes {
			if p.Assumption != nil {
				withProbe[*p.Assumption] = true
			}
		}
		// All of them in one message: naming only the first let the retry fix it and drop another (measured with Haiku 4.5).
		var missing []string
		for _, a := range resp.Contract.Assumptions {
			if a.Source == contract.FromCatalogue && a.Status == contract.AssumptionAccepted && !withProbe[a.ID] {
				missing = append(missing, a.ID)
			}
		}
		switch len(missing) {
		case 0:
		case 1:
			return fmt.Sprintf("missing consensus probe: the catalogue assumption %s is accepted without a probe that cites it (probe.assumption); agreement is not correctness (§5 Stage 1 step 4)", missing[0])
		default:
			return fmt.Sprintf("missing consensus probe: the catalogue assumptions %s are accepted without a probe that cites each of them (one probe.assumption per id); agreement is not correctness (§5 Stage 1 step 4)", strings.Join(missing, ", "))
		}
	}
	return ""
}

// incorporateOpen leaves in S* the open assumptions that the Inquisitor listed separately (so Builders and Saboteur see
// them) and returns the note of the stage: which assumptions await a person and which catalogue inputs the consensus
// did not instantiate. In Stage 1 neither blocks; Stage 1.5 stops on an open assumption that is high or critical.
func incorporateOpen(s *contract.SliceContract, open []contract.Assumption, cat *contract.AssumptionCatalogue) string {
	has := map[string]bool{}
	for _, a := range s.Assumptions {
		has[a.ID] = true
	}
	for _, a := range open {
		if !has[a.ID] {
			s.Assumptions = append(s.Assumptions, a)
			has[a.ID] = true
		}
	}
	var notes, ids []string
	for _, a := range s.Assumptions {
		if a.Status == contract.AssumptionOpen {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) > 0 {
		notes = append(notes, fmt.Sprintf("%d open assumptions await a person (Stage 1 does not stop; 1.5 does if any is high or critical): %s", len(ids), strings.Join(ids, ", ")))
	}
	if cat != nil {
		var without []string
		for _, e := range cat.Entries {
			if !has[e.ID] {
				without = append(without, e.ID)
			}
		}
		if len(without) > 0 {
			notes = append(notes, "the consensus did not instantiate these catalogue entries: "+strings.Join(without, ", "))
		}
	}
	return strings.Join(notes, "; ")
}

// catalogue reads .a2dlc/catalogue.json from the run's base, never from the working tree (the candidate's diff cannot
// remove categories), and leaves it as the run's catalogue.json for the Inquisitor. Without a catalogue, nil.
func (m *Machine) catalogue() (*contract.AssumptionCatalogue, string, error) {
	b, err := m.app.Repo.Show(m.baseSha, catalogPath)
	if errors.Is(err, os.ErrNotExist) {
		os.Remove(filepath.Join(m.dir, ArtCatalogue))
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var c contract.AssumptionCatalogue
	if err := contract.DecodeStrictBytes(b, &c); err != nil {
		return nil, catalogPath + " of the base out of contract: " + err.Error(), nil
	}
	if err := c.Validate(); err != nil {
		return nil, catalogPath + " of the base out of contract: " + err.Error(), nil
	}
	_, err = m.writeArtifact(ArtCatalogue, c)
	return &c, "", err
}

// escalateDivergence leaves a spec_divergence per unresolved discrepancy in escalations.json and stops the run.
func (m *Machine) escalateDivergence(pending []contract.SpecDiscrepancy, rounds int) (string, string, string, error) {
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return "", "", "", err
	}
	escalations := contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: "git-tree:" + tree,
		CreatedAt: m.now().UTC().Format(timeRFC), Items: []contract.Escalation{}}
	for _, d := range pending {
		id, target := divergenceID(m.state.SliceID, d.Topic)
		msg := "α: " + d.Alpha + " · β: " + d.Beta
		escalations.Items = append(escalations.Items, contract.Escalation{ID: id, Kind: "spec_divergence", Target: target,
			Summary: fmt.Sprintf("Specifier α and β diverge and the Inquisitor did not resolve it after %d rounds: a person decides "+
				"(a2dlc answer --slice %s --probe %s --choice <alpha, beta or the rule in words> --by <name> --reason <why>) and the run resumes (the specifiers see the decision), or accept a signed S* directly (a2dlc run --slice %s --accept-contract <path>).", rounds, m.state.SliceID, id, m.state.SliceID),
			Evidence: contract.Evidence{Message: msg}, ResolveWith: []string{}, ClusterID: contract.ClusterID("spec_divergence", msg)})
	}
	artifact, err := m.writeArtifact(ArtEscalations, escalations)
	return contract.StageEscalated, fmt.Sprintf("CB-01: specification divergence after %d rounds (%d unresolved, in %s)", rounds, len(pending), ArtEscalations), artifact, err
}
