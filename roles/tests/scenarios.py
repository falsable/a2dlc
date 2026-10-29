"""Scenarios for the adapter test (roles/tests/test_adapter.sh).

  python -I -X utf8 roles/tests/scenarios.py prepare <folder>   creates <folder>/<scenario>/in with request.json and inputs
  python -I -X utf8 roles/tests/scenarios.py check <folder>     reviews <folder>/<scenario>/{out,exit} and copies each
                                                                response.json to <folder>/responses/ to validate it
                                                                against the contract
Standard library only: runs on the host, installing nothing."""
from __future__ import annotations

import copy
import hashlib
import json
import shutil
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
EXAMPLES = ROOT / "contracts" / "examples" / "valid"
SAMPLE = Path(__file__).resolve().parent / "sample"

# scenario: (role, stage, A2DLC_LLM, FAKE_SCENARIO, exit expected)
SCENARIOS = {
    "saboteur-claude-ok": ("saboteur", "4", "claude", "ok", 0),
    "saboteur-gemini-ok": ("saboteur", "4", "gemini", "ok", 0),
    "saboteur-refused": ("saboteur", "4", "claude", "refused", 0),
    "saboteur-prose": ("saboteur", "4", "gemini", "prose", 0),
    "saboteur-garbage": ("saboteur", "4", "claude", "garbage", 1),
    "saboteur-cli-fails": ("saboteur", "4", "claude", "cli_fails", 1),
    "saboteur-bad-path": ("saboteur", "4", "gemini", "bad_path", 1),
    "saboteur-claude-error": ("saboteur", "4", "claude", "claude_error", 1),
    "builder-claude-ok": ("builder_primary", "3", "claude", "ok", 0),
    "builder-gemini-ok": ("builder_primary", "3", "gemini", "ok", 0),
    "builder-no-files-changed": ("builder_primary", "3", "claude", "no_files_changed", 1),
    "role-no-prompt": ("specifier_alpha", "0", "claude", "ok", 1),
    # probe syntax: the fake CLI checks that the TypeScript override is in the prompt exactly when engine_language is
    # typescript, then answers refused (a valid answer that needs no contract payload)
    "specifier-py-refused": ("specifier_alpha", "1", "claude", "refused", 0),
    "specifier-ts-refused": ("specifier_alpha", "1", "gemini", "refused", 0),
    "specifier-rust-error": ("specifier_alpha", "1", "claude", "ok", 1),
    # propose_properties: the test files land in /out/files under tests/; a file anywhere else fails the role
    "propose-claude-ok": ("propose_properties", "propose", "claude", "ok", 0),
    "propose-outside-tests": ("propose_properties", "propose", "gemini", "outside_tests", 1),
}
# role payload of propose_properties (role-payloads.schema.json propose_request)
PROPOSE_PAYLOAD = {
    "engine_language": "python",
    "signatures": [{"file": "src/media.py", "name": "media", "signature": "def media(xs: list[float]) -> float"}],
    "diff": "--- a/src/media.py\n+++ b/src/media.py\n@@ -1,2 +1,4 @@\n def media(xs):\n+    if not xs:\n+        raise ValueError(\"empty\")\n",
}
# role payload overrides per scenario (what the core would put in request.json)
PAYLOADS = {
    "specifier-py-refused": {"engine_language": "python"},
    "specifier-ts-refused": {"engine_language": "typescript"},
    "specifier-rust-error": {"engine_language": "rust"},
    "propose-claude-ok": PROPOSE_PAYLOAD,
    "propose-outside-tests": PROPOSE_PAYLOAD,
}


def request(role: str, stage: str, extra: dict | None = None) -> dict:
    req = json.loads((EXAMPLES / "role-request__saboteur.json").read_text(encoding="utf-8"))
    req["role"], req["stage"] = role, stage
    if role.startswith("builder_"):
        req["workdir"], req["payload"] = "/work", {}
    elif role != "saboteur":
        req["payload"] = {}
    if extra:
        req["payload"] = {**req["payload"], **extra}
    return req


def prepare(t: Path) -> None:
    for name, (role, stage, llm, fake, _) in SCENARIOS.items():
        d = t / name
        (d / "in").mkdir(parents=True)
        (d / "out").mkdir()
        (d / "work").mkdir()
        (d / "in" / "request.json").write_text(json.dumps(request(role, stage, PAYLOADS.get(name)), indent=1) + "\n", encoding="utf-8")
        for a in ("signatures.json", "escalations.json"):
            shutil.copy(SAMPLE / a, d / "in" / a)
        (d / "env").write_text(f"A2DLC_ROLE={role}\nA2DLC_STAGE={stage}\nA2DLC_LLM={llm}\nFAKE_SCENARIO={fake}\n", encoding="utf-8")


