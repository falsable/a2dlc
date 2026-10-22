package run

import (
	"context"
	"fmt"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
)

// Stage −1 (Make It Testable, §5) holds what fits in the binary: the hermetic build and the baseline (CB-05, §7), and in
// refactor and brownfield slices the golden master (decision 14): the black-box I/O recorded on the base before any role
// runs. Greenfield skips the stage (§4: there is no base behavior to preserve). Without tests.baseline_command the baseline
// is skipped with its reason (skipStale re-runs it if the config carries it on resume), and a declared golden master is
// still recorded; with it, the baseline is
// established on the base, in the sandbox without network, before spending a role token. If it is not achieved (no
// JUnit, no testcases, red without an identifiable test, image down without digest, not reproducible on a second run,
// timeout), CB-05 trips: the stage escalates with a prerequisite_failure and the run stops there, because no later gate
// would have anything to compare against.

// KindPrerequisiteFailure is the escalation kind of CB-05 (contracts/escalation.schema.json).
const KindPrerequisiteFailure = "prerequisite_failure"

// ArtBaseline is the artifact of Stage −1 (contracts/baseline.schema.json): the established baseline.
const ArtBaseline = "baseline.json"

// ArtGolden is the golden master recorded by Stage −1 (contracts/golden.schema.json). Stage 4 hands it to CheckWith as
// CheckOptions.Golden (judgeLoop, recordedGolden), so the golden_replay gate judges the candidate against it.
const ArtGolden = "golden.json"

// reasonWithoutBaseline is the skipped reason of Stage −1 when no baseline is configured; skipStale recognizes it on resume.
var reasonWithoutBaseline = app.ReasonNoBaselineCommand()

// reasonGreenfield is the skipped reason of Stage −1 in a greenfield slice (protocol §4).
const reasonGreenfield = "greenfield: Stage −1 is omitted (protocol §4): there is no base behavior to make testable or to preserve"

func stageMinusOne(ctx context.Context, m *Machine) (string, string, string, error) {
	if m.state.Mode == contract.ModeGreenfield {
		return contract.StageSkipped, reasonGreenfield, "", nil
	}
	lb, err := m.app.SetBaseline(ctx, m.baseSha)
	if err != nil {
		// config outside contractJSON or binary without runner: it is the tool, not the repo; the run does not start
		return "", "", "", err
	}
	if !lb.Configured {
		// The golden master does not need the baseline command: a declared one is recorded before any role runs, so a
		// refactor without baseline_command is not left without its oracle without anyone saying so.
		golden, err := m.goldenRecord(ctx)
		if err != nil {
			return "", "", "", err
		}
		if golden.escalated {
			return golden.status, golden.reason, golden.artifact, nil
		}
		if golden.recorded {
			return contract.StageDone, fmt.Sprintf("no tests.baseline_command: no baseline to enforce; golden master of %d cases recorded on the base (%s)", golden.cases, ArtGolden), golden.artifact, nil
		}
		return contract.StageSkipped, reasonWithoutBaseline + ": no baseline to enforce", "", nil
	}
	if lb.Reason != "" {
		return m.prerequisiteFailure("baseline", fmt.Sprintf("the baseline could not be established on base %s with tests.baseline_command: %s", shortSHA(m.baseSha), lb.Reason),
			lb.Reason, "the baseline")
	}
	golden, err := m.goldenRecord(ctx)
	if err != nil {
		return "", "", "", err
	}
	if golden.escalated {
		return golden.status, golden.reason, golden.artifact, nil
	}
	note := ""
	if lb.Cached {
		note = ", from the cache"
	}
	artifact, err := m.writeArtifact(ArtBaseline, contract.Baseline{SchemaVersion: contract.SchemaVersion, SliceID: m.state.SliceID, Base: lb.BaseSha,
		Command: lb.Command, Image: lb.Image, Tests: lb.Tests, PreexistingRed: lb.Reds, Cached: lb.Cached, ImageDigest: lb.ImageDigest,
		Reruns: lb.Reruns, Stable: lb.Stable})
	if err != nil {
		return "", "", "", err
	}
	goldenNote := "no golden master (not a refactor or brownfield slice, or none configured)"
	if golden.recorded {
		goldenNote = fmt.Sprintf("golden master of %d cases recorded on the base (%s)", golden.cases, ArtGolden)
	}
	return contract.StageDone, fmt.Sprintf("hermetic baseline on %s%s, %d tests, %d preexisting reds (they do not block: Stage 4 compares against them), reproduced on %d runs; %s; no codemod seaming (declared out, README)",
		shortSHA(m.baseSha), note, lb.Tests, len(lb.Reds), lb.Reruns, goldenNote), artifact, nil
}

// goldenOutcome is what the golden step of Stage −1 left: recorded, or escalated with its stage result.
type goldenOutcome struct {
	recorded  bool
	cases     int
	escalated bool
	status    string
	reason    string
	artifact  string
}

// goldenRecord records the golden master when the slice is refactor or brownfield and config.tests.golden_master is
// declared; otherwise it does nothing. A golden master that cannot be recorded is CB-05 as well: a refactor without its
// oracle cannot be judged, and the run stops before any role runs.
func (m *Machine) goldenRecord(ctx context.Context) (goldenOutcome, error) {
	var out goldenOutcome
	if m.state.Mode != contract.ModeRefactor && m.state.Mode != contract.ModeBrownfield {
		return out, nil
	}
	cfg, err := m.app.Config()
	if err != nil {
		return out, err
	}
	if cfg.Tests == nil || cfg.Tests.GoldenMaster == nil {
		return out, nil
	}
	g, err := m.app.RecordGolden(ctx, m.baseSha)
	if err != nil {
		status, reason, artifact, err := m.prerequisiteFailure("golden_master", fmt.Sprintf("the golden master could not be recorded on base %s with tests.golden_master", shortSHA(m.baseSha)),
			err.Error(), "the golden master")
		if err != nil {
			return out, err
		}
		return goldenOutcome{escalated: true, status: status, reason: reason, artifact: artifact}, nil
	}
	g.SliceID = m.state.SliceID
	artifact, err := m.writeArtifact(ArtGolden, g)
	if err != nil {
		return out, err
	}
	out.recorded, out.cases, out.artifact = true, len(g.Cases), artifact
	return out, nil
}

// prerequisiteFailure escalates CB-05 with the given target suffix and returns the stage result (status, reason, artifact).
func (m *Machine) prerequisiteFailure(target, summary, evidence, what string) (string, string, string, error) {
	e := app.Escalation(KindPrerequisiteFailure, m.state.SliceID+"::"+target, "CB-05: "+summary+". Intervention in Stage −1: fix the command, the image or the suite; no role runs until then.", contract.Evidence{Message: evidence})
	artifact, err := m.escalate([]contract.Escalation{e})
	if err != nil {
		return "", "", "", err
	}
	return contract.StageEscalated, fmt.Sprintf("CB-05: %s could not be established (%s); see %s", what, evidence, ArtEscalations), artifact, nil
}

func shortSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
