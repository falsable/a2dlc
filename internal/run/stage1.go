package run

import (
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
	catalogPath           = ".a2dlc/catalogue.json"
	emptyTree             = "4b825dc642cb6eb9a060e54bf8d69288fbee4904" // git's empty tree (sha1)
	reasonWithoutDualSpec = "no dual-spec"                             // the reason of the skipped Stage 1 without the three roles; skipStale recognizes it
	selectionCapture      = "files of the base under tests.paths whose text names as a whole word the name (without extension) of a changed source"
)

func stageSpec(ctx context.Context, m *Machine) (string, string, string, error) {
	switch m.state.Mode {
	case contract.ModeLite:
		return contract.StageSkipped, "lite mode: no S* contract", "", nil
	case contract.ModeRefactor:
		return m.specRefactor(ctx)
	}
	return m.specDual(ctx)
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
				"(a2dlc answer --slice %s --probe %s --choice <alpha, beta or the rule in words> --by <name> --reason <why>) and the run resumes; the specifiers see the decision.", rounds, m.state.SliceID, id),
			Evidence: contract.Evidence{Message: msg}, ResolveWith: []string{}, ClusterID: contract.ClusterID("spec_divergence", msg)})
	}
	artifact, err := m.writeArtifact(ArtEscalations, escalations)
	return contract.StageEscalated, fmt.Sprintf("CB-01: specification divergence after %d rounds (%d unresolved, in %s)", rounds, len(pending), ArtEscalations), artifact, err
}
