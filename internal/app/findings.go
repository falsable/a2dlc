package app

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// The findings record (a2dlc-277): an append-only JSON-lines file with one event per transition of each finding a check
// raised, so the share of false findings per check and engine can be counted. It lives in .a2dlc/reports because that
// folder survives from one check to the next (verdict.json is overwritten each time), is git-ignored by install and is
// already in gitx.Excluded: the record is out of the tree_hash, so the §6.4 definition of the tree does not change. It
// is written after the receipt and the escalations and read by nothing that judges: it never changes a verdict.
//
// An occurrence of a finding is followed between checks against the same base_ref (a check against another base says
// nothing about it). Its outcome is, in order: the label a person or an agent set (false or irrelevant), intentional (a
// signed waiver), fixed (gone in a later check, with no waiver, from a run that looked at everything), parked (an open
// escalation waits for a person) or open. Only the label is ever set by hand; the rest is derived with no model.
const pathFindings = pathReports + "/findings.jsonl"

// Outcomes of an occurrence, as the list and the report name them.
const (
	outcomeOpen        = "open"
	outcomeParked      = "parked"
	outcomeFixed       = "fixed"
	outcomeIntentional = "intentional"
)

var (
	// ErrUnknownFinding is the id of a label that is not in the record.
	ErrUnknownFinding = errors.New("no such finding")
	// ErrInvalidLabel is a label that is not false or irrelevant, has no author, or comes from no known surface.
	ErrInvalidLabel = errors.New("invalid label")
)

// findingID is the same in every check for the same kind, source and target: that is what lets a later check see that a
// finding is gone.
func findingID(kind, source, target string) string {
	h := sha256.Sum256([]byte(kind + "\x00" + source + "\x00" + target))
	return "fnd-" + hex.EncodeToString(h[:])[:12]
}

// findingInput is what CheckWith knows besides the verdict: the waivers read from the working tree, which of them are
// signed, the protected files and their escalations.
type findingInput struct {
	waivers      map[string]contract.Waiver
	approved     map[string]bool
	guarded      map[string]string
	protectedEsc []contract.Escalation
}

type observedFinding struct {
	id, kind, source, target string
	parked                   bool
	decision                 string // the signed waiver that makes it intentional; empty if none
}

// recordsFindings says whether this check feeds the record: only the check of the working tree. A candidate of a2dlc run,
// a peer comparison or a Saboteur overlay judge other trees against the same base, and what one does not show says
// nothing about what another did.
func recordsFindings(opts CheckOptions) bool {
	return os.Getenv("A2DLC_FINDINGS") != "off" && opts.CandidateTree == "" && len(opts.Peers) == 0 && opts.TestsOverlay == "" && opts.ReportPath == ""
}

// observeFindings lists what the verdict shows as a finding, waived ones included (an intentional finding must have been
// raised). Parked is: an escalation of escalations.json is open about the same target.
func observeFindings(v contract.Verdict, in findingInput) []observedFinding {
	esc := append(escalations(v), in.protectedEsc...)
	escalated := map[string]bool{}
	for _, e := range esc {
		escalated[e.Target] = true
	}
	var out []observedFinding
	seen, targets := map[string]bool{}, map[string]bool{}
	add := func(kind, source, target string, protected bool) {
		id := findingID(kind, source, target)
		targets[target] = true
		if seen[id] {
			return
		}
		seen[id] = true
		o := observedFinding{id: id, kind: kind, source: source, target: target, parked: escalated[target]}
		if w, ok := in.waivers[target]; ok && in.approved[target] && (protected || w.Decision == contract.AcceptFinding || w.Decision == contract.EnforceNewBehavior) {
			o.decision = w.Decision
		}
		out = append(out, o)
	}
	for _, t := range v.Targets {
		switch t.Verdict {
		case contract.FAIL:
			add("behavior_divergence", "engine:"+t.Engine, t.Target, false)
		case contract.INCONCLUSIVE:
			add("tooling", "engine:"+t.Engine, t.Target, false)
		}
	}
	for _, t := range v.Tests {
		if t.Waived || t.Flaky || t.Verdict == contract.FAIL || t.Verdict == contract.INCONCLUSIVE {
			add("test_"+t.Gate, "gate:"+t.Gate, t.Target, false)
		}
	}
	for _, c := range v.Checks {
		switch c.Status {
		case contract.CheckRan:
			for _, f := range c.FindingList() {
				add("check_finding", "check:"+c.ID, f.Target, false)
			}
		case contract.CheckError, contract.CheckTimeout:
			if c.Severity == contract.CheckBlocking {
				add("tooling", "check:"+c.ID, pathConfig+"::checks."+c.ID, false)
			}
		}
	}
	if c := v.Coverage; c != nil {
		for _, f := range c.Files {
			if len(f.Uncovered) > 0 {
				add("uncovered_change", "gate:coverage", f.File+"::*", false)
			}
		}
	}
	for _, f := range sortedKeys(in.guarded) {
		add("protected_path", "gate:protected_paths", f+protectedSuffix, true)
	}
	for _, e := range v.EngineErrors {
		if len(e.Files) > 0 {
			add("tooling", "engine:"+e.Language, e.Files[0]+"::*", false)
		}
	}
	// What escalations() raises and nothing above covers: a gate or a check that did not run.
	for _, e := range esc {
		if !targets[e.Target] {
			add(e.Kind, escalationSource(e), e.Target, false)
		}
	}
	return out
}

