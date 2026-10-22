package app

import (
	"fmt"
	"sort"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/coverage"
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
