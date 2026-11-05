package plan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/gitx"
)

// SliceRun is what running one slice ended in: a plan status (passed, escalated or failed), the candidate commit of a
// passed slice and the reason.
type SliceRun struct {
	Status    string
	Candidate string
	Reason    string
}

// SliceExec runs the slices of a plan. The real one is RunExec (exec.go): an `a2dlc run` per slice. Tests script it.
type SliceExec interface {
	// Run takes the slice from its intent (a file the runner wrote) to a candidate, on base.
	Run(ctx context.Context, s contract.PlanSlice, intentPath, base string) (SliceRun, error)
	// Verifies says whether a slice that passed in an earlier invocation still verifies (P4): its run is intact, on the
	// same base, with the same candidate.
	Verifies(s contract.PlanSlice, prev contract.PlanSliceState) bool
}

// FinalCheck judges the merge of every candidate against the feature's base (P6). ok is a PASS.
type FinalCheck func(ctx context.Context, base, merged string) (ok bool, reason string, err error)

// Runner is the plan machine of fsm.go over a real repository: state on disk, merges in git, slices through Exec.
type Runner struct {
	Repo  gitx.Repo
	Exec  SliceExec
	Final FinalCheck
}

// pathPlan is under .a2dlc/run, which gitx.Excluded keeps out of the tree_hash: the plan state and the final receipt are
// gate outputs. A slice id starts with a letter, so _plan is never a slice's run.
const pathPlan = ".a2dlc/run/_plan"

// StateDir is where a feature's plan run lives: state.json, intents/<slice>.json and final.json.
func StateDir(repoDir, feature string) string {
	return filepath.Join(repoDir, filepath.FromSlash(pathPlan), feature)
}

// Order is the order the plan machine visits slices in (P1): topological, each level sorted by slice id.
func Order(p contract.Plan) []string {
	w, _ := newGraph(p).waves()
	var out []string
	for _, level := range w {
		out = append(out, level...)
	}
	return out
}

// Run is one invocation of the plan machine over planBytes (a plan that passes its gates) from the feature's base
// commit. It returns the state it left on disk and the slices it ran, in order.
func (r Runner) Run(ctx context.Context, planBytes []byte, feature contract.Intent, base string) (contract.PlanState, []string, error) {
	var p contract.Plan
	if err := contract.DecodeStrictBytes(planBytes, &p); err != nil {
		return contract.PlanState{}, nil, fmt.Errorf("the plan does not match its contract: %w", err)
	}
	baseSha, err := r.Repo.ResolveCommit(base)
	if err != nil {
		return contract.PlanState{}, nil, fmt.Errorf("base %q: %w", base, err)
	}
	files, err := r.Repo.Files(baseSha)
	if err != nil {
		return contract.PlanState{}, nil, err
	}
	if rep := Check(p, feature, files); !rep.Valid {
		return contract.PlanState{}, nil, fmt.Errorf("the plan does not pass its gates; run a2dlc plan check:\n%s", rep.Text(p))
	}
	sum := sha256.Sum256(planBytes)
	planSHA := hex.EncodeToString(sum[:])
	dir := StateDir(r.Repo.Dir, p.FeatureID)
	st, err := r.load(dir, p, planSHA, baseSha)
	if err != nil {
		return st, nil, err
	}
	st.Invocation++
	bySlice := map[string]contract.PlanSlice{}
	for _, s := range p.Slices {
		bySlice[s.SliceID] = s
	}
	idx := map[string]int{}
	for i, s := range st.Slices {
		idx[s.SliceID] = i
	}
	order := Order(p)
	reran := map[string]bool{}
	var ran []string
	for _, id := range order {
		s, cur := bySlice[id], &st.Slices[idx[id]]
		var notPassed []string
		depReran := false
		for _, d := range s.DependsOn {
			ds := st.Slices[idx[d]]
			if ds.Status != contract.PlanPassed {
				notPassed = append(notPassed, d+" ("+ds.Status+")")
			}
			depReran = depReran || reran[d]
		}
		if len(notPassed) > 0 { // P3
			*cur = contract.PlanSliceState{SliceID: id, Status: contract.PlanBlocked, FinishedIn: cur.FinishedIn,
				Reason: "blocked by " + strings.Join(notPassed, ", ")}
			continue
		}
		if cur.Status == contract.PlanPassed && !depReran && r.Exec.Verifies(s, *cur) { // P4
			continue
		}
		ran, reran[id] = append(ran, id), true
		res, sliceBase, err := r.runSlice(ctx, dir, p, feature, s, st, idx, order, baseSha)
		if err != nil {
			return st, ran, err
		}
		*cur = contract.PlanSliceState{SliceID: id, Status: res.Status, FinishedIn: st.Invocation, Reason: res.Reason}
		if sliceBase != "" {
			cur.Base = &sliceBase
		}
		if res.Status == contract.PlanPassed {
			c := res.Candidate
			cur.Candidate = &c
		}
		if err := save(dir, st); err != nil { // an interrupted invocation keeps what it finished
			return st, ran, err
		}
	}
	if err := r.finish(ctx, dir, &st, order, idx, baseSha); err != nil {
		return st, ran, err
	}
	return st, ran, save(dir, st)
}

