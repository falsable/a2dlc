"""Network-free test of cli_command in roles/adapter.py: how a claude command line that a caller sets in A2DLC_LLM_CMD
(the factory project builds this one) is split, so the flags come out as separate arguments and before -p. No model runs.

  python -I -X utf8 roles/tests/test_cli_command.py          (standard library only)
  docker run --rm --network none -v "<worktree>:/src" -w /src --entrypoint python3 a2dlc-roles:0.11 \
      -I -X utf8 roles/tests/test_cli_command.py              (what the image runs; no host Python needed)
"""
from __future__ import annotations

import importlib.util
import os
import tempfile
import unittest
from pathlib import Path

ADAPTER = Path(__file__).resolve().parents[1] / "adapter.py"

# the minimal-flag command line a caller sets, with a remaining budget of 0.75
CALLER_CMD = ("claude --setting-sources '' --strict-mcp-config --disable-slash-commands --tools Read,Edit "
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
        os.environ["A2DLC_LLM_CMD"], os.environ["A2DLC_MODEL"] = CALLER_CMD, "haiku"

    def tearDown(self):
        for k, v in self._saved.items():
            os.environ.pop(k, None)
            if v is not None:
                os.environ[k] = v

    def test_caller_flags_are_separate_arguments_before_dash_p(self):
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

    def test_feedback_work_files_includes_only_clean_finding_files_in_round_2(self):
        with tempfile.TemporaryDirectory() as tmp:
            work = Path(tmp) / "work"
            (work / "src").mkdir(parents=True)
            (work / ".a2dlc").mkdir()
            (work / "src" / "f.py").write_text("def f(x):\n    return x + 1\n", encoding="utf-8", newline="\n")
            (work / "src" / "other.py").write_text("def g():\n    return 0\n", encoding="utf-8", newline="\n")
            (work / ".a2dlc" / "config.json").write_text("{}", encoding="utf-8")
            (Path(tmp) / "secret.txt").write_text("outside", encoding="utf-8")

            req_r1 = {"payload": {"round": 1, "tier0_feedback": None}}
            self.assertEqual(adapter.feedback_work_files(req_r1, work), [])
            p1 = adapter.build_prompt("builder_primary", "3", "inst", "{}", [], None,
                                      adapter.feedback_work_files(req_r1, work))
            self.assertNotIn("## /work", p1)

            req_r2 = {
                "payload": {
                    "round": 2,
                    "tier0_feedback": {
                        "ok": False,
                        "findings": [
                            {"kind": "coverage", "file": "src/f.py", "line": 2, "message": "uncovered"},
                            {"kind": "lint", "file": "src/f.py", "line": 1, "message": "dup"},
                            {"kind": "lint", "file": "../secret.txt", "line": 1, "message": "outside"},
                            {"kind": "lint", "file": ".a2dlc/config.json", "line": 1, "message": "excluded"},
                        ],
                    },
                }
            }
            fb = adapter.feedback_work_files(req_r2, work)
            self.assertEqual(fb, [("src/f.py", "def f(x):\n    return x + 1\n")])
            p2 = adapter.build_prompt("builder_primary", "3", "inst", "{}", [], None, fb)
            self.assertIn("## /work (files named in tier0_feedback)", p2)
            self.assertIn("### /work/src/f.py", p2)
            self.assertNotIn("other.py", p2)
            self.assertNotIn("outside", p2)


class SessionArgsTest(unittest.TestCase):
    REQ = {"run_id": "r1", "slice_id": "s1", "role": "builder_primary"}

    def setUp(self):
        self._saved = {k: os.environ.get(k) for k in ("A2DLC_SESSION", "CLAUDE_CONFIG_DIR")}

    def tearDown(self):
        for k, v in self._saved.items():
            os.environ.pop(k, None)
            if v is not None:
                os.environ[k] = v

    def test_without_a_session_folder_nothing_is_kept(self):
        os.environ.pop("A2DLC_SESSION", None)
        self.assertEqual(adapter.session_args(self.REQ), ["--no-session-persistence"])

    def test_the_first_round_starts_the_session_and_the_next_resumes_it(self):
        with tempfile.TemporaryDirectory() as root:
            os.environ["A2DLC_SESSION"] = root
            flag, sid = adapter.session_args(self.REQ)
            self.assertEqual(flag, "--session-id")
            self.assertEqual(os.environ["CLAUDE_CONFIG_DIR"], os.path.join(root, "claude"))
            # what claude leaves after round 1: <config>/projects/<cwd>/<id>.jsonl
            (Path(root) / "claude" / "projects" / "-work").mkdir(parents=True)
            (Path(root) / "claude" / "projects" / "-work" / f"{sid}.jsonl").write_text("{}", encoding="utf-8")
            self.assertEqual(adapter.session_args(self.REQ), ["--resume", sid])
            other = adapter.session_args({**self.REQ, "role": "builder_orthogonal"})
            self.assertEqual(other[0], "--session-id")
            self.assertNotEqual(other[1], sid)

    def test_a_resumed_round_reports_only_its_own_usd(self):
        with tempfile.TemporaryDirectory() as root:
            os.environ["A2DLC_SESSION"] = root
            # attr, task 4 of PREREG-entropos: round 2 reported the session's 0.8011 after round 1's 0.5219
            self.assertEqual(adapter.own_cost({"usd": 0.5219, "turns": 18})["usd"], 0.5219)
            second = adapter.own_cost({"usd": 0.8011, "turns": 14})
            self.assertAlmostEqual(second["usd"], 0.2792)
            self.assertEqual(second["turns"], 14)
            self.assertIsNone(adapter.own_cost(None))
        os.environ.pop("A2DLC_SESSION")
        self.assertEqual(adapter.own_cost({"usd": 0.3}), {"usd": 0.3})


if __name__ == "__main__":
    unittest.main()
