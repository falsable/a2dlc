// a2dlc: differential gate of the A2-DLC Lite mode. See the README and ADR meta/0004.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	"github.com/falsable/a2dlc/internal/engine"
	"github.com/falsable/a2dlc/internal/gitx"
	"github.com/falsable/a2dlc/internal/mcp"
	a2run "github.com/falsable/a2dlc/internal/run"
	"github.com/falsable/a2dlc/internal/verdict"
)

// version is set by the release with -ldflags "-X main.version=<tag>".
var version = "0.1.0-dev"

const usage = `usage: a2dlc <command>
  check   [--base <ref>] [--peer <ref>]... [--tests-overlay <dir>] [--require-signatures]
          runs the engines and the test gates over what changed (base: the upstream, else the fork point from
          origin's default branch, else HEAD) and writes .a2dlc/verdict.json (0 PASS, 1 FAIL, 2 INCONCLUSIVE). --peer: tree of another builder of the same change;
          --tests-overlay: tests from outside that run over base, candidate and peers without being committed
  propose [--base <ref>] --out <empty dir outside the repo>
          asks the propose_properties role (roles.json) for the properties of the change from base to the working tree;
          prints the outcome as JSON (0 ok, 1 otherwise). Nothing is written into the repo; the answer goes to --out.
  run     --slice <id> [--intent <intent.json>] [--mode lite|refactor|greenfield|brownfield_facade] [--base <ref>]
          [--max-cost-usd <n>] [--max-rounds <n>] [--max-inner <n>] [--max-spec-rounds <n>] [--z3-image <img>]
          runs (or resumes) the protocol's state machine over a slice: Stages -1..5, roles from .a2dlc/roles.json
          in their own containers, state and ledger in .a2dlc/run/<slice>/ (0 PASS, 1 FAIL, 2 INCONCLUSIVE or escalated).
          --intent: the Stage 0 (goal, criticality, critical_paths, budget); the run is bound to that file.
          Limits without a flag come from the intent's budget, or the default: --max-rounds (Saboteur rounds, 3),
          --max-inner (Tier 0 loop, 5), --max-cost-usd (CB-03, no cap; on resume, raises the saved cap)
          --max-spec-rounds: Specifier α/β and Inquisitor rounds in Stage 1 before escalating (2 by default)
          --z3-image: z3 engine of Stage 1.5 (a2dlc-engine-z3:0.1 by default; empty leaves it skipped)
  verify-run --slice <id>          recalculates the ledger chain and the run's artifacts (0 passes, 1 does not)
  verify  [--commit HEAD]          checks the receipt without running engines (0 passes, 1 does not)
  approve <target> --key <key> [--principal <id>]   signs a target's waiver (run by a person)
  answer  --slice <id> --probe <id> --choice <c> --reason <text> --by <name>
          records a person's answer to a probe of a run (answers.json; 0 written, 2 refused, 64 bad usage)
  hook    <claude|gemini>          adapter for the agent's end-of-turn hook
  install [--agent claude] [--agent gemini]   config, .gitignore, pre-push and agent hooks
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
		Checks: engine.DockerChecks{}, Version: version, Now: time.Now}
	ctx := context.Background()
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	switch args[0] {
	case "check":
		base := fs.String("base", "", "commit base (default: the upstream, else the fork point from origin's default branch, else HEAD)")
		signatures := fs.Bool("require-signatures", false, "fails if a waiver used has no valid signature (for CI)")
		var peers list
		fs.Var(&peers, "peer", "commit of another builder of the same change (repeatable)")
		overlay := fs.String("tests-overlay", "", "folder with tests from outside, at the path they would have in the repo")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		v, err := a.CheckWith(ctx, *base, app.CheckOptions{Peers: peers, TestsOverlay: *overlay})
		if err != nil {
			fmt.Fprintln(os.Stderr, "a2dlc check:", err)
			return 2
		}
		covered, without := 0, 0
		if v.Coverage != nil {
			for _, f := range v.Coverage.Files {
				covered += len(f.ByEngine) + len(f.Covered)
				without += len(f.Uncovered)
			}
		}
		fmt.Printf("%s: %d targets, %d tests judged, %d out of scope, %d changed lines examined and %d not examined, %d waivers pending, %d engines with errors\n",
			v.Verdict, len(v.Targets), len(v.Tests), len(v.OutOfScope), covered, without, len(v.PendingWaivers), len(v.EngineErrors))
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
		for _, s := range st.Stages {
			reason := ""
			if s.Reason != "" {
				reason = ": " + s.Reason
			}
			fmt.Printf("  stage %-4s %-9s%s\n", s.ID, s.Status, reason)
		}
		out := "no verdict"
		if st.Outcome != nil {
			out = *st.Outcome
		}
		fmt.Printf("%s: run %s, slice %s, %.4f USD reported (%d roles without a report)\n", out, st.RunID, st.SliceID, st.Budget.SpentUSD, st.Budget.UnreportedRoles)
		if out == "escalated" {
			return 2
		}
		return verdict.ExitCode(out)
	case "verify-run":
		slice := fs.String("slice", "", "slice id")
		if fs.Parse(args[1:]) != nil || *slice == "" {
			fmt.Fprintln(os.Stderr, "a2dlc verify-run: missing --slice")
			return 64
		}
		problems, err := a2run.VerifyRun(a, *slice)
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
		if fs.Parse(args[1:]) != nil {
			return 64
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
	case "propose":
		base := fs.String("base", "", "commit base (default: the upstream, else the fork point from origin's default branch, else HEAD)")
		out := fs.String("out", "", "empty folder outside the repository where the role's answer goes (required)")
		if fs.Parse(args[1:]) != nil {
			return 64
		}
		if *out == "" {
			fmt.Fprintln(os.Stderr, "a2dlc propose: --out is required")
			return 64
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
	case "hook":
		if len(args) < 2 || (args[1] != "claude" && args[1] != "gemini") {
			fmt.Fprintln(os.Stderr, usage)
			return 64
		}
		return a.Hook(ctx, args[1], os.Stdin, os.Stdout, os.Stderr)
	case "install":
		var agents list
		fs.Var(&agents, "agent", "claude or gemini (repeatable)")
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
		return 0
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
