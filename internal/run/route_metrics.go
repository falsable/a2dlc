package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// Router metrics over S* (protocol §5 Stage 2: state mutation depth and estimated complexity of the rules). They are
// deterministic and computed from the contract.json that Stage 1 wrote; without S* they are null.
//
// The thresholds are code constants, not config: changing them changes which slices go to N=2, so the change stays in
// the git history. Calibration to date (2026-10-18): four real S* of a slice of a pure function (subtraction) gave
// rule_count 6 and state_mutation_depth 2 (one function with invariants, plus 1 for intent.delivery.writes); with the
// depth threshold at 2 that pure function went to N=2 and paid for a second builder that contributed nothing
// (calc.resta.b10). That is why depth triggers from 3: a single function with invariants and a declared write is not
// shared state. The rule threshold (12) has not triggered in any real run; it remains the starting point.
const (
	ruleThreshold     = 12 // rule_count >= 12 → N=2 (dual builders) even if criticality alone gives N=1
	mutationThreshold = 3  // state_mutation_depth >= 3 → N=2 (dual builders) even if criticality alone gives N=1
)

// routeWithMetrics adds the S* metrics to the route decision and, if one crosses a threshold, raises the track to N=2.
// It does not touch dual_spec_required: the threshold asks for two builders, not for Stage 1's dual-spec.
func (m *Machine) routeWithMetrics(r contract.Route) (contract.Route, error) {
	if m.state.Mode == contract.ModeLite {
		reason := "lite mode: no S*, metrics null"
		r.MetricsReason = &reason
		return r, nil
	}
	s, err := readSOfRun(m.dir)
	if err != nil {
		return r, err
	}
	if s == nil {
		reason := "no S*: " + ArtContract + " does not exist yet, metrics null"
		r.MetricsReason = &reason
		return r, nil
	}
	writes := m.intent != nil && m.intent.Delivery != nil && m.intent.Delivery.Writes != nil && *m.intent.Delivery.Writes
	rules, depth := metricsOfS(*s, writes)
	r.RuleCount, r.StateMutationDepth = &rules, &depth
	reason := fmt.Sprintf("S* with %d functions: rule_count=%d, state_mutation_depth=%d (functions with an invariant or a post that names another function, plus 1 if intent.delivery.writes)",
		len(s.Functions), rules, depth)
	r.MetricsReason = &reason

	var triggers []string
	if rules >= ruleThreshold {
		triggers = append(triggers, fmt.Sprintf("rule_count %d >= %d", rules, ruleThreshold))
	}
	if depth >= mutationThreshold {
		triggers = append(triggers, fmt.Sprintf("state_mutation_depth %d >= %d", depth, mutationThreshold))
	}
	if len(triggers) == 0 {
		return r, nil
	}
	list := strings.Join(triggers, " and ")
	if r.Track == contract.TrackN2 {
		r.Reasons = append(r.Reasons, "S* metrics: "+list+": N=2 (already was by criticality)")
	} else {
		r.Track = contract.TrackN2
		r.Reasons = append(r.Reasons, "S* metrics: "+list+": N=2 (dual builders)")
	}
	r.LemmasRequired = r.Track == contract.TrackN2
	return r, nil
}

// metricsOfS returns rule_count (pre, post, invariants and errors clauses of all functions) and state_mutation_depth:
// the functions with at least one invariant, or whose post names another function of S* as a whole word (its
// postcondition depends on another, that is, it shares state), plus one if the intent declares writes.
func metricsOfS(s contract.SliceContract, writes bool) (int, int) {
	rules := 0
	for _, f := range s.Functions {
		rules += len(f.Pre) + len(f.Post) + len(f.Invariants) + len(f.Errors)
	}
	depth := 0
	for i, f := range s.Functions {
		if len(f.Invariants) > 0 || postNamesOther(f, s.Functions, i) {
			depth++
		}
	}
	if writes {
		depth++
	}
	return rules, depth
}

// postNamesOther says whether any post clause of fs[i] names, as a whole word, another function of fs.
func postNamesOther(f contract.SpecFunction, fs []contract.SpecFunction, i int) bool {
	for j, onDisk := range fs {
		if j == i || onDisk.Name == "" {
			continue
		}
		for _, p := range f.Post {
			if namesWord(p, onDisk.Name) {
				return true
			}
		}
	}
	return false
}

// namesWord says whether name appears in text as a whole word: the byte before and the byte after are not a letter,
// digit or underscore. Non-ASCII bytes count as letters, so as not to split a word with accents. A name with dots
// (Class.method) is searched the same way.
func namesWord(text, name string) bool {
	for from := 0; from <= len(text); {
		k := strings.Index(text[from:], name)
		if k < 0 {
			return false
		}
		start := from + k
		finished := start + len(name)
		if (start == 0 || !isIdentifier(text[start-1])) && (finished == len(text) || !isIdentifier(text[finished])) {
			return true
		}
		from = start + 1
	}
	return false
}

func isIdentifier(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= 0x80
}

// readSOfRun reads contract.json from the run folder; nil if Stage 1 has not written it yet.
func readSOfRun(dir string) (*contract.SliceContract, error) {
	b, err := os.ReadFile(filepath.Join(dir, ArtContract))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s contract.SliceContract
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, fmt.Errorf("%s does not decode: %w", ArtContract, err)
	}
	return &s, nil
}
