// Package plan judges a feature plan (RFC-0008 section 3) before any Builder runs. Every rule is deterministic: it reads
// the plan, the feature's intent and the base tree's file list, and nothing else. A Planner, a person or an imported tool
// (Spec Kit, Task Master) proposes the plan; this package decides whether it can be run.
package plan

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/verdict"
)

// Rule is one gate of RFC-0008 section 3.
type Rule struct {
	ID, Title string
}

// Rules are the gates, in the order they are judged.
var Rules = []Rule{
	{"G1", "shape: unique slice ids, known dependencies, covers within the feature's acceptance criteria"},
	{"G2", "no dependency cycle"},
	{"G3", "slices that can run at the same time own no file in common"},
	{"G4", "a file two slices own is declared in shared and owned only by slices on one dependency path"},
	{"G5", "every acceptance criterion of the feature is covered by a slice"},
	{"G6", "the slices' budgets add up to no more than the feature's"},
	{"G7", "the intent derived for each slice is a valid intent"},
	{"G8", "a slice reaching a file another slice's code imports depends on it (blast radius)"},
}

// Status of a rule in a report.
const (
	Pass       = "pass"
	Fail       = "fail"
	NotChecked = "not_checked"
)

// Result is what one rule found.
type Result struct {
	Rule     string   `json:"rule"`
	Status   string   `json:"status"`
	Findings []string `json:"findings,omitempty"`
	Note     string   `json:"note,omitempty"`
}

// Report is the judgment of a plan: Valid only when no rule failed. Waves is the order the slices could run in: each wave
// depends only on earlier ones (empty when there is a cycle).
type Report struct {
	FeatureID string     `json:"feature_id"`
	Valid     bool       `json:"valid"`
	Results   []Result   `json:"results"`
	Waves     [][]string `json:"waves"`
}

// Check judges p against the feature's intent and the files of the base tree.
func Check(p contract.Plan, feature contract.Intent, baseFiles []string) Report {
	r := Report{FeatureID: p.FeatureID, Waves: [][]string{}}
	g := newGraph(p)
	add := func(rule string, findings []string, note string) {
		st := Pass
		if len(findings) > 0 {
			st = Fail
		} else if note != "" && strings.HasPrefix(note, "not checked") {
			st = NotChecked
		}
		r.Results = append(r.Results, Result{Rule: rule, Status: st, Findings: findings, Note: note})
	}

	add("G1", shape(p, feature, g), "")
	waves, cycle := g.waves()
	if len(cycle) > 0 {
		add("G2", []string{"these slices depend on each other in a cycle: " + strings.Join(cycle, ", ")}, "")
	} else {
		add("G2", nil, "")
		r.Waves = waves
	}
	reach := g.reach()
	files := universe(p, baseFiles)
	add("G3", disjoint(p, reach, files), "")
	add("G4", sharedOrdered(p, reach, files), "")
	add("G5", coverage(p, feature), "")
	findings, note := budget(p, feature)
	add("G6", findings, note)
	add("G7", sliceIntents(p, feature), "")
	add("G8", nil, "not checked: it needs the engines' deps_graph, which `a2dlc plan check` does not run yet")

	r.Valid = true
	for _, x := range r.Results {
		if x.Status == Fail {
			r.Valid = false
		}
	}
	return r
}

type graph struct {
	ids   []string
	index map[string]int
	deps  [][]int // deps[i]: the slices i depends on
}

func newGraph(p contract.Plan) graph {
	g := graph{index: map[string]int{}}
	for i, s := range p.Slices {
		if _, dup := g.index[s.SliceID]; !dup {
			g.index[s.SliceID] = i
		}
		g.ids = append(g.ids, s.SliceID)
	}
	g.deps = make([][]int, len(p.Slices))
	for i, s := range p.Slices {
		for _, d := range s.DependsOn {
			if j, ok := g.index[d]; ok && j != i {
				g.deps[i] = append(g.deps[i], j)
			}
		}
	}
	return g
}

