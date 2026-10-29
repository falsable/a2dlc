package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Stage 3: Tier 0 inner loop (§5) with one Builder (N=1, block 3 of the construction order, ADR 0004). The core
// exports the run's base to a temporary folder outside the repo, which the launcher mounts as /work (copied without
// .git), and launches builder_primary. What counts is the state of /work when each iteration ends, not what the role
// says: the core turns it into a git tree (TreeFromDir, without the gate outputs) and runs Tier 0 on it with the
// engines (operation: tier0: compile, typecheck, lint and the locked probes). If ok, the tree stays as a commit on the
// base (candidate.json and refs/a2dlc/run/<slice>/builder_primary) and Stage 4 judges that commit. If not, the findings
// go to tier0_feedback.json, which the builder sees in the next iteration over the same /work. Once the rounds are
// exhausted (max_inner_iterations: --max-inner, else intent.budget, else MaxInnerDefault; intent.go) the stage
// escalates. With N=2 builder_orthogonal also runs, each with its own /work (n2.go).

// builderInput is what the two builders of a run receive: the same intent, the same S* and the same probes.
type builderInput struct {
	intent, contractJSON json.RawMessage
	probes               []contract.Probe
	rounds               int
	boundary             *contract.Boundary // CB-04: boundary of the intent (boundary.go); nil if it does not declare one
	extra                app.Tier0Extra     // the intent's forbidden imports and side effects, unioned into the Tier 0 policy
	engineLanguage       string             // builder_request.engine_language ("" when not python or typescript: the schema default)
	critical             *bool              // builder_request.critical: the route's track is N=2
}

// buildResult is how a builder's loop ended: with a candidate (candidate) or with the stage state and its reason.
// boundary are the escalations that stop the stage before Tier 0: protected_boundary (CB-04) for a builder that crossed
// the intent's boundary, and dependency_not_allowed (sec-dependency-allowlist) for a dependency outside the allowlist.
type buildResult struct {
	status, reason, artifact string
	candidate                *contract.CandidateEntry
	notes                    []string
	boundary                 []contract.Escalation
	agot                     *exhaustion // CB-02b: the builder spent its rounds (and the tier_up, if there is one)
}

func stageTier0(ctx context.Context, m *Machine) (string, string, string, error) {
	const role = contract.RoleBuilderPrimary
	if _, ok := m.roles.Roles[role]; !ok {
		return m.tier0SinBuilder(ctx)
	}
	// what a previous run of the stage left must not reach its first iteration, and the signatures, the selection and the
	// Saboteur's tests (overlay/) belonged to the old candidates: the overlay is only resumed within a run of Stage 4
	if err := os.RemoveAll(filepath.Join(m.dir, dirOverlay)); err != nil {
		return "", "", "", err
	}
	for _, artifact := range []string{ArtTier0, ArtTier0Orthogonal, ArtCandidate, ArtSignatures, ArtSelection} {
		if err := os.Remove(filepath.Join(m.dir, artifact)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", "", err
		}
	}
	if err := m.removePriorExhaustion(); err != nil {
		return "", "", "", err
	}
	intent, err := m.readObject(ArtIntent)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	contractJSON, err := m.readObject(ArtContract)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	probes, err := m.readProbes()
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	boundary, err := readBoundary(intent)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	extra, err := readTier0Extra(intent)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	route, err := m.readRoute()
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	critical := route.Track == contract.TrackN2
	in := builderInput{intent: intent, contractJSON: contractJSON, probes: probes, rounds: m.maxInner, boundary: boundary, extra: extra,
		engineLanguage: m.engineLanguage(probes), critical: &critical}
	n2, note, err := m.trackN2()
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	p, err := m.build(ctx, role, ArtTier0, in)
	if err != nil {
		return "", "", "", err
	}
	if len(p.boundary) > 0 {
		// the boundary escalates the whole stage: with N=2 the orthogonal does not get to run
		return m.escalateBoundary(p)
	}
	if n2 {
		return m.tier0N2(ctx, p, in)
	}
	if p.candidate == nil {
		if p.agot != nil {
			artifact, err := m.escalateExhausted([]exhaustion{*p.agot})
			if err != nil {
				return "", "", "", err
			}
			return p.status, join(p.reason, note, "escalated "+KindBuilderExhaustion+" in "+artifact), p.artifact, nil
		}
		return p.status, join(append([]string{note}, p.reason)...), p.artifact, nil
	}
	c := p.candidate
	artifact, err := m.writeArtifact(ArtCandidate, contract.Candidate{SchemaVersion: contract.SchemaVersion, Builder: c.Builder,
		Commit: c.Commit, TreeHash: c.TreeHash, Iterations: c.Iterations, Unchecked: c.Unchecked, HeadersMissing: c.HeadersMissing, Tier0Reviewed: c.Tier0Reviewed, Lemmas: c.Lemmas})
	return contract.StageDone, join(append([]string{note}, p.notes...)...), artifact, err
}

