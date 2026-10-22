package run

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/graph"
)

// Stage 6: Integration (protocol §5), the part that fits in the binary of the provider repo, over a candidate that is
// already PASS (Stages 4 and 5). Declared blast radius: the changed files that match the contracts of each consumer of
// config.integration. Contract matrix directed by consumer engine: each affected consumer runs its suite, without
// network, against the candidate in /work; a red one escalates (consumer_broken) with its tests. Expand-and-contract:
// the changed migration files go through the destructive DDL linter; a finding escalates (destructive_migration). A
// run that cannot be judged leaves the stage failed (INCONCLUSIVE), never green. A consumer whose testcases are all
// skipped is not green either. The edges found inside the repo (OpenAPI and protobuf to their generated clients, and
// the imports the engines report) are listed in integration.json.discovered; they do not decide the verdict.
//
// Outside the binary, declared in the README: the graph between services (topics, cross-repo contracts) and the
// durable workflow that fires the contract when its preconditions are met.

const (
	ArtIntegration = "integration.json"
	ArtDelivery    = "delivery.json"

	// reasonWithoutIntegration is the skipped reason of Stage 6 when there is no section; skipStale recognizes it on resume once the section is present.
	reasonWithoutIntegration = "no integration section in config.json"
)

func stageIntegration(ctx context.Context, m *Machine) (string, string, string, error) {
	if reason := m.withoutPass("integration is measured on a candidate that has already passed the oracle and the kill gate"); reason != "" {
		return contract.StageSkipped, reason, "", nil
	}
	cfg, err := m.app.Config()
	if err != nil {
		return "", "", "", err
	}
	ic := cfg.Integration
	if ic == nil || len(ic.Consumers) == 0 && ic.Migrations == nil {
		return contract.StageSkipped, reasonWithoutIntegration + ": no consumers or migrations declared", "", nil
	}
	candidate, tree, err := m.judgedTree()
	if err != nil {
		return "", "", "", err
	}
	changed, deleted, err := m.app.Repo.Changed(m.baseSha, tree)
	if err != nil {
		return "", "", "", err
	}
	all := append(append([]string{}, changed...), deleted...)
	sort.Strings(all)
	rep := contract.IntegrationReport{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID, BaseSha: m.baseSha,
		Candidate: candidate, ChangedFiles: all, Consumers: []contract.ConsumerResult{},
		Migrations: contract.MigrationsReport{Files: []string{}, Destructive: []contract.DestructiveDDL{}}, Verdict: contract.PASS}
	edges, blast, notes, err := m.app.Discover(ctx, m.baseSha, tree, changed, *ic)
	if err != nil {
		// discovery never decides a verdict: a failure of it is a note, and the consumers and migrations are still judged
		edges, blast, notes = nil, nil, []string{"discovery not measured: " + err.Error()}
	}
	rep.Discovered = edges
	rep.BlastRadius = blast
	discovered := discoveredAffected(edges)
	var escalations []contract.Escalation
	var reasons []string
	inconclusive := false

	for _, c := range ic.Consumers {
		r := contract.ConsumerResult{Name: c.Name, Image: c.Image, AffectedBy: []string{}}
		if len(c.Contracts) > 0 {
			affecting, _ := app.Partition(c.Contracts, all)
			if c.Auto {
				// a discovered consumer also runs when the import graph or a contract edge reaches a changed file
				affecting = append(affecting, discovered...)
			}
			affecting = uniqueSorted(affecting)
			r.AffectedBy = append(r.AffectedBy, affecting...) // never nil: the contractJSON asks for an array
			if len(affecting) == 0 {
				r.Status = contract.ConsumerNotRun
				rep.Consumers = append(rep.Consumers, r)
				continue
			}
		}
		run, err := m.app.RunConsumer(ctx, tree, c)
		if err != nil {
			return "", "", "", err
		}
		if run.Reason != "" {
			r.Status, r.Reason = contract.ConsumerInconclusive, run.Reason
			inconclusive = true
			reasons = append(reasons, fmt.Sprintf("consumer %s: %s", c.Name, run.Reason))
			rep.Consumers = append(rep.Consumers, r)
			continue
		}
		reds := run.Reds()
		green := 0 // skipped is not green
		for _, s := range run.States {
			if s == contract.TestPassed {
				green++
			}
		}
		r.Tests = &contract.ConsumerTests{Passed: green, Failed: len(reds)}
		if len(reds) == 0 && green == 0 {
			r.Status, r.Reason = contract.ConsumerInconclusive, "no test passed: skipped is not green"
			inconclusive = true
			reasons = append(reasons, fmt.Sprintf("consumer %s: %s", c.Name, r.Reason))
			rep.Consumers = append(rep.Consumers, r)
			continue
		}
		if len(reds) == 0 {
			r.Status = contract.ConsumerPassed
			rep.Consumers = append(rep.Consumers, r)
			continue
		}
		r.Status, r.FailedTests = contract.ConsumerFailed, reds
		rep.Consumers = append(rep.Consumers, r)
		why := "runs on every slice"
		if len(r.AffectedBy) > 0 {
			why = "affected by " + list(r.AffectedBy)
		}
		for _, id := range reds {
			escalations = append(escalations, app.Escalation("consumer_broken", c.Name+"::"+id,
				fmt.Sprintf("Consumer %s fails %d of %d tests against the candidate (%s).", c.Name, len(reds), len(run.States), why),
				contract.Evidence{Candidate: run.States[id], Message: run.Messages[id]}))
		}
		reasons = append(reasons, fmt.Sprintf("consumer %s breaks %d tests (%s)", c.Name, len(reds), list(reds)))
	}

	if mc := ic.Migrations; mc != nil {
		extra, err := app.DDLExtraRules(mc.Forbidden)
		if err != nil {
			return "", "", "", err
		}
		files, _ := app.Partition(mc.Paths, changed) // a deleted file brings no DDL to read
		sort.Strings(files)
		rep.Migrations.Files = append(rep.Migrations.Files, files...)
		for _, f := range files {
			b, err := m.app.Repo.Show(tree, f)
			if err != nil {
				return "", "", "", fmt.Errorf("read %s from the candidate: %w", f, err)
			}
			for _, d := range app.DestructiveDDLs(f, b, extra) {
				rep.Migrations.Destructive = append(rep.Migrations.Destructive, d)
				// the rule goes in the target: two findings on the same line do not share an id
				escalations = append(escalations, app.Escalation("destructive_migration", fmt.Sprintf("%s::L%d:%s", d.File, d.Line, d.Rule),
					fmt.Sprintf("destructive DDL (%s) in a changed migration: expand-and-contract forbids it in the same release.", d.Rule),
					contract.Evidence{Lines: []int{d.Line}, Message: d.Statement}))
			}
		}
		if n := len(rep.Migrations.Destructive); n > 0 {
			reasons = append(reasons, fmt.Sprintf("%d destructive DDL statements in %s", n, list(files)))
		}
	}

	switch {
	case len(escalations) > 0:
		rep.Verdict = contract.FAIL
	case inconclusive:
		rep.Verdict = contract.INCONCLUSIVE
	}
	artifact, err := m.writeArtifact(ArtIntegration, rep)
	if err != nil {
		return "", "", "", err
	}
	switch rep.Verdict {
	case contract.FAIL:
		if _, err := m.escalate(escalations); err != nil {
			return "", "", "", err
		}
		return contract.StageEscalated, "integration rejects the candidate: " + strings.Join(append(reasons, notes...), "; "), artifact, nil
	case contract.INCONCLUSIVE:
		return contract.StageFailed, "integration not judged: " + strings.Join(append(reasons, notes...), "; "), artifact, nil
	}
	pass := contract.PASS // a run resumed after fixing the consumer goes back to PASS: Run does not recompute the outcome
	m.state.Outcome = &pass
	summary := integrationSummary(rep)
	if len(notes) > 0 {
		summary += "; " + strings.Join(notes, "; ")
	}
	return contract.StageDone, summary, artifact, nil
}

