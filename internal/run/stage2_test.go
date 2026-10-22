package run

import (
	"strings"
	"testing"

	"github.com/falsable/a2dlc/internal/contract"
)

// routeOfIntent runs the Router rules on an intent without touching the repo: no touched files, no S*.
func routeOfIntent(in contract.Intent, mode string) contract.Route {
	return route(&in, mode, nil, false)
}

// TestStage2TagRaisesCriticalityOneStep: a criticality tag raises a low slice to medium, as critical_paths does, and
// the reason names the tags. Greenfield medium makes dual-spec mandatory, so the effect is visible on the route.
func TestStage2TagRaisesCriticalityOneStep(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityLow, CriticalityTags: []string{"security"}}, contract.ModeGreenfield)
	if r.CriticalityEffective != contract.CriticalityMedium {
		t.Fatalf("effective criticality %q, want medium", r.CriticalityEffective)
	}
	if !r.DualSpecRequired || r.Track != contract.TrackN1 {
		t.Fatalf("medium greenfield wants dual-spec and N=1, got dual=%v track=%s", r.DualSpecRequired, r.Track)
	}
	if !reasonHas(r, "criticality_tags security: goes up to medium") {
		t.Fatalf("reasons %v do not name the tag rule", r.Reasons)
	}
}

// TestStage2TagFromHighGivesN2: high plus one tag is critical and N=2.
func TestStage2TagFromHighGivesN2(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityHigh, CriticalityTags: []string{"auth"}}, contract.ModeGreenfield)
	if r.CriticalityEffective != contract.CriticalityCritical || r.Track != contract.TrackN2 {
		t.Fatalf("got %s track %s, want critical and N=2", r.CriticalityEffective, r.Track)
	}
}

// TestStage2TagsRaiseOnlyOnce: several tags raise one level in total, like several touched critical files (schema:
// "once, however many tags there are").
func TestStage2TagsRaiseOnlyOnce(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityLow, CriticalityTags: []string{"security", "billing", "concurrency"}}, contract.ModeRefactor)
	if r.CriticalityEffective != contract.CriticalityMedium {
		t.Fatalf("three tags on low gave %q, want medium", r.CriticalityEffective)
	}
}

// TestStage2TagStopsAtCritical: a tag on a critical slice keeps it critical and says so.
func TestStage2TagStopsAtCritical(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityCritical, CriticalityTags: []string{"billing"}}, contract.ModeGreenfield)
	if r.CriticalityEffective != contract.CriticalityCritical || r.Track != contract.TrackN2 {
		t.Fatalf("got %s track %s, want critical and N=2", r.CriticalityEffective, r.Track)
	}
	if !reasonHas(r, "criticality_tags billing: already critical") {
		t.Fatalf("reasons %v do not say the tag found it already critical", r.Reasons)
	}
}

// TestStage2NoTagsKeepsDeclaredCriticality: without tags the declared level is untouched (no regression).
func TestStage2NoTagsKeepsDeclaredCriticality(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityLow}, contract.ModeGreenfield)
	if r.CriticalityEffective != contract.CriticalityLow || r.Track != contract.TrackN1 || r.DualSpecRequired {
		t.Fatalf("low without tags changed: %+v", r)
	}
	if reasonHas(r, "criticality_tags") {
		t.Fatalf("reasons mention tags without any: %v", r.Reasons)
	}
}

// TestStage2TagsAndCriticalPathsRaiseIndependently: each source raises one step, so a tag and a touched critical path
// together take low to high. Each rule stays in reasons.
func TestStage2TagsAndCriticalPathsRaiseIndependently(t *testing.T) {
	in := contract.Intent{Criticality: contract.CriticalityLow, CriticalityTags: []string{"security"}, CriticalPaths: []string{"src/auth/**"}}
	r := route(&in, contract.ModeGreenfield, []string{"src/auth/login.py"}, false)
	if r.CriticalityEffective != contract.CriticalityHigh || r.Track != contract.TrackN2 {
		t.Fatalf("got %s track %s, want high and N=2", r.CriticalityEffective, r.Track)
	}
	if !reasonHas(r, "criticality_tags security") || !reasonHas(r, "touches critical_paths") {
		t.Fatalf("reasons %v miss one of the two raises", r.Reasons)
	}
}

// TestStage2TagInLiteStaysN1: lite mode is always N=1 without dual-spec, whatever the tags, but the effective level
// still reports the tag.
func TestStage2TagInLiteStaysN1(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityLow, CriticalityTags: []string{"auth"}}, contract.ModeLite)
	if r.Track != contract.TrackN1 || r.DualSpecRequired || r.CriticalityEffective != contract.CriticalityMedium {
		t.Fatalf("lite with tag: %+v", r)
	}
}

