package run

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/junit"
)

// Stage 5: Tier 2 (protocol §5, steps 1 and 3; block 5 of ADR 0004). Gate of the suite, not of the candidate: the engine
// generates mutants only of the changed lines (operation: mutate) and each one is judged with T (the candidate's changed
// tests plus the run's overlay, where the Saboteur's are) on the candidate with the mutated file on top, against a clean
// run of T. kill_rate = killed / (killed + survived). Below the threshold, the live mutants go to the Saboteur (without
// code: file, line, operator) and it is measured again, up to --max-rounds. What was not measured (limit, engine that did
// not generate it) does not count as killed: the gate does not give PASS with anything like that listed.
//
// The pruning of equivalents (step 2) is done by the engine with conservative AST rules (logging, assert, branch with a
// constant condition): what is pruned goes to pruned_equivalent, it is not a mutant and does not enter kill_rate. Out of
// scope: the Occam selection between candidates (step 4, N=2, block 8).

const (
	ArtMutation       = "mutation.json"
	KillRateDefault   = 0.8
	MaxMutantsDefault = 30
)

func stageMutation(ctx context.Context, m *Machine) (string, string, string, error) {
	var v contract.Verdict
	if !m.stageDone("4") || readJSON(filepath.Join(m.dir, ArtVerdict), &v) != nil || v.Verdict != contract.PASS {
		return contract.StageSkipped, "Stage 4 was not PASS: mutation measures the suite of a candidate that has already passed the oracle", "", nil
	}
	cfg, err := m.app.Config()
	if err != nil {
		return "", "", "", err
	}
	if cfg.Tests == nil {
		return contract.StageSkipped, "no config.tests: there is no command to run T against the mutants", "", nil
	}
	// With no engine configured nothing can mutate the candidate's files, so the files are not "unsupported" by an engine
	// that does not exist: the stage is skipped and says so, and unsupported_files applies only when an engine is configured.
	if len(cfg.Engines) == 0 {
		return contract.StageSkipped, "no config.engines: no engine mutates the candidate, so mutation is not measured", "", nil
	}
	threshold, limit := KillRateDefault, MaxMutantsDefault
	if mc := cfg.Mutation; mc != nil {
		if mc.MinKillRate != nil {
			threshold = *mc.MinKillRate
		}
		if mc.MaxMutants > 0 {
			limit = mc.MaxMutants
		}
	}
	if m.maxMutants > 0 {
		limit = m.maxMutants // the intent belongs to this slice; config.mutation belongs to the repo
	}
	candidate, err := m.candidate() // with a builder (Stage 3) its sources are mutated, not the working tree
	if err != nil {
		return "", "", "", err
	}
	// signatures.json belongs to the candidate that the Saboteur attacks: the one from Stage 4 may be another (N=2 with
	// the primary selected after rounds of the orthogonal), so roundSaboteur regenerates it with this stage's candidate
	if err := os.Remove(filepath.Join(m.dir, ArtSignatures)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", "", err
	}
	d, err := m.app.Mutants(ctx, m.baseSha, candidate, limit)
	if err != nil {
		return "", "", "", err
	}
	// Files in scope that no engine mutates are not a skip: they stay in the report as unsupported_files and the stage is
	// INCONCLUSIVE (judge), so a change that the gate could not measure never reaches PASS.
	if len(d.Mutants) == 0 && len(d.NotGenerated) == 0 && len(d.NoEngine) == 0 {
		reason := "no mutant on the changed lines"
		if n := len(d.Pruned); n > 0 {
			reason += fmt.Sprintf(" (%d equivalent mutants pruned)", n)
		}
		return contract.StageSkipped, reason, "", nil
	}
	overlay := filepath.Join(m.dir, dirOverlay)
	reason := ""
	var rep contract.MutationReport
	for {
		if rep, err = m.measureMutants(ctx, d, overlay, threshold, limit); err != nil {
			return "", "", "", err
		}
		alive := survivors(rep)
		if err := m.escalateMutants(v, rep, alive); err != nil {
			return "", "", "", err
		}
		if rep.KillRate == nil || *rep.KillRate >= threshold || len(rep.RedOnCandidate) > 0 {
			break // PASS, or an INCONCLUSIVE the Saboteur cannot fix
		}
		if _, ok := m.roles.Roles[contract.RoleSaboteur]; !ok {
			reason = "live mutants without a saboteur role in " + pathRoles + ": nobody brings the test"
			break
		}
		if m.iter >= m.maxRounds {
			reason = fmt.Sprintf("after %d saboteur rounds (--max-rounds)", m.iter)
			break
		}
		m.iter++
		finished, err := m.roundSaboteur(ctx, "5", nil, alive, overlay, m.iter, candidate, nil)
		if err != nil {
			return "", "", "", err
		}
		if finished != "" {
			reason = finished
			break
		}
	}
	if reason != "" {
		rep.Reason += "; " + reason
	}
	if n := len(rep.PrunedEquivalent); n > 0 {
		prune := fmt.Sprintf("%d equivalent mutants pruned (see pruned_equivalent)", n)
		if rep.Reason == "" {
			rep.Reason = prune
		} else {
			rep.Reason += "; " + prune
		}
	}
	if _, err := m.writeArtifact(ArtMutation, rep); err != nil {
		return "", "", "", err
	}
	o := rep.Verdict
	m.state.Outcome = &o
	return contract.StageDone, rep.Reason, ArtMutation, nil
}

