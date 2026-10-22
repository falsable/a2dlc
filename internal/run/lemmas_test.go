package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// Lemmas of the fake engine of a2dlc run: scripted per call on the candidate's src/f.py; without a script the engine does
// not do lemmas.
func (f *engineRun) Lemmas(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candidateDir string) (contract.LemmasResponse, error) {
	if f.lemmas == nil {
		return contract.LemmasResponse{}, fmt.Errorf("%w: fake without lemmas", engine.ErrWithoutLemmas)
	}
	var budget contract.LemmasBudget
	if err := json.Unmarshal(req.Budget, &budget); err != nil {
		return contract.LemmasResponse{}, err
	}
	b, _ := os.ReadFile(filepath.Join(candidateDir, "src/f.py"))
	f.lemmaCalls++
	r := f.lemmas(budget, string(b))
	return r, engine.ValidateLemmas(r)
}

// fakeBuilderLemmas is fakeBuilder with a lemma list in the payload.
func fakeBuilderLemmas(t *testing.T, requests *[]contract.BuilderRequest, lemmas []contract.BuilderLemma, content func(contract.BuilderRequest) map[string]string) func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
	inner := fakeBuilder(t, requests, content)
	return func(s LaunchSpec) (*contract.RoleResponse, int, bool) {
		resp, exit, to := inner(s)
		var pl contract.BuilderResponse
		json.Unmarshal(resp.Payload, &pl)
		pl.Lemmas = lemmas
		resp.Payload, _ = json.Marshal(pl)
		return resp, exit, to
	}
}