// load reads the state of an earlier invocation, or starts one. A state for another plan or base is an error: the
// person decides whether to discard it.
func (r Runner) load(dir string, p contract.Plan, planSHA, baseSha string) (contract.PlanState, error) {
	path := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		st := contract.PlanState{SchemaVersion: 1, FeatureID: p.FeatureID, PlanSHA256: planSHA, BaseSha: baseSha}
		for _, s := range p.Slices {
			st.Slices = append(st.Slices, contract.PlanSliceState{SliceID: s.SliceID, Status: contract.PlanPending})
		}
		return st, nil
	}
	if err != nil {
		return contract.PlanState{}, err
	}
	var st contract.PlanState
	if err := contract.DecodeStrictBytes(b, &st); err != nil {
		return st, fmt.Errorf("%s does not match the contract: %w", path, err)
	}
	if st.PlanSHA256 != planSHA {
		return st, fmt.Errorf("%s belongs to another version of the plan (sha256 %s…); to start over with this one, move %s aside", path, st.PlanSHA256[:12], dir)
	}
	if st.BaseSha != baseSha {
		return st, fmt.Errorf("%s is on base %s, not %s; to start over on this base, move %s aside", path, st.BaseSha[:8], baseSha[:8], dir)
	}
	return st, nil
}

// runSlice computes the slice's base (P2), writes its intent and runs it. A base whose dependencies do not merge is an
// escalation of this slice: no Builder runs on a base that does not exist.
func (r Runner) runSlice(ctx context.Context, dir string, p contract.Plan, feature contract.Intent, s contract.PlanSlice,
	st contract.PlanState, idx map[string]int, order []string, baseSha string) (SliceRun, string, error) {
	deps := map[string]bool{}
	for _, d := range s.DependsOn {
		deps[d] = true
	}
	var commits []string
	for _, id := range order {
		if deps[id] {
			commits = append(commits, *st.Slices[idx[id]].Candidate)
		}
	}
	sliceBase, err := r.Repo.MergeCommits(baseSha, commits, "a2dlc plan: base of "+s.SliceID)
	if errors.Is(err, gitx.ErrMergeConflict) {
		return SliceRun{Status: contract.PlanEscalated, Reason: "the slices it depends on do not merge: " + err.Error()}, "", nil
	}
	if err != nil {
		return SliceRun{}, "", err
	}
	if err := r.Repo.UpdateRef("refs/a2dlc/plan/"+p.FeatureID+"/base/"+s.SliceID, sliceBase); err != nil {
		return SliceRun{}, "", err
	}
	in := SliceIntent(s, feature)
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return SliceRun{}, "", err
	}
	intentPath := filepath.Join(dir, "intents", s.SliceID+".json")
	if err := os.MkdirAll(filepath.Dir(intentPath), 0o755); err != nil {
		return SliceRun{}, "", err
	}
	if err := os.WriteFile(intentPath, append(b, '\n'), 0o644); err != nil {
		return SliceRun{}, "", err
	}
	res, err := r.Exec.Run(ctx, s, intentPath, sliceBase)
	if err != nil {
		if ctx.Err() != nil {
			return SliceRun{}, "", err
		}
		// one slice that cannot run fails alone; its siblings go on (P3)
		return SliceRun{Status: contract.PlanFailed, Reason: "the run did not finish: " + err.Error()}, sliceBase, nil
	}
	if res.Status == contract.PlanPassed && res.Candidate == "" {
		res = SliceRun{Status: contract.PlanFailed, Reason: "the run passed without a candidate commit"}
	}
	return res, sliceBase, nil
}

// finish merges every candidate, runs the final check when every slice passed, and sets the outcome (P6).
func (r Runner) finish(ctx context.Context, dir string, st *contract.PlanState, order []string, idx map[string]int, baseSha string) error {
	st.Merged, st.Outcome = nil, nil
	var escalated, failed, blocked []string
	var commits []string
	for _, id := range order {
		s := st.Slices[idx[id]]
		switch s.Status {
		case contract.PlanPassed:
			commits = append(commits, *s.Candidate)
		case contract.PlanEscalated:
			escalated = append(escalated, id)
		case contract.PlanFailed:
			failed = append(failed, id)
		default:
			blocked = append(blocked, id)
		}
	}
	set := func(outcome, reason string) { st.Outcome, st.Reason = &outcome, reason }
	if len(commits) < len(order) {
		var parts []string
		if len(escalated) > 0 {
			parts = append(parts, strings.Join(escalated, ", ")+" escalated")
		}
		if len(failed) > 0 {
			parts = append(parts, strings.Join(failed, ", ")+" failed")
		}
		if len(blocked) > 0 {
			parts = append(parts, fmt.Sprintf("%d slice(s) blocked", len(blocked)))
		}
		outcome := "escalated"
		if len(escalated) == 0 && len(failed) > 0 {
			outcome = contract.INCONCLUSIVE
		}
		set(outcome, strings.Join(parts, "; "))
		return nil
	}
	merged, err := r.Repo.MergeCommits(baseSha, commits, "a2dlc plan: "+st.FeatureID)
	if errors.Is(err, gitx.ErrMergeConflict) {
		set("escalated", "every slice passed but their candidates do not merge: "+err.Error())
		return nil
	}
	if err != nil {
		return err
	}
	if err := r.Repo.UpdateRef("refs/a2dlc/plan/"+st.FeatureID+"/merged", merged); err != nil {
		return err
	}
	st.Merged = &merged
	ok, reason, err := r.Final(ctx, baseSha, merged)
	if err != nil {
		return err
	}
	if !ok {
		set("escalated", "every slice passed but the final check of the merged tree did not: "+reason)
		return nil
	}
	set(contract.PASS, fmt.Sprintf("%d slice(s) passed and the merged tree passes the final check", len(order)))
	return nil
}

func save(dir string, st contract.PlanState) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, "state.json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, "state.json"))
}
