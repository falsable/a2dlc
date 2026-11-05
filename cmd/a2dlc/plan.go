package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/plan"
	a2run "github.com/falsable/a2dlc/internal/run"
	"github.com/falsable/a2dlc/internal/verdict"
)

const planUsage = `usage: a2dlc plan <check|run|propose|replan|import> ...
  plan check   --plan <plan.json> --intent <feature.json> [--base <ref>] [--json]
  plan run     --plan <plan.json> --intent <feature.json> [--base <ref>] [--jobs N] [--max-replan N] [--rounds N] [--json]
  plan propose --intent <feature.json> --out <plan.json> [--base <ref>] [--rounds N] [--json]
  plan replan  --plan <plan.json> --intent <feature.json> --slice <id> --out <plan.json> [--reason <text>] [--base <ref>] [--rounds N]
  plan import  --from <tasks.md|tasks.json> --intent <feature.json> --out <plan.json> [--format speckit|taskmaster] [--tag <tag>] [--base <ref>]`

// planCommand is the a2dlc plan family (RFC-0008). check: the gates of section 3. run: one invocation of the plan
// machine (section 5, docs/PLAN_MACHINE.md), each slice an a2dlc run in its own worktree, as many at once as the host
// and the Docker daemon can hold. propose and replan: the planner role proposes, the gates (and the replan rules)
// judge, for at most --rounds rounds. import: a Spec Kit tasks.md or a Task Master tasks.json as a candidate plan. Every
// plan a command writes is a proposal: plan check says whether it passes, and running it is the person's decision.
func planCommand(ctx context.Context, a app.App, repo gitx.Repo, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, planUsage)
		return 64
	}
	sub := args[0]
	switch sub {
	case "check", "run", "propose", "replan", "import":
	default:
		fmt.Fprintln(os.Stderr, planUsage)
		return 64
	}
	name := "a2dlc plan " + sub + ":"
	fs := flag.NewFlagSet("plan "+sub, flag.ContinueOnError)
	planPath := fs.String("plan", "", "the plan (contracts/plan.schema.json)")
	intentPath := fs.String("intent", "", "the feature's intent (contracts/intent.schema.json)")
	base := fs.String("base", "", "commit base (default: the upstream, else the fork point from origin's default branch, else HEAD)")
	asJSON := fs.Bool("json", false, "the report or the state as JSON, for an agent or a UI")
	out := fs.String("out", "", "propose, replan, import: where the proposed plan goes")
	rounds := fs.Int("rounds", 3, "propose, replan, run: rounds of the planner before giving up on the gates")
	jobs := fs.Int("jobs", 0, "run: slices at once (0: measured from the host and the Docker daemon; A2DLC_PLAN_JOBS also sets it)")
	maxReplan := fs.Int("max-replan", 1, "run: replans the plan may propose for slices whose Builders were exhausted (CB-02b); 0 never")
	z3Image := fs.String("z3-image", "a2dlc-engine-z3:0.1", "run: z3 engine image for Stage 1.5 of each slice (empty: Z3 is skipped)")
	slice := fs.String("slice", "", "replan: the slice to split")
	reason := fs.String("reason", "", "replan: why its Builders were exhausted (default: what the plan's state recorded)")
	from := fs.String("from", "", "import: the tasks file")
	format := fs.String("format", "", "import: speckit or taskmaster (default: by extension, .md or .json)")
	tag := fs.String("tag", "", "import: the Task Master tag (default master)")
	if fs.Parse(args[1:]) != nil {
		return 64
	}
	need := map[string][]string{"check": {"plan", "intent"}, "run": {"plan", "intent"}, "propose": {"intent", "out"},
		"replan": {"plan", "intent", "slice", "out"}, "import": {"from", "intent", "out"}}
	vals := map[string]string{"plan": *planPath, "intent": *intentPath, "out": *out, "slice": *slice, "from": *from}
	for _, k := range need[sub] {
		if vals[k] == "" {
			fmt.Fprintf(os.Stderr, "%s --%s is required\n%s\n", name, k, planUsage)
			return 64
		}
	}
	f, code := readFeature(name, *intentPath)
	if code != 0 {
		return code
	}
	ref := *base
	if ref == "" {
		ref = a.DefaultBase()
	}
	sha, err := repo.ResolveCommit(ref)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s base %q: %v\n", name, ref, err)
		return 64
	}
	files, err := repo.Files(sha)
	if err != nil {
		fmt.Fprintln(os.Stderr, name, err)
		return 2
	}
	var planBytes []byte
	var p contract.Plan
	if *planPath != "" {
		if planBytes, p, code = readPlan(name, *planPath); code != 0 {
			return code
		}
	}
	switch sub {
	case "check":
		return report(plan.Check(p, f, files), p, *asJSON)
	case "run":
		return planRun(ctx, a, repo, *planPath, planBytes, p, f, sha, files, *z3Image, *jobs, *maxReplan, *rounds, *asJSON)
	case "propose", "replan":
		pl, code := planner(name, a, *rounds)
		if code != 0 {
			return code
		}
		var pr plan.Proposal
		if sub == "propose" {
			pr, err = pl.Propose(ctx, f, sha, files)
		} else {
			why := *reason
			if why == "" {
				why = stateReason(a, p, *slice)
			}
			pr, err = pl.Replan(ctx, f, sha, files, p, *slice, why)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, name, err)
			return 2
		}
		return writeProposal(name, pr, *out, *asJSON)
	case "import":
		src, err := os.ReadFile(*from)
		if err != nil {
			fmt.Fprintln(os.Stderr, name, err)
			return 64
		}
		kind := *format
		if kind == "" {
			kind = map[string]string{".md": "speckit", ".json": "taskmaster"}[strings.ToLower(filepath.Ext(*from))]
		}
		var im plan.Imported
		switch kind {
		case "speckit":
			im, err = plan.ImportSpecKit(src, f)
		case "taskmaster":
			im, err = plan.ImportTaskMaster(src, f, *tag)
		default:
			fmt.Fprintf(os.Stderr, "%s --format speckit or taskmaster (could not tell from %s)\n", name, *from)
			return 64
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s %s: %v\n", name, *from, err)
			return 1
		}
		if err := writeJSON(*out, im.Plan); err != nil {
			fmt.Fprintln(os.Stderr, name, err)
			return 2
		}
		for _, n := range im.Notes {
			fmt.Fprintln(os.Stderr, name, n)
		}
		fmt.Fprintf(os.Stderr, "%s %d slice(s) from %s written to %s; the gates:\n", name, len(im.Plan.Slices), *from, *out)
		return report(plan.Check(im.Plan, f, files), im.Plan, *asJSON)
	}
	return 64
}

