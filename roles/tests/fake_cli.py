#!/usr/bin/env python3
"""Fake CLI to test roles/adapter.py without network or credentials (A2DLC_LLM_CMD="python3 /fake/fake_cli.py").

It poses as `claude -p --output-format json` or as `gemini -p --output-format json` depending on its arguments
(claude carries --no-session-persistence; gemini, --skip-trust), reads the prompt from stdin and answers what the adapter
expects. Before answering it checks what the adapter should have passed to it: if something does not add up, it exits
with 2 and says so on stderr, and the integration test fails.

FAKE_SCENARIO chooses the answer:
  ok            (default) the role complies: saboteur tests, or a file edited in the cwd and a builder summary
  refused       the role refuses
  prose         the JSON comes inside a ```json block and with text around it
  garbage       the model's text is not JSON
  cli_fails     the CLI exits with 3 without JSON, and a credential on stderr that the adapter must not repeat
  bad_path      a file with a path that escapes /out
  claude_error  claude reports is_error with a cost
  outside_tests propose_properties delivers its test under src/ (the adapter must refuse it)
"""
from __future__ import annotations

import json
import os
import re
import sys


def fail(msg: str) -> None:
    print("fake_cli: " + msg, file=sys.stderr)
    sys.exit(2)


def follows(argv: list[str], flag: str) -> str | None:
    return argv[argv.index(flag) + 1] if flag in argv and argv.index(flag) + 1 < len(argv) else None


def main() -> int:
    argv = sys.argv[1:]
    prompt = sys.stdin.read()
    claude = "--no-session-persistence" in argv
    gemini = "--skip-trust" in argv
    if claude == gemini:
        fail(f"I cannot tell whether I was called as claude or as gemini: {argv}")
    role = os.environ.get("A2DLC_ROLE", "")
    builder = role.startswith("builder_")
    scenario = os.environ.get("FAKE_SCENARIO", "ok")

    # what the adapter must have assembled
    if "-p" not in argv or follows(argv, "--output-format") != "json":
        fail("missing -p or --output-format json")
    if claude:
        if builder:
            if follows(argv, "--permission-mode") != "acceptEdits" or not (follows(argv, "--allowedTools") or "").startswith("Read"):
                fail("the claude builder needs --permission-mode acceptEdits and --allowedTools")
        elif follows(argv, "--tools") != "":
            fail("a role that is not a builder must not have tools (--tools \"\")")
    else:
        if builder != (follows(argv, "--approval-mode") == "yolo"):
            fail("--approval-mode yolo is only for the builders")
    if os.environ.get("A2DLC_MODEL") and (follows(argv, "--model") or follows(argv, "-m")) != os.environ["A2DLC_MODEL"]:
        fail("A2DLC_MODEL did not reach the CLI")
    if "## Response format" not in prompt or "### request.json" not in prompt:
        fail("the prompt does not carry the output format or the request.json")
    # the TypeScript probe override is in the prompt exactly when the role payload says engine_language typescript
    req_match = re.search(r"### request\.json\n\n```json\n(.*?)\n```", prompt, re.S)
    req_payload = (json.loads(req_match.group(1)).get("payload") or {}) if req_match else {}
    if ("## Probe expressions in TypeScript" in prompt) != (req_payload.get("engine_language") == "typescript"):
        fail("the TypeScript probe override does not match engine_language in the payload")
    if not role or f'"role": "{role}"' not in prompt:
        fail("the request.json in the prompt is not the role's")
    if role == "saboteur" and ("# Role: Saboteur" not in prompt or "### signatures.json" not in prompt):
        fail("the saboteur prompt does not carry its instructions or signatures.json")

    if scenario == "cli_fails":
        print("Error: not authenticated; ANTHROPIC_API_KEY=" + os.environ.get("ANTHROPIC_API_KEY", ""), file=sys.stderr)
        return 3

    if scenario == "refused":
        response = {"status": "refused", "reason": "signatures.json is missing the symbol for line 14.", "files": [], "payload": {}}
    elif builder:
        with open(os.path.join(os.getcwd(), "EDITED_BY_FAKE.txt"), "w", encoding="utf-8") as f:
            f.write("the fake builder edited /work\n")
        response = {"status": "ok", "reason": None, "files": [], "payload": {"summary": "Implemented what was asked.", "files_changed": ["EDITED_BY_FAKE.txt"]}}
        if scenario == "no_files_changed":  # the old template shape: outside the contract
            del response["payload"]["files_changed"]
    elif role == "propose_properties":
        # one property test under tests/ (outside_tests: the same test written under src/, which the adapter must refuse)
        path = "src/media.py" if scenario == "outside_tests" else "tests/test_media_props.py"
        response = {"status": "ok", "reason": None,
                    "files": [{"path": path, "content": "from hypothesis import given\n"}],
                    "payload": {"properties": [{"target": "src/media.py::media",
                                                "property": "the mean lies between the smallest and the largest element",
                                                "file": path}]}}
    else:
        m = re.search(r"### request\.json\n\n```json\n(.*?)\n```", prompt, re.S)
        file = json.loads(m.group(1))["payload"]["uncovered"][0]["file"] if m else "src/x.py"
        response = {"status": "ok", "reason": None, "payload": {"targets": [file + "::media"]},
                     "files": [{"path": "tests/test_media_saboteur.py", "content": "def test_media_empty():\n    assert True\n"}]}
        if scenario == "bad_path":
            response["files"][0]["path"] = "../outside.py"

    text = json.dumps(response, ensure_ascii=False)
    if scenario == "prose":
        text = "Here is my delivery:\n```json\n" + text + "\n```\nI hope it helps."
    elif scenario == "garbage":
        text = "I could not generate the JSON, sorry."

    if claude:
        doc = {"type": "result", "subtype": "success", "is_error": False, "result": text, "session_id": "s",
               "total_cost_usd": 0.0123, "num_turns": 2, "usage": {"input_tokens": 100, "cache_read_input_tokens": 10, "output_tokens": 50},
               "modelUsage": {"claude-fake": {"costUSD": 0.0123}, "claude-haiku-fake": {"costUSD": 0.0001}}}
        if scenario == "claude_error":
            doc.update(subtype="error_max_turns", is_error=True, result="ran out of turns")
            del doc["result"]
        print(json.dumps(doc))
    else:
        print("Loaded cached credentials.", file=sys.stderr)
        print(json.dumps({"response": text, "stats": {"models": {"gemini-fake": {"tokens": {"prompt": 90, "candidates": 40}}}}}))
    return 0


if __name__ == "__main__":
    sys.exit(main())
