package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// N=2 cross-builder and Occam selection (block 8 of the build order, ADR 0004; protocol §5 Stages 2 to 5).
//
// Stage 3: if route.json says track N=2 and roles.json has builder_orthogonal, after the primary the orthogonal runs with
// the same Tier 0 loop, with its own /work exported from the base and its own feedback (tier0_feedback_orthogonal.json):
// it sees nothing of the primary. They run in sequence, not in parallel (Invoke is sequential). candidate.json lists the
// ones that converged (candidates) and the ones that did not (dropped); if one does not converge, the other continues as
// N=1 and the reason stays in the stage.
//
// Stage 4: with two candidates, each one goes through the oracle loop → Saboteur with the other as peer (tests_diff: each
// builder's tests run on the other's tree), then the equivalence_s differential (the same generated inputs through the
// engines, app.Equivalence). If both fail a test that the other passes, or the two differ on an input, it is CB-01: the spec leaves room
// for two behaviours and a person decides. Otherwise, Occam selection happens at the end of Stage 4, not in Stage 5: Stage
// 5 is the mutation of block 5, and its kill gate measures the suite, not the candidate, so choosing earlier would not
// change what the gate measures and lets the mutation run on the chosen one.

const (
	ArtTier0Orthogonal   = "tier0_feedback_orthogonal.json" // the orthogonal's Tier 0 feedback (N=2)
	ArtSelection         = "selection.json"                 // the Occam selection between the two candidates
	ArtVerdictPrimary    = "verdict_primary.json"
	ArtVerdictOrthogonal = "verdict_orthogonal.json"
)

// trackN2 says whether Stage 3 runs both builders: route.json with track N=2 (read with readRoute, strictly against
// contracts/route.schema.json: a route.json that does not match the contract is an error, not an N=1) and
// builder_orthogonal in roles.json. If the router asks for N=2 and the role is missing, it returns the note that goes
// into the stage's reason: it never silently falls back to N=1. In refactor and brownfield_facade the second builder is
// the legacy one (route.json says so): the differential against the Stage 4 base already covers it, so no orthogonal
// LLM is launched and there is nothing to note.
func (m *Machine) trackN2() (bool, string, error) {
	r, err := m.readRoute()
	if err != nil {
		return false, "", err
	}
	if r.Track != contract.TrackN2 {
		return false, "", nil
	}
	if m.state.Mode == contract.ModeRefactor || m.state.Mode == contract.ModeBrownfield {
		return false, "", nil
	}
	if _, ok := m.roles.Roles[contract.RoleBuilderOrthogonal]; !ok {
		return false, "route.json asks for N=2 and " + pathRoles + " has no builder_orthogonal: ran N=1", nil
	}
	return true, "", nil
}

