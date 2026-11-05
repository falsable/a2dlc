package app

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
)

// Background level 1 (the agent's feedback loop). The Stop hook runs at the end of every agent turn and must not make it
// wait, so it runs level 0 and never blocks on work that is still running (owner's decision of 2026-10-27). What it adds
// here is the answer of a level 1 check the hook started in the background on an earlier turn, once that answer exists
// and is about the tree the agent has now:
//
//   - no answer for the current tree, and none running: start `a2dlc check --background` detached and allow the stop;
//   - an answer for the current tree that is not PASS: tell the agent what it can fix by itself (lines no test executed,
//     tests without teeth, counterexamples, INCONCLUSIVE targets) through the Stop hook's block, at most once per tree and
//     at most agent_hook.max_blocks times per session; what needs a person (signatures, protected paths) is never a block.
//
// The background check writes .a2dlc/reports/background.json, never .a2dlc/verdict.json: the receipt that verify reads
// stays the one the agent's own `a2dlc check` wrote at "done". Nothing here changes what verify, the pre-push or CI decide.
const (
	pathBackground     = pathReports + "/background.json"
	pathBackgroundLock = pathReports + "/background.lock"
	pathBackgroundLog  = pathReports + "/background.log"
	pathHookFeedback   = pathReports + "/hook_feedback.json"
	// backgroundEnv set to "off" turns the background check off (a machine that cannot spare the engines' memory while
	// the agent works).
	backgroundEnv = "A2DLC_BACKGROUND"
	// backgroundStale is when a lock left by a background check that died stops counting as running.
	backgroundStale  = 35 * time.Minute
	maxBlocksDefault = 4
)

// spawnBackground starts `a2dlc check --background` in dir, detached from the hook (spawn_*.go). Tests replace it.
var spawnBackground = startDetached

type backgroundLock struct {
	Tree    string `json:"tree"`
	Started string `json:"started"`
}

type hookFeedback struct {
	Session string `json:"session"`
	Blocks  int    `json:"blocks"`
	Tree    string `json:"tree"` // the last tree the agent was told about
}

func bareTree(h string) string { return strings.TrimPrefix(h, "git-tree:") }

// BackgroundReport is CheckOptions.ReportPath for `a2dlc check --background`.
func BackgroundReport() string { return pathBackground }

// BackgroundDone removes the lock a background check holds; the command calls it whatever the check's outcome.
func (a App) BackgroundDone() { os.Remove(a.path(pathBackgroundLock)) }

// backgroundStep is what the Stop hook adds to level 0: a reason to give the agent (non-empty means block the stop with
// it), or "" to allow. Every error is an allow: the background loop is help, never a gate.
func (a App) backgroundStep(session string) string {
	if os.Getenv(backgroundEnv) == "off" || os.Getenv(warmEnv) != "" {
		// The warm tree does not support two checks of one repo at once (warm.go).
		return ""
	}
	tree, err := a.Repo.WorktreeTree()
	if err != nil {
		return ""
	}
	var v contract.Verdict
	if a.read("", pathBackground, &v) == nil && bareTree(v.TreeHash) == tree {
		return a.feedbackFor(session, tree, v)
	}
	if a.backgroundRunning() {
		return ""
	}
	if a.write(pathBackgroundLock, backgroundLock{Tree: tree, Started: a.Now().UTC().Format(time.RFC3339)}) != nil {
		return ""
	}
	if err := spawnBackground(a.Repo.Dir, a.path(pathBackgroundLog)); err != nil {
		a.BackgroundDone()
	}
	return ""
}

func (a App) backgroundRunning() bool {
	var l backgroundLock
	if a.read("", pathBackgroundLock, &l) != nil {
		return false
	}
	t, err := time.Parse(time.RFC3339, l.Started)
	return err == nil && a.Now().Sub(t) < backgroundStale
}

func (a App) maxBlocks() int {
	base, err := a.Repo.ResolveCommit(a.defaultBase())
	if err != nil {
		return maxBlocksDefault
	}
	cfg, err := a.baseConfig(base, "")
	if err != nil || cfg.AgentHook == nil || cfg.AgentHook.MaxBlocks < 1 {
		return maxBlocksDefault
	}
	return cfg.AgentHook.MaxBlocks
}

func (a App) feedbackFor(session, tree string, v contract.Verdict) string {
	if v.Verdict == contract.PASS {
		return ""
	}
	items := feedbackItems(v)
	if len(items) == 0 {
		return ""
	}
	var fb hookFeedback
	if a.read("", pathHookFeedback, &fb) != nil || fb.Session != session {
		fb = hookFeedback{Session: session}
	}
	if fb.Tree == tree || fb.Blocks >= a.maxBlocks() {
		return ""
	}
	fb.Blocks++
	fb.Tree = tree
	if a.write(pathHookFeedback, fb) != nil {
		return ""
	}
	return fmt.Sprintf("a2dlc (background check of the current tree, %s): fix what you can before you finish:\n- %s\n"+
		"Then run `a2dlc check`. A behavior change you made on purpose needs a waiver in .a2dlc/waivers.json, which a person signs; say so instead of changing the code back.",
		v.Verdict, strings.Join(items, "\n- "))
}

// feedbackItems are the findings of v the agent can resolve without a person, one line each, in a stable order.
func feedbackItems(v contract.Verdict) []string {
	var out []string
	for _, e := range escalations(v) {
		switch e.Kind {
		case "uncovered_change":
			out = append(out, fmt.Sprintf("%s: lines %s changed and no test executed them; write a test that runs them",
				strings.TrimSuffix(e.Target, "::*"), ranges(e.Evidence.Lines)))
		case "test_without_teeth", "check_failed":
			out = append(out, fmt.Sprintf("%s: %s", e.Target, e.Summary))
		}
	}
	for _, t := range v.Targets {
		if t.Waived || t.Note != "" {
			continue
		}
		switch {
		case t.Verdict == contract.FAIL && t.Counterexample != nil:
			out = append(out, fmt.Sprintf("%s: behavior changed: args %s gave %s before and %s now; restore it if the change was not intended",
				t.Target, t.Counterexample.Args, t.Counterexample.Old, t.Counterexample.New))
		case t.Verdict == contract.INCONCLUSIVE:
			out = append(out, fmt.Sprintf("%s: not decided (%s)", t.Target, t.Reason))
		}
	}
	sort.Strings(out)
	return out
}

// markBlock is a JSON Stop-hook answer that keeps the agent working, with reason as what it is told.
func markBlock(reason string) map[string]string {
	return map[string]string{"decision": "block", "reason": reason}
}
