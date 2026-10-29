package factory

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/run"
)

// Deps is what a run needs from outside the package: the target repository, the launcher that starts role containers,
// and how to build the app (engines, test runner) for a lane's worktree.
type Deps struct {
	Repo         gitx.Repo
	Launcher     run.Launcher
	NewApp       func(gitx.Repo) app.App
	Now          func() time.Time
	Out          string // <out>/<run id>/ gets the lanes' worktrees, intents and the report
	Base         string // the commit lanes start from; empty = HEAD
	ConfigSHA256 string
}

// Precheck says why an issue is not a ticket, or "" if it is. An issue without the command that decides it is done
// cannot be accepted or rejected, so it never reaches a model.
func Precheck(is contract.FactoryIssue) string {
	if strings.TrimSpace(is.DoneCommand) == "" {
		return "no done_command: not a ticket"
	}
	return ""
}

func sliceID(issueID, runID string) string {
	return issueID + "." + strings.TrimPrefix(runID, "run-")[:6]
}

// intentFor is the lane's intent: the issue as the goal, its done command as the acceptance, lite mode, low criticality.
func intentFor(is contract.FactoryIssue, slice string) contract.Intent {
	return contract.Intent{SchemaVersion: 1, SliceID: slice, Mode: contract.ModeLite, Goal: is.Title + "\n\n" + is.Body,
		Invariants: []string{}, NonGoals: []string{}, Criticality: contract.CriticalityLow, CriticalPaths: []string{},
		Acceptance: []string{is.DoneCommand},
		Budget:     &contract.IntentBudget{MaxInnerIterations: run.MaxInnerDefault, MaxTier1AdversarialRounds: 1}}
}

// testRule is the one sentence the lane's goal adds to the issue (bead a2dlc-3fu.18): the acceptance condition the
// testGuard enforces, stated to the builder because nothing else tells it, so that a builder that appends its test to
// the repo's only test file does not lose a paid lane. It reads the same globs the guard does, and both arms get it
// because both arms' lanes go through runLane. A repo that declares no config.tests.paths has no rule to state.
func testRule(globs []string) string {
	if len(globs) == 0 {
		return ""
	}
	return "Existing test files matching config.tests.paths (" + strings.Join(globs, ", ") + ") must not be modified or deleted; new tests go in new files."
}

// laneIntent is intentFor with the lane's acceptance rule appended to the goal, as its own paragraph.
func laneIntent(is contract.FactoryIssue, slice string, globs []string) contract.Intent {
	it := intentFor(is, slice)
	if rule := testRule(globs); rule != "" {
		it.Goal += "\n\n" + rule
	}
	return it
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func notRun(issue, status, reason string) contract.FactoryLane {
	return contract.FactoryLane{IssueID: issue, Status: status, Reason: reason, Stages: []contract.StageUsage{}}
}