// measureMutants runs the round: clean T on the candidate and, if it is green, T on each mutant (up to the limit).
func (m *Machine) measureMutants(ctx context.Context, d app.DiffMutable, overlay string, threshold float64, limit int) (contract.MutationReport, error) {
	rep := contract.MutationReport{SchemaVersion: contract.SchemaVersion, Round: m.iter, Threshold: threshold, MaxMutants: limit,
		Tests: []string{}, RedOnCandidate: []string{}, Mutants: []contract.MutantResult{}, NotGenerated: d.NotGenerated,
		PrunedEquivalent: append([]contract.PrunedMutant{}, d.Pruned...), Unsupported: d.NoEngine, UnsupportedFiles: unsupportedNames(d.NoEngine)}
	ov, err := filesOverlay(overlay)
	if err != nil {
		return rep, err
	}
	inTests := map[string]bool{}
	for _, f := range d.Tests {
		inTests[f] = true
	}
	for _, f := range ov {
		inTests[f.Path] = true
	}
	for f := range inTests {
		rep.Tests = append(rep.Tests, f)
	}
	sort.Strings(rep.Tests)
	withoutRunning := "" // the reason no mutant runs in this round
	if len(rep.Tests) > 0 {
		clean, err := m.app.RunTests(ctx, d.Tree, ov, rep.Tests, d.Sources)
		switch {
		case err != nil:
			return rep, err
		case clean.Reason != "":
			withoutRunning = "the clean run of T on the candidate could not be judged: " + clean.Reason
		default:
			// A red test without mutating would kill every mutant and fake kill_rate 1: the round is not measured.
			if rep.RedOnCandidate = clean.With(junit.Red); len(rep.RedOnCandidate) > 0 {
				withoutRunning = "T has red tests on the candidate without mutation: the oracle (Stage 4) has to judge them first"
			}
		}
	}
	for i, mu := range d.Mutants {
		r := contract.MutantResult{Mutant: mu.Mutant, KilledBy: []string{}}
		switch {
		case i >= limit:
			r.Status, r.Reason = contract.MutantNotRun, fmt.Sprintf("max_mutants cap=%d", limit)
		case withoutRunning != "":
			r.Status, r.Reason = contract.MutantNotRun, withoutRunning
		case ctx.Err() != nil:
			r.Status, r.Reason = contract.MutantNotRun, "run interrupted: "+ctx.Err().Error()
		case len(rep.Tests) == 0:
			r.Status, r.Reason = contract.MutantSurvived, "T empty: no test can kill it"
		default:
			c, err := m.app.RunTests(ctx, d.Tree, append(append([]app.OverlayFile{}, ov...), app.OverlayFile{Path: mu.File, Content: mu.Content}), rep.Tests, d.Sources)
			switch {
			case err != nil:
				return rep, err
			case c.Reason != "" && ctx.Err() != nil:
				r.Status, r.Reason = contract.MutantNotRun, "run interrupted: "+ctx.Err().Error()
			case c.Reason != "":
				r.Status, r.Reason = contract.MutantInconclusive, c.Reason
			default:
				// the clean run had no reds: any red here was caused by the mutant
				if r.KilledBy = c.With(junit.Red); len(r.KilledBy) > 0 {
					r.Status = contract.MutantKilled
				} else {
					r.Status = contract.MutantSurvived
				}
			}
		}
		rep.Mutants = append(rep.Mutants, r)
	}
	judge(&rep, threshold)
	return rep, nil
}

// unsupportedNames is unsupported_files: the files of NoEngine, sorted and without repeats (the schema has uniqueItems).
func unsupportedNames(fs []contract.FileReason) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, f := range fs {
		if !seen[f.File] {
			seen[f.File] = true
			out = append(out, f.File)
		}
	}
	sort.Strings(out)
	return out
}

