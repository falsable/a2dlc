package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
)

// Exe is the a2dlc binary the agent tools (run, plan run, approve --pending, answer) launch. They run the CLI itself, in
// the repository, with the same flags, so what an agent gets over MCP is what a person gets in a terminal: the same
// stdout, the same stderr and the same exit code, by construction. Tests point it at their own binary.
var Exe = os.Executable

// The result of a tool that ran the CLI. Json is stdout when it is a JSON document (run and plan run print their state,
// approve --pending its list); the exit code is the table of docs/COMPATIBILITY.md. isError is exit_code != 0.
type cliResult struct {
	ExitCode int             `json:"exit_code"`
	Stdout   string          `json:"stdout"`
	Stderr   string          `json:"stderr"`
	JSON     json.RawMessage `json:"json,omitempty"`
}

// refusal is what a tool answers when it will not launch the CLI: machine-readable, with the exit code the CLI uses for
// bad usage. No tool asks a person anything: a question the CLI would put on a terminal is refused here.
func refusal(code, msg string) callToolResult {
	b, _ := json.MarshalIndent(map[string]any{"error": map[string]any{"code": code, "message": msg, "exit_code": 64}}, "", "  ")
	return callToolResult{Content: []contentBlock{{Type: "text", Text: string(b)}}, IsError: true}
}

// agentFlag is one argument of a tool and the CLI flag it becomes. kind: s string, i non-negative integer.
type agentFlag struct {
	key, flag string
	kind      byte
	required  bool
}

type agentTool struct {
	name, description string
	command           []string
	fixed             []string
	budget            bool // max_cost_usd is required and becomes --max-cost-usd
	flags             []agentFlag
}

var agentTools = []agentTool{
	{name: "a2dlc_run", command: []string{"run"}, fixed: []string{"--json"}, budget: true,
		description: "Run (or resume) the protocol's state machine over a slice, as `a2dlc run --json`: Stages -1..5 with the roles of .a2dlc/roles.json in their own containers. max_cost_usd is required: it is the cost cap of the run (CB-03), and the tool refuses without it. Returns exit_code (0 PASS, 1 FAIL, 2 INCONCLUSIVE or escalated to a person, 64 bad usage), stdout, stderr and json (the run's state, contracts/run-state.schema.json). A run that escalated is resolved by a person (a2dlc_answer, a2dlc_approve_pending lists what waits for a signature) and resumed with the same call. isError is true unless the run passed.",
		flags: []agentFlag{
			{"slice", "slice", 's', true}, {"intent", "intent", 's', false}, {"mode", "mode", 's', false}, {"base", "base", 's', false},
			{"max_rounds", "max-rounds", 'i', false}, {"max_inner", "max-inner", 'i', false}, {"max_spec_rounds", "max-spec-rounds", 'i', false},
			{"z3_image", "z3-image", 's', false}}},
	{name: "a2dlc_plan_run", command: []string{"plan", "run"}, fixed: []string{"--json"}, budget: true,
		description: "Run (or resume) a valid feature plan, as `a2dlc plan run --json`: each slice an a2dlc run in its own worktree, then a final check of the merge. max_cost_usd is required and is the cost cap of each slice's run (the plan machine has no cap of its own); the tool refuses without it. Returns exit_code (0 PASS, 2 escalated, 1 plan refused, 64 bad usage), stdout, stderr and json (the plan's state, contracts/plan-state.schema.json). isError is true unless the plan passed.",
		flags: []agentFlag{
			{"plan", "plan", 's', true}, {"intent", "intent", 's', true}, {"base", "base", 's', false}, {"jobs", "jobs", 'i', false},
			{"max_replan", "max-replan", 'i', false}, {"rounds", "rounds", 'i', false}, {"z3_image", "z3-image", 's', false}}},
	{name: "a2dlc_approve_pending", command: []string{"approve", "--pending"}, fixed: []string{"--json"},
		description: "List every waiver of the current receipt that waits for a signature, grouped by the reason it gives, as `a2dlc approve --pending --json`. It signs nothing and takes no argument: signing is a person's act with their own key (`a2dlc approve --pending` in a terminal), and a tool call that tries to pass key, principal or yes is refused with code signing_requires_a_person. Returns exit_code, stdout, stderr and json (the list). Read-only.",
		flags:       nil},
	{name: "a2dlc_answer", command: []string{"answer"},
		description: "Record a person's answer to a probe of a run, as `a2dlc answer`: writes answers.json in the run's folder, and a2dlc_run resumes with it. The answer carries who gave it (by) and why (reason); the tool records what it is given and authenticates nobody, so call it only with an answer a person gave. Returns exit_code (0 recorded, 2 the run or the probe is not answerable, 64 bad usage), stdout and stderr.",
		flags: []agentFlag{{"slice", "slice", 's', true}, {"probe", "probe", 's', true}, {"choice", "choice", 's', true},
			{"reason", "reason", 's', true}, {"by", "by", 's', true}}},
}