// tier0N2 runs the orthogonal after the primary (p) and writes candidate.json with those that converged.
func (m *Machine) tier0N2(ctx context.Context, p buildResult, in builderInput) (string, string, string, error) {
	o, err := m.build(ctx, contract.RoleBuilderOrthogonal, ArtTier0Orthogonal, in)
	if err != nil {
		return "", "", "", err
	}
	if len(o.boundary) > 0 {
		return m.escalateBoundary(o)
	}
	var notes []string
	if fp, fo := m.roles.Roles[contract.RoleBuilderPrimary].Family, m.roles.Roles[contract.RoleBuilderOrthogonal].Family; fp == fo {
		notes = append(notes, "N=2 with the same declared family ("+fp+") in both builders: does not lower correlated failures")
	}
	var candidates []contract.CandidateEntry
	var dropped []contract.DroppedBuilder
	failed := false
	for _, x := range []struct {
		role string
		c    buildResult
	}{{contract.RoleBuilderPrimary, p}, {contract.RoleBuilderOrthogonal, o}} {
		for _, n := range x.c.notes {
			notes = append(notes, x.role+": "+n)
		}
		if x.c.candidate != nil {
			candidates = append(candidates, *x.c.candidate)
			continue
		}
		failed = failed || x.c.status == contract.StageFailed
		dropped = append(dropped, contract.DroppedBuilder{Builder: x.role, Reason: x.c.reason})
	}
	if len(candidates) == 0 {
		st := contract.StageEscalated
		if failed {
			st = contract.StageFailed // a tool problem, not a builder problem: INCONCLUSIVE
		}
		reason := "N=2 without a candidate: " + dropped[0].Builder + ": " + dropped[0].Reason + "; " +
			dropped[1].Builder + ": " + dropped[1].Reason
		// CB-02b: builders that exhausted their rounds (and their tier_up, if any) go into a builder_exhaustion escalation
		var ags []exhaustion
		for _, x := range []buildResult{p, o} {
			if x.agot != nil {
				ags = append(ags, *x.agot)
			}
		}
		if st == contract.StageEscalated && len(ags) > 0 {
			artifact, err := m.escalateExhausted(ags)
			if err != nil {
				return "", "", "", err
			}
			return st, join(append([]string{"CB-02b: " + reason}, notes...)...), artifact, nil
		}
		return st, join(append([]string{reason}, notes...)...), "", nil
	}
	for _, d := range dropped {
		notes = append(notes, "N=2: "+d.Builder+" without a candidate ("+d.Reason+"); "+candidates[0].Builder+" continues alone")
	}
	c := candidates[0]
	artifact, err := m.writeArtifact(ArtCandidate, contract.Candidate{SchemaVersion: contract.SchemaVersion, Builder: c.Builder,
		Commit: c.Commit, TreeHash: c.TreeHash, Iterations: c.Iterations, Unchecked: c.Unchecked, HeadersMissing: c.HeadersMissing, Tier0Reviewed: c.Tier0Reviewed, Lemmas: c.Lemmas, Candidates: candidates, Dropped: dropped})
	return contract.StageDone, join(notes...), artifact, err
}

// selected is the commit of the chosen candidate in selection.json, if there is one and it chose; otherwise, the top-level one.
func (m *Machine) selected(c *contract.Candidate) (string, error) {
	b, err := os.ReadFile(filepath.Join(m.dir, ArtSelection))
	if errors.Is(err, os.ErrNotExist) {
		return c.Commit, nil
	}
	if err != nil {
		return "", err
	}
	var s contract.Selection
	if err := contract.DecodeStrictBytes(b, &s); err != nil {
		return "", fmt.Errorf("%s does not match the contract: %w", ArtSelection, err)
	}
	if s.Selected != nil {
		for _, e := range c.Candidates {
			if e.Builder == *s.Selected {
				return e.Commit, nil
			}
		}
	}
	return c.Commit, nil
}

// seal adds the v2 fields to the run's verdict: the stage that emitted it and the hash of S* if the run has one.
func (m *Machine) seal(v *contract.Verdict) error {
	v.Stage = "4"
	h, err := HashFile(filepath.Join(m.dir, ArtContract))
	switch {
	case err == nil:
		v.ContractHash = h
	case !errors.Is(err, os.ErrNotExist):
		return err
	}
	return nil
}

// stageJudgment is Stage 4: with two candidates, the cross judgment and the selection; otherwise, stageTier1 (saboteur.go).
func stageJudgment(ctx context.Context, m *Machine) (string, string, string, error) {
	for _, artifact := range []string{ArtSelection, ArtVerdictPrimary, ArtVerdictOrthogonal, ArtSignatures} {
		if err := os.Remove(filepath.Join(m.dir, artifact)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", "", err
		}
	}
	c, err := m.readCandidate()
	if err != nil {
		return "", "", "", err
	}
	if c == nil || len(c.Candidates) < 2 {
		return stageTier1(ctx, m)
	}
	return m.judgmentN2(ctx, c.Candidates[0], c.Candidates[1])
}