func escalationSource(e contract.Escalation) string {
	switch {
	case strings.HasPrefix(e.Target, pathConfig+"::checks."):
		return "check:" + strings.TrimPrefix(e.Target, pathConfig+"::checks.")
	case strings.HasPrefix(e.Target, pathConfig+"::tests."):
		return "gate:" + strings.TrimPrefix(e.Target, pathConfig+"::tests.")
	case e.Target == "equivalence_s":
		return "gate:equivalence_s"
	}
	return "gate:" + e.Kind
}

// runComplete says whether the check looked at everything it was asked to. Only then does a finding's absence mean it is
// gone: an engine that did not answer, a check that was down or a diff the budget cut leaves findings unseen, not fixed.
func runComplete(v contract.Verdict) bool {
	if len(v.EngineErrors) > 0 || verdict.HasCut(v.DiffBudget) {
		return false
	}
	for _, c := range v.Checks {
		if c.Status == contract.CheckError || c.Status == contract.CheckTimeout {
			return false
		}
	}
	if g := v.TestGates; g != nil {
		for _, x := range []*contract.Gate{&g.NOPOracle, &g.Baseline, g.TestsDiff, g.EquivalenceS, g.Mutation} {
			if x != nil && x.Status == contract.GateError {
				return false
			}
		}
	}
	return v.Coverage == nil || v.Coverage.Status != contract.CoverageUnavailable
}

type occKey struct{ id, base string }

// occurrence is one life of a finding between its raised event and its fixed one, for one base.
type occurrence struct {
	raised      contract.FindingEvent
	intentional *contract.FindingEvent
	label       *contract.FindingEvent
	fixed       bool
}

func (o *occurrence) outcome() string {
	switch {
	case o.label != nil:
		return o.label.Outcome
	case o.intentional != nil:
		return outcomeIntentional
	case o.fixed:
		return outcomeFixed
	case o.raised.State == contract.FindingParked:
		return outcomeParked
	}
	return outcomeOpen
}

// replay folds the events into occurrences, oldest first; last maps each (id, base) to its latest one.
func replay(evs []contract.FindingEvent) (occs []*occurrence, last map[occKey]*occurrence) {
	last = map[occKey]*occurrence{}
	for _, e := range evs {
		e := e
		k := occKey{e.ID, e.BaseRef}
		if e.Event == contract.FindingRaised {
			o := &occurrence{raised: e}
			occs = append(occs, o)
			last[k] = o
			continue
		}
		o := last[k]
		if o == nil {
			continue
		}
		switch e.Event {
		case contract.FindingFixed:
			o.fixed = true
		case contract.FindingIntentional:
			o.intentional = &e
		case contract.FindingLabeled:
			o.label = &e
		}
	}
	return occs, last
}

