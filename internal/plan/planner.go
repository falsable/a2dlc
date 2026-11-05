package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/falsable/a2dlc/internal/contract"
	a2run "github.com/falsable/a2dlc/internal/run"
)

// maxPlannerFiles caps the base's file list in the request: a monorepo's full listing would not fit a prompt. The request
// says when the list was cut (files_truncated).
const maxPlannerFiles = 20000

// Planner asks the planner role (roles.json) for a plan and judges each answer with the plan gates (and, for a replan,
// the replan rules). A plan that breaks a rule goes back to the role with the findings, for at most Rounds rounds: the
// role proposes, a2dlc judges, as the Builder loop does with Tier 0.
type Planner struct {
	Role     contract.Role
	Launcher a2run.Launcher
	Rounds   int
	// Work is a folder for the calls' /in and /out (outside the repository); empty uses the system's temporary folder.
	Work string
}

// Proposal is what the planner ended with: the last plan it answered (nil if it never answered one), its report, the
// replan rules' results for a replan, the rounds it took and what the calls reported they cost.
type Proposal struct {
	Plan    *contract.Plan
	Report  Report
	Replan  []Result
	Rounds  int
	CostUSD float64
	Status  string // ok, or why the last call gave no plan (refused, timeout, invalid response)
	Reason  string
}

// Accepted says whether the proposal passes every gate (and every replan rule).
func (p Proposal) Accepted() bool {
	if p.Plan == nil || !p.Report.Valid {
		return false
	}
	for _, r := range p.Replan {
		if r.Status == Fail {
			return false
		}
	}
	return true
}

// Propose asks for a plan of feature over the base's files.
func (pl Planner) Propose(ctx context.Context, feature contract.Intent, baseSha string, files []string) (Proposal, error) {
	return pl.loop(ctx, feature, baseSha, files, nil)
}

// Replan asks for a plan in which slice id of current is split into sub-slices (CB-02b): reason is why its Builders
// were exhausted.
func (pl Planner) Replan(ctx context.Context, feature contract.Intent, baseSha string, files []string, current contract.Plan, id, reason string) (Proposal, error) {
	return pl.loop(ctx, feature, baseSha, files, &contract.PlannerReplan{Plan: current, SliceID: id, Reason: reason})
}

func (pl Planner) loop(ctx context.Context, feature contract.Intent, baseSha string, files []string, replan *contract.PlannerReplan) (Proposal, error) {
	rounds := pl.Rounds
	if rounds < 1 {
		rounds = 3
	}
	list, truncated := files, false
	if len(list) > maxPlannerFiles {
		list, truncated = list[:maxPlannerFiles], true
	}
	var out Proposal
	findings := []contract.PlannerFinding{}
	var previous *contract.Plan
	for round := 1; round <= rounds; round++ {
		out.Rounds = round
		req := contract.PlannerRequest{FeatureIntent: feature, Files: list, FilesTruncated: truncated, Round: round, MaxRounds: rounds,
			Findings: findings, PreviousPlan: previous, Replan: replan}
		p, status, reason, cost, err := pl.call(ctx, feature.SliceID, baseSha, round, req)
		out.CostUSD += cost
		if err != nil {
			return out, err
		}
		out.Status, out.Reason = status, reason
		if p == nil {
			if status == contract.RoleRefused {
				return out, nil // a refusal is the role's answer; asking again would not change it
			}
			continue
		}
		out.Plan, out.Report, out.Replan = p, Check(*p, feature, files), nil
		if replan != nil {
			out.Replan = CheckReplan(replan.Plan, *p, replan.SliceID, files)
		}
		if out.Accepted() {
			return out, nil
		}
		findings = plannerFindings(out.Report, out.Replan)
		previous = p
	}
	return out, nil
}

// plannerFindings are the failed rules of the gates and of the replan rules, as the next round's request carries them.
func plannerFindings(r Report, replan []Result) []contract.PlannerFinding {
	title := map[string]string{}
	for _, x := range append(append([]Rule{}, Rules...), ReplanRules...) {
		title[x.ID] = x.Title
	}
	out := []contract.PlannerFinding{}
	for _, res := range append(append([]Result{}, replan...), r.Results...) {
		if res.Status == Fail && len(res.Findings) > 0 {
			out = append(out, contract.PlannerFinding{Rule: res.Rule, Title: title[res.Rule], Findings: res.Findings})
		}
	}
	return out
}

// call makes one launch of the planner role and returns its plan when it answered one that decodes strictly.
func (pl Planner) call(ctx context.Context, feature, baseSha string, round int, req contract.PlannerRequest) (*contract.Plan, string, string, float64, error) {
	work, err := os.MkdirTemp(pl.Work, "a2dlc-planner-")
	if err != nil {
		return nil, "", "", 0, err
	}
	defer os.RemoveAll(work)
	inDir, outDir := filepath.Join(work, "in"), filepath.Join(work, "out")
	if err := os.MkdirAll(inDir, 0o755); err != nil {
		return nil, "", "", 0, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, "", "", 0, err
	}
	h := sha256.Sum256([]byte(fmt.Sprintf("%s\x00%s\x00%d\x00%v", feature, baseSha, round, req.Replan != nil)))
	runID := "run-" + hex.EncodeToString(h[:6])
	mode := req.FeatureIntent.Mode
	if mode == "" {
		mode = contract.ModeLite
	}
	rr, err := a2run.WriteRoleRequest(inDir, runID, feature, mode, a2run.PlannerRole, a2run.PlannerStage, req)
	if err != nil {
		return nil, "", "", 0, err
	}
	lr, err := pl.Launcher.Launch(ctx, pl.Role, a2run.LaunchSpec{Role: rr.Role, Stage: rr.Stage, Attempt: rr.Attempt,
		Name: fmt.Sprintf("a2dlc-planner-%s-%d", runID, round), InDir: inDir, OutDir: outDir})
	if err != nil {
		return nil, "", "", 0, fmt.Errorf("launch %s: %w", rr.Role, err)
	}
	if lr.TimedOut {
		return nil, "timeout", "the planner timed out", 0, nil
	}
	raw, err := os.ReadFile(filepath.Join(outDir, "response.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, "no response", fmt.Sprintf("the planner left no response.json (exit %d)", lr.Exit), 0, nil
	} else if err != nil {
		return nil, "", "", 0, err
	}
	resp, why := a2run.ValidateRoleResponse(raw, runID, a2run.PlannerRole, a2run.PlannerStage)
	cost := 0.0
	if resp.Cost != nil {
		cost = resp.Cost.USD
	}
	if why != "" {
		return nil, "invalid", why, cost, nil
	}
	reason := resp.Reason
	if resp.Status != contract.RoleOK {
		return nil, resp.Status, reason, cost, nil
	}
	var pr contract.PlannerResponse
	if err := contract.DecodeStrictBytes(resp.Payload, &pr); err != nil {
		return nil, "invalid", "payload does not match planner_response: " + err.Error(), cost, nil
	}
	return &pr.Plan, contract.RoleOK, reason, cost, nil
}