// readProbes are the S* probes that Stage 1 left in probes.json; nil if there is no file.
func (m *Machine) readProbes() ([]contract.Probe, error) {
	b, err := os.ReadFile(filepath.Join(m.dir, ArtProbes))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var probeFile contract.Probes
	if err := contract.DecodeStrictBytes(b, &probeFile); err != nil {
		return nil, fmt.Errorf("probes.json out of contract (contracts/probes.schema.json): %w", err)
	}
	return probeFile.Probes, nil
}

// tier0SinBuilder is Stage 3 when roles.json has no builder_primary: the candidate is already in the working tree. Its
// changes are checked as a builder's are, before Tier 0: the intent's boundary (CB-04) and the dependency allowlist, since
// nobody else reviews them and Tier 0 must not run on a tree that crosses them. If Stage 1 left probes, Tier 0 runs them
// anyway on that tree (measured 2026-10-17: without this, S* said resta(3, 5) == -2, the candidate returned 0 and nobody
// executed it). A red probe is a violation of S* that no role can iterate on, so the stage escalates with the findings in
// tier0.json and the run stops there. Without probes, skipped.
func (m *Machine) tier0SinBuilder(ctx context.Context) (string, string, string, error) {
	const reason = "no builder_primary role in roles.json: the candidate is already in the tree"
	intent, err := m.readObject(ArtIntent)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	boundary, err := readBoundary(intent)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	extra, err := readTier0Extra(intent)
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	tree, err := m.app.Repo.WorktreeTree()
	if err != nil {
		return "", "", "", err
	}
	diffs, err := m.app.Repo.DiffNames(m.baseSha, tree)
	if err != nil {
		return "", "", "", err
	}
	crossed, err := m.reviewBoundary(m.baseSha, tree, diffs, boundary, "")
	if err != nil {
		return "", "", "", err
	}
	deps, _, err := m.reviewDependencies("", tree, diffs)
	if err != nil {
		return "", "", "", err
	}
	if items := append(crossed, deps...); len(items) > 0 {
		var where []string
		for _, e := range items {
			where = append(where, e.Target)
		}
		artifact, err := m.escalate(items)
		if err != nil {
			return "", "", "", err
		}
		return contract.StageEscalated, join(reason, "CB-04 or dependency_not_allowed in the working tree ("+strings.Join(where, ", ")+
			"): without a builder nobody fixes them; see "+ArtEscalations), artifact, nil
	}
	probes, err := m.readProbes()
	if err != nil {
		return contract.StageFailed, err.Error(), "", nil
	}
	if len(probes) == 0 {
		return contract.StageSkipped, reason, "", nil
	}
	res, unreviewed, err := m.app.Tier0With(ctx, m.baseSha, tree, probes, extra)
	if err != nil {
		return contract.StageFailed, "Tier 0 could not run on the working tree: " + err.Error(), "", nil
	}
	artifact, err := m.writeArtifact(ArtTier0, res)
	if err != nil {
		return "", "", "", err
	}
	notes := []string{reason + "; Tier 0 ran the S* probes on it"}
	if len(unreviewed) > 0 {
		notes = append(notes, "Tier 0 did not review: "+strings.Join(unreviewed, ", "))
	}
	if !res.OK {
		var ids []string
		for _, f := range res.Findings {
			ids = append(ids, f.Kind+" "+f.File+": "+f.Message)
		}
		return contract.StageEscalated, join(append(notes, fmt.Sprintf("Tier 0 red in the working tree (%d findings in %s; without a builder nobody fixes them): %s",
			len(res.Findings), ArtTier0, strings.Join(ids, " | ")))...), artifact, nil
	}
	// there were probes and none could run (without file/expr or without engine): nobody executed S* on the tree. It is
	// not done (nothing was reviewed) nor an escalation (there is no builder to fix and no decision to make): skipped with
	// the reason, as without probes; Stage 4 and the Saboteur keep judging the tree.
	if !res.Reviewed {
		return contract.StageSkipped, join(append(notes, noteTier0NotRun(false, unreviewed)+"; the S* probes were left unexecuted on the working tree")...), artifact, nil
	}
	return contract.StageDone, join(notes...), artifact, nil
}