func readFeature(name, path string) (contract.Intent, int) {
	var f contract.Intent
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, name, err)
		return f, 64
	}
	if err := contract.DecodeStrictBytes(b, &f); err != nil {
		fmt.Fprintf(os.Stderr, "%s %s does not match the intent contract: %v\n", name, path, err)
		return f, 1
	}
	if err := f.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "%s the feature's intent is not valid: %v\n", name, err)
		return f, 1
	}
	return f, 0
}

func readPlan(name, path string) ([]byte, contract.Plan, int) {
	var p contract.Plan
	b, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, name, err)
		return nil, p, 64
	}
	if err := contract.DecodeStrictBytes(b, &p); err != nil {
		fmt.Fprintf(os.Stderr, "%s %s does not match the plan contract: %v\n", name, path, err)
		return nil, p, 1
	}
	return b, p, 0
}

func report(r plan.Report, p contract.Plan, asJSON bool) int {
	if asJSON {
		json.NewEncoder(os.Stdout).Encode(r)
	} else {
		fmt.Println(r.Text(p))
	}
	if !r.Valid {
		return 1
	}
	return 0
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		return err
	}
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

// planner is the planner role of roles.json, with the docker launcher.
func planner(name string, a app.App, rounds int) (plan.Planner, int) {
	b, err := os.ReadFile(filepath.Join(a.Repo.Dir, ".a2dlc", "roles.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s the planner role is configured in .a2dlc/roles.json: %v\n", name, err)
		return plan.Planner{}, 64
	}
	var rs contract.Roles
	if err := contract.DecodeStrictBytes(b, &rs); err != nil {
		fmt.Fprintf(os.Stderr, "%s .a2dlc/roles.json does not match the contract: %v\n", name, err)
		return plan.Planner{}, 1
	}
	role, err := a2run.RoleConfig(rs, a2run.PlannerRole)
	if err != nil {
		fmt.Fprintln(os.Stderr, name, err)
		return plan.Planner{}, 64
	}
	return plan.Planner{Role: role, Launcher: a2run.DockerLauncher{}, Rounds: rounds}, 0
}

