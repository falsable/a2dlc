package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Lemma statuses of engine-lemmas-response (and candidate.json lemmas[].status).
const (
	LemmaPass         = "pass"
	LemmaFail         = "fail"
	LemmaInconclusive = "inconclusive"
	LemmaNoPair       = "no_pair"
)

// LemmaModeCheck and LemmaModeDiff are the budget.mode values of operation: lemmas (engine-request.schema.json):
// check runs the postcondition only; diffbehavior also compares each lemma with the base's lemma of the same name.
const (
	LemmaModeCheck = "check"
	LemmaModeDiff  = "diffbehavior"
)

// Lemmas checks the lemmas a builder declared on its candidate (§5 Stage 2, decision 12: pure internal functions with a
// declared postcondition). Every engine that has a changed file receives the whole list (operation: lemmas, budget
// {functions, mode}) and answers one verdict per lemma; a lemma is a function name, not a file, so the engines locate it
// themselves. The verdicts of several engines are merged per lemma by precedence fail > pass > no_pair > inconclusive:
// an engine that does not find the function says inconclusive and never hides another engine's verdict. An engine
// without the operation (ErrWithoutLemmas) is listed in notes and leaves its lemmas to the others; without any engine
// that answers, every lemma stays inconclusive, never pass. An engine that fails for another reason is a tool error.
// mode is LemmaModeCheck or LemmaModeDiff (refactor).
func (a App) Lemmas(ctx context.Context, base, candidate string, lemmas []contract.BuilderLemma, mode string) ([]contract.LemmaResult, []string, error) {
	notes := []string{}
	out := make([]contract.LemmaResult, 0, len(lemmas))
	for _, l := range lemmas {
		out = append(out, contract.LemmaResult{Name: l.Name, Status: LemmaInconclusive, Message: "no engine checked this lemma"})
	}
	if len(lemmas) == 0 {
		return out, notes, nil
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return out, nil, fmt.Errorf("base %q: %w", base, err)
	}
	cfg, err := a.configAt(baseSha)
	if err != nil {
		return out, nil, err
	}
	tree, err := a.candidateTree(candidate)
	if err != nil {
		return out, nil, err
	}
	changed, _, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return out, nil, err
	}
	byEngine, _ := verdict.Assign(cfg.Engines, changed)
	if len(byEngine) == 0 {
		notes = append(notes, "no engine covers the changed files: the lemmas were not checked")
		return out, notes, nil
	}
	if a.Runner == nil {
		return out, nil, fmt.Errorf("this binary has no engines to check lemmas")
	}
	lr, ok := a.Runner.(engine.LemmasRunner)
	if !ok {
		notes = append(notes, "the engine runner of this binary does not do lemmas: the lemmas were not checked")
		return out, notes, nil
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
	if err != nil {
		return out, nil, err
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
	if err != nil {
		return out, nil, err
	}
	defer os.RemoveAll(candDir)
	if err := a.Repo.Export(baseSha, baseDir); err != nil {
		return out, nil, err
	}
	if err := a.Repo.Export(tree, candDir); err != nil {
		return out, nil, err
	}
	specs := make([]contract.LemmaSpec, 0, len(lemmas))
	for _, l := range lemmas {
		specs = append(specs, contract.LemmaSpec{Name: l.Name, Postcondition: l.Postcondition})
	}
	b, err := json.Marshal(contract.LemmasBudget{Functions: specs, Mode: mode})
	if err != nil {
		return out, nil, err
	}
	answered := false
	for _, i := range sortedIndexes(byEngine) {
		e, files := cfg.Engines[i], byEngine[i]
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: b}
		r, err := lr.Lemmas(ctx, e, req, baseDir, candDir)
		if errors.Is(err, engine.ErrWithoutLemmas) {
			notes = append(notes, "engine "+e.Language+" does not do lemmas")
			continue
		}
		if err != nil {
			return out, nil, fmt.Errorf("lemmas of engine %s: %w", e.Language, err)
		}
		answered = true
		for _, got := range r.Lemmas {
			for k := range out {
				if out[k].Name == got.Name && lemmaRank(got.Status) > lemmaRank(out[k].Status) {
					out[k] = got
				}
			}
		}
	}
	if !answered {
		notes = append(notes, "no engine checked the lemmas: they stay inconclusive")
	}
	return out, notes, nil
}

// lemmaRank orders the statuses for the merge across engines: a verdict beats a non-verdict, and a counterexample beats
// everything. inconclusive ranks 0 so the initial placeholder is replaced by any engine answer that says more.
func lemmaRank(status string) int {
	switch status {
	case LemmaFail:
		return 3
	case LemmaPass:
		return 2
	case LemmaNoPair:
		return 1
	}
	return 0
}
