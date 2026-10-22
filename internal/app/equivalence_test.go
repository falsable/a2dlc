package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
)

// eqEngine is a fake differential over lines of the form "fn NAME SIGNATURE => OUTPUT": the public functions of a file are
// those lines, and two candidates differ on a name when its OUTPUT differs. An OUTPUT of UNDECIDED gives INCONCLUSIVE.
// unsupported is returned as equivalence_unsupported; fail makes Run error. The functions named in identical are returned
// as identical instead of judged, and the ones in skip are left out of the response.
type eqEngine struct {
	engine.Runner
	unsupported []string
	identical   map[string]bool
	skip        map[string]bool
	fail        bool
	runs        int
	lastReq     contract.Request
}

type fnDef struct {
	name, sig, out string
	line           int
}

// fnDefs reads the functions of a file; a missing file has none.
func fnDefs(path string) []fnDef {
	b, _ := os.ReadFile(path)
	var out []fnDef
	for i, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "fn ") {
			continue
		}
		head, res, _ := strings.Cut(line, " => ")
		fields := strings.Fields(head)
		out = append(out, fnDef{name: fields[1], sig: strings.Join(fields[2:], " "), out: res, line: i + 1})
	}
	return out
}

func (e *eqEngine) Signatures(_ context.Context, _ contract.EngineConfig, req contract.Request, _, candDir string) (contract.SignaturesResponse, error) {
	r := contract.SignaturesResponse{SchemaVersion: 1, Files: []contract.SignatureFile{}}
	for _, f := range req.ChangedFiles {
		sf := contract.SignatureFile{File: f, Symbols: []contract.Signature{}}
		for _, d := range fnDefs(filepath.Join(candDir, f)) {
			sf.Symbols = append(sf.Symbols, contract.Signature{Name: d.name, Signature: d.sig, Lines: []int{d.line, d.line}})
		}
		r.Files = append(r.Files, sf)
	}
	return r, nil
}

func (e *eqEngine) Run(_ context.Context, _ contract.EngineConfig, req contract.Request, baseDir, candDir string) (contract.Response, error) {
	e.runs++
	e.lastReq = req
	if e.fail {
		return contract.Response{}, errors.New("container down")
	}
	r := contract.Response{SchemaVersion: 1, Targets: []contract.TargetResult{}, OutOfScope: []contract.OutOfScope{},
		EquivalenceUnsupported: e.unsupported}
	for _, f := range req.ChangedFiles {
		a := map[string]string{}
		for _, d := range fnDefs(filepath.Join(baseDir, f)) {
			a[d.name] = d.out
		}
		b := fnDefs(filepath.Join(candDir, f))
		sort.Slice(b, func(i, j int) bool { return b[i].name < b[j].name })
		for _, d := range b {
			if e.identical[d.name] {
				r.Identical = append(r.Identical, f+"::"+d.name)
			}
			if e.identical[d.name] || e.skip[d.name] {
				continue
			}
			ta := contract.TargetResult{Target: f + "::" + d.name, Engine: "crosshair-diffbehavior", EngineVersion: "fake",
				Verdict: contract.PASS, SearchBudget: json.RawMessage(`{"iterations":1}`), Generators: "typed_default", Equivalence: "strict"}
			switch {
			case d.out == "UNDECIDED":
				ta.Verdict, ta.Reason = contract.INCONCLUSIVE, "budget exhausted"
			case a[d.name] != d.out:
				ta.Verdict = contract.FAIL
				ta.Counterexample = &contract.Counterexample{Args: "x=1", Old: a[d.name], New: d.out}
			}
			r.Targets = append(r.Targets, ta)
		}
	}
	return r, nil
}

// twoCandidates makes a repo whose base has src/lib/f.ts with one line, and two candidates that each rewrite it.
func twoCandidates(t *testing.T, fileA, fileB string) (App, *eqEngine, string, string, string) {
	t.Helper()
	a, _, dir := newRepo(t)
	base := git(t, dir, "rev-parse", "HEAD")
	write(t, dir, "src/lib/f.ts", fileA)
	git(t, dir, "commit", "-qam", "candidate a")
	shaA := git(t, dir, "rev-parse", "HEAD")
	git(t, dir, "checkout", "-q", base)
	write(t, dir, "src/lib/f.ts", fileB)
	git(t, dir, "commit", "-qam", "candidate b")
	shaB := git(t, dir, "rev-parse", "HEAD")
	m := &eqEngine{}
	a.Runner = m
	return a, m, base, shaA, shaB
}

