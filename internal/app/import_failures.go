package app

import (
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// ReasonCouldNotImport is how an engine starts the reason of a target whose module it could not import (the Python
// engine writes "could not import: ModuleNotFoundError: No module named 'x'"). That is a problem of the engine image, not
// of the code: summaries count it apart from the INCONCLUSIVE of code the engine cannot judge (a2dlc-pe6.6).
const ReasonCouldNotImport = "could not import:"

var noModuleRe = regexp.MustCompile(`No module named '([^'.]+)`)

// CouldNotImport counts the targets of v that could not be imported and names the missing top-level modules, sorted.
func CouldNotImport(v contract.Verdict) (int, []string) {
	n := 0
	seen := map[string]bool{}
	for _, t := range v.Targets {
		if t.Verdict != contract.INCONCLUSIVE || !strings.HasPrefix(t.Reason, ReasonCouldNotImport) {
			continue
		}
		n++
		if m := noModuleRe.FindStringSubmatch(t.Reason); m != nil {
			seen[m[1]] = true
		}
	}
	mods := make([]string, 0, len(seen))
	for m := range seen {
		mods = append(mods, m)
	}
	sort.Strings(mods)
	return n, mods
}

// CouldNotImportNote is the line the check prints under the summary when some targets could not be imported: what is
// missing and the way out. "" when none.
func CouldNotImportNote(v contract.Verdict) string {
	n, mods := CouldNotImport(v)
	if n == 0 {
		return ""
	}
	what := "modules the engine image lacks"
	if len(mods) > 0 {
		what = "missing in the engine image: " + strings.Join(mods, ", ")
	}
	return "note: " + strconv.Itoa(n) + " target(s) could not import, which is the engine image missing the repo's dependencies and not code the engine cannot judge (" + what +
		"); run a2dlc install to derive an engine image from them, and a2dlc doctor to see what else is missing"
}
