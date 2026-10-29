"""Network-free test of cli_command in roles/adapter.py: how the claude command line that cmd/factory sets in A2DLC_LLM_CMD
(internal/factory/claude.go, ClaudeCommand) is split, so the flags come out as separate arguments and before -p. No model runs.

  python -I -X utf8 roles/tests/test_cli_command.py          (standard library only)
  docker run --rm --network none -v "<worktree>:/src" -w /src --entrypoint python3 a2dlc-roles:0.11 \
      -I -X utf8 roles/tests/test_cli_command.py              (what the image runs; no host Python needed)
"""
from __future__ import annotations

import importlib.util
import os
import unittest
from pathlib import Path

ADAPTER = Path(__file__).resolve().parents[1] / "adapter.py"

# the string TestClaudeCommandIsExactlyTheMinimalFlagSet pins on the Go side, with a remaining budget of 0.75
FACTORY_CMD = ("claude --setting-sources '' --strict-mcp-config --disable-slash-commands --tools Read,Edit "
               "--exclude-dynamic-system-prompt-sections --max-budget-usd 0.7500")


def load_adapter():
    spec = importlib.util.spec_from_file_location("a2dlc_adapter", ADAPTER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


adapter = load_adapter()


class CliCommandTest(unittest.TestCase):
    def setUp(self):
        self._saved = {k: os.environ.get(k) for k in ("A2DLC_LLM_CMD", "A2DLC_MODEL")}
        os.environ["A2DLC_LLM_CMD"], os.environ["A2DLC_MODEL"] = FACTORY_CMD, "haiku"

    def tearDown(self):
        for k, v in self._saved.items():
            os.environ.pop(k, None)
            if v is not None:
                os.environ[k] = v

    def test_factory_flags_are_separate_arguments_before_dash_p(self):
        cmd = adapter.cli_command("claude", True)
        head = ["claude", "--setting-sources", "", "--strict-mcp-config", "--disable-slash-commands", "--tools", "Read,Edit",
                "--exclude-dynamic-system-prompt-sections", "--max-budget-usd", "0.7500"]
        self.assertEqual(cmd[:len(head)], head)  # the empty setting-sources value survives as its own empty argument
        self.assertEqual(cmd[len(head)], "-p")
        self.assertEqual(cmd[cmd.index("--model") + 1], "haiku")

    def test_the_builder_keeps_its_own_tool_permission_and_no_turn_flag(self):
        cmd = adapter.cli_command("claude", True)
        self.assertIn("--allowedTools", cmd)
        self.assertNotIn("--max-turns", cmd)  # claude has no such flag: the turn cap is the core's

    def test_without_the_override_the_command_is_plain_claude(self):
        del os.environ["A2DLC_LLM_CMD"]
        cmd = adapter.cli_command("claude", True)
        self.assertEqual(cmd[:2], ["claude", "-p"])
        self.assertNotIn("--max-budget-usd", cmd)


if __name__ == "__main__":
    unittest.main()
