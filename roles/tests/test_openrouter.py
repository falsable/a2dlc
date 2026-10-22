"""Network-free test of the A2DLC_LLM=openrouter backend of roles/adapter.py: a local HTTP server on 127.0.0.1 plays
OpenRouter and returns what the scenario asks for; the key is fake and must never appear in response.json. A local
TCP server plays an egress proxy (it records the request heads and refuses them), so the proxy route is checked without
any network.

  python -I -X utf8 roles/tests/test_openrouter.py          (standard library only)
  docker run --rm --network none -v "<worktree>:/src" -w /src --entrypoint python3 a2dlc-roles:dev-roles \
      -I -X utf8 roles/tests/test_openrouter.py              (what the image runs; no host Python needed)

Scenarios (HTTP): ok (the saboteur delivers a test and OpenRouter reports a cost), http_401 (status error with the
provider's body and the key masked), no_choices (status error), builder (no tools: sees /work in the prompt, delivers
the full file in files and the adapter writes it to /work), builder_empty (ok without files: error), propose (the
propose_properties role delivers its test under tests/), propose_bad (the same delivered under src/: error).
Checks: the probe-syntax fragments (engine_language, Probe and Postcondition in TypeScript), the propose_properties
role and its response shape, the lemmas of a builder, the prompt files, the proxy route (HTTPS_PROXY with and without
a scheme, NO_PROXY, HTTP_PROXY, the lower-case precedence), the masking of proxy credentials, and the helpers."""
from __future__ import annotations

import copy
import importlib.util
import json
import os
import shutil
import socketserver
import subprocess
import sys
import tempfile
import threading
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from types import ModuleType
from typing import Callable

ROOT = Path(__file__).resolve().parents[2]
ADAPTER = ROOT / "roles" / "adapter.py"
EXAMPLES = ROOT / "contracts" / "examples" / "valid"
SAMPLE = Path(__file__).resolve().parent / "sample"
KEY = "fake-test-key-0123456789abcdef"  # credential-looking value for clean(), not in any provider's format
PROXY_PASSWORD = "hunter2-proxy-secret"  # credential inside a proxy URL, must be masked like a key
TS_PROBE_HEADING = "## Probe expressions in TypeScript"
TS_POSTCONDITION_HEADING = "## Postconditions in TypeScript"

MODEL_OK = {"status": "ok", "reason": None, "payload": {"targets": ["src/media.py::media"]},
             "files": [{"path": "tests/test_media_or.py", "content": "def test_media_or():\n    assert True\n"}]}
MODEL_PROPOSE = {"status": "ok", "reason": None,
                 "payload": {"properties": [{"target": "src/media.py::media",
                                             "property": "the mean lies between the smallest and the largest element",
                                             "file": "tests/test_media_or.py"}]},
                 "files": [{"path": "tests/test_media_or.py", "content": "def test_media_or():\n    assert True\n"}]}
MODEL_PROPOSE_BAD = {"status": "ok", "reason": None,
                     "payload": {"properties": [{"target": "src/media.py::media", "property": "p",
                                                 "file": "src/media.py"}]},
                     "files": [{"path": "src/media.py", "content": "def media(xs):\n    return 0\n"}]}
CALC_NEW = "def add(a, b):\n    return a + b\n\n\ndef subtract(a, b):\n    return a - b\n"
MODEL_BUILDER = {"status": "ok", "reason": None, "payload": {"summary": "add subtraction", "files_changed": ["whatever the model says is ignored"]},
                  "files": [{"path": "src/calc.py", "content": CALC_NEW}]}
MODEL_BUILDER_EMPTY = {"status": "ok", "reason": None, "payload": {"summary": "nothing", "files_changed": []}, "files": []}
MODELS = {"builder": MODEL_BUILDER, "builder_empty": MODEL_BUILDER_EMPTY, "propose": MODEL_PROPOSE,
          "propose_bad": MODEL_PROPOSE_BAD}


