// Package mcp implements the Model Context Protocol (MCP) JSON-RPC 2.0 stdio server for A2-DLC.
package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/falsable/a2dlc/internal/app"
	"github.com/falsable/a2dlc/internal/contract"
	a2run "github.com/falsable/a2dlc/internal/run"
)

const ProtocolVersion = "2024-11-05"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type callToolParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type callToolResult struct {
	Content []contentBlock `json:"content"`
	IsError bool           `json:"isError,omitempty"`
}

// Serve runs the JSON-RPC 2.0 MCP server over in/out until EOF.
func Serve(ctx context.Context, a app.App, in io.Reader, out io.Writer) error {
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	enc := json.NewEncoder(out)

	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// Skip HTTP-like Content-Length headers if a client sends LSP-style framing.
		if bytes.HasPrefix(line, []byte("Content-Length:")) {
			continue
		}
		var req rpcRequest
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(rpcResponse{
				JSONRPC: "2.0",
				ID:      json.RawMessage("null"),
				Error:   &rpcError{Code: -32700, Message: "parse error: " + err.Error()},
			})
			continue
		}
		// Notifications have no ID and expect no response.
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue
		}
		resp := handleRequest(ctx, a, req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func handleRequest(ctx context.Context, a app.App, req rpcRequest) rpcResponse {
	switch req.Method {
	case "initialize":
		ver := a.Version
		if ver == "" {
			ver = "0.3.0"
		}
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities": map[string]any{
					"tools": map[string]any{},
				},
				"serverInfo": map[string]any{
					"name":    "a2dlc-mcp",
					"version": ver,
				},
			},
		}
	case "ping":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}
	case "tools/list":
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": toolsCatalog(),
			},
		}
	case "tools/call":
		var params callToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return rpcResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error:   &rpcError{Code: -32602, Message: "invalid params: " + err.Error()},
			}
		}
		res := dispatchTool(ctx, a, params)
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  res,
		}
	default:
		return rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error:   &rpcError{Code: -32601, Message: "method not found: " + req.Method},
		}
	}
}