// list joins paths or ids with a comma, cutting to five.
func list(ss []string) string {
	if len(ss) > 5 {
		return fmt.Sprintf("%s and %d more", strings.Join(ss[:5], ", "), len(ss)-5)
	}
	return strings.Join(ss, ", ")
}

// discoveredAffected are the files the discovered edges reach from the change: the importers of a changed module (the
// From of an import edge), the dependents from the Code Graph Indexer (the From of a code_graph edge), and the generated
// clients of a changed contract (the To of a contract edge).
func discoveredAffected(edges []contract.DiscoveredEdge) []string {
	var out []string
	for _, e := range edges {
		if e.Via == graph.ViaImport || e.Via == graph.ViaCodeGraph {
			out = append(out, e.From)
		} else {
			out = append(out, e.To)
		}
	}
	return uniqueSorted(out)
}

// uniqueSorted returns ss sorted, without repeats, and never nil.
func uniqueSorted(ss []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range ss {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func integrationSummary(rep contract.IntegrationReport) string {
	ran, notRun := 0, 0
	for _, c := range rep.Consumers {
		if c.Status == contract.ConsumerNotRun {
			notRun++
		} else {
			ran++
		}
	}
	parts := []string{fmt.Sprintf("%d consumers green against the candidate", ran)}
	if notRun > 0 {
		parts = append(parts, fmt.Sprintf("%d with nothing of theirs changed", notRun))
	}
	if n := len(rep.Migrations.Files); n > 0 {
		parts = append(parts, fmt.Sprintf("%d migrations without destructive DDL", n))
	}
	if rep.BlastRadius != nil {
		parts = append(parts, fmt.Sprintf("%d files in blast radius (%d impacted)", len(rep.BlastRadius.Ranking), rep.BlastRadius.TotalImpacted))
	}
	return strings.Join(parts, "; ")
}

// withoutPass says why a stage after the judgment does not run: the verdict of Stage 4 (verdict.json) or, if 5
// ran, that of the kill gate (mutation.json) is not PASS. It reads the artifacts and not state.outcome, which Stages 6
// and 7 overwrite: this way a run resumed after fixing a consumer goes back to judging 6. "" if everything is PASS.
func (m *Machine) withoutPass(what string) string {
	var v contract.Verdict
	if !m.stageDone("4") || readJSON(filepath.Join(m.dir, ArtVerdict), &v) != nil {
		return "Stage 4 was not PASS: " + what
	}
	if v.Verdict != contract.PASS {
		return fmt.Sprintf("the Stage 4 verdict is %s, not PASS: %s", v.Verdict, what)
	}
	if m.stageDone("5") {
		var rep contract.MutationReport
		if readJSON(filepath.Join(m.dir, ArtMutation), &rep) != nil || rep.Verdict != contract.PASS {
			return fmt.Sprintf("the Stage 5 kill gate was not PASS (%s): %s", rep.Verdict, what)
		}
	}
	return ""
}

// judgedTree is the commit of the candidate (the one selected with N=2) and the treeish to read it with; without a
// builder, nil and the working tree as git tree.
func (m *Machine) judgedTree() (*string, string, error) {
	candidate, err := m.candidate()
	if err != nil {
		return nil, "", err
	}
	if candidate != "" {
		return &candidate, candidate, nil
	}
	tree, err := m.app.Repo.WorktreeTree()
	return nil, tree, err
}
