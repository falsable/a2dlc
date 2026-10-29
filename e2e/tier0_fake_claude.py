#!/usr/bin/env python3
"""A claude that applies a canned patch: no model, no network. It stands where `claude -p` stands in the roles image.

Its bin/claude is not in git (the repo's .gitignore has `bin/`): e2e/stage3_tests_tier0.sh copies this file there.
The issue's body carries a marker [patch:<name>]; the builder prompt includes it, and this copies
/fake/patches/<name>/ over the working directory (/work), then answers in the shape `claude -p --output-format json`
does, with a cost, so the ledger has a cost to count. Flags are accepted and ignored.
A second marker [on-coverage-finding:<name>] names the patch to apply instead once the request carries a Tier 0 finding of
kind coverage (a changed line no test executed): the builder's second round, after the first left such a line.
"""
import json
import os
import re
import shutil
import sys

prompt = sys.stdin.read()
m = re.search(r"\[patch:([a-z0-9-]+)\]", prompt)
patch = m.group(1) if m else None
after = re.search(r"\[on-coverage-finding:([a-z0-9-]+)\]", prompt)
if patch and after and re.search(r'"kind":\s*"coverage"', prompt):
    patch = after.group(1)
changed = []
if patch:
    src = os.path.join("/fake/patches", patch)
    for root, _, files in os.walk(src):
        for name in files:
            rel = os.path.relpath(os.path.join(root, name), src)
            dst = os.path.join(os.getcwd(), rel)
            os.makedirs(os.path.dirname(dst), exist_ok=True)
            shutil.copyfile(os.path.join(root, name), dst)
            changed.append(rel.replace(os.sep, "/"))
if changed:
    response = {"status": "ok", "reason": None, "files": [],
                "payload": {"summary": "Applied the canned patch " + patch, "files_changed": sorted(changed)}}
else:
    response = {"status": "refused", "reason": "no canned patch for this issue", "files": [], "payload": {}}
print(json.dumps({"type": "result", "subtype": "success", "is_error": False, "result": json.dumps(response), "session_id": "fake",
                  "total_cost_usd": 0.02, "num_turns": 3,
                  "usage": {"input_tokens": 400, "cache_creation_input_tokens": 300, "cache_read_input_tokens": 800, "output_tokens": 90}}))
