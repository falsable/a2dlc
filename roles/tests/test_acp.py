"""Zero-cost test of the acp backend of roles/adapter.py (A2DLC_LLM=acp): the command it gives acpx, how it reads the ACP
messages acpx relays, and the stop at A2DLC_BUDGET_USD. roles/tests/fake_acpx.py stands in for acpx through A2DLC_LLM_CMD.
No network, no model.

  python -I -X utf8 roles/tests/test_acp.py          (standard library only)
  docker run --rm --network none -v "<worktree>:/src" -w /src --entrypoint python3 a2dlc-roles:0.14 \
      -I -X utf8 roles/tests/test_acp.py              (what the image runs)
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path

HERE = Path(__file__).resolve().parent
ADAPTER = HERE.parent / "adapter.py"
REQUEST = HERE.parents[1] / "contracts" / "examples" / "valid" / "role-request__saboteur.json"


def run_adapter(scenario: str, extra_env: dict[str, str] | None = None) -> tuple[int, float, dict, Path]:
    tmp = Path(tempfile.mkdtemp())
    for d in ("in", "out", "work"):
        (tmp / d).mkdir()
    req = json.loads(REQUEST.read_text(encoding="utf-8"))
    req["role"], req["stage"], req["workdir"], req["payload"] = "builder_primary", "3", "/work", {}
    (tmp / "in" / "request.json").write_text(json.dumps(req), encoding="utf-8")
    env = {**os.environ, "A2DLC_REQUEST": str(tmp / "in" / "request.json"), "A2DLC_RESPONSE": str(tmp / "out" / "response.json"),
           "A2DLC_ROLE": "builder_primary", "A2DLC_STAGE": "3", "A2DLC_LLM": "acp", "A2DLC_WORKDIR": str(tmp / "work"),
           "A2DLC_PROMPTS_DIR": str(HERE.parent / "prompts"), "A2DLC_ACP_AGENT": "gemini --experimental-acp",
           "A2DLC_LLM_CMD": f"{Path(sys.executable).as_posix()} {(HERE / 'fake_acpx.py').as_posix()}",
           "A2DLC_MODEL": "fake-model", "FAKE_ACPX": scenario, "HOME": str(tmp / "home"), "FAKE_ACPX_ARGV": str(tmp / "argv.json"), **(extra_env or {})}
    started = time.monotonic()
    done = subprocess.run([sys.executable, str(ADAPTER)], env=env, capture_output=True, text=True, timeout=60)
    took = time.monotonic() - started
    return done.returncode, took, json.loads((tmp / "out" / "response.json").read_text(encoding="utf-8")), tmp


class Acp(unittest.TestCase):
    def test_a_builder_turn_is_answered_with_the_cost_the_agent_reported(self):
        code, _, response, tmp = run_adapter("ok")
        self.assertEqual(code, 0, response)
        self.assertEqual(response["status"], "ok")
        self.assertEqual(response["payload"], {"summary": "wrote done.txt", "files_changed": ["done.txt"]})
        self.assertEqual(response["cost"], {"usd": 0.0123})
        self.assertNotIn("model", response)  # acpx does not prove which model answered
        self.assertEqual((tmp / "work" / "done.txt").read_text(encoding="utf-8"), "done\n")
        methods = [json.loads(x).get("method") for x in (tmp / "out" / "stream.jsonl").read_text(encoding="utf-8").splitlines()]
        self.assertEqual(methods[0], "session/prompt")  # the stream is acpx's stdout, untouched

    def test_the_command_is_explicit_about_permissions_and_reads_the_prompt_from_stdin(self):
        _, _, _, tmp = run_adapter("ok")
        argv = json.loads((tmp / "argv.json").read_text(encoding="utf-8"))
        self.assertEqual(argv, ["--format", "json", "--json-strict", "--approve-all", "--non-interactive-permissions", "fail",
                                "--model", "fake-model", "--agent", "gemini --experimental-acp", "exec", "--file", "-"])

    def test_no_usd_in_the_stream_means_no_cost(self):
        code, _, response, _ = run_adapter("no_cost")
        self.assertEqual(code, 0)
        self.assertIsNone(response["cost"])

    def test_a_turn_that_does_not_end_normally_is_an_error(self):
        code, _, response, _ = run_adapter("refusal")
        self.assertEqual(code, 1)
        self.assertEqual(response["reason"], "the agent ended with refusal")

    def test_an_agent_error_keeps_the_stream_and_says_so(self):
        code, _, response, _ = run_adapter("error")
        self.assertEqual(code, 1)
        self.assertIn("acpx exited with 1 without a result event", response["reason"])
        self.assertIn("the agent crashed", response["reason"])

    def test_the_call_stops_once_the_reported_cost_reaches_the_budget(self):
        code, took, response, _ = run_adapter("budget", {"A2DLC_BUDGET_USD": "0.5", "A2DLC_TIMEOUT_S": "60"})
        self.assertEqual(code, 1)
        self.assertLess(took, 20)  # stopped at the second report, not at the 60 s cap
        # the watcher reads every 0.25 s and the fake reports every 0.2 s, so it may already see the third report
        self.assertRegex(response["reason"], r"reached the budget of USD 0\.5 \(USD 0\.[69] reported\) and was stopped")
        self.assertIn(response["cost"]["usd"], (0.6, 0.9))

    def test_a_turn_cut_at_the_cap_reports_the_steps_the_agent_stored(self):
        code, took, response, _ = run_adapter("store_cut", {"A2DLC_TIMEOUT_S": "4"})
        self.assertEqual(code, 1)
        self.assertLess(took, 30)
        self.assertEqual(response["error_kind"], "cli_cut")
        self.assertAlmostEqual(response["cost"]["usd"], 0.03)  # the hour-old USD 5 step is not this call's

    def test_the_budget_stops_a_turn_through_the_agents_store(self):
        code, took, response, _ = run_adapter("store_budget", {"A2DLC_BUDGET_USD": "0.05", "A2DLC_TIMEOUT_S": "60"})
        self.assertEqual(code, 1)
        self.assertLess(took, 30)
        self.assertIn("reached the budget of USD 0.05", response["reason"])
        self.assertGreaterEqual(response["cost"]["usd"], 0.05)

    def test_acp_without_an_agent_is_refused(self):
        code, _, response, _ = run_adapter("ok", {"A2DLC_ACP_AGENT": ""})
        self.assertEqual(code, 1)
        self.assertIn("A2DLC_LLM=acp needs A2DLC_ACP_AGENT", response["reason"])


if __name__ == "__main__":
    unittest.main()