func TestEquivalenceDivergenceIsCB01WithCounterexample(t *testing.T) {
	a, _, base, shaA, shaB := twoCandidates(t, "fn f (x) => 2x\n", "fn f (x) => 3x\n")
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Divergences) != 1 || eq.Divergences[0].Target != "src/lib/f.ts::f" || eq.Divergences[0].Counterexample == nil ||
		eq.Divergences[0].Counterexample.Old != "2x" || eq.Divergences[0].Counterexample.New != "3x" {
		t.Fatalf("%+v", eq)
	}
	if eq.Gate.Status != contract.GateRan || !strings.Contains(eq.Gate.Reason, "1 diverge") {
		t.Fatalf("gate %+v", eq.Gate)
	}
	esc := EquivalenceEscalations(eq)
	if len(esc) != 1 || esc[0].Kind != "behavior_divergence" || esc[0].Target != "src/lib/f.ts::f" || esc[0].Evidence.Counterexample == nil ||
		esc[0].ClusterID == "" {
		t.Fatalf("%+v", esc)
	}
}

func TestEquivalenceEqualCandidatesPass(t *testing.T) {
	a, _, base, shaA, shaB := twoCandidates(t, "fn f (x) => 2x\n", "fn f (x) => 2x\n")
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Divergences) != 0 || len(eq.Inconclusive) != 0 || eq.Gate.Status != contract.GateRan || !strings.Contains(eq.Gate.Reason, "1 functions compared") {
		t.Fatalf("%+v", eq)
	}
}

// With N=2 on calc.py, the functions neither candidate touched were INCONCLUSIVE in every two-candidate run: the engine
// only judges what differs. Declared identical, they no longer count against the gate; left out silently, they still do.
func TestEquivalenceIdenticalFunctionsAreNotUnjudged(t *testing.T) {
	file := "fn f (x) => 2x\nfn g (x) => 1\nfn h (x) => 0\n"
	a, m, base, shaA, shaB := twoCandidates(t, file, file)
	m.identical, m.skip = map[string]bool{"g": true}, map[string]bool{"h": true}
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Inconclusive) != 1 || eq.Inconclusive[0].Target != "src/lib/f.ts::h" ||
		!strings.Contains(eq.Inconclusive[0].Reason, "the engines did not judge this function") {
		t.Fatalf("only the silently skipped h is INCONCLUSIVE: %+v", eq.Inconclusive)
	}
	if !strings.Contains(eq.Gate.Reason, "1 functions compared") || !strings.Contains(eq.Gate.Reason, "1 identical in both candidates") {
		t.Fatalf("gate %+v", eq.Gate)
	}
}

func TestEquivalenceOneSidedFunctionIsInconclusive(t *testing.T) {
	a, _, base, shaA, shaB := twoCandidates(t, "fn f (x) => 2x\nfn g (x) => 1\n", "fn f (x) => 2x\n")
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(eq.Divergences) != 0 || len(eq.Inconclusive) != 1 || eq.Inconclusive[0].Target != "src/lib/f.ts::g" ||
		eq.Inconclusive[0].Verdict != contract.INCONCLUSIVE || !strings.Contains(eq.Inconclusive[0].Reason, "one candidate only") {
		t.Fatalf("%+v", eq)
	}
	esc := EquivalenceEscalations(eq)
	if len(esc) != 1 || esc[0].Kind != "tooling" {
		t.Fatalf("%+v", esc)
	}
}

func TestEquivalenceSignatureChangeIsOneSidedOnBothSides(t *testing.T) {
	a, _, base, shaA, shaB := twoCandidates(t, "fn f (x) => 2x\n", "fn f (x, y) => 2x\n")
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	// both sides name the target src/lib/f.ts::f: one INCONCLUSIVE for it, not one per side
	if len(eq.Divergences) != 0 || len(eq.Inconclusive) != 1 || !strings.Contains(eq.Gate.Reason, "no function in both") {
		t.Fatalf("%+v", eq)
	}
}

func TestEquivalenceWithoutPublicFunctionsIsNotApplicable(t *testing.T) {
	a, m, base, shaA, shaB := twoCandidates(t, "export const f = 2\n", "export const f = 3\n")
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eq.Gate.Status != contract.GateNotApplicable || m.runs != 0 || len(eq.Divergences) != 0 {
		t.Fatalf("%+v runs=%d", eq, m.runs)
	}
}