// judge sets kill_rate, verdict and reason. Unsupported files (in scope, not mutated) block PASS whatever the kill rate:
// what was not measured does not count.
func judge(rep *contract.MutationReport, threshold float64) {
	judgeMutants(rep, threshold)
	if n := len(rep.UnsupportedFiles); n > 0 {
		rep.Verdict = contract.INCONCLUSIVE
		why := fmt.Sprintf("%d in-scope files not mutated (unsupported_files): what was not measured does not count", n)
		if rep.Reason != "" {
			why += "; " + rep.Reason
		}
		rep.Reason = why
	}
}

// judgeMutants sets kill_rate, verdict and reason from the mutants alone.
func judgeMutants(rep *contract.MutationReport, threshold float64) {
	n := map[string]int{}
	for _, r := range rep.Mutants {
		n[r.Status]++
	}
	judged := n[contract.MutantKilled] + n[contract.MutantSurvived]
	rep.Verdict = contract.INCONCLUSIVE
	if judged == 0 {
		rep.KillRate, rep.Reason = nil, fmt.Sprintf("no mutant ended killed or survived (%d inconclusive, %d not_run, %d files with mutants not generated)",
			n[contract.MutantInconclusive], n[contract.MutantNotRun], len(rep.NotGenerated))
		if len(rep.RedOnCandidate) > 0 {
			rep.Reason = "T has red tests on the candidate without mutation (" + strings.Join(rep.RedOnCandidate, ", ") + "): " + rep.Reason
		}
		return
	}
	k := float64(n[contract.MutantKilled]) / float64(judged)
	rep.KillRate = &k
	switch {
	case k < threshold:
		rep.Reason = fmt.Sprintf("kill gate: kill_rate %.2f < %.2f, %d live mutants", k, threshold, n[contract.MutantSurvived])
	case n[contract.MutantNotRun] > 0 || n[contract.MutantInconclusive] > 0 || len(rep.NotGenerated) > 0:
		rep.Reason = fmt.Sprintf("kill_rate %.2f >= %.2f, but %d mutants not run, %d not judged and %d files with mutants not generated: what was not measured does not count",
			k, threshold, n[contract.MutantNotRun], n[contract.MutantInconclusive], len(rep.NotGenerated))
	default:
		rep.Verdict, rep.Reason = contract.PASS, ""
	}
}

// survivors are the live mutants as the Saboteur sees them, only if the gate ended below the threshold.
func survivors(rep contract.MutationReport) []contract.SaboteurSurvivor {
	var out []contract.SaboteurSurvivor
	if rep.KillRate == nil || *rep.KillRate >= rep.Threshold {
		return out
	}
	for _, r := range rep.Mutants {
		if r.Status == contract.MutantSurvived {
			out = append(out, contract.SaboteurSurvivor{File: r.File, Line: r.Line, Operator: r.Operator, Description: r.Description})
		}
	}
	return out
}

// escalateMutants rewrites the run's escalations.json: what the oracle left and a mutant_survived for each live mutant
// while the gate is below the threshold. It is what the Saboteur sees in /in.
func (m *Machine) escalateMutants(v contract.Verdict, rep contract.MutationReport, alive []contract.SaboteurSurvivor) error {
	escalations := contract.Escalations{SchemaVersion: contract.SchemaVersion, TreeHash: v.TreeHash, CreatedAt: v.CreatedAt, Items: []contract.Escalation{}}
	var previous contract.Escalations
	if readJSON(filepath.Join(m.dir, ArtEscalations), &previous) == nil {
		escalations.TreeHash, escalations.CreatedAt = previous.TreeHash, previous.CreatedAt
		for _, e := range previous.Items {
			if e.Kind != "mutant_survived" {
				escalations.Items = append(escalations.Items, e)
			}
		}
	}
	if len(alive) > 0 {
		for _, r := range rep.Mutants {
			if r.Status == contract.MutantSurvived {
				escalations.Items = append(escalations.Items, app.MutantEscalation(r.Mutant, fmt.Sprintf("Live mutant in %s:%d (%s): no test of T kills it; kill_rate %.2f < %.2f.",
					r.File, r.Line, r.Operator, *rep.KillRate, rep.Threshold)))
			}
		}
	}
	_, err := m.writeArtifact(ArtEscalations, escalations)
	return err
}

func (m *Machine) stageDone(id string) bool {
	for _, s := range m.state.Stages {
		if s.ID == id {
			return s.Status == contract.StageDone
		}
	}
	return false
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return contract.DecodeStrictBytes(b, v)
}

// filesOverlay reads the run's overlay (the Saboteur's tests) as files with their path in the repo.
func filesOverlay(dir string) ([]app.OverlayFile, error) {
	rels, err := relativePathsUnder(dir)
	if err != nil {
		return nil, err
	}
	out := []app.OverlayFile{}
	for _, rel := range rels {
		b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, err
		}
		out = append(out, app.OverlayFile{Path: rel, Content: b})
	}
	return out, nil
}