def check(t: Path) -> int:
    failures = 0
    (t / "responses").mkdir(exist_ok=True)

    def bad(name: str, msg: str) -> None:
        nonlocal failures
        failures += 1
        print(f"FAIL {name}: {msg}")

    for name, (role, stage, llm, fake, exit_expected) in SCENARIOS.items():
        d = t / name
        output = int((d / "exit").read_text().strip())
        if output != exit_expected:
            bad(name, f"expected exit {exit_expected}, got {output}: {(d / 'log').read_text(encoding='utf-8', errors='replace')[-400:]}")
            continue
        path = d / "out" / "response.json"
        if not path.is_file():
            bad(name, "did not write response.json")
            continue
        r = json.loads(path.read_text(encoding="utf-8"))
        shutil.copy(path, t / "responses" / f"role-response__{name}.json")
        req = request(role, stage, PAYLOADS.get(name))
        problems = []
        for k in ("run_id", "role", "stage", "attempt"):
            if r.get(k) != req[k]:
                problems.append(f"{k} was not copied from the request")
        files_out = sorted(p.relative_to(d / "out").as_posix() for p in (d / "out").rglob("*") if p.is_file())
        expected_status = {0: "ok", 1: "error"}[exit_expected]
        if fake == "refused":
            expected_status = "refused"
        if r["status"] != expected_status:
            problems.append(f"status {r['status']} (expected {expected_status})")
        if r["status"] != "ok" and not r.get("reason"):
            problems.append("no reason")
        if fake == "cli_fails" and "sk-test-secret" in json.dumps(r):
            problems.append("the environment credential leaked into response.json")
        # cost and model: what the CLI said, nothing made up
        # without a prompt, or with an unknown engine_language, the adapter fails before calling the CLI
        called_cli = name not in ("role-no-prompt", "specifier-rust-error")
        if llm == "claude" and called_cli and fake in ("ok", "refused", "prose", "claude_error"):
            if r["cost"] != {"usd": 0.0123, "input_tokens": 110, "cache_read_input_tokens": 10, "output_tokens": 50}:
                problems.append(f"cost {r['cost']}")
            if r["status"] != "error" and r.get("model") != "claude-fake":
                problems.append(f"model {r.get('model')}")
        if llm == "gemini" and fake in ("ok", "refused", "prose"):
            if r["cost"] is not None or r.get("model") != "gemini-fake":
                problems.append(f"gemini: cost {r['cost']} model {r.get('model')}")
        if r["status"] == "ok" and role == "saboteur":
            tf = r["payload"]["test_files"]
            if [x["path"] for x in tf] != ["tests/test_media_saboteur.py"]:
                problems.append(f"test_files {tf}")
            for x in tf:
                f = d / "out" / "tests" / x["path"]
                if not f.is_file() or "sha256:" + hashlib.sha256(f.read_bytes()).hexdigest() != x["sha256"]:
                    problems.append(f"the hash of {x['path']} is not that of the written file")
            if r["payload"]["targets"] != [req["payload"]["uncovered"][0]["file"] + "::media"]:
                problems.append(f"targets {r['payload']['targets']}")
            if files_out != ["response.json", "tests/tests/test_media_saboteur.py"]:  # /out/tests/<repo path>
                problems.append(f"files in /out: {files_out}")
        if r["status"] == "ok" and role.startswith("builder_"):
            if r["payload"] != {"summary": "Implemented what was asked.", "files_changed": ["EDITED_BY_FAKE.txt"]}:
                problems.append(f"payload {r['payload']}")
            if not (d / "work" / "EDITED_BY_FAKE.txt").is_file():
                problems.append("the CLI did not run with cwd=/work")
            if files_out != ["response.json"]:
                problems.append(f"files in /out: {files_out}")
        if fake == "no_files_changed" and "files_changed" not in r.get("reason", ""):
            problems.append(f"reason {r.get('reason')}")
        if r["status"] == "refused" and (r["payload"] != {} or files_out != ["response.json"]):
            problems.append("refused must leave payload {} and no deliveries in /out")
        if fake == "bad_path" and files_out != ["response.json"]:
            problems.append(f"a bad path left files in /out: {files_out}")
        if name == "role-no-prompt" and "no prompt for role" not in r.get("reason", ""):
            problems.append(f"reason {r.get('reason')}")
        if role == "propose_properties":
            if r["status"] == "ok":
                if files_out != ["files/tests/test_media_props.py", "response.json"]:
                    problems.append(f"files in /out: {files_out}")
                props = r["payload"].get("properties", [])
                if [x["file"] for x in props] != ["tests/test_media_props.py"] or props[0]["target"] != "src/media.py::media":
                    problems.append(f"properties {props}")
            elif files_out != ["response.json"]:
                problems.append(f"a rejected proposal left files in /out: {files_out}")
        if problems:
            bad(name, "; ".join(problems))
        else:
            print(f"ok   {name} (exit {output}, status {r['status']})")
    print(f"\n{failures} scenario failures")
    return 1 if failures else 0


if __name__ == "__main__":
    folder = Path(sys.argv[2])
    if sys.argv[1] == "prepare":
        prepare(folder)
    else:
        sys.exit(check(folder))
