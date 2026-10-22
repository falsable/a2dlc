package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Equivalence is the N=2 differential of Stage 4 (the equivalence_s gate): the two candidates of a slice, run through the
// engines on the same generated inputs. Functions are matched by file, name and signature (signatures.json); a function
// present in one candidate only is not compared and stays INCONCLUSIVE. Divergences are the counterexamples the engines
// found, shrunk by them: each one is CB-01 (protocol §7), and a person decides which behavior is the spec.
type Equivalence struct {
	Gate         contract.Gate
	Divergences  []contract.TargetResult // FAIL: the two candidates return different outputs for Counterexample
	Inconclusive []contract.TargetResult // one-sided functions, functions no engine judged, engine errors
}

// Equivalence compares the candidates candA and candB against each other (base is the run's base, as in CheckWith).
// relaxations are the π_S relaxations of the contract, passed to the engines as in CheckWith. The gate is not_applicable
// when the two candidates share no public function in the changed files.
func (a App) Equivalence(ctx context.Context, base, candA, candB string, relaxations []string) (Equivalence, error) {
	eq := Equivalence{Gate: contract.Gate{Status: contract.GateNotApplicable}, Divergences: []contract.TargetResult{},
		Inconclusive: []contract.TargetResult{}}
	cfg, err := a.config()
	if err != nil {
		return eq, err
	}
	if base == "" {
		base = a.defaultBase()
	}
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return eq, fmt.Errorf("base %q: %w", base, err)
	}
	shaA, err := a.Repo.ResolveCommit(candA)
	if err != nil {
		return eq, fmt.Errorf("candidate %q: %w", candA, err)
	}
	shaB, err := a.Repo.ResolveCommit(candB)
	if err != nil {
		return eq, fmt.Errorf("candidate %q: %w", candB, err)
	}
	sigsA, errsA, err := a.SignaturesOf(ctx, baseSha, shaA)
	if err != nil {
		return eq, err
	}
	sigsB, errsB, err := a.SignaturesOf(ctx, baseSha, shaB)
	if err != nil {
		return eq, err
	}
	matched, oneSided := pairSignatures(sigsA, sigsB)
	if len(matched) == 0 && len(oneSided) == 0 {
		eq.Gate.Reason = "no public function in the changed files of either candidate"
		return eq, nil
	}
	for _, t := range oneSided {
		eq.Inconclusive = append(eq.Inconclusive, inconclusive(t, "present in one candidate only: not compared"))
	}
	if len(matched) == 0 {
		eq.Gate = contract.Gate{Status: contract.GateRan, Reason: fmt.Sprintf("no function in both candidates: %d not compared (one-sided)", len(oneSided))}
		return eq, nil
	}
	files, byFile := []string{}, map[string][]string{}
	for _, t := range matched {
		file := strings.SplitN(t, "::", 2)[0]
		if _, ok := byFile[file]; !ok {
			files = append(files, file)
		}
		byFile[file] = append(byFile[file], t)
	}
	sort.Strings(files)

	engineErrs := append(errorStrings(errsA, "candidate A"), errorStrings(errsB, "candidate B")...)
	byEngine, noEngine := verdict.Assign(cfg.Engines, files)
	noEng := map[string]bool{}
	for _, f := range noEngine {
		noEng[f] = true
		for _, t := range byFile[f] {
			eq.Inconclusive = append(eq.Inconclusive, inconclusive(t, "no engine in config.engines covers the file"))
		}
	}
	judged := map[string]contract.TargetResult{}
	identical := map[string]bool{} // engine-response.identical: unjudged because nothing about them differs
	switch {
	case len(byEngine) == 0:
	case a.Runner == nil:
		engineErrs = append(engineErrs, "this binary has no engine runner")
	default:
		baseDir, candDir, cleanup, err := a.exportPair(shaA, shaB)
		if err != nil {
			return eq, err
		}
		defer cleanup()
		for _, i := range sortedIndexes(byEngine) {
			e := cfg.Engines[i]
			budget := e.Budget
			if len(budget) == 0 {
				budget = json.RawMessage("{}")
			}
			req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: shaA,
				BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: byEngine[i], Budget: budget,
				GeneratorsOverride: generators(candDir), EquivalenceRelaxations: relaxations}
			resp, err := a.Runner.Run(ctx, e, req, baseDir, candDir)
			if err != nil {
				engineErrs = append(engineErrs, e.Language+": "+err.Error())
				continue
			}
			for _, t := range ApplyUnsupported(resp.Targets, resp.EquivalenceUnsupported) {
				judged[t.Target] = t
			}
			for _, t := range resp.Identical {
				identical[t] = true
			}
		}
	}

	compared, same := 0, 0
	for _, f := range files {
		for _, t := range byFile[f] {
			r, ok := judged[t]
			if !ok {
				// Only the engine may say a function needs no judging: inferring it from its silence would hide an engine
				// that skipped a function it should have compared.
				if identical[t] {
					same++
					continue
				}
				if !noEng[f] {
					eq.Inconclusive = append(eq.Inconclusive, inconclusive(t, "the engines did not judge this function"))
				}
				continue
			}
			switch r.Verdict {
			case contract.PASS:
				compared++
			case contract.FAIL:
				compared++
				eq.Divergences = append(eq.Divergences, r)
			default:
				eq.Inconclusive = append(eq.Inconclusive, inconclusive(t, r.Reason))
			}
		}
	}
	sort.Slice(eq.Divergences, func(i, j int) bool { return eq.Divergences[i].Target < eq.Divergences[j].Target })
	sort.Slice(eq.Inconclusive, func(i, j int) bool { return eq.Inconclusive[i].Target < eq.Inconclusive[j].Target })
	eq.Gate = contract.Gate{Status: contract.GateRan, Reason: fmt.Sprintf("%d functions compared: %d diverge; %d INCONCLUSIVE",
		compared, len(eq.Divergences), len(eq.Inconclusive))}
	if same > 0 {
		eq.Gate.Reason += fmt.Sprintf("; %d identical in both candidates (not judged: the engine found nothing that differs)", same)
	}
	if len(engineErrs) > 0 {
		eq.Gate.Status = contract.GateError
		eq.Gate.Reason = "engine errors: " + strings.Join(engineErrs, "; ")
	}
	return eq, nil
}