func (m *Machine) judgmentN2(ctx context.Context, p, o contract.CandidateEntry) (string, string, string, error) {
	for _, artifact := range []string{ArtVerdict, ArtSignatures} {
		if err := os.Remove(filepath.Join(m.dir, artifact)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", "", err
		}
	}
	overlay := filepath.Join(m.dir, dirOverlay)
	verdictP, reasonP, fuzzP, err := m.judge(ctx, app.CheckOptions{CandidateTree: p.Commit, Peers: []string{o.Commit}}, ArtVerdictPrimary)
	if err != nil {
		return "", "", "", err
	}
	before, err := relativePathsUnder(overlay)
	if err != nil {
		return "", "", "", err
	}
	// the signatures the Saboteur sees in the orthogonal's rounds are those of its candidate, not the primary's
	if err := os.Remove(filepath.Join(m.dir, ArtSignatures)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", "", err
	}
	verdictO, reasonO, fuzzO, err := m.judge(ctx, app.CheckOptions{CandidateTree: o.Commit, Peers: []string{p.Commit}}, ArtVerdictOrthogonal)
	if err != nil {
		return "", "", "", err
	}
	after, err := relativePathsUnder(overlay)
	if err != nil {
		return "", "", "", err
	}
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		// the Saboteur brought tests in the orthogonal's rounds: the primary is judged again with them, without new rounds,
		// with the same π_S relaxations as the first pass (judge sets them in each check)
		if verdictP, err = m.app.CheckWith(ctx, m.baseSha, app.CheckOptions{CandidateTree: p.Commit, Peers: []string{o.Commit}, TestsOverlay: overlay,
			EquivalenceRelaxations: m.relaxations(), StrictDivergence: true}); err != nil {
			return "", "", "", err
		}
		if err := m.seal(&verdictP); err != nil {
			return "", "", "", err
		}
		if _, err := m.writeArtifact(ArtVerdictPrimary, verdictP); err != nil {
			return "", "", "", err
		}
		reasonP = join(reasonP, "judged again with the tests from the orthogonal's rounds")
	}
	// equivalence_s: the two candidates on the same generated inputs, through the engines (the π_S relaxations as in the
	// judgments above). It goes into both verdicts; a function that was not compared is not a pass.
	eq, err := m.app.Equivalence(ctx, m.baseSha, p.Commit, o.Commit, m.relaxations())
	if err != nil {
		return "", "", "", err
	}
	withEquivalence(&verdictP, eq)
	withEquivalence(&verdictO, eq)
	if _, err := m.writeArtifact(ArtVerdictPrimary, verdictP); err != nil {
		return "", "", "", err
	}
	if _, err := m.writeArtifact(ArtVerdictOrthogonal, verdictO); err != nil {
		return "", "", "", err
	}
	extra := append(app.EquivalenceEscalations(eq), append(fuzzP, fuzzO...)...)
	judgments := []judgment{{p, verdictP}, {o, verdictO}}
	selection := contract.Selection{SchemaVersion: contract.SchemaVersion}
	for _, j := range judgments {
		selection.Candidates = append(selection.Candidates, contract.SelectionCandidate{Builder: j.c.Builder, Commit: j.c.Commit, Verdict: j.v.Verdict})
	}
	reasons := []string{}
	for i, mot := range []string{reasonP, reasonO} {
		if mot != "" {
			reasons = append(reasons, judgments[i].c.Builder+": "+mot)
		}
	}
	if eq.Gate.Status != contract.GateNotApplicable {
		reasons = append(reasons, "equivalence_s: "+eq.Gate.Reason)
	}
	// CB-01: each one fails a test that the other passes, or the two give different outputs on the same input. Both meet
	// what was asked of them and are not equivalent: the spec is under-specified and choosing A or B is a person's call (§7).
	divergentP, divergentO := divergent(verdictP), divergent(verdictO)
	testBased := len(divergentP) > 0 && len(divergentO) > 0
	if testBased || len(eq.Divergences) > 0 {
		var parts []string
		if testBased {
			parts = append(parts, fmt.Sprintf("each one fails a test that the other passes (%s: %s; %s: %s)",
				p.Builder, strings.Join(divergentP, ", "), o.Builder, strings.Join(divergentO, ", ")))
		}
		if len(eq.Divergences) > 0 {
			parts = append(parts, "the same inputs give different outputs (equivalence_s: "+divergentTargets(eq)+")")
		}
		selection.Reason = "CB-01: divergence between builders: " + strings.Join(parts, "; ")
		if err := m.closeN2(selection, judgments, verdictP, extra); err != nil {
			return "", "", "", err
		}
		return contract.StageEscalated, join(append([]string{selection.Reason}, reasons...)...), ArtSelection, nil
	}
	winner, why := -1, ""
	var passing []int
	for i, j := range judgments {
		if j.v.Verdict == contract.PASS {
			passing = append(passing, i)
		}
	}
	switch len(passing) {
	case 2:
		var complexities [2]*contract.Complexity
		var unmeasured []string
		for i, j := range judgments {
			c, err := m.app.Complexity(ctx, m.baseSha, j.c.Commit)
			if err != nil {
				unmeasured = append(unmeasured, j.c.Builder+": "+err.Error())
				continue
			}
			complexities[i] = &c
			selection.Candidates[i].Complexity = &c
		}
		winner, why = occam(complexities)
		why = "both PASS; " + why
		if len(unmeasured) > 0 {
			why += " (complexity unmeasured: " + strings.Join(unmeasured, "; ") + ")"
		}
	case 1:
		winner = passing[0]
		other := judgments[1-winner]
		why = fmt.Sprintf("only %s gave PASS (%s: %s)", judgments[winner].c.Builder, other.c.Builder, other.v.Verdict)
	default:
		worst := 0
		if rank(verdictO.Verdict) > rank(verdictP.Verdict) {
			worst = 1
		}
		why = fmt.Sprintf("neither gave PASS (%s: %s, %s: %s): the outcome is the worst one, the one from %s", p.Builder, verdictP.Verdict, o.Builder, verdictO.Verdict, judgments[worst].c.Builder)
		judgments[0], judgments[worst] = judgments[worst], judgments[0] // the verdict that remains is the worst
		winner = -1
	}
	final := judgments[0].v
	if winner >= 0 {
		b := judgments[winner].c.Builder
		selection.Selected = &b
		final = judgments[winner].v
	}
	selection.Reason = why
	if err := m.closeN2(selection, []judgment{{p, verdictP}, {o, verdictO}}, final, extra); err != nil {
		return "", "", "", err
	}
	if _, err := m.writeArtifact(ArtVerdict, final); err != nil {
		return "", "", "", err
	}
	out := final.Verdict
	m.state.Outcome = &out
	return contract.StageDone, join(append([]string{why}, reasons...)...), ArtVerdict, nil
}