// writeProposal writes the planner's last plan to out, accepted or not, and prints its report: a plan that still breaks
// a gate after every round is the person's to fix, and they need it written to fix it.
func writeProposal(name string, pr plan.Proposal, out string, asJSON bool) int {
	if pr.Plan == nil {
		fmt.Fprintf(os.Stderr, "%s the planner gave no plan in %d round(s): %s %s\n", name, pr.Rounds, pr.Status, pr.Reason)
		return 2
	}
	if err := writeJSON(out, pr.Plan); err != nil {
		fmt.Fprintln(os.Stderr, name, err)
		return 2
	}
	for _, r := range pr.Replan {
		if r.Status == plan.Fail {
			fmt.Fprintf(os.Stderr, "%s replan rule %s: %s\n", name, r.Rule, strings.Join(r.Findings, "; "))
		}
	}
	verdictWord := "passes every gate"
	if !pr.Accepted() {
		verdictWord = "still breaks a gate"
	}
	fmt.Fprintf(os.Stderr, "%s plan written to %s after %d round(s), %.4f USD reported; it %s:\n", name, out, pr.Rounds, pr.CostUSD, verdictWord)
	code := report(pr.Report, *pr.Plan, asJSON)
	if !pr.Accepted() {
		return 1
	}
	return code
}

// stateReason is what the plan's state recorded for slice (its escalation), for a replan without --reason.
func stateReason(a app.App, p contract.Plan, slice string) string {
	st, err := plan.LoadState(a.Repo.Dir, p.FeatureID)
	if err == nil {
		for _, s := range st.Slices {
			if s.SliceID == slice && s.Reason != "" {
				return s.Reason
			}
		}
	}
	return "its Builders were exhausted (CB-02b)"
}