// pairSignatures matches the functions of both candidates by file, name and signature. It returns the matched targets
// ("file::name", sorted) and the one-sided ones from either side, sorted and without repeats.
func pairSignatures(a, b []contract.SignatureFile) (matched, oneSided []string) {
	type key struct{ file, name, sig string }
	keysA, keysB := map[key]bool{}, map[key]bool{}
	for _, f := range a {
		for _, s := range f.Symbols {
			keysA[key{f.File, s.Name, s.Signature}] = true
		}
	}
	for _, f := range b {
		for _, s := range f.Symbols {
			keysB[key{f.File, s.Name, s.Signature}] = true
		}
	}
	for _, f := range a {
		for _, s := range f.Symbols {
			t := f.File + "::" + s.Name
			if keysB[key{f.File, s.Name, s.Signature}] {
				matched = append(matched, t)
			} else {
				oneSided = append(oneSided, t)
			}
		}
	}
	for _, f := range b {
		for _, s := range f.Symbols {
			if !keysA[key{f.File, s.Name, s.Signature}] {
				oneSided = append(oneSided, f.File+"::"+s.Name)
			}
		}
	}
	sort.Strings(matched)
	sort.Strings(oneSided)
	return matched, dedupe(oneSided)
}

// ApplyUnsupported marks the targets that an engine judged with relaxations it does not apply (equivalence_unsupported,
// block 7). A PASS under the strict comparison says nothing about the relaxed one: it becomes INCONCLUSIVE with the
// relaxations in the reason. A FAIL stays FAIL, with the relaxations in its reason: a strict difference is a difference.
// The input is not modified.
func ApplyUnsupported(targets []contract.TargetResult, unsupported []string) []contract.TargetResult {
	if len(unsupported) == 0 {
		return targets
	}
	note := "relaxations not applied by the engine, compared strictly: " + strings.Join(unsupported, ", ")
	out := make([]contract.TargetResult, len(targets))
	for i, t := range targets {
		switch t.Verdict {
		case contract.PASS:
			t.Verdict, t.Reason = contract.INCONCLUSIVE, note
		case contract.FAIL:
			t.Reason = joinReason(t.Reason, note)
		}
		out[i] = t
	}
	return out
}

