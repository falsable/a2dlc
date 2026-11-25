package run

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// Divergences as builder feedback (§5 Stage 3). Once Tier 0 and the lemmas accept the builder's tree, the core runs the
// engines' differential on it against the run's base (app.Divergences, the operation Stage 4 runs) and sends back each
// target that FAILs with a counterexample carrying old and new, as a finding of kind divergence: the builder restores the
// old behavior if the intent does not ask for the change, or adds a test that pins the new behavior if it does.
// A divergence the builder keeps on purpose does not go away, and the differential cannot tell a wanted change from an
// accident, so each function and input is returned once and never on the last round: after that the run goes on, Stage 4
// judges the divergence (failing in strict runs, N=2, or when never returned; receipt note outside critical_code when returned and kept) and the stage reason says the
// builder was told. It only adds feedback; nothing here raises or lowers a verdict, and an engine that cannot run is a note.

// divergenceReview is what the differential left: the feedback that sends the builder back (nil if none), the notes,
// the kept divergences already returned to the builder, and the engine diff answers for RFC-0005.
type divergenceReview struct {
	feedback *contract.Tier0Response
	notes    []string
	kept     []contract.ReturnedDivergence
	answers  app.DiffAnswers // the engines' answers on tree, for Stage 4 with config.run_differential "stage3" (RFC-0005)
}

// checkDivergences runs the differential on tree. returned holds the divergences the builder has already been told about
// (it is updated); nextRound is whether there is a round left to act on one.
func (m *Machine) checkDivergences(ctx context.Context, tree string, returned map[string]bool, nextRound bool) (divergenceReview, error) {
	found, notes, answers, err := m.app.Divergences(ctx, m.baseSha, tree)
	if err != nil {
		return divergenceReview{}, err
	}
	r := divergenceReview{notes: notes, answers: answers}
	seenKept := map[string]bool{}
	for _, d := range found {
		key := "divergence|" + d.Target + "|" + d.Args
		if nextRound && !returned[key] {
			returned[key] = true
			if r.feedback == nil {
				r.feedback = &contract.Tier0Response{SchemaVersion: contract.SchemaVersion, Findings: []contract.Tier0Finding{}}
			}
			r.feedback.Findings = append(r.feedback.Findings, contract.Tier0Finding{Kind: contract.Tier0Divergence, File: divergenceFile(d), Message: divergenceFindingMessage(d)})
			continue
		}
		why := "no round is left to act on it"
		if returned[key] {
			why = "returned to the builder, which kept it"
			if !seenKept[key] {
				seenKept[key] = true
				r.kept = append(r.kept, contract.ReturnedDivergence{Target: d.Target, Args: d.Args})
			}
		}
		r.notes = append(r.notes, fmt.Sprintf("divergence %s (args: %s; old: %s; new: %s): %s; Stage 4 judges it as it always does", d.Target, d.Args, d.Old, d.New, why))
	}
	sort.Slice(r.kept, func(i, j int) bool {
		if r.kept[i].Target != r.kept[j].Target {
			return r.kept[i].Target < r.kept[j].Target
		}
		return r.kept[i].Args < r.kept[j].Args
	})
	return r, nil
}

// divergenceFile is the file of a target ("file::function").
func divergenceFile(d app.Divergence) string {
	if i := strings.Index(d.Target, "::"); i > 0 {
		return d.Target[:i]
	}
	return d.Target
}

// divergenceFindingMessage is what the builder reads: the function, the input, both results and the two ways out.
func divergenceFindingMessage(d app.Divergence) string {
	return fmt.Sprintf("%s gives a different result than the base: for the input %s the old code returned %s and the new code returns %s. "+
		"If the intent does not ask for this change, restore the old behavior; if it does, add a test that pins the new behavior.",
		d.Target, d.Args, d.Old, d.New)
}
