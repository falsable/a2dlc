package run

import "strings"

// noteTier0NotRun is the note of the stage when Tier 0 ran nothing on the candidate: no engine had files
// or probes to review. The candidate still passes (Tier 0 found nothing to say), but with everything listed as not
// reviewed and described with the reason, so that nobody reads it as a reviewed tree. It does not change the verdict of Stage 4: its
// coverage precondition already leaves INCONCLUSIVE what no engine examined. Empty if Tier 0 did run something.
func noteTier0NotRun(reviewed bool, unreviewed []string) string {
	if reviewed {
		return ""
	}
	if len(unreviewed) == 0 {
		return "Tier 0 ran nothing: there were no changed files or probes"
	}
	return "Tier 0 ran nothing: " + strings.Join(unreviewed, ", ")
}