// EquivalenceEscalations are what the differential asks of a person: each divergence is a behavior_divergence (CB-01)
// with its shrunk counterexample; each INCONCLUSIVE function is a tooling escalation with its reason. Neither one takes
// a waiver: which behavior is the spec is a person's decision, and a function that was not compared is not a pass.
func EquivalenceEscalations(eq Equivalence) []contract.Escalation {
	out := []contract.Escalation{}
	for _, t := range eq.Divergences {
		out = append(out, escalation("behavior_divergence", t.Target,
			"The two candidates return different outputs for the same input (CB-01): the spec leaves room for both, and a person decides.",
			contract.Evidence{Counterexample: t.Counterexample}))
	}
	for _, t := range eq.Inconclusive {
		out = append(out, escalation("tooling", t.Target, "Not compared between the candidates: "+t.Reason+".", contract.Evidence{}))
	}
	return out
}

// FuzzEscalations turns the findings of the fuzzing round into escalations, one per function: the shortest input that
// raised the error (ties by input text), with the seed that reproduces it. targetOf maps a function name to its
// "file::name" target; a function it does not know keeps its bare name. Nothing here changes the coverage.
func FuzzEscalations(resp contract.FuzzResponse, targetOf map[string]string, seed, seconds int) []contract.Escalation {
	best := map[string]contract.FuzzFinding{}
	for _, f := range resp.Findings {
		b, ok := best[f.Function]
		if !ok || len(f.Input) < len(b.Input) || (len(f.Input) == len(b.Input) && f.Input < b.Input) {
			best[f.Function] = f
		}
	}
	names := make([]string, 0, len(best))
	for n := range best {
		names = append(names, n)
	}
	sort.Strings(names)
	out := []contract.Escalation{}
	for _, n := range names {
		f := best[n]
		target := targetOf[n]
		if target == "" {
			target = n
		}
		summary := fmt.Sprintf("Fuzzing (seed %d, %d s) found an input that raises an error no engine expected in %s.", seed, seconds, n)
		out = append(out, escalation("behavior_divergence", target, summary,
			contract.Evidence{Counterexample: &contract.Counterexample{Args: f.Input, Old: "", New: f.Error}}))
	}
	return out
}

// exportPair exports the two candidates to temporary folders: /base is A and /cand is B, as the differential reads them.
func (a App) exportPair(shaA, shaB string) (string, string, func(), error) {
	treeA, err := a.candidateTree(shaA)
	if err != nil {
		return "", "", nil, err
	}
	treeB, err := a.candidateTree(shaB)
	if err != nil {
		return "", "", nil, err
	}
	dirA, err := os.MkdirTemp("", "a2dlc-eq-a-*")
	if err != nil {
		return "", "", nil, err
	}
	dirB, err := os.MkdirTemp("", "a2dlc-eq-b-*")
	if err != nil {
		os.RemoveAll(dirA)
		return "", "", nil, err
	}
	cleanup := func() {
		os.RemoveAll(dirA)
		os.RemoveAll(dirB)
	}
	if err := a.Repo.Export(treeA, dirA); err != nil {
		cleanup()
		return "", "", nil, err
	}
	if err := a.Repo.Export(treeB, dirB); err != nil {
		cleanup()
		return "", "", nil, err
	}
	return dirA, dirB, cleanup, nil
}

func inconclusive(target, reason string) contract.TargetResult {
	return contract.TargetResult{Target: target, Verdict: contract.INCONCLUSIVE, Reason: reason, Equivalence: contract.EquivalenceStrict}
}

func errorStrings(errs []contract.EngineError, side string) []string {
	out := []string{}
	for _, e := range errs {
		out = append(out, side+" "+e.Language+": "+e.Error)
	}
	return out
}

func joinReason(a, b string) string {
	if a == "" {
		return b
	}
	return a + "; " + b
}

func dedupe(in []string) []string {
	out := []string{}
	for i, s := range in {
		if i == 0 || in[i-1] != s {
			out = append(out, s)
		}
	}
	return out
}