func TestEquivalenceUnsupportedRelaxationMakesPassInconclusive(t *testing.T) {
	a, m, base, shaA, shaB := twoCandidates(t, "fn f (x) => 2x\n", "fn f (x) => 2x\n")
	m.unsupported = []string{"ignore_order"}
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, []string{"ignore_order"})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.lastReq.EquivalenceRelaxations) != 1 || len(eq.Inconclusive) != 1 || !strings.Contains(eq.Inconclusive[0].Reason, "ignore_order") {
		t.Fatalf("%+v request=%+v", eq, m.lastReq)
	}
}

func TestEquivalenceEngineErrorIsGateError(t *testing.T) {
	a, m, base, shaA, shaB := twoCandidates(t, "fn f (x) => 2x\n", "fn f (x) => 3x\n")
	m.fail = true
	eq, err := a.Equivalence(context.Background(), base, shaA, shaB, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eq.Gate.Status != contract.GateError || !strings.Contains(eq.Gate.Reason, "container down") || len(eq.Inconclusive) != 1 {
		t.Fatalf("%+v", eq)
	}
}

func TestApplyUnsupportedKeepsFailAndDoesNotTouchTheInput(t *testing.T) {
	in := []contract.TargetResult{{Target: "a::p", Verdict: contract.PASS}, {Target: "a::q", Verdict: contract.FAIL, Reason: "r"}}
	out := ApplyUnsupported(in, []string{"float_tolerance:1e-9"})
	if out[0].Verdict != contract.INCONCLUSIVE || !strings.Contains(out[0].Reason, "float_tolerance") {
		t.Fatalf("PASS not downgraded: %+v", out[0])
	}
	if out[1].Verdict != contract.FAIL || !strings.HasPrefix(out[1].Reason, "r;") {
		t.Fatalf("FAIL changed: %+v", out[1])
	}
	if in[0].Verdict != contract.PASS || in[1].Reason != "r" {
		t.Fatal("the input was modified")
	}
	if got := ApplyUnsupported(in, nil); got[0].Verdict != contract.PASS {
		t.Fatal("without unsupported relaxations nothing changes")
	}
}

func TestPairSignaturesMatchesByFileNameAndSignature(t *testing.T) {
	sig := func(name, s string) contract.Signature {
		return contract.Signature{Name: name, Signature: s, Lines: []int{1, 1}}
	}
	a := []contract.SignatureFile{{File: "x.py", Symbols: []contract.Signature{sig("f", "(a)"), sig("g", "()")}}}
	b := []contract.SignatureFile{{File: "x.py", Symbols: []contract.Signature{sig("f", "(a)"), sig("f", "(a, b)")}}, {File: "y.py", Symbols: []contract.Signature{sig("f", "(a)")}}}
	matched, one := pairSignatures(a, b)
	if len(matched) != 1 || matched[0] != "x.py::f" {
		t.Fatalf("matched %v", matched)
	}
	want := []string{"x.py::f", "x.py::g", "y.py::f"}
	if strings.Join(one, ",") != strings.Join(want, ",") {
		t.Fatalf("one-sided %v, want %v", one, want)
	}
}

func TestFuzzEscalationsKeepTheShortestInputPerFunction(t *testing.T) {
	resp := contract.FuzzResponse{Findings: []contract.FuzzFinding{
		{Function: "f", Input: "x=1000", Error: "ZeroDivisionError", Seed: 42},
		{Function: "f", Input: "x=0", Error: "ZeroDivisionError", Seed: 42},
		{Function: "g", Input: "y=2", Error: "KeyError", Seed: 42},
	}}
	esc := FuzzEscalations(resp, map[string]string{"f": "src/a.py::f"}, 42, 10)
	if len(esc) != 2 {
		t.Fatalf("%+v", esc)
	}
	if esc[0].Target != "src/a.py::f" || esc[0].Evidence.Counterexample.Args != "x=0" || esc[0].Evidence.Counterexample.New != "ZeroDivisionError" {
		t.Fatalf("%+v", esc[0])
	}
	if esc[1].Target != "g" {
		t.Fatalf("a function without a known target keeps its name: %+v", esc[1])
	}
	if FuzzEscalations(contract.FuzzResponse{}, nil, 42, 10) == nil || len(FuzzEscalations(contract.FuzzResponse{}, nil, 42, 10)) != 0 {
		t.Fatal("no findings, no escalations")
	}
}