type judgment struct {
	c contract.CandidateEntry
	v contract.Verdict
}

// closeN2 writes selection.json, escalations.json with the escalations of both candidates (each with its builder) and
// leaves as the repo's .a2dlc/verdict.json the verdict that counts (the selected, the worst, or the primary under CB-01):
// check writes its own on each pass and otherwise the last candidate checked would win.
func (m *Machine) closeN2(selection contract.Selection, judgments []judgment, final contract.Verdict, extra []contract.Escalation) error {
	if _, err := m.writeArtifact(ArtSelection, selection); err != nil {
		return err
	}
	escalations := contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: final.TreeHash, CreatedAt: final.CreatedAt, Items: []contract.Escalation{}}
	for _, j := range judgments {
		for _, e := range app.Escalations(j.v) {
			e.Builder = j.c.Builder
			escalations.Items = append(escalations.Items, e)
		}
	}
	// the differential and the fuzzing round judge both candidates at once: their escalations name no builder
	escalations.Items = append(escalations.Items, extra...)
	// one escalation per id (kind plus target): the primary's and the orthogonal's fuzzing findings on the same function, or a
	// fuzzing finding and an equivalence divergence on it, are the same thing about the same target
	escalations.Items = app.DedupeByID(escalations.Items)
	if _, err := m.writeArtifact(ArtEscalations, escalations); err != nil {
		return err
	}
	return m.app.SaveVerdict(final)
}