// build runs the builder → Tier 0 loop of a builder with its own /work, freshly exported from the base. tier0Artifact is
// the artifact where its findings remain for the next iteration: each builder has its own, and the policy only lets it
// see its own (policy.go). Rounds are counted per builder (Round, MaxRounds, Iterations of the candidate) and added to
// m.iter, which is what the state records for the stage.
func (m *Machine) build(ctx context.Context, role, tier0Artifact string, in builderInput) (c buildResult, err error) {
	// /work lives outside the repo: inside .a2dlc/run/ each WorktreeTree (hook, check, approve) would rehash an entire copy
	// of the repo. The candidate survives as a commit.
	work, err := os.MkdirTemp("", "a2dlc-work-*")
	if err != nil {
		return buildResult{}, err
	}
	defer os.RemoveAll(work)
	if err := m.app.Repo.Export(m.baseSha, work); err != nil {
		return buildResult{}, err
	}
	// CB-02b: if roles.json declares tier_up, iteration in.rounds+1 is run by that role, with the same /work and the last
	// feedback. A defer labels how that iteration ends: ok (the note), or escalated for any reason (CB-02b).
	var feedback *contract.Tier0Response
	header, err := m.requestHeader(in.intent, in.contractJSON)
	if err != nil {
		return buildResult{status: contract.StageFailed, reason: err.Error()}, nil
	}
	tierUp := m.roles.Roles[role].TierUp
	agent := exhaustion{role: role, rounds: in.rounds}
	extra, inTierUp := 0, false
	if tierUp != nil {
		extra, agent.tierUp = 1, tierUp.Family
	}
	defer func() {
		if err != nil || !inTierUp {
			return
		}
		switch {
		case c.status == contract.StageDone:
			c.notes = append([]string{fmt.Sprintf("CB-02b: builder exhausted after %d rounds; the higher tier (%s) resolved it", in.rounds, tierUp.Family)}, c.notes...)
		case c.status == contract.StageEscalated && c.agot == nil && len(c.boundary) == 0:
			// a boundary or dependency escalation is a decision for a person, not a builder that ran out of rounds: it stays
			agent.reasonTierUp, agent.last = c.reason, findingsSummary(feedback)
			c = m.exhausted(agent, tier0Artifact)
		}
	}()
	for round := 1; round <= in.rounds+extra; round++ {
		cfg, maxRounds := (*contract.Role)(nil), in.rounds
		if round > in.rounds {
			cfg, maxRounds, inTierUp = tierUp, round, true
		}
		m.iter++
		pl := contract.BuilderRequest{Intent: in.intent, Contract: in.contractJSON, Probes: in.probes, Round: round, MaxRounds: maxRounds, Tier0Feedback: feedback, Header: &header, Boundary: in.boundary,
			EngineLanguage: in.engineLanguage, Paradigm: m.roles.Roles[role].Paradigm, Critical: in.critical}
		invocation, err := m.invokeRole(ctx, role, cfg, "3", pl, work)
		if err != nil {
			return buildResult{}, err
		}
		switch {
		case invocation.Response == nil:
			return buildResult{status: contract.StageFailed, reason: fmt.Sprintf("the builder did not give a valid answer in %d attempts", len(invocation.Entries))}, nil
		case invocation.Response.Status == contract.RoleRefused:
			return buildResult{status: contract.StageEscalated, reason: "the builder refused: " + invocation.Response.Reason}, nil
		}
		var resp contract.BuilderResponse
		if err := contract.DecodeStrictBytes(invocation.Response.Payload, &resp); err != nil {
			return buildResult{status: contract.StageFailed, reason: "builder payload out of contract: " + err.Error()}, nil
		}
		if dup := duplicateLemma(resp.Lemmas); dup != "" {
			return buildResult{status: contract.StageFailed, reason: "builder payload out of contract: lemma " + dup + " is declared twice"}, nil
		}
		// the full tree (with the gate outputs the base has committed, like approvals.json) is what gets committed and what
		// says whether the builder touched .a2dlc/; tree_hash is that commit's, without the outputs
		tree, err := m.app.Repo.TreeFromDirComplete(m.baseSha, work)
		if err != nil {
			return buildResult{}, err
		}
		diffs, err := m.app.Repo.DiffNames(m.baseSha, tree)
		if err != nil {
			return buildResult{}, err
		}
		var delGate []string
		for _, d := range diffs {
			if strings.HasPrefix(d, ".a2dlc/") {
				delGate = append(delGate, d)
			}
		}
		// check ignores .a2dlc/ in the diff and reads config and waivers from the working tree, but the commit would carry
		// them and a merge of the candidate would put them into the repo: the gate is not the builder's.
		if len(delGate) > 0 {
			return buildResult{status: contract.StageEscalated, reason: "CB-04: the builder touched " + strings.Join(delGate, ", ") + " in /work: the config, the waivers and the gate signatures are not its own"}, nil
		}
		if len(diffs) == 0 {
			return buildResult{status: contract.StageEscalated, reason: fmt.Sprintf("the builder left no changes in /work (round %d)", round)}, nil
		}
		// CB-04 of the intent's boundary: crossing it escalates without Tier 0 or another iteration (boundary.go)
		boundary, err := m.reviewBoundary(m.baseSha, tree, diffs, in.boundary, role)
		if err != nil {
			return buildResult{}, err
		}
		if len(boundary) > 0 {
			var where []string
			for _, e := range boundary {
				where = append(where, e.Target)
			}
			return buildResult{status: contract.StageEscalated, boundary: boundary,
				reason: "CB-04: the builder crossed the intent's boundary in /work (" + strings.Join(where, ", ") + "): raising privileges is a person's decision, who widens the boundary in the intent and relaunches; see " + ArtEscalations}, nil
		}
		// sec-dependency-allowlist: a dependency the candidate adds or changes that the base's allowlist does not name
		// escalates the stage, before Tier 0 and with no waiver: adding it is the owner's signed change to the allowlist
		deps, allowFile, err := m.reviewDependencies(role, tree, diffs)
		if err != nil {
			return buildResult{}, err
		}
		if len(deps) > 0 {
			var where []string
			for _, e := range deps {
				where = append(where, e.Target)
			}
			return buildResult{status: contract.StageEscalated, boundary: deps,
				reason: "dependency_not_allowed: the builder added or changed dependencies that " + allowFile + " does not allow (" + strings.Join(where, ", ") + "); see " + ArtEscalations}, nil
		}
		// the builder's loop is the one place where config.tests is the Tier 0 of the files no engine covers
		extra := in.extra
		extra.Tests = true
		res, unreviewed, err := m.app.Tier0With(ctx, m.baseSha, tree, in.probes, extra)
		if err != nil {
			// a downed engine is not feedback: sending it to the builder would spend paid rounds on something that is not its own
			return buildResult{status: contract.StageFailed, reason: "Tier 0 could not run: " + err.Error()}, nil
		}
		if _, err := m.writeArtifact(tier0Artifact, res); err != nil {
			return buildResult{}, err
		}
		if !res.OK {
			feedback = &res
			continue
		}
		// Tier 0 ok without having run anything (no engine had files or probes) is not a reviewed candidate: what nobody
		// executed is INCONCLUSIVE, never PASS (§5 Stage 3); the stage escalates instead of giving the tree a pass
		if !res.Reviewed {
			return buildResult{status: contract.StageEscalated, reason: noteTier0NotRun(false, unreviewed) + "; nobody reviewed the builder's candidate and the stage does not accept it"}, nil
		}
		// lemmas (§5 Stage 2, decision 12; lemmas.go): a refuted postcondition is the builder's, so it goes back as Tier 0
		// feedback and spends the round; the rest is recorded in candidate.json
		lem, err := m.checkLemmas(ctx, tree, resp.Lemmas, in.critical != nil && *in.critical)
		if err != nil {
			return buildResult{status: contract.StageFailed, reason: "the lemmas could not be checked: " + err.Error()}, nil
		}
		if lem.feedback != nil {
			if _, err := m.writeArtifact(tier0Artifact, *lem.feedback); err != nil {
				return buildResult{}, err
			}
			feedback = lem.feedback
			continue
		}
		// @a2dlc headers (§6.3): one declared for another run fails the stage; the missing ones go to candidate.json and the reason
		rev, err := reviewHeaders(work, diffs, header)
		if err != nil {
			return buildResult{}, err
		}
		if len(rev.mismatch) > 0 {
			return buildResult{status: contract.StageFailed, reason: strings.Join(rev.mismatch, " | ")}, nil
		}
		commit, err := m.app.Repo.CommitOn(tree, m.baseSha, "a2dlc "+role+" "+m.state.RunID, m.now())
		if err != nil {
			return buildResult{}, err
		}
		var notes []string
		if len(unreviewed) > 0 {
			notes = append(notes, "Tier 0 did not review: "+strings.Join(unreviewed, ", "))
		}
		notes = append(notes, rev.notes...)
		notes = append(notes, lem.notes...)
		if err := m.app.Repo.UpdateRef("refs/a2dlc/run/"+m.state.SliceID+"/"+role, commit); err != nil {
			notes = append(notes, "the commit "+commit[:10]+" was left without a ref: "+err.Error())
		}
		hash, err := m.app.Repo.CommitTree(commit)
		if err != nil {
			return buildResult{}, err
		}
		reviewed := res.Reviewed
		return buildResult{status: contract.StageDone, notes: notes,
			candidate: &contract.CandidateEntry{Builder: role, Commit: commit, TreeHash: hash, Iterations: round, Unchecked: unreviewed, HeadersMissing: rev.faltan, Tier0Reviewed: &reviewed, Lemmas: lem.recorded}}, nil
	}
	agent.last = findingsSummary(feedback)
	return m.exhausted(agent, tier0Artifact), nil
}