class Fake(BaseHTTPRequestHandler):
    scenario = "ok"
    received: list[dict] = []

    def do_POST(self):  # noqa: N802
        n = int(self.headers.get("Content-Length", "0"))
        body = json.loads(self.rfile.read(n).decode("utf-8"))
        Fake.received.append({"auth": self.headers.get("Authorization"), "body": body})
        if Fake.scenario == "http_401":
            doc, code = {"error": {"message": "User not found. key=" + KEY, "code": 401}}, 401
        elif Fake.scenario == "no_choices":
            doc, code = {"id": "x", "model": body["model"], "choices": []}, 200
        elif Fake.scenario == "entropos_ok":
            doc, code = {
                "id": "chatcmpl-entropos-1",
                "model": "deepseek-r1",
                "choices": [{"message": {"role": "assistant", "content": json.dumps(MODEL_OK)}}],
                "usage": {"prompt_tokens": 1200, "completion_tokens": 80, "cost": 0.00045},
                "_entropos_router_meta": {
                    "provider": "deepseek",
                    "routedModel": "deepseek-r1",
                    "cost_usd": 0.00045,
                    "saved_usd": 0.00165,
                    "discount_pct": "78.5%",
                    "failover": False,
                },
            }, 200
        elif Fake.scenario == "entropos_401":
            doc, code = {"error": {"message": "Missing, invalid or revoked Entropos API key: " + KEY, "code": "invalid_api_key"}}, 401
        elif Fake.scenario in MODELS:
            m = MODELS[Fake.scenario]
            doc, code = {"id": "gen-2", "model": body["model"], "choices": [{"message": {"role": "assistant", "content": json.dumps(m)}}],
                         "usage": {"prompt_tokens": 900, "completion_tokens": 60, "cost": 0.0011}}, 200
        else:
            doc, code = {"id": "gen-1", "model": body["model"], "choices": [{"message": {"role": "assistant",
                         "content": "Here it goes:\n```json\n" + json.dumps(MODEL_OK) + "\n```"}}],
                         "usage": {"prompt_tokens": 1200, "completion_tokens": 80, "cost": 0.0021}}, 200
        data = json.dumps(doc).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, *a):  # silence
        pass


class RefusingProxy(socketserver.ThreadingTCPServer):
    """Plays an egress proxy: it records the head of every request it gets, then answers 502."""
    allow_reuse_address = True
    daemon_threads = True

    def __init__(self) -> None:
        self.seen: list[bytes] = []
        super().__init__(("127.0.0.1", 0), ProxyStub)


class ProxyStub(socketserver.StreamRequestHandler):
    def handle(self) -> None:
        head = b""
        while not head.endswith(b"\r\n\r\n") and len(head) < 65536:
            line = self.rfile.readline()
            if not line:
                break
            head += line
        self.server.seen.append(head)  # type: ignore[attr-defined]
        self.wfile.write(b"HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")


def seen_text(proxy: RefusingProxy) -> str:
    return b"".join(proxy.seen).decode("latin-1")


def load_adapter() -> ModuleType:
    spec = importlib.util.spec_from_file_location("a2dlc_adapter", ADAPTER)
    assert spec and spec.loader
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def raises(fn: Callable[[], object], exc: type[Exception]) -> bool:
    try:
        fn()
    except exc:
        return True
    return False


