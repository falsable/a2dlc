"""Zero-cost test of the cut-off path of roles/adapter.py: a fake `claude` streams its first events and then sleeps past a
short A2DLC_TIMEOUT_S. The adapter must cut it off, answer status error, and leave the partial stream next to
response.json, with the cost if the stream already carried a result event. No network, no model.

  python -I -X utf8 roles/tests/test_stream_cut.py          (standard library only)
  docker run --rm --network none -v "<worktree>:/src" -w /src --entrypoint python3 a2dlc-roles:0.14 \
      -I -X utf8 roles/tests/test_stream_cut.py              (what the image runs)
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


def run_adapter(scenario: str, extra_env: dict[str, str]) -> tuple[float, dict, Path]:
    tmp = Path(tempfile.mkdtemp())
    (tmp / "in").mkdir()
    (tmp / "out").mkdir()
    (tmp / "work").mkdir()
    req = json.loads(REQUEST.read_text(encoding="utf-8"))
    req["role"], req["stage"], req["workdir"], req["payload"] = "builder_primary", "3", "/work", {}
    (tmp / "in" / "request.json").write_text(json.dumps(req), encoding="utf-8")
    env = {**os.environ, "A2DLC_REQUEST": str(tmp / "in" / "request.json"), "A2DLC_RESPONSE": str(tmp / "out" / "response.json"),
           "A2DLC_ROLE": "builder_primary", "A2DLC_STAGE": "3", "A2DLC_LLM": "claude", "A2DLC_WORKDIR": str(tmp / "work"),
           "A2DLC_PROMPTS_DIR": str(HERE.parent / "prompts"), "A2DLC_LLM_CMD": f"{Path(sys.executable).as_posix()} {(HERE / 'fake_cli.py').as_posix()}",
           "CLAUDE_CODE_DISABLE_BACKGROUND_TASKS": "1", "FAKE_SCENARIO": scenario, "A2DLC_TIMEOUT_S": "3", **extra_env}
    started = time.monotonic()
    done = subprocess.run([sys.executable, str(ADAPTER)], env=env, capture_output=True, text=True, timeout=60)
    took = time.monotonic() - started
    assert done.returncode == 1, done.stderr
    return took, json.loads((tmp / "out" / "response.json").read_text(encoding="utf-8")), tmp / "out" / "stream.jsonl"


class CutOff(unittest.TestCase):
    def test_a_call_that_sleeps_past_the_cap_leaves_its_partial_stream(self):
        took, response, stream = run_adapter("sleep_cut", {})
        self.assertLess(took, 30)  # cut at ~2.7 s, not after the fake's hour
        self.assertEqual(response["status"], "error")
        self.assertIn("exceeded the cap of 2.7 s and was cut off", response["reason"])
        self.assertEqual(response["error_kind"], "cli_cut")
        self.assertIn("3 events, 1 assistant messages, 1 tool calls", response["reason"])
        self.assertIsNone(response["cost"])  # no result event yet, so no USD to report
        kinds = [json.loads(x)["type"] for x in stream.read_text(encoding="utf-8").splitlines()]
        self.assertEqual(kinds, ["system", "assistant", "user"])

    def test_a_cut_call_that_already_printed_its_result_keeps_the_cost_it_reported(self):
        _, response, stream = run_adapter("sleep_cut", {"FAKE_SLEEP_COST": "1"})
        self.assertEqual(response["status"], "error")
        self.assertEqual(response["cost"]["usd"], 0.0123)
        self.assertEqual(response["cost"]["input_tokens"], 110)
        self.assertEqual(json.loads(stream.read_text(encoding="utf-8").splitlines()[-1])["type"], "result")

    def test_a_cut_call_without_result_estimates_deduped_5m_1h_cost_or_errors_when_unpriced(self):
        # 1. Known priced model deduplicates msg-1|req-1 using max(output_tokens)=150 and prices 5m vs 1h writes apart.
        _, resp, _ = run_adapter("sleep_cut", {"FAKE_SLEEP_MODEL": "claude-sonnet-4-5-20250929", "FAKE_SLEEP_CACHE": "1"})
        self.assertEqual(resp["status"], "error")
        cost = resp["cost"]
        self.assertIsNotNone(cost)
        self.assertTrue(cost["estimated"])
        # Expected: (1000*3 + 150*15 + 2000*0.3 + 400*3.75 + 100*6.0) / 1e6 = 0.00795
        self.assertAlmostEqual(cost["usd"], 0.00795, places=6)
        self.assertEqual(cost["output_tokens"], 150)
        self.assertEqual(cost["input_tokens"], 3500)
        self.assertEqual(cost["cache_read_input_tokens"], 2000)
        self.assertEqual(cost["cache_creation_input_tokens"], 500)

        # 2. Unknown model never falls back to 0: estimate_claude_stream_cost raises and response stays error with cost=None.
        import importlib.util
        spec = importlib.util.spec_from_file_location("adapter", str(ADAPTER))
        mod = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(mod)
        events = [{"type": "assistant", "requestId": "r1", "message": {"id": "m1", "model": "unknown-tier-x", "usage": {"input_tokens": 100, "output_tokens": 10}}}]
        with self.assertRaises(ValueError):
            mod.estimate_claude_stream_cost(events)
        _, resp_unknown, _ = run_adapter("sleep_cut", {"FAKE_SLEEP_MODEL": "unknown-tier-x"})
        self.assertEqual(resp_unknown["status"], "error")
        self.assertIsNone(resp_unknown["cost"])
        self.assertIn("no price in table", resp_unknown["reason"])


if __name__ == "__main__":
    unittest.main()