// A refuted postcondition is the builder's (§5 Stage 2): it goes back as Tier 0 feedback (kind lemma) and spends a round;
// when the next round holds, the candidate records the verdicts.
func TestStage3RefutedLemmaGoesBackToTheBuilder(t *testing.T) {
	a, dir, eng := repoBuilder(t)
	withRouterN2(t) // critical route; builder_orthogonal absent, so the primary runs alone with critical=true
	eng.lemmas = func(b contract.LemmasBudget, f string) contract.LemmasResponse {
		if len(b.Functions) != 1 || b.Functions[0].Postcondition != "result >= 0" || b.Mode != app.LemmaModeCheck {
			t.Fatalf("%+v", b)
		}
		if strings.Contains(f, "abs(") {
			return contract.LemmasResponse{SchemaVersion: 1, Lemmas: []contract.LemmaResult{{Name: "f", Status: app.LemmaPass, Message: "confirmed"}}}
		}
		return contract.LemmasResponse{SchemaVersion: 1, Lemmas: []contract.LemmaResult{{Name: "f", Status: app.LemmaFail,
			Message: "CrossHair found an input for which the lemma breaks its postcondition", Counterexample: map[string]any{"args": "x=-1", "returned": "-1"}}}}
	}
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilderLemmas(t, &requests, []contract.BuilderLemma{{Name: "f", Postcondition: "result >= 0"}}, func(pl contract.BuilderRequest) map[string]string {
		if pl.Tier0Feedback != nil {
			return map[string]string{"src/f.py": "def f(x: int) -> int:\n    return abs(x)\n"}
		}
		return map[string]string{"src/f.py": "def f(x: int) -> int:\n    return x\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "lem", contract.ModeLite, "f nonneg" })
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "lem", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || len(requests) != 2 || eng.lemmaCalls != 2 || eng.tier0 != 2 {
		t.Fatalf("%+v: requests %d, lemmas %d, tier0 %d", s3, len(requests), eng.lemmaCalls, eng.tier0)
	}
	fb := requests[1].Tier0Feedback
	if fb == nil || fb.OK || len(fb.Findings) != 1 || fb.Findings[0].Kind != contract.Tier0Lemma || fb.Findings[0].File != "f" ||
		!strings.Contains(fb.Findings[0].Message, "args: x=-1; returned: -1") {
		t.Fatalf("%+v", fb)
	}
	if !strings.Contains(s3.Reason, "lemmas: 1 declared, 1 pass") {
		t.Fatalf("%q", s3.Reason)
	}
	var c contract.Candidate
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lem", ArtCandidate), &c)
	if c.Iterations != 2 || len(c.Lemmas) != 1 || c.Lemmas[0].Name != "f" || c.Lemmas[0].Status != app.LemmaPass {
		t.Fatalf("%+v", c)
	}
	// tier0.json kept the feedback of the refuted round, then the clean one of the round that converged
	var tier0 contract.Tier0Response
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lem", ArtTier0), &tier0)
	if !tier0.OK {
		t.Fatalf("%+v", tier0)
	}
}

// pass, no_pair and inconclusive never block (§5: a lemma that passes does not raise the verdict): recorded and noted.
func TestStage3LemmaVerdictsWithoutCounterexampleAreRecordedOnly(t *testing.T) {
	a, dir, eng := repoBuilder(t)
	withRouterN2(t)
	eng.lemmas = func(contract.LemmasBudget, string) contract.LemmasResponse {
		return contract.LemmasResponse{SchemaVersion: 1, Lemmas: []contract.LemmaResult{
			{Name: "f", Status: app.LemmaPass, Message: "confirmed"},
			{Name: "g", Status: app.LemmaNoPair, Message: "the base has no lemma named g"},
			{Name: "h", Status: app.LemmaInconclusive, Message: "no top-level function named h in the candidate tree"},
		}}
	}
	var requests []contract.BuilderRequest
	lemmas := []contract.BuilderLemma{{Name: "f", Postcondition: "true"}, {Name: "g", Postcondition: "true"}, {Name: "h", Postcondition: "true"}}
	launcherFake := &fakeLauncher{script: fakeBuilderLemmas(t, &requests, lemmas, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x: int) -> int:\n    return x\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "lem", contract.ModeLite, "f" })
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "lem", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	s3 := st.Stages[5]
	if s3.Status != contract.StageDone || len(requests) != 1 || !strings.Contains(s3.Reason, "lemmas: 3 declared, 1 pass, 1 no_pair, 1 inconclusive (g: no_pair") {
		t.Fatalf("%+v", s3)
	}
	var c contract.Candidate
	readJSONTest(t, filepath.Join(dir, ".a2dlc/run/lem", ArtCandidate), &c)
	if len(c.Lemmas) != 3 || c.Lemmas[1].Status != app.LemmaNoPair || c.Lemmas[2].Status != app.LemmaInconclusive {
		t.Fatalf("%+v", c.Lemmas)
	}
}

// Lemmas required by the route and not declared are a note, not a round; a lemma declared twice is out of contract.
func TestStage3LemmasRequiredButNotDeclaredIsANote(t *testing.T) {
	a, _, eng := repoBuilder(t)
	withRouterN2(t)
	var requests []contract.BuilderRequest
	launcherFake := &fakeLauncher{script: fakeBuilder(t, &requests, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	path := intentFile(t, func(in *contract.Intent) { in.SliceID, in.Mode, in.Goal = "lem", contract.ModeLite, "f" })
	st, err := Run(context.Background(), a, launcherFake, Options{Slice: "lem", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.Status != contract.StageDone || !strings.Contains(s3.Reason, "lemmas required by the route (N=2) and the builder declared none") || eng.lemmaCalls != 0 {
		t.Fatalf("%+v %d", s3, eng.lemmaCalls)
	}
	// the same with a lemma declared twice
	a, _, _ = repoBuilder(t)
	withRouterN2(t)
	launcherFake = &fakeLauncher{script: fakeBuilderLemmas(t, &requests, []contract.BuilderLemma{{Name: "f", Postcondition: "true"}, {Name: "f", Postcondition: "result > 0"}}, func(contract.BuilderRequest) map[string]string {
		return map[string]string{"src/f.py": "def f(x):\n    return x * 2\n"}
	})}
	st, err = Run(context.Background(), a, launcherFake, Options{Slice: "lem", Intent: path})
	if err != nil {
		t.Fatal(err)
	}
	if s3 := st.Stages[5]; s3.Status != contract.StageFailed || !strings.Contains(s3.Reason, "lemma f is declared twice") {
		t.Fatalf("%+v", s3)
	}
}

// Refactor: a lemma that diverges from the base's ({args, old, new}) is a candidate counterexample, not builder feedback:
// recorded as fail, with the note that Stage 4 is what tests the main function.
func TestClassifyLemmasRefactorDivergenceIsRecordedNotFedBack(t *testing.T) {
	r := classifyLemmas([]contract.LemmaResult{
		{Name: "f", Status: app.LemmaFail, Message: "the candidate's lemma diverges from the base's lemma", Counterexample: map[string]any{"args": "x=2", "old": "4", "new": "5"}},
		{Name: "g", Status: app.LemmaFail, Message: "broken", Counterexample: map[string]any{"args": "x=0", "raised": "ZeroDivisionError"}},
	})
	if r.feedback == nil || len(r.feedback.Findings) != 1 || r.feedback.Findings[0].File != "g" || !strings.Contains(r.feedback.Findings[0].Message, "raised: ZeroDivisionError") {
		t.Fatalf("%+v", r.feedback)
	}
	if len(r.recorded) != 2 || r.recorded[0].Status != app.LemmaFail || len(r.notes) != 2 ||
		!strings.Contains(r.notes[0], "lemmas: 2 declared, 2 fail") || !strings.Contains(r.notes[1], "lemma f diverges from the base lemma (args: x=2; new: 5; old: 4)") {
		t.Fatalf("%+v %v", r.recorded, r.notes)
	}
	if r := classifyLemmas(nil); r.feedback != nil || len(r.notes) != 0 || len(r.recorded) != 0 {
		t.Fatalf("%+v", r)
	}
}
