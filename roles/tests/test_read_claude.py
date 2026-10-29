"""Network-free test of read_claude in roles/adapter.py: what the claude -p JSON puts in the role's cost. No model runs.

  python -I -X utf8 roles/tests/test_read_claude.py          (standard library only)
  docker run --rm --network none -v "<worktree>:/src" -w /src --entrypoint python3 a2dlc-roles:0.11 \
      -I -X utf8 roles/tests/test_read_claude.py              (what the image runs; no host Python needed)
"""
from __future__ import annotations

import importlib.util
import json
import unittest
from pathlib import Path

ADAPTER = Path(__file__).resolve().parents[1] / "adapter.py"


def load_adapter():
    spec = importlib.util.spec_from_file_location("a2dlc_adapter", ADAPTER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


adapter = load_adapter()


def claude_json(**over) -> str:
    doc = {"type": "result", "subtype": "success", "is_error": False, "result": "done", "total_cost_usd": 0.5,
           "num_turns": 3,
           "usage": {"input_tokens": 100, "cache_creation_input_tokens": 30, "cache_read_input_tokens": 10, "output_tokens": 50}}
    doc.update(over)
    return json.dumps(doc)


class ReadClaudeCost(unittest.TestCase):
    def test_four_counters_stay_separate_and_input_is_the_total(self):
        _, cost, _ = adapter.read_claude(claude_json(), "", 0)
        self.assertEqual(cost, {"usd": 0.5, "input_tokens": 140, "cache_read_input_tokens": 10,
                                "cache_creation_input_tokens": 30, "output_tokens": 50, "turns": 3})
        self.assertGreaterEqual(cost["input_tokens"], cost["cache_read_input_tokens"] + cost["cache_creation_input_tokens"])

    def test_cache_counters_are_absent_when_the_cli_does_not_report_them(self):
        _, cost, _ = adapter.read_claude(claude_json(usage={"input_tokens": 7, "output_tokens": 2}), "", 0)
        self.assertEqual(cost, {"usd": 0.5, "input_tokens": 7, "output_tokens": 2, "turns": 3})

    def test_a_reported_zero_cache_counter_is_kept(self):
        _, cost, _ = adapter.read_claude(claude_json(usage={"input_tokens": 7, "cache_read_input_tokens": 0, "output_tokens": 2}), "", 0)
        self.assertEqual(cost["cache_read_input_tokens"], 0)
        self.assertNotIn("cache_creation_input_tokens", cost)

    def test_turns_are_absent_without_num_turns(self):
        doc = json.loads(claude_json())
        del doc["num_turns"]
        _, cost, _ = adapter.read_claude(json.dumps(doc), "", 0)
        self.assertNotIn("turns", cost)

    def test_invalid_num_turns_is_not_recorded(self):
        for bad in (0, -1, True, "3", 2.5, None):
            _, cost, _ = adapter.read_claude(claude_json(num_turns=bad), "", 0)
            self.assertNotIn("turns", cost, bad)

    def test_cache_and_turns_survive_a_failed_call(self):
        with self.assertRaises(adapter.Failure) as caught:
            adapter.read_claude(claude_json(subtype="error_max_turns", is_error=True, result=None, num_turns=9), "", 1)
        cost = caught.exception.cost
        self.assertEqual(cost["turns"], 9)
        self.assertEqual(cost["cache_read_input_tokens"], 10)
        self.assertEqual(cost["cache_creation_input_tokens"], 30)

    def test_golden_claude_result_gives_the_cost_the_go_chain_test_reads(self):
        # internal/run/costchain_test.go reads claude_result.cost.json as the cost of every fake role call: this pins that
        # file to what the adapter makes of a claude -p result, so the Go test starts from a real adapter output.
        golden = Path(__file__).resolve().parent / "golden"
        _, cost, _ = adapter.read_claude((golden / "claude_result.json").read_text(encoding="utf-8"), "", 0)
        self.assertEqual(cost, json.loads((golden / "claude_result.cost.json").read_text(encoding="utf-8")))

    def test_no_cost_means_no_cost(self):
        doc = json.loads(claude_json())
        del doc["total_cost_usd"]
        _, cost, _ = adapter.read_claude(json.dumps(doc), "", 0)
        self.assertIsNone(cost)


if __name__ == "__main__":
    unittest.main()