// TestStage2DuplicateTagsRaiseOnceAndNameEachTag: a repeated tag raises one level and the reason names it once.
func TestStage2DuplicateTagsRaiseOnceAndNameEachTag(t *testing.T) {
	r := routeOfIntent(contract.Intent{Criticality: contract.CriticalityLow, CriticalityTags: []string{"security", "security"}}, contract.ModeGreenfield)
	if r.CriticalityEffective != contract.CriticalityMedium {
		t.Fatalf("duplicate tag gave %q, want medium", r.CriticalityEffective)
	}
	if !reasonHas(r, "criticality_tags security: goes up to medium") || reasonHas(r, "security, security") {
		t.Fatalf("reasons %v: the tag must be named once", r.Reasons)
	}
}

// TestStage2TagOnTouchedCriticalPathAtHigh: high plus a touched critical path goes to critical; the tag then finds it
// already critical, and both reasons are kept in order.
func TestStage2TagOnTouchedCriticalPathAtHigh(t *testing.T) {
	in := contract.Intent{Criticality: contract.CriticalityHigh, CriticalityTags: []string{"auth"}, CriticalPaths: []string{"src/auth/**"}}
	r := route(&in, contract.ModeGreenfield, []string{"src/auth/login.py"}, false)
	if r.CriticalityEffective != contract.CriticalityCritical || r.Track != contract.TrackN2 || !r.LemmasRequired {
		t.Fatalf("got %s track %s lemmas %v, want critical, N=2 and lemmas", r.CriticalityEffective, r.Track, r.LemmasRequired)
	}
	path := indexOfReason(r, "touches critical_paths (src/auth/login.py): goes up to critical")
	tag := indexOfReason(r, "criticality_tags auth: already critical")
	if path < 0 || tag < 0 || path > tag {
		t.Fatalf("reasons %v: want the path raise to critical, then the tag found it already critical", r.Reasons)
	}
}

// TestStage2TagWithExpectedProbesNamesBoth: the touched files are the files the S* probes name (expected=true), and the
// reason says so next to the tag reason.
func TestStage2TagWithExpectedProbesNamesBoth(t *testing.T) {
	in := contract.Intent{Criticality: contract.CriticalityLow, CriticalityTags: []string{"security"}, CriticalPaths: []string{"src/auth/**"}}
	r := route(&in, contract.ModeGreenfield, []string{"src/auth/login.py"}, true)
	if !reasonHas(r, "the S* probes expect to touch critical_paths") || !reasonHas(r, "criticality_tags security") {
		t.Fatalf("reasons %v miss the expected-probe or the tag reason", r.Reasons)
	}
	if r.CriticalityEffective != contract.CriticalityHigh {
		t.Fatalf("got %q, want high", r.CriticalityEffective)
	}
}

// TestStage2LemmasRequiredOnlyInN2: lemmas_required is true exactly where the track is N=2, and false in lite mode even
// with a tag that would raise the level.
func TestStage2LemmasRequiredOnlyInN2(t *testing.T) {
	cases := []struct {
		name string
		in   contract.Intent
		mode string
		want bool
	}{
		{"low N=1", contract.Intent{Criticality: contract.CriticalityLow}, contract.ModeGreenfield, false},
		{"high N=2", contract.Intent{Criticality: contract.CriticalityHigh}, contract.ModeGreenfield, true},
		{"medium plus tag is high N=2", contract.Intent{Criticality: contract.CriticalityMedium, CriticalityTags: []string{"auth"}}, contract.ModeRefactor, true},
		{"lite with tags stays N=1", contract.Intent{Criticality: contract.CriticalityHigh, CriticalityTags: []string{"auth"}}, contract.ModeLite, false},
	}
	for _, c := range cases {
		r := routeOfIntent(c.in, c.mode)
		if (r.Track == contract.TrackN2) != c.want || r.LemmasRequired != c.want {
			t.Errorf("%s: track %s lemmas_required %v, want N=2 and lemmas %v", c.name, r.Track, r.LemmasRequired, c.want)
		}
	}
}

// TestRaiseForTags: the helper raises once for any non-empty tag set, dedupes, sorts, and returns "" without tags.
func TestRaiseForTags(t *testing.T) {
	if lvl, why := raiseForTags(1, nil); lvl != 1 || why != "" {
		t.Fatalf("no tags: level %d reason %q, want unchanged and empty", lvl, why)
	}
	if lvl, why := raiseForTags(1, []string{"billing", "auth", "billing"}); lvl != 2 || why != "criticality_tags auth, billing: goes up to high" {
		t.Fatalf("level %d reason %q", lvl, why)
	}
	if lvl, why := raiseForTags(3, []string{"auth"}); lvl != 3 || why != "criticality_tags auth: already critical" {
		t.Fatalf("critical: level %d reason %q", lvl, why)
	}
}

func indexOfReason(r contract.Route, part string) int {
	for i, s := range r.Reasons {
		if strings.Contains(s, part) {
			return i
		}
	}
	return -1
}

func reasonHas(r contract.Route, part string) bool {
	for _, s := range r.Reasons {
		if strings.Contains(s, part) {
			return true
		}
	}
	return false
}