func toolsCatalog() []toolDef {
	return []toolDef{
		{
			Name:        "a2dlc_status",
			Description: "Inspect the current A2-DLC verification state (.a2dlc/verdict.json, .a2dlc/escalations.json, .a2dlc/waivers.json) and tree_hash validity without launching Docker containers.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		{
			Name:        "a2dlc_check",
			Description: "Run the A2-DLC differential and property verification gate over changed files in the repository and write .a2dlc/verdict.json.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"base": map[string]any{
						"type":        "string",
						"description": "Optional git base ref to compare against (default: upstream or HEAD).",
					},
				},
			},
		},
		{
			Name:        "a2dlc_verify",
			Description: "Verify in <50ms that .a2dlc/verdict.json is PASS, matches the current git tree_hash, and has valid SSH signatures for any waivers.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"commit": map[string]any{
						"type":        "string",
						"description": "Commit ref to verify (default: HEAD).",
					},
				},
			},
		},
		{
			Name:        "a2dlc_verify_run",
			Description: "Verify the SHA-256 hash-chained isolation ledger (ledger.json) and Stage -1..7 artifacts for an autonomous A2-DLC slice run.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slice": map[string]any{
						"type":        "string",
						"description": "Slice identifier under .a2dlc/run/<slice>/.",
					},
				},
				"required": []string{"slice"},
			},
		},
		{
			Name:        "a2dlc_intent_from_ticket",
			Description: "Convert a ticket or PRD fetched from Jira, Linear, GitHub Issues, GitLab, or Confluence (via Composio or vendor MCP servers) into a schema-validated A2-DLC Stage 0 intent.json file.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"slice_id": map[string]any{
						"type":        "string",
						"description": "Slice identifier (e.g. 'linear.eng-42' or 'jira.pay-104'; lowercase letters, digits, '.', '_', '-').",
					},
					"goal": map[string]any{
						"type":        "string",
						"description": "Ticket title and goal in natural language.",
					},
					"mode": map[string]any{
						"type":        "string",
						"description": "Execution mode: 'greenfield', 'refactor', 'brownfield_facade', or 'lite' (default: 'greenfield').",
					},
					"criticality": map[string]any{
						"type":        "string",
						"description": "Criticality level: 'low', 'medium', 'high', or 'critical' (default: 'medium').",
					},
					"invariants": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Business invariants extracted from the ticket/PRD.",
					},
					"acceptance": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Acceptance criteria from the Jira/Linear/GitHub ticket.",
					},
					"non_goals": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Explicit non-goals for this slice.",
					},
					"critical_paths": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Path globs that raise criticality if touched.",
					},
					"max_cost_usd": map[string]any{
						"type":        "number",
						"description": "Maximum USD budget cap for the autonomous run (default: 2.0).",
					},
				},
				"required": []string{"slice_id", "goal"},
			},
		},
		{
			Name:        "a2dlc_format_report",
			Description: "Format the current .a2dlc/verdict.json, escalations, and waivers as a Markdown compliance & counterexample report ready to post to GitHub PRs, GitLab MRs, Jira, Linear, Slack, or Confluence.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}

func dispatchTool(ctx context.Context, a app.App, params callToolParams) callToolResult {
	switch params.Name {
	case "a2dlc_status":
		return toolStatus(a)
	case "a2dlc_check":
		base, _ := params.Arguments["base"].(string)
		v, err := a.CheckWith(ctx, base, app.CheckOptions{})
		if err != nil {
			return errorResult("a2dlc check failed: " + err.Error())
		}
		b, _ := json.MarshalIndent(v, "", "  ")
		return callToolResult{
			Content: []contentBlock{{Type: "text", Text: string(b)}},
			IsError: v.Verdict != "PASS",
		}
	case "a2dlc_verify":
		commit, _ := params.Arguments["commit"].(string)
		if strings.TrimSpace(commit) == "" {
			commit = "HEAD"
		}
		problems, err := a.Verify(commit)
		if err != nil {
			return errorResult("a2dlc verify error: " + err.Error())
		}
		payload := map[string]any{
			"commit":   commit,
			"valid":    len(problems) == 0,
			"problems": problems,
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		return callToolResult{
			Content: []contentBlock{{Type: "text", Text: string(b)}},
			IsError: len(problems) > 0,
		}
	case "a2dlc_verify_run":
		slice, _ := params.Arguments["slice"].(string)
		if strings.TrimSpace(slice) == "" {
			return errorResult("missing required argument: slice")
		}
		problems, err := a2run.VerifyRun(a, slice)
		if err != nil {
			return errorResult("a2dlc verify-run error: " + err.Error())
		}
		payload := map[string]any{
			"slice":    slice,
			"valid":    len(problems) == 0,
			"problems": problems,
		}
		b, _ := json.MarshalIndent(payload, "", "  ")
		return callToolResult{
			Content: []contentBlock{{Type: "text", Text: string(b)}},
			IsError: len(problems) > 0,
		}
	case "a2dlc_intent_from_ticket":
		return toolIntentFromTicket(a, params.Arguments)
	case "a2dlc_format_report":
		return toolFormatReport(a)
	default:
		return errorResult("unknown tool: " + params.Name)
	}
}

func toolStatus(a app.App) callToolResult {
	out := map[string]any{
		"repository": a.Repo.Dir,
	}
	readJSON := func(rel string) any {
		p := filepath.Join(a.Repo.Dir, filepath.FromSlash(rel))
		b, err := os.ReadFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return map[string]string{"error": err.Error()}
		}
		var v any
		if err := json.Unmarshal(b, &v); err != nil {
			return map[string]string{"error": fmt.Sprintf("invalid JSON in %s: %v", rel, err)}
		}
		return v
	}
	out["verdict"] = readJSON(".a2dlc/verdict.json")
	out["escalations"] = readJSON(".a2dlc/escalations.json")
	out["waivers"] = readJSON(".a2dlc/waivers.json")
	problems, err := a.Verify("HEAD")
	if err != nil {
		out["head_verify_error"] = err.Error()
	} else {
		out["head_verified"] = len(problems) == 0
		out["head_problems"] = problems
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return callToolResult{
		Content: []contentBlock{{Type: "text", Text: string(b)}},
	}
}

func stringListArg(args map[string]any, key string, fallback []string) []string {
	raw, ok := args[key]
	if !ok || raw == nil {
		return fallback
	}
	arr, ok := raw.([]any)
	if !ok {
		return fallback
	}
	var out []string
	for _, item := range arr {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	if len(out) == 0 {
		return fallback
	}
	return out
}

func toolIntentFromTicket(a app.App, args map[string]any) callToolResult {
	sliceID, _ := args["slice_id"].(string)
	sliceID = strings.ToLower(strings.TrimSpace(sliceID))
	goal, _ := args["goal"].(string)
	goal = strings.TrimSpace(goal)
	if sliceID == "" || goal == "" {
		return errorResult("missing required arguments: slice_id and goal")
	}
	mode, _ := args["mode"].(string)
	if mode == "" {
		mode = contract.ModeGreenfield
	}
	crit, _ := args["criticality"].(string)
	if crit == "" {
		crit = contract.CriticalityMedium
	}
	costCap := 2.0
	if c, ok := args["max_cost_usd"].(float64); ok && c > 0 {
		costCap = c
	}
	maxMutants := 30
	intent := contract.Intent{
		SchemaVersion: contract.SchemaVersion,
		SliceID:       sliceID,
		Mode:          mode,
		Goal:          goal,
		Invariants:    stringListArg(args, "invariants", []string{"Preserve existing public contract invariants"}),
		NonGoals:      stringListArg(args, "non_goals", []string{"Unrelated module modifications"}),
		Criticality:   crit,
		Budget: &contract.IntentBudget{
			MaxCostUSD:                &costCap,
			MaxInnerIterations:        5,
			MaxTier1AdversarialRounds: 3,
			MaxMutants:                &maxMutants,
		},
		CriticalPaths: stringListArg(args, "critical_paths", []string{}),
		Acceptance:    stringListArg(args, "acceptance", []string{goal}),
	}
	b, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return errorResult("marshal intent: " + err.Error())
	}
	var checked contract.Intent
	if err := contract.DecodeStrictBytes(b, &checked); err != nil {
		return errorResult("intent validation failed: " + err.Error())
	}
	relPath := filepath.ToSlash(filepath.Join(".a2dlc", "intents", sliceID+".intent.json"))
	absPath := filepath.Join(a.Repo.Dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return errorResult("mkdir intents: " + err.Error())
	}
	if err := os.WriteFile(absPath, append(b, '\n'), 0o644); err != nil {
		return errorResult("write intent: " + err.Error())
	}
	resp := map[string]any{
		"status":      "ok",
		"intent_path": relPath,
		"run_command": fmt.Sprintf("a2dlc run --slice %s --intent %s", sliceID, relPath),
		"intent":      checked,
	}
	outBytes, _ := json.MarshalIndent(resp, "", "  ")
	return callToolResult{
		Content: []contentBlock{{Type: "text", Text: string(outBytes)}},
	}
}

func toolFormatReport(a app.App) callToolResult {
	verdictPath := filepath.Join(a.Repo.Dir, ".a2dlc", "verdict.json")
	b, err := os.ReadFile(verdictPath)
	if err != nil {
		return errorResult("cannot read .a2dlc/verdict.json (run a2dlc_check first): " + err.Error())
	}
	var v contract.Verdict
	if err := json.Unmarshal(b, &v); err != nil {
		return errorResult("invalid .a2dlc/verdict.json: " + err.Error())
	}
	var sb strings.Builder
	badge := "✅ PASS"
	if v.Verdict == contract.FAIL {
		badge = "❌ FAIL"
	} else if v.Verdict == contract.INCONCLUSIVE {
		badge = "⚠️ INCONCLUSIVE"
	}
	fmt.Fprintf(&sb, "## 🛡️ A2-DLC Verification Receipt: **%s**\n\n", badge)
	fmt.Fprintf(&sb, "- **Base Ref:** `%s`\n", v.BaseRef)
	fmt.Fprintf(&sb, "- **Bound Tree Hash:** `%s`\n", v.TreeHash)
	fmt.Fprintf(&sb, "- **Targets Judged:** `%d`  •  **Tests Judged:** `%d`  •  **Out of Scope:** `%d`\n\n", len(v.Targets), len(v.Tests), len(v.OutOfScope))

	if len(v.Targets) > 0 {
		sb.WriteString("| Target Symbol | Engine | Verdict | Counterexample / Details |\n")
		sb.WriteString("| :--- | :--- | :--- | :--- |\n")
		for _, t := range v.Targets {
			detail := t.Reason
			if t.Counterexample != nil {
				detail = fmt.Sprintf("`args=%s` → `old=%s`, `new=%s`", t.Counterexample.Args, t.Counterexample.Old, t.Counterexample.New)
			}
			fmt.Fprintf(&sb, "| `%s` | `%s` | **%s** | %s |\n", t.Target, t.Engine, t.Verdict, detail)
		}
		sb.WriteString("\n")
	}
	return callToolResult{
		Content: []contentBlock{{Type: "text", Text: sb.String()}},
	}
}

func errorResult(msg string) callToolResult {
	return callToolResult{
		Content: []contentBlock{{Type: "text", Text: msg}},
		IsError: true,
	}
}
