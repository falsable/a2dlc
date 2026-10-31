package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Divergence is one function on which the engines' differential found an input where the base and the candidate differ.
type Divergence struct {
	Target   string // file::function, as in the verdict
	Args     string
	Old, New string
}

type divergenceProbeKey struct{}

// WithDivergenceProbe marks the engine calls that Divergences makes, so that a caller wrapping the runner can tell them
// from the judgment of Stage 4.
func WithDivergenceProbe(ctx context.Context) context.Context {
	return context.WithValue(ctx, divergenceProbeKey{}, true)
}

// DivergenceProbe says whether ctx belongs to a call made by Divergences.
func DivergenceProbe(ctx context.Context) bool {
	v, _ := ctx.Value(divergenceProbeKey{}).(bool)
	return v
}

// Divergences runs the configured engines' differential (operation diff, the one Check runs) on the files that changed from
// base to candidate and returns the targets that FAIL with a counterexample carrying old and new. It is information for the
// builder, never a verdict: an engine that cannot run, or a repo without engines for the changed files, gives a note and no
// divergence, and nothing here raises or lowers what Check says afterwards, except that with config.run_differential "stage3"
// a2dlc run hands the answers it returns to Check (RFC-0005).
func (a App) Divergences(ctx context.Context, base, candidate string) ([]Divergence, []string, DiffAnswers, error) {
	baseSha, err := a.Repo.ResolveCommit(base)
	if err != nil {
		return nil, nil, DiffAnswers{}, fmt.Errorf("base %q: %w", base, err)
	}
	cfg, err := a.configAt(baseSha)
	if err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	tree, err := a.candidateTree(candidate)
	if err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	changed, _, err := a.Repo.Changed(baseSha, tree)
	if err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	sources := changed
	if cfg.Tests != nil {
		_, sources = partition(cfg.Tests.Paths, changed)
	}
	byEngine, noEngine := verdict.Assign(cfg.Engines, sources)
	if len(byEngine) == 0 {
		return nil, nil, DiffAnswers{}, nil
	}
	if covered := len(sources) - len(noEngine); cfg.MaxFiles > 0 && covered > cfg.MaxFiles {
		return nil, []string{fmt.Sprintf("the differential was not run for the builder: %d covered files exceed max_files=%d", covered, cfg.MaxFiles)}, DiffAnswers{}, nil
	}
	if a.Runner == nil {
		return nil, []string{"this binary has no engines: the differential was not run for the builder"}, DiffAnswers{}, nil
	}
	baseDir, err := os.MkdirTemp("", "a2dlc-base-*")
	if err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	defer os.RemoveAll(baseDir)
	candDir, err := os.MkdirTemp("", "a2dlc-cand-*")
	if err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	defer os.RemoveAll(candDir)
	if err := a.Repo.Export(baseSha, baseDir); err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	if err := a.Repo.Export(tree, candDir); err != nil {
		return nil, nil, DiffAnswers{}, err
	}
	var out []Divergence
	var notes []string
	answers := DiffAnswers{tree: tree}
	ctx = WithDivergenceProbe(ctx)
	for _, i := range sortedIndexes(byEngine) {
		e, files := cfg.Engines[i], byEngine[i]
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseRef: baseSha,
			BaseDir: "/base", CandidateDir: "/cand", ChangedFiles: files, Budget: e.Budget, GeneratorsOverride: generators(candDir)}
		if len(req.Budget) == 0 {
			req.Budget = json.RawMessage("{}")
		}
		resp, err := a.Runner.Run(ctx, e, req, baseDir, candDir)
		if err != nil {
			notes = append(notes, fmt.Sprintf("the %s engine could not run the differential for the builder: %s", e.Language, strings.TrimSpace(err.Error())))
			continue
		}
		answers.add(e, req, resp)
		for _, t := range resp.Targets {
			if t.Verdict == contract.FAIL && t.Counterexample != nil {
				out = append(out, Divergence{Target: t.Target, Args: t.Counterexample.Args, Old: t.Counterexample.Old, New: t.Counterexample.New})
			}
		}
	}
	return out, notes, answers, nil
}

// DiffAnswers are the engines' diff answers that Divergences got on one tree. With config.run_differential "stage3"
// (RFC-0005), a2dlc run hands them to Stage 4, and Check takes an answer instead of running the engine again only when its
// own request for that engine on the same tree is the same bytes: same engine entry, base, files, budget and generators, and
// no equivalence relaxation that Stage 3 did not send. An engine that failed in Stage 3 left no answer and runs again.
type DiffAnswers struct {
	tree    string
	answers []diffAnswer
}

type diffAnswer struct {
	engine, request []byte // marshaled: the comparison is on what the engine was given
	response        contract.Response
}

func (d *DiffAnswers) add(e contract.EngineConfig, req contract.Request, resp contract.Response) {
	eb, rb, err := diffKey(e, req)
	if err == nil {
		d.answers = append(d.answers, diffAnswer{engine: eb, request: rb, response: resp})
	}
}

// answer is the response Stage 3 got for req to engine e on tree, if it has one. The target and out-of-scope lists are
// copies: Check marks targets in place (cut, unsupported relaxations), and a Saboteur round may judge the same tree again.
func (d DiffAnswers) answer(tree string, e contract.EngineConfig, req contract.Request) (contract.Response, bool) {
	if d.tree == "" || d.tree != tree {
		return contract.Response{}, false
	}
	eb, rb, err := diffKey(e, req)
	if err != nil {
		return contract.Response{}, false
	}
	for _, x := range d.answers {
		if bytes.Equal(x.engine, eb) && bytes.Equal(x.request, rb) {
			r := x.response
			r.Targets = append([]contract.TargetResult{}, r.Targets...)
			r.OutOfScope = append([]contract.OutOfScope{}, r.OutOfScope...)
			return r, true
		}
	}
	return contract.Response{}, false
}

func diffKey(e contract.EngineConfig, req contract.Request) ([]byte, []byte, error) {
	eb, err := json.Marshal(e)
	if err != nil {
		return nil, nil, err
	}
	rb, err := json.Marshal(req)
	return eb, rb, err
}