// join joins the non-empty reasons of a stage.
func join(parts ...string) string {
	var out []string
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, "; ")
}

// readTier0Extra reads what the intent adds to the Tier 0 policy: intent.capabilities.forbidden_imports and
// intent.boundary.forbidden_side_effects. A nil intent or one without them adds nothing.
func readTier0Extra(intent json.RawMessage) (app.Tier0Extra, error) {
	var out app.Tier0Extra
	if intent == nil {
		return out, nil
	}
	var in struct {
		Capabilities *contract.Capabilities `json:"capabilities"`
		Boundary     *contract.Boundary     `json:"boundary"`
	}
	if err := json.Unmarshal(intent, &in); err != nil {
		return out, fmt.Errorf("intent.json does not match the contract: %w", err)
	}
	if in.Capabilities != nil {
		out.ForbiddenImports = in.Capabilities.ForbiddenImports
	}
	if in.Boundary != nil {
		out.ForbiddenSideEffects = in.Boundary.ForbiddenSideEffects
	}
	return out, nil
}

// engineLanguage is builder_request.engine_language: the language of the only engine the config has, or else the one of
// the engine that covers the probes' files when they all agree. Only python and typescript are named by the schema; any
// other answer, or none, leaves the field to its default (python). The config is read by Tier 0 too, so an unreadable one
// is left to it.
func (m *Machine) engineLanguage(probes []contract.Probe) string {
	cfg, err := m.app.Config()
	if err != nil {
		return ""
	}
	langs := map[string]bool{}
	if len(cfg.Engines) == 1 {
		langs[cfg.Engines[0].Language] = true
	} else {
		var files []string
		for _, p := range probes {
			if p.File != nil && *p.File != "" {
				files = append(files, *p.File)
			}
		}
		byEngine, _ := verdict.Assign(cfg.Engines, files)
		for i := range byEngine {
			langs[cfg.Engines[i].Language] = true
		}
	}
	if len(langs) != 1 {
		return ""
	}
	for l := range langs {
		if l == "python" || l == "typescript" {
			return l
		}
	}
	return ""
}

