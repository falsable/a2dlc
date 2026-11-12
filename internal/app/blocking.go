package app

import (
	"fmt"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
)

// clipAt bounds each counterexample field on stdout; the whole value stays in .a2dlc/verdict.json.
const clipAt = 120

// Clip shortens s to clipAt runes, with an ellipsis when it cuts.
func Clip(s string) string {
	if r := []rune(s); len(r) > clipAt {
		return string(r[:clipAt]) + "…"
	}
	return s
}

// blockingLine is one cause that keeps a verdict from PASS. Person marks a cause only a person can resolve (the base
// config and the signatures are theirs), so the Stop hook leaves it out of what it hands the agent.
type blockingLine struct {
	Text   string
	Person bool
}

// noEngineReason says why a source file no engine covers was not judged and how to resolve it. The tooling escalation
// and the line check prints use the same sentence.
func noEngineReason(f string) string {
	return fmt.Sprintf("%s is %s source and no engine in %s covers it, so this change was not judged: add an engine for it, or list the path under config.out_of_scope_paths",
		f, sourceLanguage[filepath.Ext(f)], pathConfig)
}

// blockingCauses names what keeps v from PASS apart from tests: targets, unexamined changed lines, source files no engine
// judged and pending waivers, one line each. It is the one place that words them, for check and for the Stop hook.
func blockingCauses(v contract.Verdict, cfg contract.Config) []blockingLine {
	var out []blockingLine
	for _, t := range v.Targets {
		if t.Waived || t.Verdict == contract.PASS || t.Note != "" {
			continue
		}
		l := fmt.Sprintf("%s %s", t.Verdict, t.Target)
		if c := t.Counterexample; c != nil {
			l += fmt.Sprintf(": args=%s old=%s new=%s", Clip(c.Args), Clip(c.Old), Clip(c.New))
		}
		if t.Reason != "" {
			l += " (" + t.Reason + ")"
		}
		out = append(out, blockingLine{Text: l})
	}
	if v.Coverage != nil {
		for _, f := range v.Coverage.Files {
			if len(f.Uncovered) == 0 {
				continue
			}
			out = append(out, blockingLine{Text: fmt.Sprintf("INCONCLUSIVE %s: %d changed lines that no engine examined and no test executed (%s): write a test that runs them",
				f.File, len(f.Uncovered), ranges(f.Uncovered))})
		}
	}
	// out_of_scope no_engine is also written for docs and data; only noEngineSources tells the unjudged sources apart.
	var noEngine []string
	for _, o := range v.OutOfScope {
		if o.Reason == "no_engine" {
			noEngine = append(noEngine, o.File)
		}
	}
	unjudged, _ := noEngineSources(cfg, noEngine)
	for _, f := range unjudged {
		out = append(out, blockingLine{Text: "INCONCLUSIVE " + f + ": " + noEngineReason(f), Person: true})
	}
	for _, p := range v.PendingWaivers {
		out = append(out, blockingLine{Text: "PENDING " + p + ": the waiver awaits a person's signature (a2dlc approve --pending)", Person: true})
	}
	return out
}

// BlockingLines is what check prints for the causes of v that are not tests, one line each. The config is the base's, as in
// the check that wrote v.
// shortcut: with no readable base config (first adoption, a hand-built verdict) a file under tests.source_paths that no
// engine covers is named too; upgrade when check records the unjudged files in the verdict.
func (a App) BlockingLines(v contract.Verdict) []string {
	cfg, err := a.configAt(v.BaseRef)
	if err != nil {
		cfg = contract.Config{}
	}
	var out []string
	for _, l := range blockingCauses(v, cfg) {
		out = append(out, l.Text)
	}
	return out
}
