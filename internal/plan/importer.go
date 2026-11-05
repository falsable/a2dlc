package plan

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// The importers turn another tool's task list into a candidate plan (RFC-0008 section 2): they propose, the plan gates
// judge, exactly as with the planner role. An importer never invents what the source does not say: a task that names no
// file gets no owns (G7 then rejects it, naming the task), and the edges are the source's own.

// Imported is a candidate plan and what the importer could not take from the source, for the person.
type Imported struct {
	Plan  contract.Plan
	Notes []string
}

// pathRe finds file paths in task text: a path with a directory and an extension, or a bare file name with a known
// source extension. Template placeholders ("[name]") and URLs are not paths.
var pathRe = regexp.MustCompile("(?:^|[\\s`'\"(])((?:[A-Za-z0-9_.@-]+/)+[A-Za-z0-9_.@-]*\\.[A-Za-z0-9]+|[A-Za-z0-9_-]+\\.(?:py|ts|tsx|js|jsx|go|rs|java|kt|cs|rb|php|md|json|ya?ml|toml|sql|css|scss|html|sh))")

func pathsIn(text string) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range pathRe.FindAllStringSubmatch(text, -1) {
		p := strings.TrimRight(m[1], ".,;:)")
		if strings.ContainsAny(p, "[]") || strings.Contains(text, "://"+p) || strings.HasPrefix(p, "../") || strings.HasPrefix(p, "/") {
			continue
		}
		p = strings.TrimPrefix(p, "./")
		if p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

var wordRe = regexp.MustCompile(`[A-Za-z][A-Za-z0-9]{3,}`)

func words(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordRe.FindAllString(strings.ToLower(s), -1) {
		out[w] = true
	}
	return out
}

// assignCovers gives each acceptance criterion of the feature to the slice whose text shares the most words of four
// letters or more with it (ties to the earlier slice). A criterion no slice shares a word with stays uncovered, and G5
// names it: the person assigns it.
func assignCovers(p *contract.Plan, texts []string, acceptance []string) []string {
	var notes []string
	for i := range p.Slices {
		p.Slices[i].Covers = []int{}
	}
	for c, crit := range acceptance {
		cw := words(crit)
		best, score := -1, 0
		for i, t := range texts {
			n := 0
			for w := range words(t) {
				if cw[w] {
					n++
				}
			}
			if n > score {
				best, score = i, n
			}
		}
		if best < 0 {
			notes = append(notes, fmt.Sprintf("acceptance criterion %d (%q) matches no task: assign it with covers", c, crit))
			continue
		}
		p.Slices[best].Covers = append(p.Slices[best].Covers, c)
	}
	return notes
}

func newSlice(feature contract.Intent, id, goal string, owns, deps []string, acceptance string, share float64) contract.PlanSlice {
	if owns == nil {
		owns = []string{}
	}
	if deps == nil {
		deps = []string{}
	}
	return contract.PlanSlice{SliceID: feature.SliceID + "." + id, Goal: goal, Owns: owns, DependsOn: deps, Criticality: feature.Criticality,
		Budget: contract.PlanBudget{MaxCostUSD: share}, Acceptance: []string{acceptance}, Invariants: []string{}, Covers: []int{}}
}

// share is each slice's part of the feature's cost cap, rounded down to the cent so they never add up to more; 1 USD
// each without a cap (G6 is then not checked).
func share(feature contract.Intent, n int) float64 {
	if n == 0 || feature.Budget == nil || feature.Budget.MaxCostUSD == nil {
		return 1
	}
	s := float64(int(*feature.Budget.MaxCostUSD/float64(n)*100)) / 100
	if s <= 0 {
		s = 0.01
	}
	return s
}

var (
	specTaskRe  = regexp.MustCompile(`^\s*[-*] \[[ xX]\] (T\d+)\s+(.*)$`)
	specPhaseRe = regexp.MustCompile(`^#{2,3}\s+Phase\s+[0-9N]+\s*:?\s*(.*)$`)
	specMarkRe  = regexp.MustCompile(`^\[([^\]]+)\]\s*`)
	specDepsRe  = regexp.MustCompile(`(?i)\(depends on ([T0-9,\s and]+)\)`)
	specIDRe    = regexp.MustCompile(`T\d+`)
)

type specTask struct {
	id, text, phase string
	parallel        bool
	deps            []string
}

// ImportSpecKit reads a Spec Kit tasks.md: `- [ ] T001 [P] [US1] Description with exact/file/paths.py`, grouped under
// `## Phase N: ...` headings. One slice per task. Edges: the phases' order (Setup, then Foundational, then the user
// stories, which do not depend on one another, then Polish; any other phase after the one before it); inside a phase,
// a task without [P] after every earlier task of the phase and a [P] task after the phase's earlier tasks without [P];
// and "(depends on T012, T013)". A task already checked ([x]) is done and is left out, with its edges.
func ImportSpecKit(src []byte, feature contract.Intent) (Imported, error) {
	var tasks []specTask
	done := map[string]bool{}
	phase := ""
	sc := bufio.NewScanner(bytes.NewReader(src))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if m := specPhaseRe.FindStringSubmatch(line); m != nil {
			phase = strings.TrimSpace(m[1])
			continue
		}
		m := specTaskRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		t := specTask{id: m[1], phase: phase}
		rest := m[2]
		for {
			mk := specMarkRe.FindStringSubmatch(rest)
			if mk == nil {
				break
			}
			if mk[1] == "P" {
				t.parallel = true
			}
			rest = rest[len(mk[0]):]
		}
		t.text = strings.TrimSpace(rest)
		if d := specDepsRe.FindStringSubmatch(t.text); d != nil {
			t.deps = specIDRe.FindAllString(d[1], -1)
		}
		if strings.Contains(line, "[x]") || strings.Contains(line, "[X]") {
			done[t.id] = true
		}
		tasks = append(tasks, t)
	}
	if len(tasks) == 0 {
		return Imported{}, fmt.Errorf("no task lines (- [ ] T001 ...) in the file")
	}
	kind := func(ph string) string {
		l := strings.ToLower(ph)
		switch {
		case strings.Contains(l, "setup"):
			return "setup"
		case strings.Contains(l, "foundational"):
			return "foundational"
		case strings.Contains(l, "user story"):
			return "story"
		case strings.Contains(l, "polish"):
			return "polish"
		}
		return "other"
	}
	var phases []string
	byPhase := map[string][]specTask{}
	for _, t := range tasks {
		if _, ok := byPhase[t.phase]; !ok {
			phases = append(phases, t.phase)
		}
		byPhase[t.phase] = append(byPhase[t.phase], t)
	}
	ids := func(ts []specTask) []string {
		var out []string
		for _, t := range ts {
			out = append(out, t.id)
		}
		return out
	}
	// what each phase starts after
	after := map[string][]string{}
	var setup, foundational, stories, previous []string
	for _, ph := range phases {
		ts := byPhase[ph]
		switch kind(ph) {
		case "setup":
			after[ph] = nil
			setup = append(setup, ids(ts)...)
		case "foundational":
			after[ph] = setup
			foundational = append(foundational, ids(ts)...)
		case "story":
			if len(foundational) > 0 {
				after[ph] = foundational
			} else {
				after[ph] = setup
			}
			stories = append(stories, ids(ts)...)
		case "polish":
			after[ph] = append(append(append([]string{}, setup...), foundational...), stories...)
		default:
			after[ph] = previous
		}
		previous = ids(ts)
	}
	p := contract.Plan{SchemaVersion: 1, FeatureID: feature.SliceID, Shared: []string{}}
	var notes, texts []string
	live := 0
	for _, t := range tasks {
		if !done[t.id] {
			live++
		}
	}
	each := share(feature, live)
	for _, ph := range phases {
		var earlier, earlierSerial []string
		for _, t := range byPhase[ph] {
			deps := map[string]bool{}
			for _, d := range after[ph] {
				deps[d] = true
			}
			inPhase := earlier
			if t.parallel {
				inPhase = earlierSerial
			}
			for _, d := range inPhase {
				deps[d] = true
			}
			for _, d := range t.deps {
				deps[d] = true
			}
			earlier = append(earlier, t.id)
			if !t.parallel {
				earlierSerial = append(earlierSerial, t.id)
			}
			if done[t.id] {
				continue
			}
			var dl []string
			for d := range deps {
				if !done[d] && d != t.id {
					dl = append(dl, feature.SliceID+"."+strings.ToLower(d))
				}
			}
			sort.Strings(dl)
			owns := pathsIn(t.text)
			if len(owns) == 0 {
				notes = append(notes, fmt.Sprintf("%s names no file: Spec Kit asks for exact paths in each task; add them to owns", t.id))
			}
			p.Slices = append(p.Slices, newSlice(feature, strings.ToLower(t.id), t.text, owns, dl, t.text, each))
			texts = append(texts, t.text)
		}
	}
	notes = append(notes, assignCovers(&p, texts, feature.Acceptance)...)
	return Imported{Plan: p, Notes: notes}, nil
}

