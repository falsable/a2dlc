package app

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

const pathEscalations = ".a2dlc/escalations.json"

// escalation builds one escalation. Its id is kind plus target: the same thing about the same target is one escalation.
// Its cluster is kind plus the normalized message (contract.ClusterID), not the target: one root cause that shows up on
// several targets groups together (decision 3). The message is the failure's, or the summary when the failure has none.
func escalation(kind, target, summary string, ev contract.Evidence, resolve ...string) contract.Escalation {
	h := sha256.Sum256([]byte(kind + "\x00" + target))
	if resolve == nil {
		resolve = []string{}
	}
	cause := ev.Message
	if cause == "" {
		cause = summary
	}
	return contract.Escalation{ID: "esc-" + hex.EncodeToString(h[:])[:12], Kind: kind, Target: target, Summary: summary,
		Evidence: ev, ResolveWith: resolve, ClusterID: contract.ClusterID(kind, cause)}
}

// Escalations exposes escalations to a2dlc run, which stores them as an artifact of the run and passes samples of them
// to the Saboteur.
func Escalations(v contract.Verdict) []contract.Escalation { return escalations(v) }

// ToolingEscalation is an escalation for a tool that did not run on target (INCONCLUSIVE, not a behavior): nothing about
// it is decided by a waiver.
func ToolingEscalation(target, summary string) contract.Escalation {
	return escalation("tooling", target, summary, contract.Evidence{})
}

// DedupeByID keeps one escalation per id, as the doc of escalation says: the first one, except that when two items with
// a counterexample collide, the one whose input is shorter (ties by input text) replaces it, so the shortest input of a
// target is the one a person sees. A replacement without a builder keeps the builder of the item it replaces.
func DedupeByID(items []contract.Escalation) []contract.Escalation {
	out := make([]contract.Escalation, 0, len(items))
	at := map[string]int{}
	for _, e := range items {
		i, seen := at[e.ID]
		if !seen {
			at[e.ID] = len(out)
			out = append(out, e)
			continue
		}
		if shorterCounterexample(e, out[i]) {
			if e.Builder == "" {
				e.Builder = out[i].Builder
			}
			out[i] = e
		}
	}
	return out
}

// shorterCounterexample says whether a's counterexample has a shorter input than b's (ties by input text); false if either
// has none.
func shorterCounterexample(a, b contract.Escalation) bool {
	ca, cb := a.Evidence.Counterexample, b.Evidence.Counterexample
	if ca == nil || cb == nil {
		return false
	}
	return len(ca.Args) < len(cb.Args) || (len(ca.Args) == len(cb.Args) && ca.Args < cb.Args)
}