def run(t: Path, scenario: str, role: str, stage: str, url: str, payload: dict | None = None,
        env_extra: dict | None = None) -> tuple[int, dict]:
    d = t / scenario
    (d / "in").mkdir(parents=True)
    (d / "out").mkdir()
    req = json.loads((EXAMPLES / "role-request__saboteur.json").read_text(encoding="utf-8"))
    req["role"], req["stage"] = role, stage
    if payload is not None:
        req["payload"].update(payload)
    # the test owns the environment: no A2DLC_ leftovers, no credential, no proxy variable from the host
    env = {k: v for k, v in os.environ.items()
           if not k.startswith("A2DLC_") and k != "OPENROUTER_API_KEY" and not k.lower().endswith("_proxy") and k.lower() != "no_proxy"}
    if role.startswith("builder_"):
        req["workdir"], req["payload"] = "/work", {"intent": {"goal": "add subtraction"}, "contract": None, "probes": None, "round": 1, "max_rounds": 3, "tier0_feedback": None}
        # test /work: one source file, one binary and the gate config (which does not go into the prompt)
        (d / "work" / "src").mkdir(parents=True)
        (d / "work" / "src" / "calc.py").write_text("def add(a, b):\n    return a + b\n", encoding="utf-8")
        (d / "work" / "logo.bin").write_bytes(b"\xff\xfe\x00binary")
        (d / "work" / ".a2dlc").mkdir()
        (d / "work" / ".a2dlc" / "config.json").write_text("{}", encoding="utf-8")
        env["A2DLC_WORKDIR"] = str(d / "work")
    (d / "in" / "request.json").write_text(json.dumps(req, indent=1) + "\n", encoding="utf-8")
    for a in ("signatures.json", "escalations.json"):
        shutil.copy(SAMPLE / a, d / "in" / a)
    env.update({"A2DLC_REQUEST": str(d / "in" / "request.json"), "A2DLC_RESPONSE": str(d / "out" / "response.json"),
                "A2DLC_ROLE": role, "A2DLC_STAGE": stage, "A2DLC_LLM": "openrouter", "A2DLC_MODEL": "provider/model-x",
                "A2DLC_OPENROUTER_URL": url, "OPENROUTER_API_KEY": KEY, "A2DLC_TIMEOUT_S": "20",
                "A2DLC_PROMPTS_DIR": str(ROOT / "roles" / "prompts")})
    env.update(env_extra or {})
    Fake.scenario = scenario
    r = subprocess.run([sys.executable, "-I", "-X", "utf8", str(ADAPTER)], env=env, capture_output=True, text=True, encoding="utf-8")
    resp = json.loads((d / "out" / "response.json").read_text(encoding="utf-8"))
    return r.returncode, resp


def last_prompt() -> str:
    return Fake.received[-1]["body"]["messages"][0]["content"]


def check_http(t: Path, url: str, failures: list[str]) -> None:
    code, r = run(t, "ok", "saboteur", "4", url)
    p = Fake.received[-1]
    expected = (code == 0 and r["status"] == "ok" and r["cost"] == {"usd": 0.0021, "input_tokens": 1200, "output_tokens": 80}
                and r["model"] == "provider/model-x" and r["payload"]["targets"] == ["src/media.py::media"]
                and r["payload"]["test_files"][0]["path"] == "tests/test_media_or.py"
                and (t / "ok" / "out" / "tests" / "tests" / "test_media_or.py").exists()
                and p["auth"] == "Bearer " + KEY and p["body"]["model"] == "provider/model-x"
                and p["body"]["usage"] == {"include": True} and "request.json" in p["body"]["messages"][0]["content"])
    if not expected:
        failures.append(f"ok: exit={code} {json.dumps(r)[:300]}")
    code, r = run(t, "http_401", "saboteur", "4", url)
    if not (code == 1 and r["status"] == "error" and "401" in r["reason"] and KEY not in json.dumps(r)):
        failures.append(f"http_401: exit={code} {json.dumps(r)[:300]}")
    code, r = run(t, "no_choices", "saboteur", "4", url)
    if not (code == 1 and r["status"] == "error" and "choices" in r["reason"]):
        failures.append(f"no_choices: exit={code} {json.dumps(r)[:300]}")
    code, r = run(t, "builder", "builder_primary", "3", url)
    prompt = last_prompt()
    written = (t / "builder" / "work" / "src" / "calc.py").read_text(encoding="utf-8")
    expected = (code == 0 and r["status"] == "ok" and r["payload"] == {"summary": "add subtraction", "files_changed": ["src/calc.py"]}
                and written == CALC_NEW and r["cost"]["usd"] == 0.0011
                and "### /work/src/calc.py" in prompt and "def add" in prompt and "You have NO tools" in prompt
                and "logo.bin (binary)" in prompt and ".a2dlc" not in prompt.split("## /work\n\nThe repository")[1]
                and not (t / "builder" / "out" / "files").exists())
    if not expected:
        failures.append(f"builder: exit={code} {json.dumps(r)[:300]} prompt_work={'## /work' in prompt}")
    code, r = run(t, "builder_empty", "builder_primary", "3", url)
    if not (code == 1 and r["status"] == "error" and "with no file at all" in r["reason"]):
        failures.append(f"builder_empty: exit={code} {json.dumps(r)[:300]}")