type tmTask struct {
	ID           json.RawMessage `json:"id"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	Details      string          `json:"details"`
	TestStrategy string          `json:"testStrategy"`
	Status       string          `json:"status"`
	Dependencies []any           `json:"dependencies"`
	Subtasks     []tmTask        `json:"subtasks"`
}

func (t tmTask) text() string {
	parts := []string{t.Title, t.Description, t.Details, t.TestStrategy}
	for _, s := range t.Subtasks {
		parts = append(parts, s.text())
	}
	return strings.Join(parts, "\n")
}

func rawID(r json.RawMessage) string {
	var n json.Number
	if err := json.Unmarshal(r, &n); err == nil {
		return n.String()
	}
	var s string
	json.Unmarshal(r, &s)
	return s
}

// ImportTaskMaster reads a Task Master tasks.json: the tagged format ({"<tag>": {"tasks": [...]}}, tag "master" when
// tag is empty) or the older one ({"tasks": [...]}). One slice per top-level task, its subtasks folded into it (their
// text and files count as the task's); done and cancelled tasks are left out, with their edges; a dependency on a
// subtask ("3.2") is a dependency on its task. owns are the paths the task's text names: Task Master has no file field.
func ImportTaskMaster(src []byte, feature contract.Intent, tag string) (Imported, error) {
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(src, &doc); err != nil {
		return Imported{}, fmt.Errorf("not a tasks.json: %w", err)
	}
	raw, ok := doc["tasks"]
	if !ok {
		if tag == "" {
			tag = "master"
		}
		var tagged struct {
			Tasks json.RawMessage `json:"tasks"`
		}
		if b, ok := doc[tag]; !ok {
			var tags []string
			for k := range doc {
				tags = append(tags, k)
			}
			sort.Strings(tags)
			return Imported{}, fmt.Errorf("no tag %q in tasks.json (tags: %s)", tag, strings.Join(tags, ", "))
		} else if err := json.Unmarshal(b, &tagged); err != nil || tagged.Tasks == nil {
			return Imported{}, fmt.Errorf("tag %q has no tasks", tag)
		}
		raw = tagged.Tasks
	}
	var tasks []tmTask
	if err := json.Unmarshal(raw, &tasks); err != nil {
		return Imported{}, fmt.Errorf("tasks: %w", err)
	}
	skip := map[string]bool{}
	live := 0
	for _, t := range tasks {
		if t.Status == "done" || t.Status == "cancelled" {
			skip[rawID(t.ID)] = true
		} else {
			live++
		}
	}
	if live == 0 {
		return Imported{}, fmt.Errorf("every task is done or cancelled: nothing to plan")
	}
	p := contract.Plan{SchemaVersion: 1, FeatureID: feature.SliceID, Shared: []string{}}
	var notes, texts []string
	each := share(feature, live)
	for _, t := range tasks {
		id := rawID(t.ID)
		if skip[id] {
			continue
		}
		deps := map[string]bool{}
		for _, d := range t.Dependencies {
			var s string
			switch v := d.(type) {
			case float64:
				s = strconv.Itoa(int(v))
			case string:
				s = strings.SplitN(v, ".", 2)[0]
			}
			if s != "" && s != id && !skip[s] {
				deps[feature.SliceID+".t"+s] = true
			}
		}
		var dl []string
		for d := range deps {
			dl = append(dl, d)
		}
		sort.Strings(dl)
		text := t.text()
		owns := pathsIn(text)
		if len(owns) == 0 {
			notes = append(notes, fmt.Sprintf("task %s (%q) names no file: Task Master has no file field; add them to owns", id, t.Title))
		}
		acc := t.TestStrategy
		if strings.TrimSpace(acc) == "" {
			acc = t.Description
		}
		if strings.TrimSpace(acc) == "" {
			acc = t.Title
		}
		goal := t.Title
		if t.Description != "" {
			goal += ": " + t.Description
		}
		p.Slices = append(p.Slices, newSlice(feature, "t"+id, goal, owns, dl, acc, each))
		texts = append(texts, text)
	}
	notes = append(notes, assignCovers(&p, texts, feature.Acceptance)...)
	return Imported{Plan: p, Notes: notes}, nil
}