func (a App) readFindingEvents() ([]contract.FindingEvent, error) {
	f, err := os.Open(a.path(pathFindings))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []contract.FindingEvent
	r := bufio.NewReader(f)
	for n := 1; ; n++ {
		line, err := r.ReadString('\n')
		if s := strings.TrimSpace(line); s != "" {
			var e contract.FindingEvent
			if derr := contract.DecodeStrictBytes([]byte(s), &e); derr != nil {
				return nil, fmt.Errorf("%s line %d: %w", pathFindings, n, derr)
			}
			out = append(out, e)
		}
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

// appendFindingEvents writes the events in a single write, so a concurrent check cannot cut one in half.
func (a App) appendFindingEvents(evs []contract.FindingEvent) error {
	if len(evs) == 0 {
		return nil
	}
	var b strings.Builder
	for _, e := range evs {
		line, err := json.Marshal(e)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	if err := os.MkdirAll(filepath.Dir(a.path(pathFindings)), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(a.path(pathFindings), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteString(b.String()); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// recordFindings appends the transitions that this check shows: raised for a finding with no live occurrence on this
// base, intentional when a signed waiver now covers it, fixed for a live one that is gone and has no waiver.
func (a App) recordFindings(v contract.Verdict, in findingInput) error {
	evs, err := a.readFindingEvents()
	if err != nil {
		return err
	}
	occs, last := replay(evs)
	now := a.Now().UTC().Format(time.RFC3339)
	event := func(kind, id string) contract.FindingEvent {
		return contract.FindingEvent{SchemaVersion: contract.SchemaVersion, Event: kind, At: now, ID: id, BaseRef: v.BaseRef, TreeHash: v.TreeHash}
	}
	var out []contract.FindingEvent
	present := map[string]bool{}
	for _, o := range observeFindings(v, in) {
		present[o.id] = true
		k := occKey{o.id, v.BaseRef}
		occ := last[k]
		if occ == nil || occ.fixed {
			e := event(contract.FindingRaised, o.id)
			e.Kind, e.Source, e.Target, e.State = o.kind, o.source, o.target, contract.FindingOpen
			if o.parked {
				e.State = contract.FindingParked
			}
			out = append(out, e)
			occ = &occurrence{raised: e}
			last[k] = occ
		}
		// shortcut: once intentional, an occurrence stays so even if its waiver is later removed; a new occurrence
		// starts only after a fixed one. Upgrade if the report shows waivers dropped with the finding still there.
		if o.decision != "" && occ.intentional == nil {
			e := event(contract.FindingIntentional, o.id)
			e.Decision = o.decision
			out = append(out, e)
			occ.intentional = &e
		}
	}
	if runComplete(v) {
		for _, occ := range occs {
			k := occKey{occ.raised.ID, occ.raised.BaseRef}
			if last[k] != occ || occ.fixed || k.base != v.BaseRef || present[k.id] {
				continue
			}
			if _, waived := in.waivers[occ.raised.Target]; waived {
				continue
			}
			out = append(out, event(contract.FindingFixed, k.id))
		}
	}
	return a.appendFindingEvents(out)
}

// LabelFinding records that a person, or an agent acting through the CLI or the MCP server, says a finding was false or
// irrelevant. It is the only way either outcome exists. The label goes on the finding's latest occurrence and records who
// set it (by is not authenticated) and through which surface; an earlier label of the same occurrence is replaced in the
// report but stays in the record.
func (a App) LabelFinding(id, outcome, by, via, reason string) (contract.FindingEvent, error) {
	switch {
	case outcome != contract.OutcomeFalse && outcome != contract.OutcomeIrrelevant:
		return contract.FindingEvent{}, fmt.Errorf("%w: outcome is false or irrelevant, not %q", ErrInvalidLabel, outcome)
	case strings.TrimSpace(by) == "":
		return contract.FindingEvent{}, fmt.Errorf("%w: it must say who sets it", ErrInvalidLabel)
	case via != contract.ViaCLI && via != contract.ViaMCP:
		return contract.FindingEvent{}, fmt.Errorf("%w: via is cli or mcp, not %q", ErrInvalidLabel, via)
	}
	evs, err := a.readFindingEvents()
	if err != nil {
		return contract.FindingEvent{}, err
	}
	occs, _ := replay(evs)
	for i := len(occs) - 1; i >= 0; i-- {
		if occs[i].raised.ID != id {
			continue
		}
		e := contract.FindingEvent{SchemaVersion: contract.SchemaVersion, Event: contract.FindingLabeled, At: a.Now().UTC().Format(time.RFC3339),
			ID: id, BaseRef: occs[i].raised.BaseRef, Outcome: outcome, By: strings.TrimSpace(by), Via: via, Reason: reason}
		return e, a.appendFindingEvents([]contract.FindingEvent{e})
	}
	return contract.FindingEvent{}, fmt.Errorf("%w: %s", ErrUnknownFinding, id)
}

// FindingRow is one occurrence of a finding, as `a2dlc finding list` shows it.
type FindingRow struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	Outcome  string `json:"outcome"`
	BaseRef  string `json:"base_ref"`
	RaisedAt string `json:"raised_at"`
	By       string `json:"by,omitempty"`
}

// FindingList is every occurrence in the record, oldest first; openOnly keeps those with no outcome yet (open or parked).
func (a App) FindingList(openOnly bool) ([]FindingRow, error) {
	evs, err := a.readFindingEvents()
	if err != nil {
		return nil, err
	}
	occs, _ := replay(evs)
	rows := []FindingRow{}
	for _, o := range occs {
		out := o.outcome()
		if openOnly && out != outcomeOpen && out != outcomeParked {
			continue
		}
		r := FindingRow{ID: o.raised.ID, Kind: o.raised.Kind, Source: o.raised.Source, Target: o.raised.Target, Outcome: out,
			BaseRef: o.raised.BaseRef, RaisedAt: o.raised.At}
		if o.label != nil {
			r.By = o.label.By
		}
		rows = append(rows, r)
	}
	return rows, nil
}

// SourceCount is one row of the report. Raised counts occurrences: every raised finding ends in exactly one of fixed,
// intentional, false, irrelevant or open. Parked is the part of open that waits on an open escalation.
type SourceCount struct {
	Source      string `json:"source"`
	Raised      int    `json:"raised"`
	Fixed       int    `json:"fixed"`
	Intentional int    `json:"intentional"`
	False       int    `json:"false"`
	Irrelevant  int    `json:"irrelevant"`
	Open        int    `json:"open"`
	Parked      int    `json:"parked"`
}

func (c *SourceCount) add(outcome string) {
	c.Raised++
	switch outcome {
	case outcomeFixed:
		c.Fixed++
	case outcomeIntentional:
		c.Intentional++
	case contract.OutcomeFalse:
		c.False++
	case contract.OutcomeIrrelevant:
		c.Irrelevant++
	case outcomeParked:
		c.Parked++
		c.Open++
	default:
		c.Open++
	}
}

// FindingReport is the record counted per source: engine:<engine>, check:<id> or gate:<name>.
type FindingReport struct {
	Sources []SourceCount `json:"sources"`
	Total   SourceCount   `json:"total"`
}

// FindingReport counts the record per check and engine. It reads the record only.
func (a App) FindingReport() (FindingReport, error) {
	evs, err := a.readFindingEvents()
	if err != nil {
		return FindingReport{}, err
	}
	occs, _ := replay(evs)
	by := map[string]*SourceCount{}
	r := FindingReport{Sources: []SourceCount{}, Total: SourceCount{Source: "total"}}
	for _, o := range occs {
		c := by[o.raised.Source]
		if c == nil {
			c = &SourceCount{Source: o.raised.Source}
			by[o.raised.Source] = c
		}
		c.add(o.outcome())
		r.Total.add(o.outcome())
	}
	for _, c := range by {
		r.Sources = append(r.Sources, *c)
	}
	sort.Slice(r.Sources, func(i, j int) bool { return r.Sources[i].Source < r.Sources[j].Source })
	return r, nil
}

// Text is the report as a table.
func (r FindingReport) Text() string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "source\traised\tfixed\tintentional\tfalse\tirrelevant\topen\t(parked)")
	for _, c := range append(append([]SourceCount{}, r.Sources...), r.Total) {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t(%d)\n", c.Source, c.Raised, c.Fixed, c.Intentional, c.False, c.Irrelevant, c.Open, c.Parked)
	}
	w.Flush()
	return b.String()
}
