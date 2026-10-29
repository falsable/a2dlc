package factory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/run"
)

// Model triage (bead a2dlc-3fu.15, owner decision 2026-10-29, PREREG Q6). When the config has `triage`, every issue that
// passes precheck gets one model call before any lane starts. The call sees the issue's title and body and nothing else
// (no repository: its workdir is empty), and answers build or triaged_out. A triaged_out issue gets no lane. The call
// goes through the arm's own launcher: arm minimal pins a cheap model and no tools, arm control is default claude -p with
// the config's model, because the control carries none of the factory's tricks. Its spend goes through the same
// accountant as a lane's, so the caps bound it, and into the report's triage, so cost per accepted change counts it.

const triagePrompt = `You triage the issues of a software factory. Decide from the issue text alone, without reading or changing any file, whether the issue is a change a developer can implement and check with the project's tests (build), or whether it is too vague to implement, a question, or not a change to the code (triaged_out).
Reply with payload.summary equal to exactly one word, build or triaged_out, files [] and payload.files_changed [].

Issue %s: %s

%s`

// triageConfig is the config the triage calls run under: arm control as it is; arm minimal with the pinned model and no
// tools at all.
func triageConfig(cfg contract.FactoryConfig) contract.FactoryConfig {
	if cfg.Arm == contract.ArmControl {
		return cfg
	}
	t := cfg
	t.Model = cfg.Triage.Model
	t.Tools = []string{}
	return t
}

// laneCounters is what the accountant has recorded for one id so far.
func (a *accountant) laneCounters(id string) contract.Counters {
	a.mu.Lock()
	defer a.mu.Unlock()
	return *a.lane(id)
}

// runTriage triages the issues that passed precheck, in the batch's order. An issue it could not triage (a call that
// failed, a cap, an answer that is neither word) is build, with the reason, so triage never hides an issue; once the run
// is killed it stops, and the lanes' admission deals with the kill.
func runTriage(ctx context.Context, d Deps, cfg contract.FactoryConfig, acct *accountant, runID string, batch contract.FactoryBatch) *contract.TriageReport {
	tcfg := triageConfig(cfg)
	rep := &contract.TriageReport{Model: tcfg.Model, Issues: []contract.TriageIssue{}}
	role := LaneRoles(tcfg).Roles[Role]
	for _, is := range batch.Issues {
		if Precheck(is) != "" {
			continue
		}
		if killed, _ := acct.wasKilled(); killed || ctx.Err() != nil {
			break
		}
		choice, why := triageOne(ctx, d, tcfg, acct, role, runID, is)
		it := contract.TriageIssue{IssueID: is.ID, Choice: choice, Reason: why, Usage: acct.laneCounters(triageLane(is.ID))}
		rep.Usage.Plus(it.Usage)
		rep.Issues = append(rep.Issues, it)
	}
	return rep
}

func triageLane(issue string) string { return "triage-" + issue }

func triageOne(ctx context.Context, d Deps, cfg contract.FactoryConfig, acct *accountant, role contract.Role, runID string, is contract.FactoryIssue) (choice, why string) {
	base := filepath.Join(d.Out, runID, "triage", is.ID)
	in, out, work := filepath.Join(base, "in"), filepath.Join(base, "out"), filepath.Join(base, "work")
	for _, dir := range []string{in, out, work} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return contract.TriageBuild, "triage setup: " + err.Error()
		}
	}
	slice := sliceID(triageLane(is.ID), runID)
	intent := intentFor(contract.FactoryIssue{ID: is.ID, Title: is.Title, Body: is.Body}, slice)
	intent.Goal = fmt.Sprintf(triagePrompt, is.ID, is.Title, is.Body)
	intent.Acceptance = []string{}
	intentJSON, _ := json.Marshal(intent)
	payload, _ := json.Marshal(contract.BuilderRequest{Intent: intentJSON, Contract: json.RawMessage("null"), Probes: []contract.Probe{}, Round: 1, MaxRounds: 1})
	wd := "/work"
	req := contract.RoleRequest{SchemaVersion: 1, RunID: runID, SliceID: slice, Mode: contract.ModeLite, Role: Role, Stage: "3", Attempt: 1,
		Inputs: []contract.HashedFile{}, Workdir: &wd, Payload: payload}
	rb, _ := json.MarshalIndent(req, "", " ")
	if err := os.WriteFile(filepath.Join(in, "request.json"), append(rb, '\n'), 0o644); err != nil {
		return contract.TriageBuild, "triage setup: " + err.Error()
	}
	cctx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Caps.LaneSeconds)*time.Second)
	defer cancel()
	launcher := capLauncher{inner: d.Launcher, acct: acct, lane: triageLane(is.ID), cfg: cfg}
	_, err := launcher.Launch(cctx, role, run.LaunchSpec{Role: Role, Stage: "3", Attempt: 1, InDir: in, OutDir: out, WorkDir: work,
		Name: "a2dlc-" + runID + "-triage-" + is.ID})
	var capErr *CapError
	switch {
	case errors.As(err, &capErr):
		return contract.TriageBuild, "triage not run: " + capErr.Reason
	case err != nil:
		return contract.TriageBuild, "triage call failed: " + err.Error()
	}
	return triageAnswer(out, runID)
}

// triageAnswer reads the choice from the role's answer. Only the exact word counts (case and surrounding quotes or a
// full stop aside); anything else is build, with the reason.
func triageAnswer(outDir, runID string) (choice, why string) {
	if why := agentResponseProblem(outDir, runID); why != "" {
		return contract.TriageBuild, "triage answer unusable: " + why
	}
	b, _ := os.ReadFile(filepath.Join(outDir, "response.json"))
	var resp contract.RoleResponse
	var ans contract.BuilderResponse
	if contract.DecodeStrictBytes(b, &resp) != nil || json.Unmarshal(resp.Payload, &ans) != nil {
		return contract.TriageBuild, "triage answer unusable: no builder payload"
	}
	switch word := strings.Trim(strings.ToLower(strings.TrimSpace(ans.Summary)), " .`'\""); word {
	case contract.TriageBuild, contract.TriageTriagedOut:
		return word, ""
	}
	s := strings.TrimSpace(ans.Summary)
	if len(s) > 60 {
		s = s[:60] + "..."
	}
	return contract.TriageBuild, fmt.Sprintf("triage answer unusable: %q", s)
}
