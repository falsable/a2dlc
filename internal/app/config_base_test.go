package app

import (
	"context"
	"testing"
)

// Tier 0 judges with the base's config.tier0: a working-tree edit that adds a forbidden import is not sent to the engines,
// as check reads the config from the base.
func TestTier0JudgesWithTheBasesConfig(t *testing.T) {
	a, _, dir := newRepo(t)
	c := candidateOf(t, a, map[string]string{"src/lib/f.ts": "export const f = 2\n"})
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[{"language":"typescript","image":"fake","paths":["src/lib/**"],"budget":{}}],"tier0":{"forbidden_imports":["fs"]}}`)
	seenPolicy = nil
	if _, _, err := a.Tier0(context.Background(), "HEAD", c, nil); err != nil {
		t.Fatal(err)
	}
	if seenPolicy != nil {
		t.Fatalf("the working tree's tier0 policy reached the engines: %+v", seenPolicy)
	}
}

// The Stage -1 baseline runs the base's baseline_command: a working-tree edit of config.json does not change the command
// that is judged.
func TestSetBaselineRunsTheBasesCommand(t *testing.T) {
	a, _, _, dir := gatesRepo(t)
	write(t, dir, ".a2dlc/config.json", `{"schema_version":1,"engines":[],"tests":{"image":"fake","paths":["tests/**"],"command":"nop","baseline_command":"evil"}}`)
	out, err := a.SetBaseline(context.Background(), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if out.Command != "baseline" {
		t.Fatalf("the working tree's command was judged: %q", out.Command)
	}
}