func (t agentTool) def() toolDef {
	props := map[string]any{}
	var required []string
	for _, f := range t.flags {
		typ := "string"
		if f.kind == 'i' {
			typ = "integer"
		}
		props[f.key] = map[string]any{"type": typ, "description": "The --" + f.flag + " flag of the CLI."}
		if f.required {
			required = append(required, f.key)
		}
	}
	if t.budget {
		props["max_cost_usd"] = map[string]any{"type": "number", "exclusiveMinimum": 0, "description": "Required. Cost cap in USD (--max-cost-usd), above zero: 0 would mean no cap in the CLI, and a tool never runs without one."}
		required = append(required, "max_cost_usd")
	}
	schema := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		sort.Strings(required)
		schema["required"] = required
	}
	return toolDef{Name: t.name, Description: t.description, InputSchema: schema}
}

func agentToolDefs() []toolDef {
	var out []toolDef
	for _, t := range agentTools {
		out = append(out, t.def())
	}
	return out
}

// callAgentTool runs the tool named name; ok is false when name is not an agent tool.
func callAgentTool(ctx context.Context, a app.App, name string, args map[string]any) (callToolResult, bool) {
	for _, t := range agentTools {
		if t.name == name {
			return t.call(ctx, a, args), true
		}
	}
	return callToolResult{}, false
}

func (t agentTool) call(ctx context.Context, a app.App, args map[string]any) callToolResult {
	known := map[string]agentFlag{}
	for _, f := range t.flags {
		known[f.key] = f
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		switch {
		case t.name == "a2dlc_approve_pending" && (k == "key" || k == "principal" || k == "yes"):
			return refusal("signing_requires_a_person", "a2dlc_approve_pending only lists what waits for a signature; a person signs with their own key in a terminal (a2dlc approve --pending)")
		case k == "max_cost_usd" && t.budget:
		default:
			if _, ok := known[k]; !ok {
				return refusal("unknown_argument", fmt.Sprintf("%s takes no argument %q", t.name, k))
			}
		}
	}
	cli := append([]string{}, t.command...)
	if t.budget {
		raw, ok := args["max_cost_usd"]
		if !ok {
			return refusal("max_cost_usd_required", t.name+" needs max_cost_usd: the cost cap in USD, above zero. An agent run never goes without a budget")
		}
		n, ok := raw.(float64)
		if !ok || math.IsNaN(n) || math.IsInf(n, 0) || n <= 0 {
			return refusal("max_cost_usd_invalid", fmt.Sprintf("max_cost_usd must be a number above zero, got %v", raw))
		}
		cli = append(cli, "--max-cost-usd="+strconv.FormatFloat(n, 'f', -1, 64))
	}
	for _, f := range t.flags {
		raw, present := args[f.key]
		if !present {
			if f.required {
				return refusal("missing_argument", fmt.Sprintf("%s needs %s", t.name, f.key))
			}
			continue
		}
		switch f.kind {
		case 's':
			s, ok := raw.(string)
			if !ok {
				return refusal("invalid_argument", fmt.Sprintf("%s must be a string", f.key))
			}
			if f.required && strings.TrimSpace(s) == "" {
				return refusal("missing_argument", fmt.Sprintf("%s must not be empty", f.key))
			}
			cli = append(cli, "--"+f.flag+"="+s)
		case 'i':
			n, ok := raw.(float64)
			if !ok || n != math.Trunc(n) || n < 0 || n > 1e9 {
				return refusal("invalid_argument", fmt.Sprintf("%s must be a non-negative integer", f.key))
			}
			cli = append(cli, "--"+f.flag+"="+strconv.Itoa(int(n)))
		}
	}
	return runCLI(ctx, a, append(cli, t.fixed...))
}

// runCLI launches the binary in the repository with no stdin: whatever the CLI would ask on a terminal reads end of
// input and fails there instead of waiting for a person.
func runCLI(ctx context.Context, a app.App, args []string) callToolResult {
	exe, err := Exe()
	if err != nil {
		return errorResult("cannot find the a2dlc binary: " + err.Error())
	}
	cmd := exec.CommandContext(ctx, exe, args...)
	cmd.Dir = a.Repo.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	res := cliResult{}
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			return errorResult("cannot run a2dlc " + strings.Join(args, " ") + ": " + err.Error())
		}
		res.ExitCode = ee.ExitCode()
	}
	res.Stdout, res.Stderr = stdout.String(), stderr.String()
	if trimmed := bytes.TrimSpace(stdout.Bytes()); json.Valid(trimmed) && len(trimmed) > 0 && trimmed[0] == '{' {
		res.JSON = json.RawMessage(trimmed)
	}
	b, _ := json.MarshalIndent(res, "", "  ")
	return callToolResult{Content: []contentBlock{{Type: "text", Text: string(b)}}, IsError: res.ExitCode != 0}
}
