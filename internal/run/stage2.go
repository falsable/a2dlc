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
)

// Stage 2: Complexity & Risk Routing (protocol §5). The Router is core and deterministic: it decides the track (N=1 or N=2)
// and whether Stage 1 has to run dual-spec, with the route rules. With S* it adds the metrics of route_metrics.go
// (rule_count and state_mutation_depth), which can also raise the track to N=2. It does not read a dependency graph: the depth
// comes from the invariants and from the posts that name another function of S*.

func stageRoute(_ context.Context, m *Machine) (string, string, string, error) {
	r, err := m.computeRoute()
	if err != nil {
		return "", "", "", err
	}
	artifact, err := m.writeArtifact(ArtRoute, r)
	return contract.StageDone, "", artifact, err
}

// readRoute is what Stage 1 consults (dual_spec_required), which runs before Stage 2; Stage 3 reads the track directly
// from route.json. If Stage 2 already wrote route.json it returns it; if not, it computes the same decision with the
// same rules without writing it (the artifact belongs to Stage 2).
func (m *Machine) readRoute() (contract.Route, error) {
	var r contract.Route
	b, err := os.ReadFile(filepath.Join(m.dir, ArtRoute))
	switch {
	case err == nil:
		if err := contract.DecodeStrictBytes(b, &r); err != nil {
			return r, fmt.Errorf("%s does not match the contract: %w", ArtRoute, err)
		}
		return r, nil
	case !errors.Is(err, os.ErrNotExist):
		return r, err
	}
	return m.computeRoute()
}

// computeRoute gathers what the Router needs from the repo: the files changed or deleted from the base to the working
// tree, only if the intent declares critical_paths. With a builder the working tree is still the base and nothing has
// been touched yet: then the expected footprint is the files that the S* probes name (probes.json, Stage 1), which is
// where Tier 0 will evaluate the change (measured 2026-10-17: with critical_paths src/** and a builder, the Router gave
// N=1 because nobody had touched anything yet).
func (m *Machine) computeRoute() (contract.Route, error) {
	var touched []string
	expected := false
	if m.intent != nil && len(m.intent.CriticalPaths) > 0 {
		tree, err := m.app.Repo.WorktreeTree()
		if err != nil {
			return contract.Route{}, err
		}
		changed, deleted, err := m.app.Repo.Changed(m.baseSha, tree)
		if err != nil {
			return contract.Route{}, err
		}
		touched = append(changed, deleted...)
		if len(touched) == 0 {
			probes, err := m.readProbes()
			if err != nil {
				return contract.Route{}, err
			}
			seen := map[string]bool{}
			for _, p := range probes {
				if p.File != nil && *p.File != "" && !seen[*p.File] {
					seen[*p.File] = true
					touched = append(touched, *p.File)
				}
			}
			expected = len(touched) > 0
		}
	}
	return m.routeWithMetrics(route(m.intent, m.state.Mode, touched, expected))
}

// route are the rules of the Router, in order:
//  1. the criticality is the intent's; without intent, low;
//  2. if a touched file matches critical_paths, it goes up one level (once, not per file); expected says that the touched
//     ones are not changes in the tree but the files that the S* probes name (the reason declares it). Any
//     criticality_tags goes up one level the same way (once, not per tag), independently of critical_paths: a tag and a
//     touched critical path together raise two levels;
//  3. lite: N=1 and no dual-spec always (there is no S* or builders);
//  4. high or critical: N=2. In refactor and brownfield_facade the second builder is the legacy one (§5 Stage 2);
//  5. greenfield with medium or more: dual-spec mandatory. In brownfield_facade Stage 1 also runs dual-spec, but without
//     obligation: without the roles it is skipped. In refactor Stage 1 is behavior capture (§4), so dual-spec does not apply.
func route(in *contract.Intent, mode string, touched []string, expected bool) contract.Route {
	r := contract.Route{SchemaVersion: contract.SchemaVersion, Track: contract.TrackN1, TouchedCritical: []string{}}
	level := 0
	if in == nil {
		r.Reasons = append(r.Reasons, "no intent: low criticality by default")
	} else {
		level = contract.CriticalityRank(in.Criticality)
		r.Reasons = append(r.Reasons, "criticality declared in the intent: "+in.Criticality)
		if matched, _ := app.Partition(in.CriticalPaths, touched); len(matched) > 0 {
			sort.Strings(matched)
			r.TouchedCritical = matched
			list := strings.Join(matched, ", ")
			if len(matched) > 5 {
				list = fmt.Sprintf("%s and %d more", strings.Join(matched[:5], ", "), len(matched)-5)
			}
			touches := "touches critical_paths"
			if expected {
				touches = "the S* probes expect to touch critical_paths"
			}
			var why string
			level, why = raiseOne(level, fmt.Sprintf("%s (%s)", touches, list))
			r.Reasons = append(r.Reasons, why)
		}
		// Tags raise on their own, independently of the touched critical paths: a tag and a touched critical path
		// together raise two levels.
		var why string
		level, why = raiseForTags(level, in.CriticalityTags)
		if why != "" {
			r.Reasons = append(r.Reasons, why)
		}
	}
	effective := contract.Criticalities[level]
	r.CriticalityEffective = effective
	high := level >= contract.CriticalityRank(contract.CriticalityHigh)
	switch {
	case mode == contract.ModeLite:
		r.Reasons = append(r.Reasons, "lite mode: always N=1 and no dual-spec (there is no S*)")
		return r
	case high && mode == contract.ModeGreenfield:
		r.Track, r.LemmasRequired = contract.TrackN2, true
		r.Reasons = append(r.Reasons, "criticality "+effective+": N=2 (primary and orthogonal builder)")
	case high:
		r.Track, r.LemmasRequired = contract.TrackN2, true
		r.Reasons = append(r.Reasons, "criticality "+effective+": N=2; in mode "+mode+" the second builder is the legacy one (Stage 4 differential)")
	default:
		r.Reasons = append(r.Reasons, "criticality "+effective+": N=1")
	}
	switch {
	case mode == contract.ModeGreenfield && level >= contract.CriticalityRank(contract.CriticalityMedium):
		r.DualSpecRequired = true
		r.Reasons = append(r.Reasons, "greenfield with criticality "+effective+": dual-spec mandatory")
	case mode == contract.ModeRefactor && high:
		r.Reasons = append(r.Reasons, "refactor mode: Stage 1 is behavior capture, not dual-spec")
	case mode == contract.ModeBrownfield && high:
		r.Reasons = append(r.Reasons, "brownfield_facade mode: Stage 1 runs dual-spec if the roles specifier_alpha, specifier_beta and inquisitor are present, but it is not mandatory: without them it is skipped")
	}
	return r
}

// raiseOne moves the level up one step, or says the slice is already critical. Each source of a raise calls it once.
func raiseOne(level int, what string) (int, string) {
	if level < len(contract.Criticalities)-1 {
		level++
		return level, fmt.Sprintf("%s: goes up to %s", what, contract.Criticalities[level])
	}
	return level, fmt.Sprintf("%s: already critical", what)
}

// raiseForTags raises the level once for the whole set of criticality_tags, however many there are. Duplicates are
// dropped and the reason names each tag once, sorted. It returns "" when there are no tags.
func raiseForTags(level int, tags []string) (int, string) {
	if len(tags) == 0 {
		return level, ""
	}
	seen := map[string]bool{}
	var uniq []string
	for _, t := range tags {
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	sort.Strings(uniq)
	return raiseOne(level, "criticality_tags "+strings.Join(uniq, ", "))
}
