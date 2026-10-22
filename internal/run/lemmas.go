package run

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// Lemmas as a gate (§5 Stage 2, decision 12). In a critical slice the builder structures the code as pure internal
// functions with a declared postcondition and lists them in its payload (lemmas); the core checks each one with the
// engines' lemmas operation (app.Lemmas) on the tree Tier 0 accepted, before the tree becomes the candidate:
//   - a postcondition the engine refutes ({args, returned} or {args, raised}) belongs to the builder, not to S*: it goes
//     back as Tier 0 feedback (kind lemma) and spends a round, like a compile error. Exhausting the rounds on it is CB-02b.
//   - in refactor, a lemma that diverges from the base's lemma of the same name ({args, old, new}) is a candidate
//     counterexample, not a contract FAIL: it is recorded as fail in candidate.json and in the stage reason, and Stage 4's
//     differential over the public functions is what tests it on the main function (see the README deviations).
//   - pass never raises the verdict: the composition is still judged by Stage 4. no_pair and inconclusive are recorded.
// A lemma declared in a standard slice is checked too: the builder claimed the postcondition. Lemmas required by the
// route and not declared are a note, not a round: §5 asks the builder for them by prompt and leaves the verdict to Stage 4.

// lemmaReview is what checking a builder's lemmas left: the feedback that sends it back (nil if none), what candidate.json
// records and the notes for the stage reason.
type lemmaReview struct {
	feedback *contract.Tier0Response
	recorded []contract.CandidateLemma
	notes    []string
}

// checkLemmas runs the declared lemmas against tree (app.Lemmas) and classifies the verdicts (classifyLemmas).
func (m *Machine) checkLemmas(ctx context.Context, tree string, declared []contract.BuilderLemma, required bool) (lemmaReview, error) {
	if len(declared) == 0 {
		var r lemmaReview
		if required {
			r.notes = append(r.notes, "lemmas required by the route (N=2) and the builder declared none")
		}
		return r, nil
	}
	mode := app.LemmaModeCheck
	if m.state.Mode == contract.ModeRefactor {
		mode = app.LemmaModeDiff
	}
	results, notes, err := m.app.Lemmas(ctx, m.baseSha, tree, declared, mode)
	if err != nil {
		return lemmaReview{}, err
	}
	r := classifyLemmas(results)
	r.notes = append(notes, r.notes...)
	return r, nil
}

// duplicateLemma is the first lemma name declared twice, or "" (role-payloads: names are unique, the core checks it).
func duplicateLemma(lemmas []contract.BuilderLemma) string {
	seen := map[string]bool{}
	for _, l := range lemmas {
		if seen[l.Name] {
			return l.Name
		}
		seen[l.Name] = true
	}
	return ""
}

// classifyLemmas turns the engines' verdicts into builder feedback (a refuted postcondition), candidate.json entries and
// notes. A fail whose counterexample carries old and new is a refactor divergence: recorded, not fed back.
func classifyLemmas(results []contract.LemmaResult) lemmaReview {
	var r lemmaReview
	counts := map[string]int{}
	var others []string
	for _, l := range results {
		r.recorded = append(r.recorded, contract.CandidateLemma{Name: l.Name, Status: l.Status})
		counts[l.Status]++
		switch {
		case l.Status == app.LemmaFail && isDivergence(l.Counterexample):
			r.notes = append(r.notes, fmt.Sprintf("lemma %s diverges from the base lemma (%s): recorded as fail; Stage 4's differential over the public functions is the test on the main function (§5 Stage 2)",
				l.Name, counterexampleText(l.Counterexample)))
		case l.Status == app.LemmaFail:
			if r.feedback == nil {
				r.feedback = &contract.Tier0Response{SchemaVersion: contract.SchemaVersion, Findings: []contract.Tier0Finding{}}
			}
			r.feedback.Findings = append(r.feedback.Findings, contract.Tier0Finding{Kind: contract.Tier0Lemma, File: l.Name,
				Message: fmt.Sprintf("lemma %s: %s (%s)", l.Name, l.Message, counterexampleText(l.Counterexample))})
		case l.Status != app.LemmaPass:
			others = append(others, l.Name+": "+l.Status+" ("+l.Message+")")
		}
	}
	if len(results) > 0 {
		parts := []string{fmt.Sprintf("lemmas: %d declared", len(results))}
		for _, s := range []string{app.LemmaPass, app.LemmaFail, app.LemmaNoPair, app.LemmaInconclusive} {
			if counts[s] > 0 {
				parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
			}
		}
		summary := strings.Join(parts, ", ")
		if len(others) > 0 {
			summary += " (" + strings.Join(others, "; ") + ")"
		}
		r.notes = append([]string{summary}, r.notes...)
	}
	return r
}

// isDivergence: the counterexample shape of a refactor diff ({args, old, new}; engines/py/lemmas.py).
func isDivergence(c map[string]any) bool {
	_, old := c["old"]
	_, nw := c["new"]
	return old && nw
}

// counterexampleText renders a counterexample as "k=v" pairs in key order, so the builder reads it in the feedback.
func counterexampleText(c map[string]any) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %v", k, c[k]))
	}
	return strings.Join(parts, "; ")
}
