package app

import (
	"fmt"
	"regexp"
	"strings"
	"sync"

	"github.com/falsable/a2dlc/internal/junit"
)

// Failures that come from the environment the tests run in, not from the change (bead a2dlc-cgl). A new test that imports a
// package the test image does not have, or calls an executable it does not have, fails the same way on the base and on the
// candidate: it says nothing about the change, so the NOP gate answers INCONCLUSIVE and names the missing piece instead of
// blaming the candidate. What the change itself was to provide (a symbol, a module of the repo) is not environment.
//
// shortcut: the wording of each runner's missing-module and missing-executable errors is matched here, one pattern per
// ecosystem; upgrade to a field the engine or the runner reports when a language needs more than a message.
var (
	// python: ModuleNotFoundError: No module named 'x.y'
	missingPyModule = regexp.MustCompile(`No module named ['"]?([A-Za-z0-9_.]+)`)
	// node: Cannot find module 'x' / Cannot find package '@scope/x'
	missingNodeModule = regexp.MustCompile(`Cannot find (?:module|package) '([^']+)'`)
	// python: FileNotFoundError: [Errno 2] No such file or directory: 'node'. A bare name with no extension is how a
	// subprocess names an executable; a data file has a directory or an extension.
	missingPyExecutable = regexp.MustCompile(`No such file or directory: ['"]([^'"/\\.]+)['"]`)
	// go: exec: "node": executable file not found; node: spawn node ENOENT; sh: node: command not found; sh: 1: node: not found
	missingExecutable = regexp.MustCompile(`exec: "([^"/\\.]+)": executable file not found|spawn ([^\s/\\.]+) ENOENT|([A-Za-z0-9_+-]+): command not found|(?:sh|bash|dash): (?:\d+: )?([A-Za-z0-9_+-]+): not found`)
)

// missingFromEnvironment names what the text of a failure says is missing from the environment, or "". inRepo says whether
// a name is a directory or a file stem of the candidate's tree: a module that starts with one is the repo's own, and a test
// that cannot import it fails because of the change.
func missingFromEnvironment(text string, inRepo func(string) bool) string {
	if m := missingPyModule.FindStringSubmatch(text); m != nil {
		if top, _, _ := strings.Cut(m[1], "."); top != "" && !inRepo(top) {
			return fmt.Sprintf("module %q is not installed", m[1])
		}
		return ""
	}
	if m := missingNodeModule.FindStringSubmatch(text); m != nil {
		name := m[1]
		if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "/") || strings.HasPrefix(name, "node:") {
			return ""
		}
		top, rest, _ := strings.Cut(name, "/")
		if strings.HasPrefix(top, "@") {
			second, _, _ := strings.Cut(rest, "/")
			top += "/" + second
		}
		if !inRepo(top) && !inRepo(strings.TrimPrefix(top, "@")) {
			return fmt.Sprintf("package %q is not installed", name)
		}
		return ""
	}
	for _, re := range []*regexp.Regexp{missingPyExecutable, missingExecutable} {
		if m := re.FindStringSubmatch(text); m != nil {
			for _, name := range m[1:] {
				if name != "" {
					return fmt.Sprintf("executable %q is not on the PATH", name)
				}
			}
		}
	}
	return ""
}

// environmentCause is how the NOP gate asks of a red testcase whether the environment caused it: what is missing, or "". The
// names of the candidate's tree are read once, and only when a missing module asks; a tree that cannot be read counts every
// module as the repo's own, which keeps the failure a FAIL.
func (a App) environmentCause(in gateInput) func(junit.Case) string {
	var once sync.Once
	var names map[string]bool
	inRepo := func(name string) bool {
		once.Do(func() {
			files, err := a.Repo.Files(in.tree)
			if err != nil {
				return
			}
			names = map[string]bool{}
			for _, f := range files {
				parts := strings.Split(f, "/")
				for _, dir := range parts[:len(parts)-1] {
					names[dir] = true
				}
				names[fileStem(f)] = true
			}
		})
		return names == nil || names[name]
	}
	return func(c junit.Case) string { return missingFromEnvironment(c.Message+"\n"+c.Detail, inRepo) }
}