// reviewDependencies returns one dependency_not_allowed escalation per dependency that the candidate adds or changes
// outside the base's allowlist (app.DependencyChanges), and the allowlist file the message names (config.supply_chain).
// diffs are the paths the candidate changed; tree is its tree. Unparsed manifests come out as unparsed:<file>.
func (m *Machine) reviewDependencies(role, tree string, diffs []string) ([]contract.Escalation, string, error) {
	allowFile, err := m.app.DepsAllowlistFile()
	if err != nil {
		return nil, "", err
	}
	allow, err := m.app.DepsAllowlist(m.baseSha)
	if err != nil {
		return nil, "", err
	}
	changes, err := m.app.DependencyChanges(m.baseSha, tree, diffs, allow)
	if err != nil {
		return nil, "", err
	}
	var items []contract.Escalation
	for _, c := range changes {
		if c.Allowed {
			continue
		}
		target := c.Ecosystem + ":" + c.Name
		e := itemEscalation("dependency_not_allowed", target,
			"The builder added or changed a dependency that the allowlist does not name. A person adds it to the allowlist (a signed change) or the builder drops it; the builder cannot waive it.",
			"dependency "+target+" is not in "+allowFile)
		e.Builder = role
		items = append(items, e)
	}
	return items, allowFile, nil
}