// withEquivalence records the equivalence_s gate in a verdict. config.tests unset leaves the other two test gates
// not_configured, as the schema wants them present. A PASS with a function that was not compared is INCONCLUSIVE.
func withEquivalence(v *contract.Verdict, eq app.Equivalence) {
	if v.TestGates == nil {
		na := contract.Gate{Status: contract.GateNotConfigured, Reason: "config.tests is not set"}
		v.TestGates = &contract.TestGates{NOPOracle: na, Baseline: na}
	}
	g := eq.Gate
	v.TestGates.EquivalenceS = &g
	if v.Verdict == contract.PASS && len(eq.Inconclusive) > 0 {
		v.Verdict = contract.INCONCLUSIVE
	}
}

// divergentTargets lists the functions where the two candidates differ, for the CB-01 reason.
func divergentTargets(eq app.Equivalence) string {
	out := make([]string, 0, len(eq.Divergences))
	for _, t := range eq.Divergences {
		out = append(out, t.Target)
	}
	return strings.Join(out, ", ")
}

// divergent are the tests in tests_diff that the candidate fails and some peer passes (without waiver): half of a CB-01.
func divergent(v contract.Verdict) []string {
	var out []string
	for _, t := range v.Tests {
		if t.Gate != contract.GateTestsDiff || t.Verdict != contract.FAIL || t.Waived {
			continue
		}
		for tree, st := range t.Trees {
			if strings.HasPrefix(tree, "peer:") && st == contract.TestPassed {
				out = append(out, t.Target)
				break
			}
		}
	}
	return out
}

// rank orders the verdicts from worst to best for the test case without any PASS: FAIL is worse than INCONCLUSIVE.
func rank(v string) int {
	switch v {
	case contract.FAIL:
		return 2
	case contract.INCONCLUSIVE:
		return 1
	}
	return 0
}

// occam chooses between two candidates with PASS the one with lower complexity, lexicographically (§5 Stage 5): sum of
// cyclomatic, then of ast_nodes, then diff lines (added + removed). A metric that is missing on either side is not
// compared; a tie goes to the primary (index 0), so the choice is deterministic.
func occam(complexities [2]*contract.Complexity) (int, string) {
	if complexities[0] == nil || complexities[1] == nil {
		return 0, "builder_primary wins by tie-break: the complexity of one of them could not be measured"
	}
	a, b := complexities[0], complexities[1]
	metrics := []struct {
		name string
		x, y *int
	}{{"cyclomatic", a.Cyclomatic, b.Cyclomatic}, {"ast_nodes", a.ASTNodes, b.ASTNodes}}
	la, lb := a.LocAdded+a.LocRemoved, b.LocAdded+b.LocRemoved
	metrics = append(metrics, struct {
		name string
		x, y *int
	}{"diff lines", &la, &lb})
	note := ""
	if len(a.Unmeasured) > 0 || len(b.Unmeasured) > 0 {
		note = fmt.Sprintf(" (unmeasured: builder_primary %v, builder_orthogonal %v)", a.Unmeasured, b.Unmeasured)
	}
	for _, metric := range metrics {
		if metric.x == nil || metric.y == nil || *metric.x == *metric.y {
			continue
		}
		if *metric.y < *metric.x {
			return 1, fmt.Sprintf("builder_orthogonal wins for lower complexity (%s %d versus %d)%s", metric.name, *metric.y, *metric.x, note)
		}
		return 0, fmt.Sprintf("builder_primary wins for lower complexity (%s %d versus %d)%s", metric.name, *metric.x, *metric.y, note)
	}
	return 0, "tie in complexity: builder_primary wins by tie-break" + note
}
