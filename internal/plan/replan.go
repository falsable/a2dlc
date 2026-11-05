package plan

import (
	"fmt"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// ReplanRules judge a replan (RFC-0008 section 5, P5): the slice old.<id> whose Builders were exhausted is replaced by
// sub-slices that split its work and nothing else changes. They run before the plan gates (Check) judge the new plan.
var ReplanRules = []Rule{
	{"R1", "only the exhausted slice changes: it is gone, every other slice is as it was except for the edges that pointed at it"},
	{"R2", "the sub-slices own a partition of what the exhausted slice owned: nothing outside it, and every file of it"},
	{"R3", "the sub-slices depend only on what the exhausted slice depended on, or on each other"},
	{"R4", "a slice that depended on the exhausted slice depends on every sub-slice no other sub-slice depends on"},
}

// CheckReplan judges next as a replan of slice id in old, over the files of the base tree. It returns one Result per
// rule; a replan is acceptable when none failed and Check(next) is valid.
func CheckReplan(old, next contract.Plan, id string, baseFiles []string) []Result {
	var x *contract.PlanSlice
	oldBy := map[string]contract.PlanSlice{}
	for i, s := range old.Slices {
		oldBy[s.SliceID] = s
		if s.SliceID == id {
			x = &old.Slices[i]
		}
	}
	results := make([]Result, len(ReplanRules))
	for i, r := range ReplanRules {
		results[i] = Result{Rule: r.ID, Status: Pass}
	}
	fail := func(rule int, f string, a ...any) {
		results[rule].Status = Fail
		results[rule].Findings = append(results[rule].Findings, fmt.Sprintf(f, a...))
	}
	if x == nil {
		fail(0, "the plan has no slice %s", id)
		return results
	}
	var subs []contract.PlanSlice
	subIDs := map[string]bool{}
	for _, s := range next.Slices {
		if _, ok := oldBy[s.SliceID]; !ok {
			subs = append(subs, s)
			subIDs[s.SliceID] = true
		}
	}
	if len(subs) < 2 {
		fail(0, "%s is replaced by %d new slice(s): a replan splits it into two or more", id, len(subs))
	}
	if next.FeatureID != old.FeatureID {
		fail(0, "the feature id changed from %s to %s", old.FeatureID, next.FeatureID)
	}
	// sinks: sub-slices no other sub-slice depends on; a dependent of x must see all of them
	dependedOn := map[string]bool{}
	for _, s := range subs {
		for _, d := range s.DependsOn {
			dependedOn[d] = true
		}
	}
	var sinks []string
	for _, s := range subs {
		if !dependedOn[s.SliceID] {
			sinks = append(sinks, s.SliceID)
		}
	}
	sort.Strings(sinks)
	seen := map[string]bool{}
	for _, s := range next.Slices {
		o, existed := oldBy[s.SliceID]
		if !existed {
			continue
		}
		seen[s.SliceID] = true
		if s.SliceID == id {
			fail(0, "%s is still in the plan", id)
			continue
		}
		// R1: the same slice except for the edge to x, replaced by sub-slices
		wantDeps := map[string]bool{}
		pointedAtX := false
		for _, d := range o.DependsOn {
			if d == id {
				pointedAtX = true
				continue
			}
			wantDeps[d] = true
		}
		gotOld := map[string]bool{}
		var gotSubs []string
		for _, d := range s.DependsOn {
			if subIDs[d] {
				gotSubs = append(gotSubs, d)
			} else {
				gotOld[d] = true
			}
		}
		same := s
		same.DependsOn = o.DependsOn
		if Definition(same) != Definition(o) {
			fail(0, "%s changed besides its edges", s.SliceID)
		}
		if !sameKeys(wantDeps, gotOld) {
			fail(0, "%s changed its dependencies on slices other than %s", s.SliceID, id)
		}
		if !pointedAtX && len(gotSubs) > 0 {
			fail(0, "%s did not depend on %s and now depends on %s", s.SliceID, id, strings.Join(gotSubs, ", "))
		}
		// R4
		if pointedAtX {
			have := map[string]bool{}
			for _, d := range gotSubs {
				have[d] = true
			}
			var missing []string
			for _, k := range sinks {
				if !have[k] {
					missing = append(missing, k)
				}
			}
			if len(missing) > 0 {
				fail(3, "%s depended on %s and does not depend on %s, so it would run without part of its work", s.SliceID, id, strings.Join(missing, ", "))
			}
		}
	}
	for _, o := range old.Slices {
		if o.SliceID != id && !seen[o.SliceID] {
			fail(0, "%s was dropped", o.SliceID)
		}
	}
	// R2: inside x's owns, and covering it
	for _, s := range subs {
		for _, g := range s.Owns {
			if !within(g, x.Owns) {
				fail(1, "%s owns %s, which %s did not own", s.SliceID, g, id)
			}
		}
	}
	for _, f := range baseFiles {
		if !matchAny(x.Owns, f) {
			continue
		}
		owned := false
		for _, s := range subs {
			owned = owned || matchAny(s.Owns, f)
		}
		if !owned {
			fail(1, "%s owned %s and no sub-slice owns it", id, f)
		}
	}
	for _, g := range x.Owns {
		if isGlob(g) {
			continue
		}
		owned := false
		for _, s := range subs {
			owned = owned || matchAny(s.Owns, g)
		}
		if !owned {
			fail(1, "%s owned %s and no sub-slice owns it", id, g)
		}
	}
	// R3
	allowed := map[string]bool{}
	for _, d := range x.DependsOn {
		allowed[d] = true
	}
	for _, s := range subs {
		for _, d := range s.DependsOn {
			if !allowed[d] && !subIDs[d] {
				fail(2, "%s depends on %s, which %s did not depend on", s.SliceID, d, id)
			}
		}
	}
	return results
}

// within says whether every file glob g can match is one owns matches: a path that owns matches, or a glob whose fixed
// directory lies under a glob of owns that ends in /** (or that is the same glob).
func within(g string, owns []string) bool {
	if !isGlob(g) {
		return matchAny(owns, g)
	}
	for _, o := range owns {
		if g == o {
			return true
		}
		if strings.HasSuffix(o, "/**") && underDir(strings.TrimSuffix(o, "/**"), staticPrefix(g)) {
			return true
		}
	}
	return false
}

func sameKeys(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if !b[k] {
			return false
		}
	}
	return true
}
