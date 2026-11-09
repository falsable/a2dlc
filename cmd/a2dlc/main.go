// a2dlc: differential gate of the A2-DLC Lite mode. See the README and ADR meta/0004.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/mcp"
	"github.com/falsable/a2dlc/internal/plan"
	a2run "github.com/falsable/a2dlc/internal/run"
	"github.com/falsable/a2dlc/internal/verdict"
)

// version is set by the release with -ldflags "-X main.version=<tag>".
var version = "0.1.0-dev"

const usage = `usage: a2dlc <command>
  check   [--level 0|1] [--base <ref>] [--peer <ref>]... [--tests-overlay <dir>] [--require-signatures]
          runs the engines and the test gates over what changed (base: the upstream, else the fork point from
          origin's default branch, else HEAD) and writes .a2dlc/verdict.json (0 PASS, 1 FAIL, 2 INCONCLUSIVE). --peer: tree of another builder of the same change;
          --tests-overlay: tests from outside that run over base, candidate and peers without being committed.
          --level 1 (default) is the blocking gate above, for "done" and the PR check. --level 0 is the static level for
          every turn: it runs no test, informs and never blocks, writes .a2dlc/reports/level0.json and exits 0 (2 only if a2dlc itself fails)
  plan check --plan <plan.json> --intent <feature intent.json> [--base <ref>] [--json]
          judges a feature plan (RFC-0008) before any Builder runs: 0 valid, 1 not valid, 64 bad usage; --json for an agent
  plan run --plan <plan.json> --intent <feature intent.json> [--base <ref>] [--jobs N] [--max-replan N] [--max-cost-usd <n>] [--json]
          runs (or resumes) a valid plan: each slice an a2dlc run in its own worktree, side by side when their files are
          disjoint, as many at once as the host and the Docker daemon hold (measured; --jobs or A2DLC_PLAN_JOBS sets it),
          then a final check of the merge (.a2dlc/run/_plan/<feature>/state.json; 0 PASS, 2 escalated, 1 plan refused).
          A slice whose Builders are exhausted (CB-02b) gets a split proposed by the planner role, written next to the plan
  plan propose --intent <feature.json> --out <plan.json> [--rounds N]   the planner role proposes, the gates judge
  plan replan --plan <plan.json> --intent <feature.json> --slice <id> --out <plan.json>   split one slice (CB-02b)
  plan import --from <tasks.md|tasks.json> --intent <feature.json> --out <plan.json>   Spec Kit or Task Master as a plan
  propose [--base <ref>] --out <empty dir outside the repo>
          asks the propose_properties role (roles.json) for the properties of the change from base to the working tree;
          prints the outcome as JSON (0 ok, 1 otherwise). Nothing is written into the repo; the answer goes to --out.
  propose --from-escalations --out <empty dir outside the repo>
          no role call: the counterexamples that earlier runs left in .a2dlc/escalations.json and .a2dlc/run/*/escalations.json
          become properties for the next intent (proposals.json in --out, contracts/proposals.schema.json). Deduplicated by
          the hash of target, args and old output against .a2dlc/reports/proposals.jsonl; a target waived with
          enforce_new_behavior is left out. It proposes only: it never edits an intent. 0 done (also with nothing new), 2 failed.
  run     --slice <id> [--intent <intent.json>] [--mode lite|refactor|greenfield|brownfield_facade] [--base <ref>]
          [--max-cost-usd <n>] [--max-rounds <n>] [--max-inner <n>] [--max-spec-rounds <n>] [--z3-image <img>] [--json]
          runs (or resumes) the protocol's state machine over a slice: Stages -1..5, roles from .a2dlc/roles.json
          in their own containers, state and ledger in .a2dlc/run/<slice>/ (0 PASS, 1 FAIL, 2 INCONCLUSIVE or escalated).
          --intent: the Stage 0 (goal, criticality, critical_paths, budget); the run is bound to that file.
          Limits without a flag come from the intent's budget, or the default: --max-rounds (Saboteur rounds, 3),
          --max-inner (Tier 0 loop, 5), --max-cost-usd (CB-03, no cap; on resume, raises the saved cap)
          --max-spec-rounds: Specifier α/β and Inquisitor rounds in Stage 1 before escalating (2 by default)
          --z3-image: z3 engine of Stage 1.5 (a2dlc-engine-z3:0.1 by default; empty leaves it skipped)
          --json: prints the run's state (contracts/run-state.schema.json) instead of the stage lines, for an agent
  verify-run --slice <id>          recalculates the ledger chain and the run's artifacts (0 passes, 1 does not)
  verify-run --plan <feature>      checks that a plan run was accepted: the plan's signed approval or a plan.accept authorization
  plan approve --plan <plan.json> --expires <RFC 3339> --key <key>   sign a plan so plan run accepts it
  verify  [--commit HEAD] [--strict]  the pre-push, without running engines: blocks (exit 1) only on evidence, a FAIL or critical
          or protected code left unjudged; no receipt, a stale one or an INCONCLUSIVE one are warnings. --strict blocks on anything but a PASS receipt for this tree
  approve <target> --key <key> [--principal <id>]   signs a target's waiver (run by a person)
  authorize --target <path::symbol>... --decision <enforce_new_behavior|preserve_legacy|plan.accept> --intent <file> --expires <RFC 3339>
          --key <key> [--principal <id>] [--base <commit>]
          signs, before the code exists, that those exact targets may change (run by a person): a covered waiver needs no
          approval, until the date, on that base and for that intent (RFC-0007 Part B; .a2dlc/authorizations.json)
  approve --pending [--json] [--yes] --key <key> [--principal <id>]
          shows every waiver of the current receipt that waits for a signature, grouped by the reason it gives, and signs
          them all with one unlock of the key (run by a person); --json lists them for an agent or a UI and signs nothing
  answer  --slice <id> --probe <id> --choice <c> --reason <text> --by <name>
          records a person's answer to a probe of a run (answers.json; 0 written, 2 refused, 64 bad usage)
  finding report|list|label        what happened to each finding a check raised (.a2dlc/reports/findings.jsonl, out of the tree_hash;
          never changes a verdict; A2DLC_FINDINGS=off stops writing it). report [--json]: per check and engine, raised, fixed, intentional,
          false, irrelevant, open. list [--open] [--json]: the finding ids. label --id <id> --as false|irrelevant --by <name> [--reason <text>]
          [--json]: the only way a finding becomes false or irrelevant (0 written, 1 no such finding, 2 record unreadable, 64 bad usage)
  journal [--out <file>]           the development journal as one HTML page for a person or an auditor: where the change stands,
          what waits on a person, behavior changes with their waiver and signer, open findings, and the sha256 of every file
          it read. Rendered from the repo's own files with a fixed template and no model; it reads only and verifies no
          signature. 0 written, 2 a source unreadable or out of contract, 64 bad usage
  hook    <claude|gemini>          adapter for the agent's end-of-turn hook
  install [--agent claude] [--agent gemini] [--no-derive-image] [--dev-deps]   config, .gitignore, pre-push and agent hooks;
          a Python repo with requirements*.txt or pyproject dependencies gets an engine image derived from them (needs the network once)
  doctor  [--json]                 health check of this repo's setup: pre-push hook binary, config version, engines declared vs
          detected, engine images, Docker, allowed signers. Each finding has a stable id, a severity (blocking, warning, info)
          and a fix; --json prints them (contracts/doctor.schema.json), for an agent. It repairs nothing and writes nothing.
          Exit 0 with no blocking finding, 1 with any, 2 if doctor itself fails, 64 on bad usage
  mcp                              runs the Model Context Protocol (MCP) JSON-RPC 2.0 server over stdio
  version`