def check_entropos(t: Path, url: str, failures: list[str]) -> None:
    """Entropos Complexity Router (A2DLC_LLM=entropos): routes to dynamically selected model,
    tracks cost from usage.cost, parses _entropos_router_meta, and validates bearer auth."""
    code, r = run(t, "entropos_ok", "saboteur", "4", url,
                  env_extra={"A2DLC_LLM": "entropos", "A2DLC_ENTROPOS_URL": url, "ENTROPOS_API_KEY": KEY})
    p = Fake.received[-1]
    expected = (code == 0 and r["status"] == "ok" and r["cost"] == {"usd": 0.00045, "input_tokens": 1200, "output_tokens": 80}
                and r["model"] == "deepseek-r1" and p["auth"] == "Bearer " + KEY
                and p["body"]["model"] == "provider/model-x" and "request.json" in p["body"]["messages"][0]["content"])
    if not expected:
        failures.append(f"entropos_ok: exit={code} {json.dumps(r)[:300]}")
    code, r = run(t, "entropos_401", "saboteur", "4", url,
                  env_extra={"A2DLC_LLM": "entropos", "A2DLC_ENTROPOS_URL": url, "ENTROPOS_API_KEY": KEY})
    if not (code == 1 and r["status"] == "error" and "401" in r["reason"] and KEY not in json.dumps(r)):
        failures.append(f"entropos_401: exit={code} {json.dumps(r)[:300]}")


def check_probe_syntax(t: Path, url: str, failures: list[str]) -> None:
    """engine_language: a TypeScript run gets the TypeScript override; Python (explicit or absent) gets none, and the
    prompt's own text is unchanged; an unknown value fails the run."""
    code, r = run(t, "probe_ts", "specifier_alpha", "1", url, payload={"engine_language": "typescript"})
    if not (code == 0 and r["status"] == "ok" and TS_PROBE_HEADING in last_prompt() and "raises(RangeError, media, [])" in last_prompt()):
        failures.append(f"probe_ts: exit={code} {json.dumps(r)[:200]}")
    code, r = run(t, "probe_py", "specifier_alpha", "1", url, payload={"engine_language": "python"})
    if not (code == 0 and TS_PROBE_HEADING not in last_prompt() and "(Python today)" in last_prompt()):
        failures.append(f"probe_py: exit={code} {json.dumps(r)[:200]}")
    code, r = run(t, "probe_absent", "specifier_alpha", "1", url)
    if not (code == 0 and TS_PROBE_HEADING not in last_prompt() and '"engine_language"' not in last_prompt()):
        failures.append(f"probe_absent: exit={code} {json.dumps(r)[:200]}")
    code, r = run(t, "probe_unknown", "specifier_alpha", "1", url, payload={"engine_language": "rust"})
    if not (code == 1 and r["status"] == "error" and "engine_language" in r["reason"] and "rust" in r["reason"]):
        failures.append(f"probe_unknown: exit={code} {json.dumps(r)[:200]}")