// planRun is one invocation of `a2dlc plan run`. Exit codes as a2dlc run: 0 PASS, 2 escalated, the verdict's code for
// INCONCLUSIVE, 1 for a plan that does not pass its gates or a state on another base. With --max-replan above the
// replans already proposed and a planner role configured, a slice whose Builders were exhausted (CB-02b) gets a
// proposed split, written next to the plan: running it is the person's decision.
func planRun(ctx context.Context, a app.App, repo gitx.Repo, planPath string, planBytes []byte, p contract.Plan, f contract.Intent,
	base string, files []string, z3Image string, jobs, maxReplan, rounds int, asJSON bool) int {
	opts := a2run.Options{MaxSpecRounds: a2run.MaxSpecRoundsDefault}
	if z3Image != "" {
		opts.Z3 = engine.Z3{Image: z3Image}
	}
	cfg, err := a.ConfigAt(base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc plan run:", err)
		return 1
	}
	var roles contract.Roles
	if b, err := os.ReadFile(filepath.Join(a.Repo.Dir, ".a2dlc", "roles.json")); err == nil {
		if err := contract.DecodeStrictBytes(b, &roles); err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc plan run: .a2dlc/roles.json does not match the contract:", err)
			return 1
		}
	}
	r := plan.Runner{Repo: repo, Exec: plan.RunExec{App: a, Launcher: a2run.DockerLauncher{}, Options: opts, Feature: p.FeatureID},
		Final: plan.AppFinalCheck(a, f), Slots: plan.AutoSlots(ctx, cfg, roles, jobs, "")}
	st, ran, err := r.Run(ctx, planBytes, f, base)
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc plan run:", err)
		return 1
	}
	if asJSON {
		json.NewEncoder(os.Stdout).Encode(st)
	} else {
		if sc := st.Scheduler; sc != nil {
			fmt.Printf("%d slice(s) at a time: %s\n", sc.Jobs, sc.Why)
		}
		fmt.Printf("invocation %d ran %d slice(s)\n", st.Invocation, len(ran))
		for _, s := range st.Slices {
			fmt.Printf("  %-40s %-9s %s\n", s.SliceID, s.Status, s.Reason)
			if s.RunDir != nil && (s.Status == contract.PlanEscalated || s.Status == contract.PlanFailed) {
				fmt.Printf("  %-40s           run: %s\n", "", *s.RunDir)
			}
		}
	}
	out := "no verdict"
	if st.Outcome != nil {
		out = *st.Outcome
	}
	if !asJSON {
		fmt.Printf("%s: plan %s, %s\n", out, st.FeatureID, st.Reason)
	}
	if exhausted := plan.Exhausted(p, st); len(exhausted) > 0 {
		proposeReplan(ctx, a, planPath, p, f, base, files, st, exhausted, maxReplan, rounds)
	}
	if out == "escalated" {
		return 2
	}
	return verdict.ExitCode(out)
}

// proposeReplan splits each exhausted slice in turn, each split on the plan the previous one left, and writes the result
// next to the plan as <plan>.replan-<n>.json when every split passes the replan rules and the gates.
func proposeReplan(ctx context.Context, a app.App, planPath string, p contract.Plan, f contract.Intent, base string, files []string,
	st contract.PlanState, exhausted []string, maxReplan, rounds int) {
	if st.Replans >= maxReplan {
		fmt.Fprintf(os.Stderr, "a2dlc plan run: %s exhausted its Builders (CB-02b); the plan already proposed %d replan(s) (--max-replan %d): a person decides\n",
			strings.Join(exhausted, ", "), st.Replans, maxReplan)
		return
	}
	pl, code := planner("a2dlc plan run:", a, rounds)
	if code != 0 {
		fmt.Fprintf(os.Stderr, "a2dlc plan run: %s exhausted its Builders (CB-02b); configure a planner role to have it split, or split it with a2dlc plan replan\n",
			strings.Join(exhausted, ", "))
		return
	}
	cur := p
	reasons := map[string]string{}
	for _, s := range st.Slices {
		reasons[s.SliceID] = s.Reason
	}
	for _, id := range exhausted {
		pr, err := pl.Replan(ctx, f, base, files, cur, id, reasons[id])
		if err != nil || !pr.Accepted() {
			why := "it did not pass the replan rules and the gates"
			if err != nil {
				why = err.Error()
			} else if pr.Plan == nil {
				why = pr.Status + " " + pr.Reason
			}
			fmt.Fprintf(os.Stderr, "a2dlc plan run: the planner could not split %s: %s; a person decides\n", id, why)
			return
		}
		cur = *pr.Plan
	}
	n := st.Replans + 1
	out := strings.TrimSuffix(planPath, filepath.Ext(planPath)) + fmt.Sprintf(".replan-%d.json", n)
	if err := writeJSON(out, cur); err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc plan run:", err)
		return
	}
	if err := plan.RecordReplan(a.Repo.Dir, p.FeatureID); err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc plan run:", err)
		return
	}
	fmt.Fprintf(os.Stderr, "a2dlc plan run: %s exhausted its Builders (CB-02b); the planner split it and the split passes every gate: %s\n"+
		"  review it (a2dlc plan check --plan %s ...) and run it (a2dlc plan run --plan %s ...): the slices it did not change keep their state\n",
		strings.Join(exhausted, ", "), out, out, out)
}