// waves is a topological order in levels (Kahn), each level sorted by slice id; with a cycle it returns the slices left.
func (g graph) waves() ([][]string, []string) {
	n := len(g.ids)
	indeg := make([]int, n)
	users := make([][]int, n)
	for i, ds := range g.deps {
		indeg[i] = len(ds)
		for _, d := range ds {
			users[d] = append(users[d], i)
		}
	}
	var out [][]string
	done := 0
	var level []int
	for i := 0; i < n; i++ {
		if indeg[i] == 0 {
			level = append(level, i)
		}
	}
	for len(level) > 0 {
		var names []string
		var next []int
		for _, i := range level {
			names = append(names, g.ids[i])
			done++
			for _, u := range users[i] {
				if indeg[u]--; indeg[u] == 0 {
					next = append(next, u)
				}
			}
		}
		sort.Strings(names)
		out = append(out, names)
		level = next
	}
	if done == n {
		return out, nil
	}
	var left []string
	for i := 0; i < n; i++ {
		if indeg[i] > 0 {
			left = append(left, g.ids[i])
		}
	}
	sort.Strings(left)
	return nil, left
}

// reach[i][j]: slice i depends on slice j, directly or through others.
func (g graph) reach() [][]bool {
	n := len(g.ids)
	r := make([][]bool, n)
	for i := range r {
		r[i] = make([]bool, n)
		for _, d := range g.deps[i] {
			r[i][d] = true
		}
	}
	for k := 0; k < n; k++ {
		for i := 0; i < n; i++ {
			if r[i][k] {
				for j := 0; j < n; j++ {
					if r[k][j] {
						r[i][j] = true
					}
				}
			}
		}
	}
	return r
}

func ordered(reach [][]bool, i, j int) bool { return reach[i][j] || reach[j][i] }

func shape(p contract.Plan, feature contract.Intent, g graph) []string {
	var bad []string
	if p.FeatureID != feature.SliceID {
		bad = append(bad, fmt.Sprintf("the plan is for %q and the intent for %q", p.FeatureID, feature.SliceID))
	}
	seen := map[string]bool{}
	for _, s := range p.Slices {
		if seen[s.SliceID] {
			bad = append(bad, fmt.Sprintf("slice id %s appears more than once", s.SliceID))
		}
		seen[s.SliceID] = true
		if !strings.HasPrefix(s.SliceID, p.FeatureID+".") {
			bad = append(bad, fmt.Sprintf("slice %s does not start with the feature id %s.", s.SliceID, p.FeatureID))
		}
		for _, d := range s.DependsOn {
			if d == s.SliceID {
				bad = append(bad, fmt.Sprintf("slice %s depends on itself", s.SliceID))
			} else if _, ok := g.index[d]; !ok {
				bad = append(bad, fmt.Sprintf("slice %s depends on %s, which is not in the plan", s.SliceID, d))
			}
		}
		for _, c := range s.Covers {
			if c >= len(feature.Acceptance) {
				bad = append(bad, fmt.Sprintf("slice %s covers acceptance criterion %d, and the feature has %d", s.SliceID, c, len(feature.Acceptance)))
			}
		}
	}
	return bad
}

