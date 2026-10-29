package factory

import (
	"strconv"

	"github.com/falsable/a2dlc/internal/contract"
)

// ControlCommand is the A2DLC_LLM_CMD of arm 0, the control of the preregistered comparison (v2 design 9.1): `claude -p`
// as the role container runs it by default, with none of the flags ClaudeCommand adds. No --setting-sources,
// --strict-mcp-config, --disable-slash-commands, --tools or --exclude-dynamic-system-prompt-sections, so the CLI loads
// what it loads in that image unless told otherwise.
//
// It keeps two flags. --max-budget-usd enforces the lane cap every arm shares (caps.lane_usd, the run and the total), and
// without it a single default call could overshoot the cap before the next admit check; it changes what a call may
// spend, not how the call is made. --disallowedTools WebSearch,WebFetch is the one stated deviation from default
// `claude -p` (PREREG Q2, see DisallowWebFlag): both arms carry it, so it is not a difference between them.
// roles/adapter.py still adds --model, --permission-mode acceptEdits and its own --allowedTools to both arms: that is the
// container's default, not a factory trick.
func ControlCommand(remainingUSD float64) string {
	return "claude " + DisallowWebFlag + " " + DisallowedWebTools + " --max-budget-usd " + strconv.FormatFloat(remainingUSD, 'f', 4, 64)
}

// Command is the A2DLC_LLM_CMD of a launch under the config's arm: the minimal flags of arm 1 (also when the config
// names no arm) or the default claude of arm 0. It is the only place the arm is read, so LaneRoles and the launcher
// cannot disagree about it.
func Command(cfg contract.FactoryConfig, remainingUSD float64) string {
	if cfg.Arm == contract.ArmControl {
		return ControlCommand(remainingUSD)
	}
	return ClaudeCommand(cfg, remainingUSD)
}
