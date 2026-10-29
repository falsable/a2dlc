// Package factory runs a batch of issues as lanes over the a2dlc state machine (ADR meta/0008). A lane is `a2dlc run
// --mode lite` in its own git worktree with builder_primary as the only role; the model is reached only through
// unmodified `claude -p` in a role container. Everything else here (precheck, worktrees, scheduling, caps, the report)
// is plain Go and spends no tokens. The import direction is factory -> run -> app; run and app know nothing of this
// package, and neither the a2dlc binary nor the protocol read any of its records.
package factory

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/falsable/a2dlc/internal/contract"
)

// Role is the only role a lane configures. The Saboteur's rounds are off because the role is absent from roles.json,
// not because of Options.MaxRounds, where 0 means "use the default".
const Role = contract.RoleBuilderPrimary

// EnvLLMCmd is the variable roles/adapter.py reads to replace the `claude` executable of its command line. The flags
// go before `-p`, so the adapter needs no knob for them and the CLI stays unmodified.
const EnvLLMCmd = "A2DLC_LLM_CMD"

// ClaudeCommand is the A2DLC_LLM_CMD of a launch: claude with minimal flags (no user settings, no MCP servers, no
// skills, a bounded tool list, the per-machine sections of the system prompt moved out of the cached prefix) and the
// spend the launch may still use as --max-budget-usd. claude has no --max-turns, so the turn cap is the core's.
func ClaudeCommand(cfg contract.FactoryConfig, remainingUSD float64) string {
	args := []string{"claude", "--setting-sources", "", "--strict-mcp-config", "--disable-slash-commands",
		"--tools", strings.Join(cfg.Tools, ","), "--exclude-dynamic-system-prompt-sections",
		"--max-budget-usd", strconv.FormatFloat(remainingUSD, 'f', 4, 64)}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return strings.Join(quoted, " ")
}

var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellQuote quotes one word the way shlex.split in the adapter reads it back; the empty word is ”.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if shellSafe.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

// LaneRoles is the roles.json of every lane: builder_primary on the configured image, its container limit equal to the
// lane's wall time, no retries (a failed call is a lane outcome, not a second spend).
func LaneRoles(cfg contract.FactoryConfig) contract.Roles {
	retries := 0
	return contract.Roles{SchemaVersion: 1, Roles: map[string]contract.Role{Role: {
		Family: "claude", Image: cfg.RolesImage, Argv: cfg.Argv, EnvFile: cfg.EnvFile, Network: cfg.Network,
		EgressAllow: cfg.EgressAllow, TimeoutS: cfg.Caps.LaneSeconds, Retries: &retries,
		Env: map[string]string{"A2DLC_LLM": "claude", "A2DLC_MODEL": cfg.Model, EnvLLMCmd: Command(cfg, cfg.Caps.LaneUSD)},
	}}}
}
