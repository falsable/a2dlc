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
// passed slice and the reason. Exhausted says the run escalated because every Builder spent its rounds (CB-02b), which a
// replan can answer; RunDir is the folder of the run, where a person answers an escalation.
type SliceRun struct {
	Status    string
	Candidate string
	Reason    string
	Exhausted bool
	RunDir    string
}

// SliceExec runs the slices of a plan. The real one is RunExec (exec.go): an `a2dlc run` per slice, each in its own
// worktree. Tests script it. Run is called from several goroutines at once when slices run side by side.
type SliceExec interface {
	// Run takes the slice from its intent (a file the runner wrote) to a candidate, on base.
	Run(ctx context.Context, s contract.PlanSlice, intentPath, base string) (SliceRun, error)
	// Verifies says whether a slice that passed in an earlier invocation still verifies (P4): its run is intact, on the
	// same base, with the same candidate.
	Verifies(s contract.PlanSlice, prev contract.PlanSliceState) bool
}

// FinalCheck judges the merge of every candidate against the feature's base (P6). ok is a PASS.
type FinalCheck func(ctx context.Context, base, merged string) (ok bool, reason string, err error)

// Slots decides how many slices run at once for a plan whose widest level has width slices, and records why.
type Slots func(width int) (int, *contract.PlanScheduler)