def check_propose(t: Path, url: str, failures: list[str]) -> None:
    """propose_properties (a2dlc propose): not a builder, so its files land in /out/files and it has no tools. A
    delivery under src/ is refused and leaves nothing in /out."""
    code, r = run(t, "propose", "propose_properties", "propose", url)
    written = t / "propose" / "out" / "files" / "tests" / "test_media_or.py"
    if not (code == 0 and r["status"] == "ok" and written.exists() and "# Role: propose_properties" in last_prompt()
            and "Response format (mandatory)" in last_prompt() and not (t / "propose" / "work").exists()
            and r["payload"] == MODEL_PROPOSE["payload"]):
        failures.append(f"propose: exit={code} {json.dumps(r)[:200]}")
    code, r = run(t, "propose_bad", "propose_properties", "propose", url)
    if not (code == 1 and r["status"] == "error" and "under tests/" in r["reason"]
            and not (t / "propose_bad" / "out" / "files").exists()):
        failures.append(f"propose_bad: exit={code} {json.dumps(r)[:200]}")


def check_proxy(t: Path, url: str, failures: list[str]) -> None:
    """The openrouter call goes through HTTPS_PROXY (CONNECT line seen by the proxy, no key on the wire), a scheme-less
    proxy value is the same HTTP proxy, NO_PROXY sends a host direct, and the proxy's password never reaches a reason."""
    proxy = RefusingProxy()
    threading.Thread(target=proxy.serve_forever, daemon=True).start()
    port = proxy.server_address[1]
    proxy_url = f"http://user:{PROXY_PASSWORD}@127.0.0.1:{port}"
    plain = f"http://127.0.0.1:{port}"
    try:
        target = "https://openrouter.ai/api/v1/chat/completions"
        code, r = run(t, "proxy_connect", "saboteur", "4", target, env_extra={"HTTPS_PROXY": proxy_url})
        lines = seen_text(proxy)
        if not (code == 1 and r["status"] == "error" and "did not answer" in r["reason"]
                and "CONNECT openrouter.ai:443 " in lines and KEY not in lines and "Bearer" not in lines
                and PROXY_PASSWORD not in json.dumps(r)):
            failures.append(f"proxy_connect: exit={code} seen={lines[:120]!r} {json.dumps(r)[:200]}")
        # HTTPS_PROXY=host:port with no scheme, as curl takes it: the same proxy, the same CONNECT
        proxy.seen.clear()
        code, r = run(t, "proxy_schemeless", "saboteur", "4", target, env_extra={"HTTPS_PROXY": f"127.0.0.1:{port}"})
        lines = seen_text(proxy)
        if not (code == 1 and r["status"] == "error" and "CONNECT openrouter.ai:443 " in lines):
            failures.append(f"proxy_schemeless: exit={code} seen={lines[:120]!r} {json.dumps(r)[:200]}")
        # NO_PROXY lists the host: plain HTTP goes straight to the local fake; the refusing proxy records nothing.
        # This end-to-end case cannot tell the adapter's NO_PROXY reading from urllib's own (it reads NO_PROXY from the
        # child's environment too); check_proxy_env checks the adapter's reading in-process.
        proxy.seen.clear()
        code, r = run(t, "proxy_bypass", "saboteur", "4", url,
                      env_extra={"HTTP_PROXY": plain, "NO_PROXY": "localhost,127.0.0.1"})
        if not (code == 0 and r["status"] == "ok" and not proxy.seen):
            failures.append(f"proxy_bypass: exit={code} seen={seen_text(proxy)[:120]!r} {json.dumps(r)[:200]}")
        # control for the bypass: the same proxy without NO_PROXY is used, so the stub records the request
        proxy.seen.clear()
        code, r = run(t, "proxy_listed", "saboteur", "4", url, env_extra={"HTTP_PROXY": plain})
        if not (code == 1 and r["status"] == "error" and proxy.seen):
            failures.append(f"proxy_listed: exit={code} seen={seen_text(proxy)[:120]!r} {json.dumps(r)[:200]}")
        # a dead proxy is used when nothing bypasses it, so the call fails: the variable is honoured, not ignored
        code, r = run(t, "proxy_dead", "saboteur", "4", url, env_extra={"HTTP_PROXY": "http://127.0.0.1:9"})
        if not (code == 1 and r["status"] == "error"):
            failures.append(f"proxy_dead: exit={code} {json.dumps(r)[:200]}")
    finally:
        proxy.shutdown()
        proxy.server_close()


