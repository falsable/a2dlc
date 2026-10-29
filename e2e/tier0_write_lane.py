"""Writes what a caller gives `a2dlc run --mode lite` for one issue: .a2dlc/roles.json in the worktree and the intent file.

    python -I -X utf8 e2e/tier0_write_lane.py <worktree> <slice id> <intent path> <title> <body> <done command>

Used by e2e/stage3_tests_tier0.sh. The role is builder_primary on the fake-claude image, no network, no retries.
"""
import json
import os
import sys

wt, slice_id, intent_path, title, body, done = sys.argv[1:7]
cmd = "claude --disallowedTools WebSearch,WebFetch --max-budget-usd 1.0000"
roles = {"schema_version": 1, "roles": {"builder_primary": {
    "family": "claude", "image": "a2dlc-roles-node-fake:0.2", "argv": ["python3", "/a2dlc/adapter.py"], "network": "none",
    "timeout_s": 300, "retries": 0, "env": {"A2DLC_LLM": "claude", "A2DLC_MODEL": "haiku", "A2DLC_LLM_CMD": cmd}}}}
os.makedirs(os.path.join(wt, ".a2dlc"), exist_ok=True)
with open(os.path.join(wt, ".a2dlc", "roles.json"), "w", encoding="utf-8", newline="\n") as f:
    json.dump(roles, f, indent=1)
intent = {"schema_version": 1, "slice_id": slice_id, "mode": "lite", "goal": title + "\n\n" + body, "invariants": [], "non_goals": [],
          "criticality": "low", "critical_paths": [], "acceptance": [done],
          "budget": {"max_cost_usd": None, "max_inner_iterations": 5, "max_tier1_adversarial_rounds": 1, "max_mutants": None}}
os.makedirs(os.path.dirname(intent_path), exist_ok=True)
with open(intent_path, "w", encoding="utf-8", newline="\n") as f:
    json.dump(intent, f, indent=1)