// Runner is the plan machine of fsm.go over a real repository: state on disk, merges in git, slices through Exec.
// Slots nil runs one slice at a time.
type Runner struct {
	Repo  gitx.Repo
	Exec  SliceExec
	Final FinalCheck
	Slots Slots
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

// Width is the size of the plan's widest level: no more slices than that can ever run at once.
func Width(p contract.Plan) int {
	w, _ := newGraph(p).waves()
	n := 1
	for _, level := range w {
		if len(level) > n {
			n = len(level)
		}
	}
	return n
}

// Definition is the SHA-256 of a slice as the plan declares it.
func Definition(s contract.PlanSlice) string {
	b, _ := json.Marshal(s)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// exhaustedPrefix starts the reason of a slice whose Builders were exhausted (CB-02b): the state keeps it, so a later
// invocation or a person sees which slices a replan can answer.
const exhaustedPrefix = "CB-02b: "

// Exhausted lists the escalated slices whose Builders were exhausted (CB-02b), in plan order.
func Exhausted(p contract.Plan, st contract.PlanState) []string {
	var out []string
	for _, id := range Order(p) {
		for _, s := range st.Slices {
			if s.SliceID == id && s.Status == contract.PlanEscalated && strings.HasPrefix(s.Reason, exhaustedPrefix) {
				out = append(out, id)
			}
		}
	}
	return out
}

type finished struct {
	id   string
	res  SliceRun
	base string
	err  error
}

// Run is one invocation of the plan machine over planBytes (a plan that passes its gates) from the feature's base
// commit. A slice starts once every slice it depends on settled in this invocation; up to Slots of them run at once,
// so the statuses are those of the sequential machine (fsm.go) and only the order slices start in can differ. It
// returns the state it left on disk and the slices it ran, in the order they started.
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
	jobs := 1
	st.Scheduler = nil
	if r.Slots != nil {
		jobs, st.Scheduler = r.Slots(Width(p))
		if jobs < 1 {
			jobs = 1
		}
	}
	bySlice := map[string]contract.PlanSlice{}
	for _, s := range p.Slices {
		bySlice[s.SliceID] = s
	}
	idx := map[string]int{}
	for i, s := range st.Slices {
		idx[s.SliceID] = i
	}
	order := Order(p)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	settled, started, reran := map[string]bool{}, map[string]bool{}, map[string]bool{}
	results := make(chan finished)
	running := 0
	var ran []string
	var fatal error
	settle := func(f finished) {
		if f.res.Exhausted && !strings.HasPrefix(f.res.Reason, exhaustedPrefix) {
			f.res.Reason = exhaustedPrefix + f.res.Reason
		}
		cur := &st.Slices[idx[f.id]]
		*cur = contract.PlanSliceState{SliceID: f.id, Status: f.res.Status, FinishedIn: st.Invocation, Reason: f.res.Reason,
			DefinitionSHA256: cur.DefinitionSHA256}
		if f.base != "" {
			b := f.base
			cur.Base = &b
		}
		if f.res.RunDir != "" {
			d := f.res.RunDir
			cur.RunDir = &d
		}
		if f.res.Status == contract.PlanPassed {
			c := f.res.Candidate
			cur.Candidate = &c
		}
		settled[f.id] = true
	}
	for {
		for progressed := true; progressed && fatal == nil; {
			progressed = false
			for _, id := range order {
				if settled[id] || started[id] {
					continue
				}
				s, cur := bySlice[id], &st.Slices[idx[id]]
				ready, depReran := true, false
				var notPassed []string
				for _, d := range s.DependsOn {
					if !settled[d] {
						ready = false
						break
					}
					if ds := st.Slices[idx[d]]; ds.Status != contract.PlanPassed {
						notPassed = append(notPassed, d+" ("+ds.Status+")")
					}
					depReran = depReran || reran[d]
				}
				if !ready {
					continue
				}
				if len(notPassed) > 0 { // P3
					*cur = contract.PlanSliceState{SliceID: id, Status: contract.PlanBlocked, FinishedIn: cur.FinishedIn,
						Reason: "blocked by " + strings.Join(notPassed, ", "), DefinitionSHA256: cur.DefinitionSHA256}
					settled[id], progressed = true, true
					continue
				}
				if cur.Status == contract.PlanPassed && !depReran && r.Exec.Verifies(s, *cur) { // P4
					settled[id], progressed = true, true
					continue
				}
				if running >= jobs {
					continue
				}
				ran, reran[id], started[id], progressed = append(ran, id), true, true, true
				sliceBase, intentPath, esc, err := r.prepare(dir, p, feature, s, st, idx, order, baseSha)
				if err != nil {
					fatal = err
					break
				}
				if esc != "" {
					settle(finished{id: id, res: SliceRun{Status: contract.PlanEscalated, Reason: esc}})
					continue
				}
				running++
				go func(id string, s contract.PlanSlice, base, intentPath string) {
					res, err := r.Exec.Run(ctx, s, intentPath, base)
					results <- finished{id: id, res: res, base: base, err: err}
				}(id, s, sliceBase, intentPath)
			}
		}
		if running == 0 {
			break
		}
		f := <-results
		running--
		if f.err != nil {
			if ctx.Err() != nil || fatal != nil {
				if fatal == nil {
					fatal = f.err
				}
				cancel()
				continue
			}
			// one slice that cannot run fails alone; its siblings go on (P3)
			f.res = SliceRun{Status: contract.PlanFailed, Reason: "the run did not finish: " + f.err.Error(), RunDir: f.res.RunDir}
		}
		if f.res.Status == contract.PlanPassed && f.res.Candidate == "" {
			f.res = SliceRun{Status: contract.PlanFailed, Reason: "the run passed without a candidate commit", RunDir: f.res.RunDir}
		}
		settle(f)
		if err := save(dir, st); err != nil && fatal == nil { // an interrupted invocation keeps what it finished
			fatal = err
			cancel()
		}
	}
	if fatal != nil {
		save(dir, st)
		return st, ran, fatal
	}
	if err := r.finish(ctx, dir, &st, order, idx, baseSha); err != nil {
		return st, ran, err
	}
	return st, ran, save(dir, st)
}

// load reads the state of an earlier invocation, or starts one. A state on another base is an error: the person
// decides whether to discard it. A state of another version of the plan (a replan, an edit) is carried over: a slice
// keeps its state when its definition and that of every slice it depends on, transitively, did not change; every other
// slice starts pending, and slices the new plan dropped are forgotten.
func (r Runner) load(dir string, p contract.Plan, planSHA, baseSha string) (contract.PlanState, error) {
	fresh := contract.PlanState{SchemaVersion: 1, FeatureID: p.FeatureID, PlanSHA256: planSHA, BaseSha: baseSha}
	for _, s := range p.Slices {
		fresh.Slices = append(fresh.Slices, contract.PlanSliceState{SliceID: s.SliceID, Status: contract.PlanPending, DefinitionSHA256: Definition(s)})
	}
	path := filepath.Join(dir, "state.json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return fresh, nil
	}
	if err != nil {
		return contract.PlanState{}, err
	}
	var st contract.PlanState
	if err := contract.DecodeStrictBytes(b, &st); err != nil {
		return st, fmt.Errorf("%s does not match the contract: %w", path, err)
	}
	if st.BaseSha != baseSha {
		return st, fmt.Errorf("%s is on base %s, not %s; to start over on this base, move %s aside", path, st.BaseSha[:8], baseSha[:8], dir)
	}
	if st.PlanSHA256 == planSHA {
		return st, nil
	}
	old := map[string]contract.PlanSliceState{}
	for _, s := range st.Slices {
		old[s.SliceID] = s
	}
	bySlice := map[string]contract.PlanSlice{}
	for _, s := range p.Slices {
		bySlice[s.SliceID] = s
	}
	kept := map[string]bool{}
	for _, id := range Order(p) {
		s := bySlice[id]
		o, ok := old[id]
		keep := ok && o.DefinitionSHA256 == Definition(s)
		for _, d := range s.DependsOn {
			keep = keep && kept[d]
		}
		kept[id] = keep
	}
	for i, s := range fresh.Slices {
		if kept[s.SliceID] {
			fresh.Slices[i] = old[s.SliceID]
		}
	}
	fresh.Invocation, fresh.Replans = st.Invocation, st.Replans
	return fresh, nil
}

// prepare computes the slice's base (P2) and writes its intent. A base whose dependencies do not merge is an
// escalation of this slice (esc): no Builder runs on a base that does not exist.
func (r Runner) prepare(dir string, p contract.Plan, feature contract.Intent, s contract.PlanSlice,
	st contract.PlanState, idx map[string]int, order []string, baseSha string) (sliceBase, intentPath, esc string, err error) {
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
	sliceBase, err = r.Repo.MergeCommits(baseSha, commits, "a2dlc plan: base of "+s.SliceID)
	if errors.Is(err, gitx.ErrMergeConflict) {
		return "", "", "the slices it depends on do not merge: " + err.Error(), nil
	}
	if err != nil {
		return "", "", "", err
	}
	if err := r.Repo.UpdateRef("refs/a2dlc/plan/"+p.FeatureID+"/base/"+s.SliceID, sliceBase); err != nil {
		return "", "", "", err
	}
	b, err := json.MarshalIndent(SliceIntent(s, feature), "", "  ")
	if err != nil {
		return "", "", "", err
	}
	intentPath = filepath.Join(dir, "intents", s.SliceID+".json")
	if err := os.MkdirAll(filepath.Dir(intentPath), 0o755); err != nil {
		return "", "", "", err
	}
	if err := os.WriteFile(intentPath, append(b, '\n'), 0o644); err != nil {
		return "", "", "", err
	}
	return sliceBase, intentPath, "", nil
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

// LoadState reads a feature's plan state.
func LoadState(repoDir, feature string) (contract.PlanState, error) {
	var st contract.PlanState
	b, err := os.ReadFile(filepath.Join(StateDir(repoDir, feature), "state.json"))
	if err != nil {
		return st, err
	}
	return st, contract.DecodeStrictBytes(b, &st)
}

// RecordReplan counts one more proposed replan in the feature's state (bounded by plan run --max-replan).
func RecordReplan(repoDir, feature string) error {
	st, err := LoadState(repoDir, feature)
	if err != nil {
		return err
	}
	st.Replans++
	return save(StateDir(repoDir, feature), st)
}