def check_prompts(failures: list[str]) -> None:
    """The prompt additions of this change are asserted here: deleting one of them turns this suite red."""
    prompts = ROOT / "roles" / "prompts"
    needles = {
        "builder_primary.md": ["payload.paradigm", "payload.critical", "Postcondition:", "payload.lemmas"],
        "builder_orthogonal.md": ["payload.paradigm", "payload.critical", "Postcondition:", "payload.lemmas"],
        "propose_properties.md": ["# Role: propose_properties", "payload.properties", "under `tests/`"],
    }
    for name, wanted in needles.items():
        text = (prompts / name).read_text(encoding="utf-8")
        for n in wanted:
            if n not in text:
                failures.append(f"{name} lacks {n!r}")


def check_proxy_env(failures: list[str]) -> None:
    """curl reads the lower-case proxy variable first, and a host named in either NO_PROXY spelling goes direct
    (measured with curl 7.88 on Linux). Windows folds the case of environment names, so the pair cannot exist there."""
    if os.name == "nt":
        return
    adapter = load_adapter()
    names = ("http_proxy", "HTTP_PROXY", "no_proxy", "NO_PROXY")
    saved = {k: os.environ.get(k) for k in names}

    def proxies(url: str) -> dict:
        # an empty ProxyHandler registers no handler: no proxy at all, and nothing read from the environment
        opener = adapter.openrouter_opener(url)
        found = [h for h in opener.handlers if isinstance(h, urllib.request.ProxyHandler)]
        return found[0].proxies if found else {}

    def only(**values: str) -> None:
        for k in names:
            os.environ.pop(k, None)
        os.environ.update(values)

    p1, p2 = "http://127.0.0.1:9101", "http://127.0.0.1:9102"
    try:
        only(HTTP_PROXY=p1, http_proxy=p2)
        if proxies("http://example.test/") != {"http": p2}:
            failures.append(f"http_proxy did not win over HTTP_PROXY: {proxies('http://example.test/')}")
        only(HTTP_PROXY=p1, no_proxy="example.test", NO_PROXY="other.test")
        if proxies("http://example.test/") != {}:
            failures.append("a host named in no_proxy did not bypass the proxy")
        only(HTTP_PROXY=p1, no_proxy="other.test", NO_PROXY="example.test")
        if proxies("http://example.test/") != {}:
            failures.append("a host named in NO_PROXY did not bypass the proxy")
        only(HTTP_PROXY=p1, no_proxy="other.test", NO_PROXY="third.test")  # control: nothing names the host
        if proxies("http://example.test/") != {"http": p1}:
            failures.append("the proxy was not used when no NO_PROXY spelling named the host")
    finally:
        for k, v in saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v


