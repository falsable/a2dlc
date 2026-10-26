package app

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/coverage"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Coverage precondition (VARA-2, rule 1): each changed line of a file in scope must be either inside an engine target
// with verdict PASS/FAIL, or executed by some test run overlaid on the candidate. What nobody examined cannot be PASS:
// before, "everything out of scope" was equivalent to PASS, and the SWE-ABS benchmark showed that this approved
// rejected patches without looking at them (RESULTADOS.md: 11 of 18).

// inScope are the changed sources that matched the paths of some engine or tests.source_paths.
func inScope(cfg contract.Config, sources []string) []string {
	var globs []string
	for _, e := range cfg.Engines {
		globs = append(globs, e.Paths...)
	}
	if cfg.Tests != nil {
		globs = append(globs, cfg.Tests.SourcePaths...)
	}
	si, _ := partition(globs, sources)
	return si
}

// precondition builds the verdict's coverage: per file in scope, which changed lines a target examined, which ones a
// test executed and which ones nobody. cov is the union of the coverage from the runs overlaid on the candidate (nil if
// none).
func (a App) precondition(cfg contract.Config, baseSha, tree string, sources []string, targets []contract.VerdictTarget, cov *coverage.Report, coverageNote string) (*contract.Coverage, error) {
	out := &contract.Coverage{Status: contract.CoverageRan, Files: []contract.CoverageFile{}}
	switch {
	case cfg.Tests == nil:
		out.Status = contract.CoverageNotConfigured
	case cov == nil:
		out.Status, out.Reason = contract.CoverageUnavailable, "no run on the candidate left $A2DLC_COVERAGE"
		if coverageNote != "" {
			out.Reason = coverageNote
		}
	}
	byFile := map[string][]contract.VerdictTarget{}
	for _, t := range targets {
		if t.Verdict == contract.PASS || t.Verdict == contract.FAIL {
			f := contract.TargetFile(t.Target)
			byFile[f] = append(byFile[f], t)
		}
	}
	for _, f := range inScope(cfg, sources) {
		lines, err := a.Repo.ChangedLines(baseSha, tree, f)
		if err != nil {
			return nil, fmt.Errorf("changed lines of %s: %w", f, err)
		}
		cf := contract.CoverageFile{File: f, ByEngine: []int{}, Covered: []int{}, Uncovered: []int{}}
		var hits map[int]int
		if cov != nil {
			hits = cov.Lookup(f)
		}
		for _, l := range lines {
			switch {
			case examined(byFile[f], l):
				cf.ByEngine = append(cf.ByEngine, l)
			case hits == nil:
				cf.Uncovered = append(cf.Uncovered, l)
			default:
				h, statement := hits[l]
				switch {
				case !statement: // blank, comment, or else: the tool does not count it
				case h > 0:
					cf.Covered = append(cf.Covered, l)
				default:
					cf.Uncovered = append(cf.Uncovered, l)
				}
			}
		}
		out.Files = append(out.Files, cf)
	}
	sort.Slice(out.Files, func(i, j int) bool { return out.Files[i].File < out.Files[j].File })
	return out, nil
}

func examined(ts []contract.VerdictTarget, l int) bool {
	for _, t := range ts {
		if t.Covers(l) {
			return true
		}
	}
	return false
}

// noteUncovered asks the engine that owns each file which of its unexamined changed lines are defensive exception
// handlers or declaration-only code (operation: classify_lines, docs/COVERAGE_NOTES.md) and moves those from Uncovered to
// Noted, so they stay in the receipt and do not block. It only ever lowers what blocks, and any failure leaves the lines
// unexamined: a runner without LineClassifier, a file with no engine, an engine without the operation, an engine that
// errors or answers outside the contract. export prepares baseDir and candDir.
func (a App) noteUncovered(ctx context.Context, cfg contract.Config, cov *contract.Coverage, export func() error, baseDir, candDir *string) {
	lc, ok := a.Runner.(engine.LineClassifier)
	if !ok || cov == nil {
		return
	}
	var files []string
	byFile := map[string]*contract.CoverageFile{}
	for i := range cov.Files {
		if f := &cov.Files[i]; len(f.Uncovered) > 0 {
			files = append(files, f.File)
			byFile[f.File] = f
		}
	}
	byEngine, _ := verdict.Assign(cfg.Engines, files)
	if len(byEngine) == 0 || export() != nil {
		return
	}
	for _, i := range sortedIndexes(byEngine) {
		e, own := cfg.Engines[i], byEngine[i]
		lines := map[string][]int{}
		for _, f := range own {
			lines[f] = byFile[f].Uncovered
		}
		budget, err := json.Marshal(contract.ClassifyBudget{Lines: lines})
		if err != nil {
			continue
		}
		req := contract.Request{SchemaVersion: contract.SchemaVersion, Language: e.Language, BaseDir: "/base", CandidateDir: "/cand",
			ChangedFiles: own, Budget: budget}
		t0 := time.Now()
		resp, err := lc.ClassifyLines(ctx, e, req, *baseDir, *candDir)
		timerOf(ctx).engineOp("classify_lines", time.Since(t0))
		if err != nil || engine.ValidateClassify(req, resp) != nil {
			continue
		}
		for _, cf := range resp.Files {
			f, noted := byFile[cf.File], map[int]bool{}
			notes := engine.SortedNotes(cf.Notes)
			for _, n := range notes {
				for _, l := range n.Lines {
					noted[l] = true
				}
			}
			if len(noted) == 0 {
				continue
			}
			rest := []int{}
			for _, l := range f.Uncovered {
				if !noted[l] {
					rest = append(rest, l)
				}
			}
			f.Uncovered, f.Noted = rest, notes
		}
	}
}

// NoTestsSectionNote is the line check prints, after its summary, when the lines no engine examined stay unexamined
// because the config has no tests section (a2dlc-6fj): the verdict says INCONCLUSIVE and, without it, nothing says why.
// It is "" when tests are configured, when nothing was left unexamined or when the receipt already gave another reason
// (a tests block the base lacks). It is a line of the CLI and not a field of the verdict: the receipt is unchanged.
func NoTestsSectionNote(v contract.Verdict) string {
	c := v.Coverage
	if c == nil || c.Status != contract.CoverageNotConfigured || c.Reason != "" || !hasUncovered(c) {
		return ""
	}
	return "no tests section configured: the changed lines no engine examined stay INCONCLUSIVE; `a2dlc install` prints a tests section for the repo's runner"
}

// hasUncovered says whether some changed line in scope was left unexamined.
func hasUncovered(c *contract.Coverage) bool {
	if c == nil {
		return false
	}
	for _, f := range c.Files {
		if len(f.Uncovered) > 0 {
			return true
		}
	}
	return false
}