// readObject reads a JSON artifact of the run, which must be an object; nil if it does not exist.
func (m *Machine) readObject(artifact string) (json.RawMessage, error) {
	b, err := os.ReadFile(filepath.Join(m.dir, artifact))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(b, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("%s is not a JSON object: %v", artifact, err)
	}
	return json.RawMessage(b), nil
}

// candidate is the commit left by Stage 3 if it finished done with candidate.json, or "" (Stage 4 then judges the
// working tree, as check does). With N=2, once Stage 4 has chosen (selection.json), it is the selected one's.
func (m *Machine) candidate() (string, error) {
	c, err := m.readCandidate()
	if err != nil || c == nil {
		return "", err
	}
	return m.selected(c)
}

// readCandidate is candidate.json if Stage 3 finished done with it; nil if not.
func (m *Machine) readCandidate() (*contract.Candidate, error) {
	for _, s := range m.state.Stages {
		if s.ID != "3" || s.Status != contract.StageDone || s.Artifact == nil || s.Artifact.Path != ArtCandidate {
			continue
		}
		b, err := os.ReadFile(filepath.Join(m.dir, ArtCandidate))
		if err != nil {
			return nil, err
		}
		var c contract.Candidate
		if err := contract.DecodeStrictBytes(b, &c); err != nil {
			return nil, fmt.Errorf("%s out of contract: %w", ArtCandidate, err)
		}
		return &c, nil
	}
	return nil, nil
}
