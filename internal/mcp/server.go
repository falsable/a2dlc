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
						"description": "Optional git base ref to compare against (default: upstream, else the fork point from origin's default branch, else HEAD).",
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
			Name:        "a2dlc_intent_from_signal",
			Description: "Close the loop from Stage 7 to Stage 0: turn a Rollback Sentinel alert (an error-rate, p99 latency or runtime-invariant breach on a canary ring of a delivered slice) into a schema-validated bugfix intent in .a2dlc/intents/<slice_id>.intent.json. Invariants, criticality, mode, boundary, SLAs, delivery and the budget cap are inherited from the alerted slice's intent when it exists; the run it starts still stops at probes, circuit breakers and signed waivers.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"from_slice": map[string]any{
						"type":        "string",
						"description": "Slice whose delivery raised the alert (its intent at .a2dlc/intents/<from_slice>.intent.json is inherited when present).",
					},
					"slice_id": map[string]any{
						"type":        "string",
						"description": "Identifier of the new bugfix slice (default: '<from_slice>.signal-<metric>').",
					},
					"metric": map[string]any{
						"type":        "string",
						"description": "Sentinel metric that breached: 'error_rate_delta', 'latency_p99_ms' or 'invariant'.",
					},
					"observed": map[string]any{
						"type":        "number",
						"description": "Observed value on the ring (for 'invariant': number of violations seen).",
					},
					"threshold": map[string]any{
						"type":        "number",
						"description": "Threshold that was breached. When the alerted slice's intent declares delivery.sentinel for that metric, the declared value is used and this one is ignored.",
					},
					"ring": map[string]any{
						"type":        "integer",
						"description": "Canary ring percentage where the sentinel fired (default: 1).",
					},
					"invariant": map[string]any{
						"type":        "string",
						"description": "For metric 'invariant': the runtime invariant that was violated.",
					},
					"max_cost_usd": map[string]any{
						"type":        "number",
						"description": "Budget cap for the bugfix run. It may lower the inherited cap, never raise it (default when nothing is inherited: 2.0).",
					},
				},
				"required": []string{"from_slice", "metric", "observed"},
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
	case "a2dlc_intent_from_signal":
		return toolIntentFromSignal(a, params.Arguments)
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
	relPath, checked, err := writeIntent(a, intent)
	if err != nil {
		return errorResult(err.Error())
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

// writeIntent validates an intent against the schema (strict decode of its own JSON) and writes it under
// .a2dlc/intents; it returns the repo-relative path. A malformed intent never reaches disk.
func writeIntent(a app.App, intent contract.Intent) (string, contract.Intent, error) {
	b, err := json.MarshalIndent(intent, "", "  ")
	if err != nil {
		return "", contract.Intent{}, fmt.Errorf("marshal intent: %w", err)
	}
	var checked contract.Intent
	if err := contract.DecodeStrictBytes(b, &checked); err != nil {
		return "", contract.Intent{}, fmt.Errorf("intent validation failed: %w", err)
	}
	relPath := filepath.ToSlash(filepath.Join(".a2dlc", "intents", intent.SliceID+".intent.json"))
	absPath := filepath.Join(a.Repo.Dir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return "", contract.Intent{}, fmt.Errorf("mkdir intents: %w", err)
	}
	if err := os.WriteFile(absPath, append(b, '\n'), 0o644); err != nil {
		return "", contract.Intent{}, fmt.Errorf("write intent: %w", err)
	}
	return relPath, checked, nil
}

const (
	signalErrorRate = "error_rate_delta"
	signalLatency   = "latency_p99_ms"
	signalInvariant = "invariant"
)

// toolIntentFromSignal is the Stage 7 → Stage 0 loop (protocol §5, ADLC "production signals create new intent"):
// a Rollback Sentinel alert becomes a bugfix intent for a new slice. What the loop never drops: the budget cap
// (inherited, given, or the 2.0 USD default), the invariants and the boundary of the alerted slice, and the human
// stops of the run it starts (probes, circuit breakers, signed waivers). An alert is text from production, so it is
// data: it only fills the goal and the acceptance; it cannot widen the boundary or raise the budget by itself.
func toolIntentFromSignal(a app.App, args map[string]any) callToolResult {
	from, _ := args["from_slice"].(string)
	from = strings.ToLower(strings.TrimSpace(from))
	metric, _ := args["metric"].(string)
	metric = strings.TrimSpace(metric)
	observed, hasObserved := args["observed"].(float64)
	if from == "" || metric == "" || !hasObserved {
		return errorResult("missing required arguments: from_slice, metric and observed")
	}
	if metric != signalErrorRate && metric != signalLatency && metric != signalInvariant {
		return errorResult("metric must be 'error_rate_delta', 'latency_p99_ms' or 'invariant'")
	}
	invariant, _ := args["invariant"].(string)
	invariant = strings.TrimSpace(invariant)
	if metric == signalInvariant && invariant == "" {
		return errorResult("metric 'invariant' needs the violated invariant text in 'invariant'")
	}
	ring := 1
	if r, ok := args["ring"].(float64); ok && r >= 1 && r <= 100 {
		ring = int(r)
	}
	sliceID, _ := args["slice_id"].(string)
	sliceID = strings.ToLower(strings.TrimSpace(sliceID))
	if sliceID == "" {
		sliceID = from + ".signal-" + strings.ReplaceAll(metric, "_", "-")
	}
	if sliceID == from {
		return errorResult("slice_id must differ from from_slice: the alert does not rewrite the alerted slice's intent")
	}
	if _, err := os.Stat(filepath.Join(a.Repo.Dir, ".a2dlc", "intents", sliceID+".intent.json")); err == nil {
		return errorResult("an intent already exists for slice " + sliceID + ": an alert never overwrites an intent; pass another slice_id")
	}

	// The alerted slice's intent, when it exists, is the source of everything the alert must not decide.
	var source *contract.Intent
	srcPath := filepath.Join(a.Repo.Dir, ".a2dlc", "intents", from+".intent.json")
	if b, err := os.ReadFile(srcPath); err == nil {
		var s contract.Intent
		if err := contract.DecodeStrictBytes(b, &s); err != nil {
			return errorResult("the alerted slice's intent is invalid (" + filepath.ToSlash(filepath.Join(".a2dlc", "intents", from+".intent.json")) + "): " + err.Error())
		}
		source = &s
	}

	// The declared sentinel wins over the payload's threshold: an alert cannot loosen the number the fix is judged by.
	threshold, hasThreshold := args["threshold"].(float64)
	payloadThresholdIgnored := false
	if source != nil && source.Delivery != nil && source.Delivery.Sentinel != nil {
		var declared *float64
		switch metric {
		case signalErrorRate:
			declared = source.Delivery.Sentinel.ErrorRateDeltaMax
		case signalLatency:
			if v := source.Delivery.Sentinel.LatencyP99MsMax; v != nil {
				f := float64(*v)
				declared = &f
			}
		}
		if declared != nil {
			payloadThresholdIgnored = hasThreshold && threshold != *declared
			threshold, hasThreshold = *declared, true
		}
	}
	if !hasThreshold && metric != signalInvariant {
		return errorResult("threshold is required: the alerted slice's intent declares no delivery.sentinel value for " + metric)
	}

	var goal, acceptance string
	switch metric {
	case signalInvariant:
		goal = fmt.Sprintf("Production signal on slice %s at ring %d%%: runtime invariant %q was violated (%v occurrences). Restore the invariant without changing the declared behaviour.", from, ring, invariant, observed)
		acceptance = fmt.Sprintf("Runtime invariant %q holds on ring %d%% after the fix", invariant, ring)
	default:
		goal = fmt.Sprintf("Production signal on slice %s at ring %d%%: %s observed %v, threshold %v; the Rollback Sentinel reverted the ring. Bring %s back under the threshold without changing the declared behaviour.", from, ring, metric, observed, threshold, metric)
		acceptance = fmt.Sprintf("%s <= %v on ring %d%% after the fix", metric, threshold, ring)
	}

	costCap := 2.0
	maxMutants := 30
	intent := contract.Intent{
		SchemaVersion: contract.SchemaVersion,
		SliceID:       sliceID,
		Mode:          contract.ModeGreenfield,
		Goal:          goal,
		ChangeKind:    "bugfix",
		Invariants:    []string{"Preserve existing public contract invariants"},
		NonGoals:      []string{"Unrelated module modifications"},
		Criticality:   contract.CriticalityHigh,
		Budget: &contract.IntentBudget{
			MaxCostUSD:                &costCap,
			MaxInnerIterations:        5,
			MaxTier1AdversarialRounds: 3,
			MaxMutants:                &maxMutants,
		},
		CriticalPaths: []string{},
		Acceptance:    []string{acceptance},
	}
	if source != nil {
		intent.Mode = source.Mode
		intent.Criticality = source.Criticality
		intent.CriticalityTags = source.CriticalityTags
		intent.Capabilities = source.Capabilities
		intent.SLAs = source.SLAs
		intent.Boundary = source.Boundary
		intent.Delivery = source.Delivery
		intent.RejectedAlternatives = source.RejectedAlternatives
		if len(source.Invariants) > 0 {
			intent.Invariants = append([]string{}, source.Invariants...)
		}
		if len(source.NonGoals) > 0 {
			intent.NonGoals = append([]string{}, source.NonGoals...)
		}
		if source.CriticalPaths != nil {
			intent.CriticalPaths = append([]string{}, source.CriticalPaths...)
		}
		if source.Budget != nil {
			b := *source.Budget
			intent.Budget = &b
		}
		intent.Acceptance = append(intent.Acceptance, source.Acceptance...)
	}
	if metric == signalInvariant && indexOf(intent.Invariants, invariant) < 0 {
		intent.Invariants = append(intent.Invariants, invariant)
	}
	// The payload can lower the cap, never raise it above the inherited one: raising a budget is a person's edit
	// to the intent, not something an alert decides.
	if c, ok := args["max_cost_usd"].(float64); ok && c > 0 {
		if inherited := intent.Budget.MaxCostUSD; source != nil && inherited != nil && c > *inherited {
			return errorResult(fmt.Sprintf("max_cost_usd %.2f is above the inherited cap %.2f of slice %s: an alert cannot raise the budget; edit that intent instead", c, *inherited, from))
		}
		intent.Budget.MaxCostUSD = &c
	}
	if intent.Budget.MaxCostUSD == nil {
		intent.Budget.MaxCostUSD = &costCap
	}

	relPath, checked, err := writeIntent(a, intent)
	if err != nil {
		return errorResult(err.Error())
	}
	resp := map[string]any{
		"status":      "ok",
		"intent_path": relPath,
		"run_command": fmt.Sprintf("a2dlc run --slice %s --intent %s", sliceID, relPath),
		"inherited_from": func() any {
			if source == nil {
				return nil
			}
			return filepath.ToSlash(filepath.Join(".a2dlc", "intents", from+".intent.json"))
		}(),
		"signal":            map[string]any{"from_slice": from, "metric": metric, "observed": observed, "ring": ring, "payload_threshold_ignored": payloadThresholdIgnored},
		"human_in_the_loop": fmt.Sprintf("the run stops at probes, circuit breakers and signed waivers; budget cap %.2f USD", *checked.Budget.MaxCostUSD),
		"intent":            checked,
	}
	outBytes, _ := json.MarshalIndent(resp, "", "  ")
	return callToolResult{
		Content: []contentBlock{{Type: "text", Text: string(outBytes)}},
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
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