type list []string

func (l *list) String() string     { return strings.Join(*l, ",") }
func (l *list) Set(s string) error { *l = append(*l, s); return nil }

func main() { os.Exit(run(os.Args[1:])) }

func run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 64
	}
	if args[0] == "version" {
		fmt.Println(version)
		return 0
	}
	cwd, _ := os.Getwd()
	repo, err := gitx.Open(cwd)
	if err != nil {
		if args[0] == "hook" && len(args) > 1 {
			return app.HookOutsideRepo(args[1], os.Stdout)
		}
		fmt.Fprintln(os.Stderr, "a2dlc:", err)
		return 64
	}
	a := app.App{Repo: repo, Runner: engine.Docker{CopyIn: engine.CopyInDefault}, Tests: engine.DockerTests{CopyIn: engine.CopyInDefault},
		Checks: engine.DockerChecks{}, Version: version, Now: time.Now, Memo: app.NewEngineMemo(), Probe: engine.HostProbe{}, Builder: engine.DockerBuilder{}}
	ctx := context.Background()
	if args[0] == "check" || args[0] == "verify" {
		warnHookBinary(a, os.Stderr)
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "check":
		base := fs.String("base", "", "commit base (default: the upstream, else the fork point from origin's default branch, else HEAD)")
		signatures := fs.Bool("require-signatures", false, "fails if a waiver used has no valid signature (for CI)")
		var peers list
		fs.Var(&peers, "peer", "commit of another builder of the same change (repeatable)")
		overlay := fs.String("tests-overlay", "", "folder with tests from outside, at the path they would have in the repo")
		level := fs.Int("level", 1, "1: the blocking gate (default); 0: the static level, which runs no test and never blocks")
		background := fs.Bool("background", false, "level 1 for the agent hook: writes .a2dlc/reports/background.json, not the receipt verify reads")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		switch *level {
		case 0:
			if len(peers) > 0 || *overlay != "" || *signatures {
				fmt.Fprintln(os.Stderr, "a2dlc check: --level 0 takes only --base")
				return 64
			}
			v, err := a.Level0(ctx, *base)
			if err != nil {
				fmt.Fprintln(os.Stderr, "a2dlc check --level 0:", err)
				return 2
			}
			fmt.Println(app.Level0Summary(v))
			return 0
		case 1:
		default:
			fmt.Fprintln(os.Stderr, "a2dlc check: --level is 0 or 1")
			return 64
		}
		if *background {
			defer a.BackgroundDone()
			if _, err := a.CheckWith(ctx, *base, app.CheckOptions{ReportPath: app.BackgroundReport()}); err != nil {
				fmt.Fprintln(os.Stderr, "a2dlc check --background:", err)
				return 2
			}
			return 0
		}
		v, err := a.CheckWith(ctx, *base, app.CheckOptions{Peers: peers, TestsOverlay: *overlay})
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc check:", err)
			return 2
		}
		covered, without, noted := 0, 0, 0
		if v.Coverage != nil {
			for _, f := range v.Coverage.Files {
				covered += len(f.ByEngine) + len(f.Covered)
				without += len(f.Uncovered)
				for _, n := range f.Noted {
					noted += len(n.Lines)
				}
			}
		}
		fmt.Printf("%s: %d targets, %d tests judged, %d out of scope, %d changed lines examined and %d not examined, %d waivers pending, %d engines with errors\n",
			v.Verdict, len(v.Targets), len(v.Tests), len(v.OutOfScope), covered, without, len(v.PendingWaivers), len(v.EngineErrors))
		if n := app.CouldNotImportNote(v); n != "" {
			fmt.Println(n)
		}
		if sig, err := a.VerdictSignature(); err == nil {
			fmt.Println("note:", sig.Line())
		}
		if c := v.Coverage; c != nil && c.Status == contract.CoverageNotConfigured && c.Reason != "" {
			fmt.Printf("note: no test ran: %s\n", c.Reason)
		} else if n := app.NoTestsSectionNote(v); n != "" {
			fmt.Printf("note: %s\n", n)
		}
		if noted > 0 {
			fmt.Printf("note: %d changed lines were not examined and do not block (defensive exception handlers or declaration-only code): see coverage.files[].noted in .a2dlc/verdict.json\n", noted)
		}
		for _, l := range blockingLines(v) {
			fmt.Println(l)
		}
		for _, t := range v.Targets {
			if t.Note == contract.NoteBehaviorDivergence {
				fmt.Printf("note: behavior divergence on %s does not fail the verdict (not critical code): args=%s old=%s new=%s\n",
					t.Target, clip(t.Counterexample.Args), clip(t.Counterexample.Old), clip(t.Counterexample.New))
			}
		}
		for _, t := range v.Targets {
			if t.Note == contract.NoteInconclusiveCoveredByTests {
				fmt.Printf("note: %s is INCONCLUSIVE (%s) and does not make the verdict INCONCLUSIVE: the tests executed its changed lines\n", t.Target, t.Reason)
			}
		}
		for _, e := range v.EngineErrors {
			fmt.Fprintf(os.Stderr, "a2dlc check: engine %s (%s): %s\n", e.Language, strings.Join(e.Files, ", "), e.Error)
		}
		if *signatures && v.Verdict == contract.PASS && len(v.PendingWaivers) > 0 {
			fmt.Fprintln(os.Stderr, "a2dlc check: waivers without a valid signature:", strings.Join(v.PendingWaivers, ", "))
			return 1
		}
		return verdict.ExitCode(v.Verdict)
	case "run":
		slice := fs.String("slice", "", "slice id (lowercase letters, digits, dot, hyphen, underscore)")
		mode := fs.String("mode", "", "greenfield, refactor, brownfield_facade or lite (empty: the intent's in a new run, the saved one on resume, or lite)")
		base := fs.String("base", "", "commit base (default: the upstream, else the fork point from origin's default branch, else HEAD)")
		intent := fs.String("intent", "", "intent.json of the slice (Stage 0)")
		cost := fs.Float64("max-cost-usd", 0, "CB-03: cost cap reported by the roles (0 = the intent's, or no cap)")
		rounds := fs.Int("max-rounds", 0, fmt.Sprintf("Saboteur rounds: over uncovered_change in Stage 4 and over live mutants in Stage 5 (0 = the intent's, or %d)", a2run.MaxRoundsDefault))
		inner := fs.Int("max-inner", 0, fmt.Sprintf("Tier 0 loop iterations in Stage 3 (0 = the intent's, or %d)", a2run.MaxInnerDefault))
		specRounds := fs.Int("max-spec-rounds", a2run.MaxSpecRoundsDefault, "Specifier α/β and Inquisitor rounds in Stage 1 before escalating")
		z3Image := fs.String("z3-image", "a2dlc-engine-z3:0.1", "z3 engine image for Stage 1.5 (empty: Z3 is skipped)")
		asJSON := fs.Bool("json", false, "print the run's state as JSON instead of the stage lines, for an agent")
		if fs.Parse(args[1:]) != nil || *slice == "" {
			fmt.Fprintln(os.Stderr, "a2dlc run: missing --slice")
			return 64
		}
		opts := a2run.Options{Slice: *slice, Mode: *mode, Base: *base, Intent: *intent, MaxCostUSD: *cost, MaxRounds: *rounds,
			MaxInner: *inner, MaxSpecRounds: *specRounds}
		if *z3Image != "" {
			opts.Z3 = engine.Z3{Image: *z3Image}
		}
		st, err := a2run.Run(ctx, a, a2run.DockerLauncher{}, opts)
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc run:", err)
			return 2
		}
		out := "no verdict"
		if st.Outcome != nil {
			out = *st.Outcome
		}
		if *asJSON {
			json.NewEncoder(os.Stdout).Encode(st)
		} else {
			for _, s := range st.Stages {
				reason := ""
				if s.Reason != "" {
					reason = ": " + s.Reason
				}
				fmt.Printf("  stage %-4s %-9s%s\n", s.ID, s.Status, reason)
			}
			fmt.Printf("%s: run %s, slice %s, %.4f USD reported (%d roles without a report)\n", out, st.RunID, st.SliceID, st.Budget.SpentUSD, st.Budget.UnreportedRoles)
		}
		if out == "escalated" {
			return 2
		}
		return verdict.ExitCode(out)
	case "verify-run":
		slice := fs.String("slice", "", "slice id")
		featurePlan := fs.String("plan", "", "feature id of a plan run (a2dlc plan run): checks the plan's signed approval or authorization")
		if fs.Parse(args[1:]) != nil || (*slice == "") == (*featurePlan == "") {
			fmt.Fprintln(os.Stderr, "a2dlc verify-run: exactly one of --slice or --plan is required")
			return 64
		}
		var problems []string
		var err error
		if *featurePlan != "" {
			problems, err = plan.VerifyRun(a.Repo.Dir, *featurePlan)
		} else {
			problems, err = a2run.VerifyRun(a, *slice)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc verify-run:", err)
			return 1
		}
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "a2dlc verify-run:", p)
		}
		if len(problems) > 0 {
			return 1
		}
		fmt.Println("a2dlc verify-run: ok")
		return 0
	case "verify":
		commit := fs.String("commit", "HEAD", "commit to verify")
		strict := fs.Bool("strict", false, "block on anything but a PASS receipt for this tree, as before the policy of evidence")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		if !*strict {
			rep, err := a.VerifyPolicy(*commit)
			if err != nil {
				fmt.Fprintln(os.Stderr, "a2dlc verify: error in a2dlc or git, this is not a verdict:", err)
				return 1
			}
			lines, blocked := rep.Lines()
			for _, l := range lines {
				fmt.Fprintln(os.Stderr, l)
			}
			if blocked {
				return 1
			}
			if len(lines) == 0 {
				fmt.Println("a2dlc verify: ok")
			}
			return 0
		}
		problems, err := a.Verify(*commit)
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc verify:", err)
			return 1
		}
		for _, p := range problems {
			fmt.Fprintln(os.Stderr, "a2dlc verify:", p)
		}
		if len(problems) > 0 {
			return 1
		}
		fmt.Println("a2dlc verify: ok")
		return 0
	case "approve":
		if len(args) >= 2 && args[1] == "--pending" {
			return approvePending(a, repo.Config("user.email"), args[2:])
		}
		if len(args) < 2 || strings.HasPrefix(args[1], "-") {
			fmt.Fprintln(os.Stderr, usage)
			return 64
		}
		key := fs.String("key", os.Getenv("A2DLC_SIGNING_KEY"), "private SSH key with passphrase")
		principal := fs.String("principal", repo.Config("user.email"), "identity in allowed_signers")
		if fs.Parse(args[2:]) != nil || *key == "" || *principal == "" {
			fmt.Fprintln(os.Stderr, "a2dlc approve: missing --key or --principal")
			return 64
		}
		if err := a.Approve(args[1], *key, *principal); err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc approve:", err)
			return 1
		}
		fmt.Println("a2dlc approve: signed", args[1])
		return 0
	case "authorize":
		key := fs.String("key", os.Getenv("A2DLC_SIGNING_KEY"), "private SSH key with passphrase")
		principal := fs.String("principal", repo.Config("user.email"), "identity in allowed_signers")
		base := fs.String("base", "", "commit the change is made on (default: HEAD); an authorization dies with its base")
		intent := fs.String("intent", "", "the intent file the run will be bound to (a2dlc run --intent), or one committed under .a2dlc/intents/")
		decision := fs.String("decision", "", "enforce_new_behavior or preserve_legacy; plan.accept (target plan::<feature_id>) accepts a feature plan to run")
		expires := fs.String("expires", "", "when the authorization stops covering anything, RFC 3339 (2026-11-12T00:00:00Z)")
		var targets list
		fs.Var(&targets, "target", "exact <path>::<symbol> (repeatable; no globs)")
		if fs.Parse(args[1:]) != nil || *key == "" || *principal == "" {
			fmt.Fprintln(os.Stderr, "a2dlc authorize: missing --key or --principal")
			return 64
		}
		au, err := a.Authorize(app.AuthorizeOptions{Key: *key, Principal: *principal, Base: *base, IntentFile: *intent,
			Decision: *decision, Expires: *expires, Targets: targets})
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc authorize:", err)
			return 1
		}
		fmt.Printf("a2dlc authorize: signed %d target(s) on base %s until %s\n", len(au.Targets), au.BaseRef[:12], au.ExpiresAt)
		return 0
	case "plan":
		return planCommand(ctx, a, repo, args[1:])
	case "propose":
		base := fs.String("base", "", "commit base (default: the upstream, else the fork point from origin's default branch, else HEAD)")
		out := fs.String("out", "", "empty folder outside the repository where the role's answer goes (required)")
		fromEsc := fs.Bool("from-escalations", false, "propose properties from the counterexamples of earlier runs (no role call)")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		if *out == "" {
			fmt.Fprintln(os.Stderr, "a2dlc propose: --out is required")
			return 64
		}
		if *fromEsc {
			if *base != "" {
				fmt.Fprintln(os.Stderr, "a2dlc propose: --from-escalations reads earlier runs, not a change: --base does not apply")
				return 64
			}
			doc, err := runProposeFromEscalations(a, *out, time.Now())
			if err != nil {
				fmt.Fprintln(os.Stderr, "a2dlc propose:", err)
				return 2
			}
			b, _ := json.MarshalIndent(doc, "", "  ")
			fmt.Println(string(b))
			return 0
		}
		res, err := runPropose(ctx, a, a2run.DockerLauncher{}, *base, *out)
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc propose:", err)
			return 2
		}
		b, err := json.MarshalIndent(res, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc propose:", err)
			return 2
		}
		fmt.Println(string(b))
		if res.Status != "ok" {
			return 1
		}
		return 0
	case "answer":
		return answerCommand(a, args[1:], os.Stderr)
	case "finding":
		return findingCommand(a, args[1:], os.Stdout, os.Stderr)
	case "journal":
		return journalCommand(a, args[1:], os.Stdout, os.Stderr)
	case "hook":
		if len(args) < 2 || (args[1] != "claude" && args[1] != "gemini") {
			fmt.Fprintln(os.Stderr, usage)
			return 64
		}
		return a.Hook(ctx, args[1], os.Stdin, os.Stdout, os.Stderr)
	case "install":
		var agents list
		fs.Var(&agents, "agent", "claude or gemini (repeatable)")
		noDerive := fs.Bool("no-derive-image", false, "do not build a Python engine image with the repo's dependencies")
		devDeps := fs.Bool("dev-deps", false, "also install the repo's dev requirements (requirements-dev.txt) in the derived engine image")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc install:", err)
			return 1
		}
		done, err := a.Install(exe, agents)
		for _, h := range done {
			fmt.Println("a2dlc install:", h)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc install:", err)
			return 1
		}
		derived, derivedWarnings := a.DeriveEngineImage(ctx, app.DeriveOptions{Skip: *noDerive, Dev: *devDeps})
		for _, m := range derived {
			fmt.Println("a2dlc install:", m)
		}
		for _, w := range derivedWarnings {
			fmt.Fprintln(os.Stderr, "a2dlc install: warning:", w)
		}
		if w, ok := a.CoverageWarning(); ok {
			fmt.Fprintln(os.Stderr, "a2dlc install: warning:", w)
		}
		if hint, ok := a.TestsHint(); ok {
			fmt.Fprintln(os.Stderr, "a2dlc install: warning:", hint.Warning)
			if hint.Stub != "" {
				fmt.Println(hint.Stub)
			}
		}
		fmt.Println("a2dlc install: next, run a2dlc doctor (--json for an agent) to see what is left to fix")
		return 0
	case "doctor":
		exe, err := os.Executable()
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc doctor:", err)
			return 2
		}
		return doctorCommand(ctx, a, exe, args[1:], os.Stdout, os.Stderr)
	case "mcp":
		if err := mcp.Serve(ctx, a, os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc mcp:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(os.Stderr, usage)
	return 64
}