// universe is every path an owns glob can be judged against: the base tree's files and every literal path a plan names
// (a file the plan will create).
func universe(p contract.Plan, baseFiles []string) []string {
	set := map[string]bool{}
	for _, f := range baseFiles {
		set[f] = true
	}
	for _, s := range p.Slices {
		for _, o := range s.Owns {
			if !isGlob(o) {
				set[o] = true
			}
		}
	}
	for _, o := range p.Shared {
		if !isGlob(o) {
			set[o] = true
		}
	}
	out := make([]string, 0, len(set))
	for f := range set {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

func isGlob(s string) bool { return strings.ContainsAny(s, "*?[{") }

// staticPrefix is the part of a glob before its first segment with a wildcard.
func staticPrefix(glob string) string {
	segs := strings.Split(glob, "/")
	var keep []string
	for _, s := range segs {
		if isGlob(s) {
			break
		}
		keep = append(keep, s)
	}
	return strings.Join(keep, "/")
}

func underDir(dir, path string) bool {
	return dir == "" || path == dir || strings.HasPrefix(path, dir+"/")
}

// overlap returns the files both glob lists match. When two globs match no file of the universe but could match the same
// file that does not exist yet (one's fixed directory contains the other's), it reports that too: the gate is
// conservative, because a plan accepted with two concurrent slices on one future file is the failure it exists to prevent.
func overlap(a, b []string, files []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range files {
		if matchAny(a, f) && matchAny(b, f) && !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	for _, x := range a {
		for _, y := range b {
			if !isGlob(x) || !isGlob(y) {
				continue
			}
			px, py := staticPrefix(x), staticPrefix(y)
			if underDir(px, py) || underDir(py, px) {
				k := x + " and " + y + " (globs that can match the same new file)"
				if !seen[k] {
					seen[k] = true
					out = append(out, k)
				}
			}
		}
	}
	return out
}

func matchAny(globs []string, path string) bool {
	for _, g := range globs {
		if verdict.Match(g, path) {
			return true
		}
	}
	return false
}

func disjoint(p contract.Plan, reach [][]bool, files []string) []string {
	var bad []string
	for i := range p.Slices {
		for j := i + 1; j < len(p.Slices); j++ {
			if ordered(reach, i, j) {
				continue
			}
			if both := overlap(p.Slices[i].Owns, p.Slices[j].Owns, files); len(both) > 0 {
				bad = append(bad, fmt.Sprintf("%s and %s can run at the same time and both own %s: give the file to one of them, or make one depend on the other",
					p.Slices[i].SliceID, p.Slices[j].SliceID, strings.Join(both, ", ")))
			}
		}
	}
	return bad
}

func sharedOrdered(p contract.Plan, reach [][]bool, files []string) []string {
	var bad []string
	for i := range p.Slices {
		for j := i + 1; j < len(p.Slices); j++ {
			if !ordered(reach, i, j) {
				continue // G3 judges these
			}
			for _, f := range overlap(p.Slices[i].Owns, p.Slices[j].Owns, files) {
				if strings.Contains(f, " and ") {
					continue // a glob-level overlap between ordered slices is allowed: they never run at once
				}
				if !matchAny(p.Shared, f) {
					bad = append(bad, fmt.Sprintf("%s and %s both own %s, which is not in shared: declare it there", p.Slices[i].SliceID, p.Slices[j].SliceID, f))
				}
			}
		}
	}
	for _, sh := range p.Shared {
		var owners []int
		for i, s := range p.Slices {
			if len(overlap([]string{sh}, s.Owns, files)) > 0 {
				owners = append(owners, i)
			}
		}
		for a := 0; a < len(owners); a++ {
			for b := a + 1; b < len(owners); b++ {
				if !ordered(reach, owners[a], owners[b]) {
					bad = append(bad, fmt.Sprintf("shared %s is owned by %s and %s, which are not on one dependency path", sh, p.Slices[owners[a]].SliceID, p.Slices[owners[b]].SliceID))
				}
			}
		}
	}
	return bad
}

func coverage(p contract.Plan, feature contract.Intent) []string {
	covered := map[int]bool{}
	for _, s := range p.Slices {
		for _, c := range s.Covers {
			covered[c] = true
		}
	}
	var bad []string
	for i, a := range feature.Acceptance {
		if !covered[i] {
			bad = append(bad, fmt.Sprintf("no slice covers acceptance criterion %d: %q", i, a))
		}
	}
	return bad
}

func budget(p contract.Plan, feature contract.Intent) ([]string, string) {
	sum := 0.0
	for _, s := range p.Slices {
		sum += s.Budget.MaxCostUSD
	}
	if feature.Budget == nil || feature.Budget.MaxCostUSD == nil {
		return nil, fmt.Sprintf("not checked: the feature's intent has no cost cap (the slices add up to %.2f USD)", sum)
	}
	if limit := *feature.Budget.MaxCostUSD; sum > limit+1e-9 {
		return []string{fmt.Sprintf("the slices add up to %.2f USD and the feature's cap is %.2f USD", sum, limit)}, ""
	}
	return nil, fmt.Sprintf("the slices add up to %.2f of %.2f USD", sum, *feature.Budget.MaxCostUSD)
}

// SliceIntent is the intent a2dlc run gets for one slice of the plan: the slice's goal, invariants, criticality and budget,
// the acceptance criteria it covers, the feature's mode, non-goals, critical paths and inner-loop limits, and the slice's
// owns as boundary.allowed_paths.
func SliceIntent(s contract.PlanSlice, feature contract.Intent) contract.Intent {
	in := contract.Intent{SchemaVersion: contract.SchemaVersion, SliceID: s.SliceID, Mode: feature.Mode, Goal: s.Goal,
		Invariants: append(append([]string{}, feature.Invariants...), s.Invariants...), NonGoals: append([]string{}, feature.NonGoals...),
		Criticality: s.Criticality, CriticalPaths: append([]string{}, feature.CriticalPaths...), Acceptance: append([]string{}, s.Acceptance...),
		ChangeKind: feature.ChangeKind}
	for _, c := range s.Covers {
		if c < len(feature.Acceptance) {
			in.Acceptance = append(in.Acceptance, feature.Acceptance[c])
		}
	}
	cost := s.Budget.MaxCostUSD
	b := contract.IntentBudget{MaxCostUSD: &cost, MaxInnerIterations: 5, MaxTier1AdversarialRounds: 3}
	if feature.Budget != nil {
		if feature.Budget.MaxInnerIterations > 0 {
			b.MaxInnerIterations = feature.Budget.MaxInnerIterations
		}
		if feature.Budget.MaxTier1AdversarialRounds > 0 {
			b.MaxTier1AdversarialRounds = feature.Budget.MaxTier1AdversarialRounds
		}
	}
	in.Budget = &b
	// The slice may change only what it owns (RFC-0008 section 1): Stage 3 enforces it as CB-04 (boundary.allowed_paths).
	in.Boundary = &contract.Boundary{AllowedPaths: append([]string{}, s.Owns...)}
	return in
}

func sliceIntents(p contract.Plan, feature contract.Intent) []string {
	var bad []string
	for _, s := range p.Slices {
		in := SliceIntent(s, feature)
		b, err := json.Marshal(in)
		if err == nil {
			var back contract.Intent
			err = contract.DecodeStrictBytes(b, &back)
			if err == nil {
				err = back.Validate()
			}
		}
		if err != nil {
			bad = append(bad, fmt.Sprintf("the intent for %s is not valid: %v", s.SliceID, err))
		}
	}
	return bad
}

// Text renders the report for the person who approves the plan.
func (r Report) Text(p contract.Plan) string {
	var b strings.Builder
	byID := map[string]contract.PlanSlice{}
	for _, s := range p.Slices {
		byID[s.SliceID] = s
	}
	if r.Valid {
		fmt.Fprintf(&b, "Plan %s: %d slices, valid. They would run in %d step(s):\n", r.FeatureID, len(p.Slices), len(r.Waves))
	} else {
		fmt.Fprintf(&b, "Plan %s: %d slices, NOT valid. Nothing runs until these are fixed:\n", r.FeatureID, len(p.Slices))
		for _, x := range r.Results {
			for _, f := range x.Findings {
				fmt.Fprintf(&b, "  - [%s] %s\n", x.Rule, f)
			}
		}
		if len(r.Waves) > 0 {
			b.WriteString("Order, once fixed:\n")
		}
	}
	for i, w := range r.Waves {
		together := ""
		if len(w) > 1 {
			together = " (at the same time)"
		}
		fmt.Fprintf(&b, "  %d.%s\n", i+1, together)
		for _, id := range w {
			s := byID[id]
			fmt.Fprintf(&b, "     %s: %s Changes %s.", id, s.Goal, strings.Join(s.Owns, ", "))
			if len(s.DependsOn) > 0 {
				fmt.Fprintf(&b, " After %s.", strings.Join(s.DependsOn, ", "))
			}
			fmt.Fprintf(&b, " Up to %.2f USD.\n", s.Budget.MaxCostUSD)
		}
	}
	for _, x := range r.Results {
		if x.Status == NotChecked {
			fmt.Fprintf(&b, "Not checked [%s]: %s\n", x.Rule, strings.TrimPrefix(x.Note, "not checked: "))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
