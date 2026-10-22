package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// Protected boundary (CB-04, §7): what the intent declares off-limits to the builder. Touching it is not a Tier 0 failure
// that the builder fixes with another iteration: it is a privilege escalation that a person decides, by widening the
// boundary in the intent and re-running the run (no waiver resolves it: resolve_with stays empty). That is why the stage
// escalates as soon as the /work change crosses it, without running Tier 0 or granting another iteration.
//
// Detection is textual on purpose: the core knows nothing about languages. forbidden_paths: a changed file that matches
// a glob. do_not_refactor: an added or removed line in a changed file that contains the symbol as a whole word. That is
// why it also flags a new call to the symbol, not only its definition.

// readBoundary reads boundary from intent.json; nil if there is no intent or it does not declare a boundary.
func readBoundary(intent json.RawMessage) (*contract.Boundary, error) {
	if intent == nil {
		return nil, nil
	}
	var in struct {
		Boundary *contract.Boundary `json:"boundary"`
	}
	if err := json.Unmarshal(intent, &in); err != nil {
		return nil, fmt.Errorf("intent.json does not match the contract: %w", err)
	}
	return in.Boundary, nil
}

// reviewBoundary returns one protected_boundary escalation per crossing, without repeating the target: a changed file
// that matches forbidden_paths (target <file>::<glob>) and, one per do_not_refactor symbol, a file whose changed line
// contains it (target <file>::<symbol>). base is the run's commit and tree the candidate's; diffs are its changes.
func (m *Machine) reviewBoundary(base, tree string, diffs []string, b *contract.Boundary, role string) ([]contract.Escalation, error) {
	if b == nil {
		return nil, nil
	}
	var items []contract.Escalation
	seen := map[string]bool{}
	add := func(target, summary, msg string) {
		if seen[target] {
			return
		}
		seen[target] = true
		e := itemEscalation("protected_boundary", target, summary, msg)
		e.Builder = role
		items = append(items, e)
	}
	for _, f := range diffs {
		glob := ""
		for _, onDisk := range b.ForbiddenPaths {
			if matched, _ := app.Partition([]string{onDisk}, []string{f}); len(matched) > 0 {
				glob = onDisk
				break
			}
		}
		if glob == "" && len(b.DoNotRefactor) == 0 {
			continue
		}
		before, after, err := m.contents(base, tree, f)
		if err != nil {
			return nil, err
		}
		removed, added := differences(before, after)
		first := "changed file without distinct lines"
		if len(added) > 0 {
			first = "+ " + added[0]
		} else if len(removed) > 0 {
			first = "- " + removed[0]
		}
		if glob != "" {
			add(f+"::"+glob, fmt.Sprintf("The builder changed %s, which the intent declares out of scope (boundary.forbidden_paths). Revert the change, or a person widens the boundary in the intent and re-runs: raising privileges is their decision, not the builder's.", f), first)
		}
		for _, s := range b.DoNotRefactor {
			re := symbolPattern(s)
			for _, l := range append(append([]string{}, added...), removed...) {
				if !re.MatchString(l) {
					continue
				}
				prefix := "- "
				if containsLine(added, l) {
					prefix = "+ "
				}
				add(f+"::"+s, fmt.Sprintf("The builder changed a line of %s that contains %s, declared do_not_refactor in the intent. Revert the change, or a person widens the boundary in the intent and re-runs: raising privileges is their decision, not the builder's.", f, s), prefix+l)
				break
			}
		}
	}
	return items, nil
}

// contents reads a file from the base and from the candidate. A file that does not exist on one side (added or deleted)
// is empty.
func (m *Machine) contents(base, tree, path string) (before, after []byte, err error) {
	before, err = m.app.Repo.Show(base, path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	after, err = m.app.Repo.Show(tree, path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return before, after, nil
}

// differences returns the lines the candidate removes and the ones it adds, as a multiset difference: a line moved
// counts as removed and added. The order is that of each content.
func differences(before, after []byte) (removed, added []string) {
	a, d := lines(before), lines(after)
	count := map[string]int{}
	for _, l := range d {
		count[l]++
	}
	for _, l := range a {
		if count[l] > 0 {
			count[l]--
		} else {
			removed = append(removed, l)
		}
	}
	count = map[string]int{}
	for _, l := range a {
		count[l]++
	}
	for _, l := range d {
		if count[l] > 0 {
			count[l]--
		} else {
			added = append(added, l)
		}
	}
	return removed, added
}

// lines splits a content into lines, without the \r of CRLF.
func lines(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	lines := strings.Split(string(b), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimSuffix(l, "\r")
	}
	return lines
}

// symbolPattern matches the symbol as a whole word: not stuck to letters, digits or _. It is \b, but it also works for a
// symbol that starts or ends with a sign (such as "." or "?"), where \b does not match.
func symbolPattern(s string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^A-Za-z0-9_])` + regexp.QuoteMeta(s) + `(?:[^A-Za-z0-9_]|$)`)
}

func containsLine(lines []string, l string) bool {
	for _, x := range lines {
		if x == l {
			return true
		}
	}
	return false
}

// escalateBoundary records the builder's crossings in escalations.json and escalates the stage: the builder does not
// fix the boundary with another iteration, a person decides it.
func (m *Machine) escalateBoundary(c buildResult) (string, string, string, error) {
	artifact, err := m.escalate(c.boundary)
	return contract.StageEscalated, c.reason, artifact, err
}