// warnHookBinary tells check and verify when the pre-push hook is pinned to another build than the one running.
func warnHookBinary(a app.App, w io.Writer) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if msg := a.HookBinaryWarning(exe); msg != "" {
		fmt.Fprintln(w, "a2dlc: warning:", msg)
	}
}

// approvePending is `a2dlc approve --pending`: the batch of RFC-0007 Part A. The list is for the person; --json is the same
// list for an agent, compact and without signing. The confirmation reads a line from stdin; the key's passphrase is asked by
// ssh-keygen once for the whole batch.
func approvePending(a app.App, email string, args []string) int {
	fs := flag.NewFlagSet("approve --pending", flag.ContinueOnError)
	key := fs.String("key", os.Getenv("A2DLC_SIGNING_KEY"), "private SSH key with passphrase")
	principal := fs.String("principal", email, "identity in allowed_signers")
	asJSON := fs.Bool("json", false, "list what waits, for an agent or a UI, and sign nothing")
	yes := fs.Bool("yes", false, "do not ask for confirmation (the key's passphrase is still asked)")
	if fs.Parse(args) != nil {
		return 64
	}
	r, err := a.Pending()
	if err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc approve --pending:", err)
		return 1
	}
	if *asJSON {
		json.NewEncoder(os.Stdout).Encode(r)
		return 0
	}
	fmt.Println(r.Text())
	if r.Count == 0 {
		return 0
	}
	if *key == "" || *principal == "" {
		fmt.Fprintln(os.Stderr, "a2dlc approve --pending: missing --key or --principal")
		return 64
	}
	if !*yes {
		fmt.Printf("\nSign these %d approval(s) as %s? [y/N] ", r.Count, *principal)
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if ans := strings.ToLower(strings.TrimSpace(line)); ans != "y" && ans != "yes" {
			fmt.Println("Nothing signed.")
			return 1
		}
	}
	if err := a.ApprovePending(r, *key, *principal); err != nil {
		fmt.Fprintln(os.Stderr, "a2dlc approve --pending:", err)
		return 1
	}
	fmt.Printf("a2dlc approve: signed %d approval(s); run a2dlc check again so the receipt sees them.\n", r.Count)
	return 0
}
