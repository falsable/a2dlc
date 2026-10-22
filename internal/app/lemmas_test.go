package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// lemmasEngine wraps the fake engine with a lemmas operation: the answer is scripted per call; nil script means the
// engine does not do lemmas.
type lemmasEngine struct {
	*fakeEngine
	script func(req contract.Request) contract.LemmasResponse
	seen   []contract.Request
}

func (e *lemmasEngine) Lemmas(_ context.Context, _ contract.EngineConfig, req contract.Request, _, _ string) (contract.LemmasResponse, error) {
	e.seen = append(e.seen, req)
	if e.script == nil {
		return contract.LemmasResponse{}, fmt.Errorf("%w: old engine", engine.ErrWithoutLemmas)
	}
	r := e.script(req)
	return r, engine.ValidateLemmas(r)
}

func TestLemmasSendsTheDeclaredFunctionsAndRecordsEachVerdict(t *testing.T) {
	a, fe, _ := newRepo(t)
	le := &lemmasEngine{fakeEngine: fe, script: func(contract.Request) contract.LemmasResponse {
		return contract.LemmasResponse{SchemaVersion: 1, Lemmas: []contract.LemmaResult{
			{Name: "g", Status: LemmaPass, Message: "confirmed"},
			{Name: "h", Status: LemmaFail, Message: "broken", Counterexample: map[string]any{"args": "x=-1", "returned": "-1"}},
		}}
	}}
	a.Runner = le
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	declared := []contract.BuilderLemma{{Name: "g", Postcondition: "result >= 0"}, {Name: "h", Postcondition: "result > 0"}, {Name: "k", Postcondition: "true"}}
	got, notes, err := a.Lemmas(context.Background(), "HEAD", c, declared, LemmaModeDiff)
	if err != nil || len(le.seen) != 1 || len(notes) != 0 {
		t.Fatalf("%v %d %v", err, len(le.seen), notes)
	}
	var budget contract.LemmasBudget
	if err := json.Unmarshal(le.seen[0].Budget, &budget); err != nil || budget.Mode != LemmaModeDiff || len(budget.Functions) != 3 || budget.Functions[1].Postcondition != "result > 0" {
		t.Fatalf("%v %+v", err, budget)
	}
	// g and h take the engine's verdict; k, which no engine answered, stays inconclusive (never pass)
	if len(got) != 3 || got[0].Status != LemmaPass || got[1].Status != LemmaFail || got[1].Counterexample["args"] != "x=-1" ||
		got[2].Status != LemmaInconclusive || !strings.Contains(got[2].Message, "no engine checked") {
		t.Fatalf("%+v", got)
	}
}

func TestLemmasWithoutAnEngineThatDoesThemStayInconclusive(t *testing.T) {
	a, fe, _ := newRepo(t)
	a.Runner = &lemmasEngine{fakeEngine: fe}
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	got, notes, err := a.Lemmas(context.Background(), "HEAD", c, []contract.BuilderLemma{{Name: "g", Postcondition: "true"}}, LemmaModeCheck)
	if err != nil || len(got) != 1 || got[0].Status != LemmaInconclusive || len(notes) != 2 ||
		!strings.Contains(notes[0], "typescript does not do lemmas") || !strings.Contains(notes[1], "stay inconclusive") {
		t.Fatalf("%v %+v %v", err, got, notes)
	}
	// a runner without the operation at all is said, not an error
	a.Runner = fe
	got, notes, err = a.Lemmas(context.Background(), "HEAD", c, []contract.BuilderLemma{{Name: "g", Postcondition: "true"}}, LemmaModeCheck)
	if err != nil || got[0].Status != LemmaInconclusive || len(notes) != 1 || !strings.Contains(notes[0], "does not do lemmas") {
		t.Fatalf("%v %+v %v", err, got, notes)
	}
	// a changed file no engine covers: nothing to ask
	c = candidateOf(t, a, map[string]string{"README.md": "bye\n"})
	a.Runner = &lemmasEngine{fakeEngine: fe}
	_, notes, err = a.Lemmas(context.Background(), "HEAD", c, []contract.BuilderLemma{{Name: "g", Postcondition: "true"}}, LemmaModeCheck)
	if err != nil || len(notes) != 1 || !strings.Contains(notes[0], "no engine covers") {
		t.Fatalf("%v %v", err, notes)
	}
}

func TestLemmaRankMergesByPrecedence(t *testing.T) {
	if !(lemmaRank(LemmaFail) > lemmaRank(LemmaPass) && lemmaRank(LemmaPass) > lemmaRank(LemmaNoPair) && lemmaRank(LemmaNoPair) > lemmaRank(LemmaInconclusive)) {
		t.Fatal("fail > pass > no_pair > inconclusive")
	}
}