// escalations is what a person has to decide, or what the tool could not decide, in the shape of
// escalation.schema.json v1. A red that the agent has to fix (its own test, a regression) is not escalated.
func escalations(v contract.Verdict) []contract.Escalation {
	out := []contract.Escalation{}
	pending := map[string]bool{}
	for _, t := range v.PendingWaivers {
		pending[t] = true
	}
	for _, t := range v.Targets {
		switch {
		case t.Waived && pending[t.Target]:
			out = append(out, escalation("signature_pending", t.Target, "The waiver awaits a signature: a2dlc approve "+t.Target, contract.Evidence{Counterexample: t.Counterexample}))
		case t.Note != "":
			// A note is in the receipt with its counterexample and asks nothing of a person.
		case t.Verdict == contract.FAIL && !t.Waived:
			out = append(out, escalation("behavior_divergence", t.Target, "The behavior changed relative to the base.", contract.Evidence{Counterexample: t.Counterexample}, contract.EnforceNewBehavior))
		case t.Verdict == contract.INCONCLUSIVE:
			out = append(out, escalation("tooling", t.Target, t.Reason, contract.Evidence{}))
		}
	}
	for _, e := range v.EngineErrors {
		out = append(out, escalation("tooling", e.Files[0]+"::*", "The engine "+e.Language+" did not respond: "+e.Error, contract.Evidence{}))
	}
	for _, t := range v.Tests {
		ev := contract.Evidence{Base: t.Base, Candidate: t.Candidate, Message: t.Message}
		if t.Reruns != nil {
			ev.RerunsBase = t.Reruns.Base
		}
		if t.Gate == contract.GateTestsDiff {
			// In tests_diff the evidence is the status per tree; base and candidate are already there.
			ev = contract.Evidence{Trees: t.Trees, Message: t.Message}
		}
		switch {
		case t.Waived && pending[t.Target]:
			summary := "The waiver awaits a signature: a2dlc approve " + t.Target
			if t.Gate == contract.GateBaseline && !t.Flaky {
				summary += ". Note: the core saw no intermittency on the base; it is a stable regression, not a flaky test."
			}
			out = append(out, escalation("signature_pending", t.Target, summary, ev))
		case t.Waived:
		case t.Flaky && t.Gate == contract.GateTestsDiff:
			out = append(out, escalation("flaky_test", t.Target, "Intermittent on the candidate: passes in some re-run; it cannot be decided.", ev))
		case t.Flaky:
			out = append(out, escalation("flaky_test", t.Target, "Intermittent on the base: quarantine is proposed.", ev, contract.QuarantineFlaky))
		case t.Verdict == contract.FAIL && t.Gate == contract.GateTestsDiff:
			out = append(out, escalation("behavior_divergence", t.Target, "The candidate fails a test that another tree passes: "+t.Reason+".", ev, contract.EnforceNewBehavior))
		case t.Verdict == contract.INCONCLUSIVE && t.Gate == contract.GateTestsDiff && strings.HasPrefix(t.Reason, redInAll):
			out = append(out, escalation("test_without_teeth", t.Target, "The test fails in all trees: it discriminates nothing; broken test or spec.", ev))
		case t.Gate == contract.GateTestsDiff && strings.HasPrefix(t.Reason, onlyInCandidate),
			t.Verdict == contract.FAIL && t.Gate == contract.GateNOP && t.Origin == "overlay" && t.Base != contract.TestPassed:
			// A Saboteur test that the candidate fails and no other tree runs: the test reads S* one way and the candidate
			// another (CB-01). It is not teeth-less (nobody passed it) nor the agent's (it did not write it). The two
			// entries (tests_diff and NOP) give the same id and leave one.
			out = append(out, escalation("behavior_divergence", t.Target, "The candidate fails a test that no other tree runs (new symbol): the spec the test reads and the candidate's differ.", ev, contract.EnforceNewBehavior))
		case t.Verdict == contract.FAIL && t.Gate == contract.GateNOP && t.Base == contract.TestPassed:
			out = append(out, escalation("test_without_teeth", t.Target, t.Reason, ev, contract.CharacterizationTest))
		case t.Verdict == contract.INCONCLUSIVE:
			out = append(out, escalation("tooling", t.Target, t.Reason, ev))
		}
	}
	if g := v.TestGates; g != nil {
		gates := []struct {
			name string
			g    contract.Gate
		}{{contract.GateNOP, g.NOPOracle}, {contract.GateBaseline, g.Baseline}}
		if g.TestsDiff != nil {
			gates = append(gates, struct {
				name string
				g    contract.Gate
			}{contract.GateTestsDiff, *g.TestsDiff})
		}
		for _, x := range gates {
			if x.g.Status == contract.GateError {
				out = append(out, escalation("tooling", pathConfig+"::tests."+x.name, x.g.Reason, contract.Evidence{}))
			}
		}
		if g.EquivalenceS != nil && g.EquivalenceS.Status == contract.GateError {
			out = append(out, escalation("tooling", "equivalence_s", g.EquivalenceS.Reason, contract.Evidence{}))
		}
	}
	out = append(out, checkEscalations(v, pending)...)
	if c := v.Coverage; c != nil {
		for _, f := range c.Files {
			if len(f.Uncovered) == 0 {
				continue
			}
			e := escalation("uncovered_change", f.File+"::*",
				fmt.Sprintf("%d changed lines that no engine examined and no test executed (%s).", len(f.Uncovered), ranges(f.Uncovered)),
				contract.Evidence{Lines: f.Uncovered})
			// The Saboteur brings the test (§3): it writes against the signatures and the contract without seeing the code.
			// a2dlc run closes the loop with --tests-overlay.
			e.Needs = &contract.Needs{Role: contract.RoleSaboteur, Artifact: "test_files"}
			out = append(out, e)
		}
	}
	// The id is kind + target: two gates that escalate the same thing about the same test (NOP and tests_diff overlaid on
	// an overlay test) leave a single escalation, the first.
	return DedupeByID(out)
}