def check_helpers(failures: list[str]) -> None:
    adapter = load_adapter()
    cases = [
        (adapter.no_proxy_matches("openrouter.ai", "openrouter.ai"), True),
        (adapter.no_proxy_matches("api.openrouter.ai", ".openrouter.ai"), True),
        (adapter.no_proxy_matches("evilopenrouter.ai", "openrouter.ai"), False),
        (adapter.no_proxy_matches("10.0.0.5", "*"), True),
        (adapter.no_proxy_matches("127.0.0.1", "localhost:8080, 127.0.0.1:9"), True),
        (adapter.no_proxy_matches("openrouter.ai", ""), False),
    ]
    for got, want in cases:
        if got is not want:
            failures.append(f"no_proxy_matches case {got!r} != {want!r}")
    # the probe syntax: absent means python; a payload that is not an object, or an engine_language that is not a
    # known string (null included), fails the run instead of running as python
    if not raises(lambda: adapter.probe_syntax({"payload": {"engine_language": "rust"}}), adapter.Failure):
        failures.append("probe_syntax accepted rust")
    for bad in ({"payload": [1]}, {"payload": None}, {"payload": {"engine_language": None}}, {"payload": {"engine_language": 3}}):
        if not raises(lambda bad=bad: adapter.probe_syntax(bad), adapter.Failure):
            failures.append(f"probe_syntax accepted {bad!r}")
    if adapter.probe_syntax({"payload": {}}) != "" or adapter.probe_syntax({}) != "":
        failures.append("probe_syntax: python or absent must add nothing")
    ts = adapter.probe_syntax({"payload": {"engine_language": "typescript"}})
    if "raises(ErrorCtor, fn, ...args)" not in ts or TS_POSTCONDITION_HEADING not in ts:
        failures.append("probe_syntax typescript lacks the raises helper or the postcondition section")
    # the instructions of a builder: the TypeScript run carries the TypeScript postcondition, the Python run does not
    ts_builder = adapter.instructions({"role": "builder_primary", "stage": "3", "payload": {"engine_language": "typescript"}})
    py_builder = adapter.instructions({"role": "builder_primary", "stage": "3", "payload": {"engine_language": "python"}})
    if TS_POSTCONDITION_HEADING not in ts_builder or "throw new Error" not in ts_builder:
        failures.append("a typescript builder prompt lacks the TypeScript postcondition")
    if TS_POSTCONDITION_HEADING in py_builder or "Postcondition:" not in py_builder:
        failures.append("a python builder prompt gained the TypeScript fragment or lost the postcondition text")
    # the lemmas of a builder: optional, shape checked, kept when the files are delivered by the model
    lemma = {"name": "media", "postcondition": "result >= 0"}
    obj = {"status": "ok", "reason": None, "files": [], "payload": {"summary": "s", "files_changed": ["a.py"], "lemmas": [lemma]}}
    _, _, _, payload = adapter.validate_model(copy.deepcopy(obj), True)
    if payload.get("lemmas") != [lemma]:
        failures.append(f"builder lemmas were not kept: {payload}")
    delivered = {"status": "ok", "reason": None, "files": [{"path": "src/a.py", "content": "x = 1\n"}],
                 "payload": {"summary": "s", "files_changed": ["ignored"], "lemmas": [lemma]}}
    _, _, files, payload = adapter.validate_model(copy.deepcopy(delivered), True, True)
    if payload != {"summary": "s", "files_changed": ["src/a.py"], "lemmas": [lemma]} or files != [("src/a.py", "x = 1\n")]:
        failures.append(f"delivered builder payload with lemmas: {payload}")
    for bad_lemmas in ([{"name": "media"}], [{"name": "media", "postcondition": "x", "extra": 1}],
                       [{"name": "", "postcondition": "x"}], "result >= 0", [lemma, 7]):
        bad = copy.deepcopy(obj)
        bad["payload"]["lemmas"] = bad_lemmas
        if not raises(lambda bad=bad: adapter.validate_model(bad, True), adapter.Failure):
            failures.append(f"builder accepted lemmas {bad_lemmas!r}")
    extra = copy.deepcopy(obj)
    extra["payload"]["other"] = 1
    if not raises(lambda: adapter.validate_model(extra, True), adapter.Failure):
        failures.append("builder accepted a payload key outside summary, files_changed and lemmas")
    # propose_properties: the response shape of role-payloads propose_response, and its tests/ rule
    tests_file = [("tests/test_media_or.py", "def test_x() -> None:\n    pass\n")]
    good = {"properties": [{"target": "src/media.py::media", "property": "p", "file": "tests/test_media_or.py"}]}
    try:
        adapter.validate_propose(copy.deepcopy(good), tests_file)
    except adapter.Failure as f:
        failures.append(f"validate_propose refused a good proposal: {f.reason}")
    bad_proposals = [
        ({"summary": "s", "targets": ["src/media.py::media"]}, tests_file),  # the old shape
        (good, [("src/media.py", "x = 1\n")]),  # a file outside tests/
        ({"properties": [{"target": "src/media.py::media", "property": "p", "file": "tests/other.py"}]}, tests_file),
        ({"properties": []}, tests_file),  # ok with no property
        ({"properties": [{"target": "media", "property": "p", "file": "tests/test_media_or.py"}]}, tests_file),
        ({"properties": [{"target": "src/media.py::media", "property": "", "file": "tests/test_media_or.py"}]}, tests_file),
        ({"properties": [{"target": "src/media.py::media", "property": "p", "file": ["tests/test_media_or.py"]}]}, tests_file),
    ]
    for payload, files in bad_proposals:
        if not raises(lambda payload=payload, files=files: adapter.validate_propose(copy.deepcopy(payload), files), adapter.Failure):
            failures.append(f"validate_propose accepted {json.dumps(payload)[:120]}")
    saved = {k: os.environ.get(k) for k in ("HTTPS_PROXY", "NO_PROXY")}
    try:
        # the password of a proxy URL is masked, raw and percent-encoded, with a scheme and without one
        os.environ["HTTPS_PROXY"] = f"http://u:{PROXY_PASSWORD}@proxy.internal:3128"
        masked = adapter.clean(f"could not reach http://u:{PROXY_PASSWORD}@proxy.internal:3128 now")
        if PROXY_PASSWORD in masked or "***" not in masked:
            failures.append(f"clean did not mask the proxy credential: {masked}")
        encoded = PROXY_PASSWORD.replace("2", "%32")
        os.environ["HTTPS_PROXY"] = f"http://u:{encoded}@proxy.internal:3128"
        masked = adapter.clean(f"refused {PROXY_PASSWORD} by proxy.internal")
        if PROXY_PASSWORD in masked:
            failures.append(f"clean did not mask the decoded proxy password: {masked}")
        os.environ["HTTPS_PROXY"] = f"u:{PROXY_PASSWORD}@proxy.internal:3128"
        masked = adapter.clean(f"reached u:{PROXY_PASSWORD}@proxy.internal:3128 with no scheme")
        if PROXY_PASSWORD in masked:
            failures.append(f"clean did not mask a scheme-less proxy password: {masked}")
        # NO_PROXY is not a secret: a host named there stays readable in a reason
        os.environ["HTTPS_PROXY"] = f"http://u:{PROXY_PASSWORD}@proxy.internal:3128"
        os.environ["NO_PROXY"] = "openrouter.ai"
        if adapter.clean("openrouter.ai did not answer") != "openrouter.ai did not answer":
            failures.append("clean masked NO_PROXY, which is not a credential")
    finally:
        for k, v in saved.items():
            if v is None:
                os.environ.pop(k, None)
            else:
                os.environ[k] = v


def main() -> int:
    srv = HTTPServer(("127.0.0.1", 0), Fake)
    url = f"http://127.0.0.1:{srv.server_address[1]}/v1/chat/completions"
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    failures: list[str] = []
    checks = 0
    with tempfile.TemporaryDirectory() as tmp:
        t = Path(tmp)
        for name, fn in (("http", lambda: check_http(t, url, failures)),
                         ("entropos", lambda: check_entropos(t, url, failures)),
                         ("probe-syntax", lambda: check_probe_syntax(t, url, failures)),
                         ("propose", lambda: check_propose(t, url, failures)),
                         ("prompts", lambda: check_prompts(failures)),
                         ("proxy", lambda: check_proxy(t, url, failures)),
                         ("proxy-env", lambda: check_proxy_env(failures)),
                         ("helpers", lambda: check_helpers(failures))):
            before = len(failures)
            fn()
            checks += 1
            print(("ok   " if len(failures) == before else "FAIL ") + name)
    srv.shutdown()
    for f in failures:
        print("FAIL", f)
    print(f"{checks} groups run; {len(failures)} failure(s)")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
