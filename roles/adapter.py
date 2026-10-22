#!/usr/bin/env python3
"""Generic adapter for the LLM roles of `a2dlc run` (A2-DLC protocol §3).

The core launches this script inside the role's container (image a2dlc-roles) with:
  /in          read-only: request.json (role-request) and the artifacts the policy lets this role see
  /out         empty and writable: response.json (role-response) and the files the role delivers go here
  /work        only for builders: writable copy of the tree (cwd)
  A2DLC_REQUEST, A2DLC_RESPONSE, A2DLC_ROLE, A2DLC_STAGE   set by the core
  A2DLC_TIMEOUT_S   role cap in seconds (roles.<role>.timeout_s): the CLI is cut a little earlier, with an error
and the provider credential arrives through --env-file: with claude or gemini only the agent's CLI reads it, never this
script; with openrouter or entropos this script is the HTTP client and sends the key in the header, never writing it out.

What it does: builds the prompt (role instructions + request.json + each file in /in), calls the headless CLI chosen
with A2DLC_LLM (claude | gemini | openrouter | entropos; default claude), requires the model to return ONE JSON object
  {"status": "ok"|"refused", "reason": str|null, "files": [{"path", "content"}], "payload": {...}}
and translates it into /out/response.json. The model's files are written under /out/tests/<path> (saboteur) or
/out/files/<path> (the others). For the saboteur, payload.test_files is computed by this script from the sha256 of what it wrote.

Optional variables (roles.<role>.env can set them):
  A2DLC_LLM             claude | gemini | openrouter | entropos. openrouter and entropos call the OpenAI/Entropos
                        API (chat/completions) with the API key from the --env-file. A builder with openrouter or
                        entropos has no tools: it receives in the prompt the files of /work (text, up to the limits below)
                        and delivers in "files" the full content of each file it changes or creates; this script writes
                        them to /work and answers with files [] and payload {summary, files_changed} like any builder.
  A2DLC_WORKDIR         TESTS ONLY: folder that replaces /work.
  A2DLC_MODEL           model passed to the CLI (--model / -m); without it, the CLI's default. With openrouter it is
                        the model id (default anthropic/claude-haiku-4.5). With entropos it is the routing model
                        (default entropos-auto).
  A2DLC_OPENROUTER_URL  TESTS ONLY: URL that replaces https://openrouter.ai/api/v1/chat/completions
  A2DLC_ENTROPOS_URL    URL that replaces http://host.docker.internal:3000/v1/chat/completions
  A2DLC_PROMPTS_DIR     prompts folder (default prompts/ next to this file)
  A2DLC_LLM_CMD         TESTS ONLY: command (split as a shell would) that replaces the CLI executable; it receives the
                        same arguments and the prompt on stdin. roles/tests/fake_cli.py uses it to test the adapter
                        without network or credentials.

Role prompt: prompts/<role>.<stage>.md if it exists (e.g. builder_primary.3.md), else prompts/<role>.md. With neither
of them, the adapter answers with an error: it does not make up instructions.

Output: exit 0 with status ok or refused; exit 1 with status error (CLI down, output that is not JSON, files outside
the contract). If the request cannot even be read, there is no response.json and exit 1.
"""
from __future__ import annotations

import hashlib
import json
import os
import posixpath
import re
import shlex
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

MAX_PROMPT_BYTES = 2_000_000
# /work in the prompt of a builder without tools (openrouter): per file and in total; what does not fit is only listed
MAX_WORK_FILE_BYTES = 200_000
MAX_WORK_TOTAL_BYTES = 600_000
WORK_EXCLUDE = (".a2dlc", ".git", "node_modules", "__pycache__", ".venv", "dist", "build")
TARGET_ID = re.compile(r"^[^\\]+::.+$")  # contracts/defs.schema.json#/$defs/target_id
# builders edit the tree in /work; the rest only answer with the JSON
BUILDER_PREFIX = "builder_"
PROPOSE_ROLE = "propose_properties"
CLAUDE_BUILDER_TOOLS = "Read,Edit,Write,Glob,Grep,Bash"
GEMINI_STDIN_PROMPT = "Follow the instructions and inputs you receive on stdin and answer with the requested JSON object."
DEFAULT_ENGINE_LANGUAGE = "python"
# The prompts show probe expressions and postcondition asserts in Python (the engine of the repo today). The core sends
# engine_language in the role payload (the engine that covers the function's file); for any other language this fragment
# is appended, so the prompt's own Python examples are overridden for that run. Python gets no fragment: its prompt
# text is unchanged.
PROBE_SYNTAX = {
    "python": "",
    "typescript": """## Probe expressions in TypeScript (this run: engine_language = "typescript")

The engine of this slice is TypeScript. Wherever the instructions above show an `expr` in Python, write it in TypeScript
instead, with these rules:
- one boolean expression over the module's exported names, with `===` and `!==` (never `==`), e.g. `media([2.5]) === 2.5`;
- for a failure, `raises(ErrorCtor, fn, ...args)` is true when calling `fn` with those arguments throws an instance of
  `ErrorCtor`, e.g. `raises(RangeError, media, [])`. `ErrorCtor` is a global error class (`Error`, `TypeError`,
  `RangeError`) or a class the module exports;
- a single expression: no statements, no `await`, no imports. `input` and `expected` stay as they are; the expression
  must match them.

## Postconditions in TypeScript (this run: engine_language = "typescript")

Where the instructions above say to enforce a postcondition with a final Python `assert`, enforce it in TypeScript
instead: just before the return, `if (!(<the postcondition expression>)) throw new Error("postcondition of <name> violated");`.
The `Postcondition: ...` line of the docstring stays as it is, in a JSDoc comment.
""",
}

OUTPUT_JSON = """\
## Response format (mandatory)

Your final answer is ONE single JSON object, with no text before or after it and no code block, with this exact shape:

{"status": "ok" | "refused", "reason": string | null, "files": [{"path": string, "content": string}], "payload": object}

- status "ok": you did what was asked. status "refused": you cannot do it with what you saw in the inputs; reason says
  what is missing or why, files is [] and payload is {}.
- files: the files you deliver, with their full content in content. path is relative, with "/" as separator, no "..",
  no absolute path and no backslashes.
- payload: what your role asks for (below, in the instructions). Do not invent fields.
- Do not use tools to write your delivery: the content goes in "files" of the JSON, not on disk.
"""

OUTPUT_BUILDER = """\
## Response format (mandatory)

You work by editing files with your tools in the current directory (/work). When you finish, your final answer is ONE
single JSON object, with no text before or after it and no code block:

{"status": "ok" | "refused", "reason": string | null, "files": [], "payload": {"summary": string, "files_changed": [string]}}

- status "refused": you cannot do it with what you saw in the inputs; reason says what is missing and payload is {}.
- files is always []. payload.summary summarizes in a few lines what you changed and why; payload.files_changed lists
  the paths you edited, relative to the repository root (no repeats). Both keys are mandatory and there are no others.
"""

OUTPUT_BUILDER_NO_TOOLS = """\
## Response format (mandatory)

You have NO tools and no disk access: wherever the instructions say "edit /work with your tools", here you deliver the
result instead. Below, under "## /work", you have the repository tree as it is now (your changes from earlier rounds
are already applied). Your final answer is ONE single JSON object, with no text before or after it and no code block:

{"status": "ok" | "refused", "reason": string | null, "files": [{"path": string, "content": string}], "payload": {"summary": string, "files_changed": [string]}}

- files: each file you change or create, with its FULL FINAL CONTENT in content (not a diff, not a fragment: what you
  deliver replaces the whole file). path is relative to the repository root, with "/" as separator, no ".." and no
  absolute path. Files you do not deliver stay as they are. Do not deliver files under .a2dlc/.
- status "refused": you cannot do it with what you saw in the inputs; reason says what is missing, files is [] and
  payload {}.
- payload.summary summarizes in a few lines what you changed and why; payload.files_changed lists the paths in files.
"""


class Failure(Exception):
    """A failure that is reported as status error in response.json."""

    def __init__(self, reason: str, cost: dict | None = None, model: str | None = None):
        super().__init__(reason)
        self.reason, self.cost, self.model = reason, cost, model


def is_builder(role: str) -> bool:
    return role.startswith(BUILDER_PREFIX)


# ---------- inputs ----------


def read_request(path: Path) -> dict:
    req = json.loads(path.read_text(encoding="utf-8"))
    for k in ("run_id", "role", "stage", "attempt"):
        if k not in req:
            raise ValueError(f"request.json is missing {k}")
    return req


def prompts_dir() -> Path:
    return Path(os.environ.get("A2DLC_PROMPTS_DIR") or Path(__file__).resolve().parent / "prompts")


def role_prompt(role: str, stage: str) -> str:
    base = prompts_dir()
    for name in (f"{role}.{stage}.md", f"{role}.md"):
        p = base / name
        if p.is_file():
            return p.read_text(encoding="utf-8")
    raise Failure(f"no prompt for role {role} (looked for {role}.{stage}.md and {role}.md in {base})")


def probe_syntax(req: dict) -> str:
    """The fragment that fixes the probe expression language of this run (engine_language in the role payload).
    Absent (no payload, or a payload without the key) means python. A payload that is not an object, or an
    engine_language that is present but not a known string (null included), fails like a missing prompt: the adapter
    does not guess the syntax."""
    if "payload" not in req:
        return PROBE_SYNTAX[DEFAULT_ENGINE_LANGUAGE]
    payload = req["payload"]
    if not isinstance(payload, dict):
        raise Failure(f"payload of the request is {type(payload).__name__}, not an object: engine_language cannot be read")
    if "engine_language" not in payload:
        return PROBE_SYNTAX[DEFAULT_ENGINE_LANGUAGE]
    language = payload["engine_language"]
    if not isinstance(language, str) or language not in PROBE_SYNTAX:
        raise Failure(f"engine_language={language!r} in the role payload: only {' or '.join(PROBE_SYNTAX)}")
    return PROBE_SYNTAX[language]


def instructions(req: dict) -> str:
    """The role prompt plus, for a non-Python engine, the probe syntax override."""
    text = role_prompt(req["role"], req["stage"])
    extra = probe_syntax(req)
    return text + "\n\n" + extra if extra else text


def input_files(in_dir: Path, request: Path) -> list[tuple[str, str]]:
    out = []
    for p in sorted(in_dir.rglob("*")):
        if p.is_file() and p.resolve() != request.resolve():
            out.append((p.relative_to(in_dir).as_posix(), p.read_text(encoding="utf-8", errors="replace")))
    return out


def work_dir() -> Path:
    return Path(os.environ.get("A2DLC_WORKDIR") or "/work")


def work_files(work: Path) -> tuple[list[tuple[str, str]], list[str]]:
    """The text files of /work for the prompt of a builder without tools, sorted, and the paths that are only listed
    (binary, files over MAX_WORK_FILE_BYTES, or what no longer fits in MAX_WORK_TOTAL_BYTES)."""
    with_files, only_listed, total = [], [], 0
    paths = sorted(p for p in work.rglob("*") if p.is_file() and not any(part in WORK_EXCLUDE for part in p.relative_to(work).parts))
    for p in paths:
        rel = p.relative_to(work).as_posix()
        data = p.read_bytes()
        try:
            text = data.decode("utf-8")
        except UnicodeDecodeError:
            only_listed.append(rel + " (binary)")
            continue
        if len(data) > MAX_WORK_FILE_BYTES or total + len(data) > MAX_WORK_TOTAL_BYTES:
            only_listed.append(rel + f" ({len(data)} bytes, not included)")
            continue
        total += len(data)
        with_files.append((rel, text))
    return with_files, only_listed


def build_prompt(role: str, stage: str, instructions: str, request_text: str, inputs: list[tuple[str, str]],
                 work: tuple[list[tuple[str, str]], list[str]] | None = None) -> str:
    """work: (files with content, paths only listed) of /work for a builder without tools; None with a CLI."""
    if is_builder(role):
        output = OUTPUT_BUILDER if work is None else OUTPUT_BUILDER_NO_TOOLS
    else:
        output = OUTPUT_JSON
    parts = [instructions.strip(), output,
              "## Inputs\n\nOnly these inputs count: you have no other source about the project.\n",
              f"### request.json\n\n```json\n{request_text.strip()}\n```\n"]
    for name, content in inputs:
        parts.append(f"### {name}\n\n```\n{content.rstrip()}\n```\n")
    if work is not None:
        with_files, only_listed = work
        parts.append("## /work\n\nThe repository in its current state. Deliver in files the full content of what you change.\n")
        if only_listed:
            parts.append("Files present but not included (binary or too large):\n" + "\n".join("- " + r for r in only_listed) + "\n")
        for rel, content in with_files:
            parts.append(f"### /work/{rel}\n\n```\n{content.rstrip()}\n```\n")
    prompt = "\n\n".join(parts) + "\n"
    if len(prompt.encode("utf-8")) > MAX_PROMPT_BYTES:
        raise Failure(f"the prompt weighs {len(prompt.encode('utf-8'))} bytes, more than the cap of {MAX_PROMPT_BYTES}")
    return prompt


# ---------- agent CLI ----------


def cli_command(llm: str, builder: bool) -> list[str]:
    model = os.environ.get("A2DLC_MODEL", "").strip()
    override = os.environ.get("A2DLC_LLM_CMD", "").strip()
    if llm == "claude":
        # -p reads the prompt from stdin (an argument cannot exceed 128 KB on Linux); --no-session-persistence: nothing on disk.
        cmd = [*(shlex.split(override) if override else ["claude"]), "-p", "--output-format", "json", "--no-session-persistence"]
        if model:
            cmd += ["--model", model]
        if builder:
            # acceptEdits: edits in /work without asking; --allowedTools opens what is needed to run tests. It runs as
            # root inside the container, where bypassPermissions is not valid.
            cmd += ["--permission-mode", "acceptEdits", "--allowedTools", CLAUDE_BUILDER_TOOLS]
        else:
            cmd += ["--tools", ""]  # the other roles only answer: no tools
        return cmd
    if llm == "gemini":
        # -p requires a value and "is appended to the stdin input": the real prompt travels through stdin.
        cmd = [*(shlex.split(override) if override else ["gemini"]), "-p", GEMINI_STDIN_PROMPT, "--output-format", "json", "--skip-trust"]
        if model:
            cmd += ["-m", model]
        if builder:
            cmd += ["--approval-mode", "yolo"]
        return cmd
    raise Failure(f"A2DLC_LLM={llm!r}: only claude, gemini, openrouter or entropos")


OPENROUTER_URL = "https://openrouter.ai/api/v1/chat/completions"
OPENROUTER_MODEL = "anthropic/claude-haiku-4.5"
ENTROPOS_URL = "http://host.docker.internal:3000/v1/chat/completions"
ENTROPOS_MODEL = "entropos-auto"


def env_first(*names: str) -> str:
    """The first of these variables that is set and not empty, in the order given."""
    for name in names:
        value = os.environ.get(name, "").strip()
        if value:
            return value
    return ""


def no_proxy_value() -> str:
    """The NO_PROXY list. Both spellings count: curl 7.88 on Linux sends a host direct when either no_proxy or NO_PROXY
    names it (measured: a host listed in only one of the two bypassed the proxy)."""
    return ",".join(v for v in (os.environ.get("no_proxy", "").strip(), os.environ.get("NO_PROXY", "").strip()) if v)


def no_proxy_matches(host: str, no_proxy: str) -> bool:
    """NO_PROXY as curl and Python read it: comma-separated; '*' is every host; 'example.com' and '.example.com' cover
    the host and its subdomains; an entry may carry a port, which is ignored."""
    host = host.lower()
    for entry in no_proxy.split(","):
        entry = entry.strip().lower()
        if not entry:
            continue
        if entry == "*":
            return True
        entry = entry.split(":", 1)[0].lstrip("*.")
        if entry and (host == entry or host.endswith("." + entry)):
            return True
    return False


def openrouter_opener(url: str) -> urllib.request.OpenerDirector:
    """The HTTP opener for the openrouter backend. The egress proxy of the run is HTTPS_PROXY (or HTTP_PROXY for an
    http URL) from the environment, and NO_PROXY lists the hosts that go direct. The lower-case variable wins over the
    upper-case one, as curl reads them (measured: http_proxy beat HTTP_PROXY with curl 7.88 on Linux). A proxy without
    a scheme (host:port) is an HTTP proxy, as curl takes it. Without a proxy the opener is built with no proxies at
    all, so nothing is read from the environment behind the adapter's back."""
    parts = urllib.parse.urlsplit(url)
    scheme = (parts.scheme or "https").lower()
    proxy = env_first(f"{scheme}_proxy", f"{scheme.upper()}_PROXY")
    if proxy and "://" not in proxy:
        proxy = "http://" + proxy
    if proxy and not no_proxy_matches(parts.hostname or "", no_proxy_value()):
        handler = urllib.request.ProxyHandler({scheme: proxy})
    else:
        handler = urllib.request.ProxyHandler({})
    return urllib.request.build_opener(handler)


def call_openrouter(prompt: str) -> tuple[str, dict | None, str | None]:
    """One chat/completions call to OpenRouter with the whole prompt as the user message. Returns the model's text, the
    cost ({usd, input_tokens, output_tokens}: OpenRouter reports it with usage.include) and the model that answered.
    The key comes from the environment (--env-file) and only travels in the header."""
    key = os.environ.get("OPENROUTER_API_KEY", "").strip()
    if not key:
        raise Failure("A2DLC_LLM=openrouter without OPENROUTER_API_KEY in the environment (roles.<role>.env_file)")
    model = os.environ.get("A2DLC_MODEL", "").strip() or OPENROUTER_MODEL
    url = os.environ.get("A2DLC_OPENROUTER_URL", "").strip() or OPENROUTER_URL
    body = json.dumps({"model": model, "messages": [{"role": "user", "content": prompt}], "temperature": 0,
                         "usage": {"include": True}}).encode("utf-8")
    request = urllib.request.Request(url, data=body, method="POST", headers={
        "Authorization": "Bearer " + key, "Content-Type": "application/json", "X-Title": "a2dlc-roles"})
    limit = cli_cap()
    try:
        with openrouter_opener(url).open(request, timeout=limit) as r:
            doc = json.loads(r.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        raise Failure(f"openrouter answered {e.code}: {clean(e.read().decode('utf-8', 'replace'))}")
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        raise Failure(f"openrouter did not answer: {clean(str(e))}")
    except ValueError as e:
        raise Failure(f"openrouter returned something that is not JSON: {clean(str(e))}")
    model = doc.get("model") if isinstance(doc.get("model"), str) else model
    cost = None
    u = doc.get("usage") if isinstance(doc.get("usage"), dict) else {}
    usd = u.get("cost")
    if isinstance(usd, (int, float)) and not isinstance(usd, bool) and usd >= 0:
        cost = {"usd": float(usd)}
        if _num(u.get("prompt_tokens")):
            cost["input_tokens"] = _num(u.get("prompt_tokens"))
        if _num(u.get("completion_tokens")):
            cost["output_tokens"] = _num(u.get("completion_tokens"))
    if isinstance(doc.get("error"), dict):
        raise Failure(f"openrouter reported an error: {clean(str(doc['error'].get('message') or doc['error']))}", cost, model)
    options = doc.get("choices")
    if not isinstance(options, list) or not options or not isinstance(options[0], dict):
        raise Failure("openrouter returned no choices", cost, model)
    message = options[0].get("message") if isinstance(options[0].get("message"), dict) else {}
    text = message.get("content")
    if not isinstance(text, str) or not text.strip():
        raise Failure("openrouter returned a message with no content", cost, model)
    return text, cost, model


def call_entropos(prompt: str) -> tuple[str, dict | None, str | None]:
    """One chat/completions call to Entropos (Complexity Router) with the prompt. Entropos probes
    Shannon entropy and Thompson sampling to route dynamically to the most cost-effective model,
    returning usage.cost and _entropos_router_meta."""
    key = os.environ.get("ENTROPOS_API_KEY", "").strip() or os.environ.get("OPENROUTER_API_KEY", "").strip()
    model = os.environ.get("A2DLC_MODEL", "").strip() or ENTROPOS_MODEL
    url = os.environ.get("A2DLC_ENTROPOS_URL", "").strip() or ENTROPOS_URL
    body = json.dumps({"model": model, "messages": [{"role": "user", "content": prompt}], "temperature": 0}).encode("utf-8")
    headers = {"Content-Type": "application/json", "X-Title": "a2dlc-roles"}
    if key:
        headers["Authorization"] = "Bearer " + key
    else:
        headers["Authorization"] = "Bearer entropos-local"
    request = urllib.request.Request(url, data=body, method="POST", headers=headers)
    limit = cli_cap()
    try:
        with openrouter_opener(url).open(request, timeout=limit) as r:
            doc = json.loads(r.read().decode("utf-8"))
    except urllib.error.HTTPError as e:
        raise Failure(f"entropos answered {e.code}: {clean(e.read().decode('utf-8', 'replace'))}")
    except (urllib.error.URLError, TimeoutError, OSError) as e:
        raise Failure(f"entropos did not answer: {clean(str(e))}")
    except ValueError as e:
        raise Failure(f"entropos returned something that is not JSON: {clean(str(e))}")
    model = doc.get("model") if isinstance(doc.get("model"), str) else model
    cost = None
    u = doc.get("usage") if isinstance(doc.get("usage"), dict) else {}
    usd = u.get("cost")
    if isinstance(usd, (int, float)) and not isinstance(usd, bool) and usd >= 0:
        cost = {"usd": float(usd)}
        if _num(u.get("prompt_tokens")):
            cost["input_tokens"] = _num(u.get("prompt_tokens"))
        if _num(u.get("completion_tokens")):
            cost["output_tokens"] = _num(u.get("completion_tokens"))
    meta = doc.get("_entropos_router_meta")
    if isinstance(meta, dict):
        prov = meta.get("provider", "unknown")
        disc = meta.get("discount_pct", "0%")
        print(f"a2dlc-roles: entropos routed model={model} provider={prov} saved={disc}", file=sys.stderr)
    if isinstance(doc.get("error"), dict):
        raise Failure(f"entropos reported an error: {clean(str(doc['error'].get('message') or doc['error']))}", cost, model)
    options = doc.get("choices")
    if not isinstance(options, list) or not options or not isinstance(options[0], dict):
        raise Failure("entropos returned no choices", cost, model)
    message = options[0].get("message") if isinstance(options[0].get("message"), dict) else {}
    text = message.get("content")
    if not isinstance(text, str) or not text.strip():
        raise Failure("entropos returned a message with no content", cost, model)
    return text, cost, model


PROXY_VARIABLE = re.compile(r"^(https?|all)_proxy$", re.I)


def proxy_passwords() -> list[str]:
    """The passwords inside the proxy variables (user:password@host), raw and percent-decoded, with or without a scheme.
    The proxy URL itself is not masked: the host and port are useful in a reason."""
    found = []
    for k, v in os.environ.items():
        if not v or not PROXY_VARIABLE.match(k):
            continue
        try:
            parts = urllib.parse.urlsplit(v if "://" in v else "//" + v)
            password = parts.password or ""
        except ValueError:  # a malformed proxy value: nothing to mask from it, and clean() must not fail
            continue
        for candidate in (password, urllib.parse.unquote(password)):
            if candidate:
                found.append(candidate)
    return found


def env_secrets() -> list[str]:
    """Values of variables that look like credentials, so they do not leak into an error message."""
    values = [v for k, v in os.environ.items() if v and len(v) >= 8 and re.search(r"KEY|TOKEN|SECRET|PASSWORD", k, re.I)]
    return values + [p for p in proxy_passwords() if len(p) >= 4]


def clean(text: str, n: int = 400) -> str:
    for s in env_secrets():
        text = text.replace(s, "***")
    text = text.strip()
    return text if len(text) <= n else "…" + text[-n:]


def first_object(text: str) -> dict | None:
    """The first JSON object that appears in a text (the CLI may put warnings before its JSON)."""
    dec = json.JSONDecoder()
    for m in re.finditer(r"\{", text):
        try:
            obj, _ = dec.raw_decode(text[m.start():])
        except ValueError:
            continue
        if isinstance(obj, dict):
            return obj
    return None


def cli_cap() -> float | None:
    """Seconds given to the CLI: the role cap (A2DLC_TIMEOUT_S, set by the core) minus a margin to write response.json
    before the core kills the container. Without the variable, no cap."""
    try:
        total = float(os.environ.get("A2DLC_TIMEOUT_S", ""))
    except ValueError:
        return None
    return max(1.0, total - min(30.0, total * 0.1)) if total > 0 else None


def call_cli(cmd: list[str], prompt: str, cwd: str) -> tuple[str, str, int]:
    limit = cli_cap()
    try:
        r = subprocess.run(cmd, input=prompt, capture_output=True, text=True, encoding="utf-8", cwd=cwd, env=os.environ.copy(), timeout=limit)
    except FileNotFoundError:
        raise Failure(f"cannot find the CLI {cmd[0]!r} in the image")
    except subprocess.TimeoutExpired:
        # the CLI prints its cost when it finishes: cut off, there is nothing to report
        raise Failure(f"the CLI {cmd[0]!r} exceeded the cap of {limit:g} s and was cut off")
    return r.stdout, r.stderr, r.returncode


def _num(x) -> int:
    return x if isinstance(x, int) and not isinstance(x, bool) and x >= 0 else 0


def read_claude(stdout: str, stderr: str, code: int) -> tuple[str, dict | None, str | None]:
    """claude -p --output-format json: {type: result, subtype, is_error, result, total_cost_usd, usage, modelUsage}."""
    doc = first_object(stdout)
    if doc is None:
        raise Failure(f"claude exited with {code} and its output is not JSON: {clean(stderr or stdout)}")
    cost = None
    usd = doc.get("total_cost_usd")
    if isinstance(usd, (int, float)) and not isinstance(usd, bool) and usd >= 0:
        cost = {"usd": float(usd)}
        u = doc.get("usage") if isinstance(doc.get("usage"), dict) else {}
        # input = what the CLI counts as input, including the cache; if it says nothing, the field is not written
        input_total = _num(u.get("input_tokens")) + _num(u.get("cache_creation_input_tokens")) + _num(u.get("cache_read_input_tokens"))
        if input_total:
            cost["input_tokens"] = input_total
        if _num(u.get("output_tokens")):
            cost["output_tokens"] = _num(u.get("output_tokens"))
    model = None
    mu = doc.get("modelUsage")
    if isinstance(mu, dict) and mu:
        # the model that spent the most; if there are no costs, the first one
        model = max(mu, key=lambda k: mu[k].get("costUSD", 0) if isinstance(mu[k], dict) and isinstance(mu[k].get("costUSD"), (int, float)) else 0)
    if doc.get("is_error") or doc.get("subtype", "success") != "success" or "result" not in doc:
        reason = doc.get("terminal_reason") if doc.get("is_error") and doc.get("terminal_reason") else doc.get("subtype") or "error"
        raise Failure(f"claude ended with {reason}: {clean(str(doc.get('result') or stderr))}", cost, model)
    if code != 0:
        raise Failure(f"claude exited with {code}: {clean(stderr)}", cost, model)
    return str(doc["result"]), cost, model


def read_gemini(stdout: str, stderr: str, code: int) -> tuple[str, dict | None, str | None]:
    """gemini -p --output-format json: {response, stats, error?}. The documentation gives no cost: cost stays null."""
    doc = first_object(stdout)
    if doc is None:
        raise Failure(f"gemini exited with {code} and its output is not JSON: {clean(stderr or stdout)}")
    model = None
    st = doc.get("stats")
    if isinstance(st, dict) and isinstance(st.get("models"), dict) and st["models"]:
        model = next(iter(st["models"]))
    if doc.get("error"):
        e = doc["error"]
        raise Failure(f"gemini reported an error: {clean(e.get('message') if isinstance(e, dict) and e.get('message') else json.dumps(e))}", None, model)
    if code != 0:
        raise Failure(f"gemini exited with {code}: {clean(stderr)}", None, model)
    if not isinstance(doc.get("response"), str):
        raise Failure("gemini returned no response", None, model)
    return doc["response"], None, model


# ---------- model response ----------


def model_object(text: str) -> dict:
    """The object {status, reason, files, payload} that the model was supposed to return, with or without a code block."""
    t = text.strip()
    t = re.sub(r"^```[a-zA-Z]*\s*\n|\n```\s*$", "", t) if t.startswith("```") else t
    obj = first_object(t)
    if obj is None or "status" not in obj:
        raise Failure("the model's answer is not the requested JSON object (no status): " + clean(text, 200))
    return obj


def clean_path(p) -> str:
    if not isinstance(p, str) or not p or p != posixpath.normpath(p) or p.startswith("/") or p.startswith("../") or "\\" in p or p == ".":
        raise Failure(f"invalid file path in files: {p!r}")
    return p


def validate_model(obj: dict, builder: bool, delivery_files: bool = False) -> tuple[str, str | None, list[tuple[str, str]], dict]:
    """delivery_files: builder without tools (openrouter), which delivers its work in files instead of editing /work."""
    status = obj.get("status")
    if status not in ("ok", "refused"):
        raise Failure(f"invalid model status: {status!r}")
    reason = obj.get("reason")
    if reason is not None and not isinstance(reason, str):
        raise Failure("model reason is not text")
    if status == "refused" and not (reason or "").strip():
        raise Failure("the model refused without saying why")
    files = obj.get("files", [])
    payload = obj.get("payload", {})
    if not isinstance(files, list) or not isinstance(payload, dict):
        raise Failure("files must be a list and payload an object")
    seen, output = set(), []
    for f in files:
        if not isinstance(f, dict) or set(f) != {"path", "content"} or not isinstance(f["content"], str):
            raise Failure("each element of files must be {path, content}")
        p = clean_path(f["path"])
        if p in seen:
            raise Failure(f"files repeats {p}")
        seen.add(p)
        output.append((p, f["content"]))
    if status == "ok" and builder and output and not delivery_files:
        raise Failure("a builder edits /work: files must be []")
    if status == "ok" and builder and delivery_files and not output:
        raise Failure("a builder without tools delivers its work in files: it answered ok with no file at all")
    if status == "ok" and builder:
        if not isinstance(payload.get("summary"), str) or not payload["summary"].strip():
            raise Failure("builder payload: summary (text) is required")
        if delivery_files:
            # what counts is what gets written to /work: files_changed is computed from files
            payload = {"summary": payload["summary"], "files_changed": [p for p, _ in output],
                       **({"lemmas": payload["lemmas"]} if "lemmas" in payload else {})}
        fc = payload.get("files_changed")
        if not {"summary", "files_changed"} <= set(payload) or not set(payload) <= {"summary", "files_changed", "lemmas"}:
            raise Failure("builder payload: summary (text) and files_changed (list) are required; lemmas is optional, no other keys")
        if not isinstance(fc, list) or any(not isinstance(x, str) or not x for x in fc) or len(set(fc)) != len(fc):
            raise Failure("payload.files_changed must be a list of paths (non-empty text) with no repeats")
        if "lemmas" in payload:
            payload = {**payload, "lemmas": validate_lemmas(payload["lemmas"])}
    return status, (reason.strip() if reason else None), output, payload


def validate_lemmas(lemmas: object) -> list[dict]:
    """payload.lemmas of a builder (role-payloads.schema.json builder_response): each lemma names an internal function
    and states its postcondition as text. Only a critical slice lists lemmas; the adapter checks the shape only."""
    if not isinstance(lemmas, list):
        raise Failure("payload.lemmas must be a list of {name, postcondition}")
    out = []
    for x in lemmas:
        if not isinstance(x, dict) or set(x) != {"name", "postcondition"} \
                or not all(isinstance(x[k], str) and x[k].strip() for k in ("name", "postcondition")):
            raise Failure(f"each payload.lemmas element must be {{name, postcondition}} with text: {clean(json.dumps(x), 120)}")
        out.append({"name": x["name"], "postcondition": x["postcondition"]})
    return out


def validate_propose(payload: dict, files: list[tuple[str, str]]) -> None:
    """propose_properties (role-payloads.schema.json propose_response): ok means at least one property, each one
    naming its target, its text and a delivered test file. The test files live under tests/ and nowhere else."""
    if set(payload) != {"properties"} or not isinstance(payload["properties"], list):
        raise Failure("propose_properties payload: properties (list) is required and there are no other keys")
    if not payload["properties"]:
        raise Failure("propose_properties answered ok with no property: refuse with the reason instead")
    delivered = {p for p, _ in files}
    for p in sorted(delivered):
        if not p.startswith("tests/"):
            raise Failure(f"propose_properties delivers {p}: test files go under tests/ and nowhere else")
    for x in payload["properties"]:
        if not isinstance(x, dict) or set(x) != {"target", "property", "file"}:
            raise Failure("each payload.properties element must be {target, property, file}")
        if not isinstance(x["target"], str) or not TARGET_ID.match(x["target"]):
            raise Failure(f"payload.properties target must be <file>::<symbol>: {x['target']!r}")
        if not isinstance(x["property"], str) or not x["property"].strip():
            raise Failure("payload.properties property must be text")
        if not isinstance(x["file"], str) or x["file"] not in delivered:
            raise Failure(f"payload.properties names {x['file']!r}, which is not one of the delivered files")


def write_files(base: Path, files: list[tuple[str, str]]) -> list[dict]:
    hashed = []
    for p, content in files:
        destination = base / p
        destination.parent.mkdir(parents=True, exist_ok=True)
        data = content.encode("utf-8")
        destination.write_bytes(data)
        hashed.append({"path": p, "sha256": "sha256:" + hashlib.sha256(data).hexdigest()})
    return hashed


def payload_saboteur(model: dict, hashed: list[dict]) -> dict:
    if not hashed:
        raise Failure("the saboteur answered ok with no test file in files")
    targets = model.get("targets", [])
    if not isinstance(targets, list) or not all(isinstance(t, str) and TARGET_ID.match(t) for t in targets) or len(set(targets)) != len(targets):
        raise Failure("payload.targets must be a list of '<path>::<symbol>' with no repeats")
    return {"test_files": hashed, "targets": targets}


# ---------- main ----------


def response(req: dict, status: str, payload: dict, reason: str | None = None, cost: dict | None = None, model: str | None = None) -> dict:
    out = {"schema_version": 1, "run_id": req["run_id"], "role": req["role"], "stage": req["stage"], "attempt": req["attempt"],
           "status": status, "cost": cost, "payload": payload}
    if reason:
        out["reason"] = reason
    if model:
        out["model"] = model
    return out


def write_response(path: Path, doc: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(doc, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")


def run(req: dict, request_path: Path, out_dir: Path) -> dict:
    role, stage = req["role"], req["stage"]
    # the core sets both values twice (request.json and environment): if they do not match, something launched the wrong role
    for var, value in (("A2DLC_ROLE", role), ("A2DLC_STAGE", stage)):
        if os.environ.get(var) and os.environ[var] != value:
            raise Failure(f"{var}={os.environ[var]!r} does not match request.json ({value!r})")
    builder = is_builder(role)
    llm = (os.environ.get("A2DLC_LLM") or "claude").strip().lower()
    cmd = cli_command(llm, builder) if llm not in ("openrouter", "entropos") else []
    work = work_dir()
    without_tools = builder and llm in ("openrouter", "entropos")
    if without_tools and not work.is_dir():
        raise Failure(f"a builder needs {work} and it does not exist")
    prompt = build_prompt(role, stage, instructions(req), request_path.read_text(encoding="utf-8"),
                          input_files(request_path.parent, request_path), work_files(work) if without_tools else None)
    print(f"a2dlc-roles: role={role} stage={stage} llm={llm} prompt={len(prompt.encode('utf-8'))}B", file=sys.stderr)
    if llm == "openrouter":
        text, cost, model = call_openrouter(prompt)
    elif llm == "entropos":
        text, cost, model = call_entropos(prompt)
    else:
        if builder and work.is_dir():
            stdout, stderr, code = call_cli(cmd, prompt, str(work))
        else:
            # the other roles touch nothing: ephemeral cwd, so /out only carries what the role delivers
            with tempfile.TemporaryDirectory() as tmp:
                stdout, stderr, code = call_cli(cmd, prompt, tmp)
        text, cost, model = (read_claude if llm == "claude" else read_gemini)(stdout, stderr, code)
    try:
        status, reason, files, payload = validate_model(model_object(text), builder, without_tools)
    except Failure as f:
        raise Failure(f.reason, cost, model)
    try:
        if status == "refused":
            return response(req, "refused", {}, reason, cost, model)
        if role == PROPOSE_ROLE:
            validate_propose(payload, files)
        if role == "saboteur":
            hashed = write_files(out_dir / "tests", files)
            payload = payload_saboteur(payload, hashed)
        elif without_tools:
            # the builder's work goes to /work, which is what the core turns into a candidate; /out only carries response.json
            write_files(work, files)
        else:
            write_files(out_dir / "files", files)
        return response(req, "ok", payload, reason, cost, model)
    except Failure as f:
        raise Failure(f.reason, cost, model)


def main() -> int:
    request_path = Path(os.environ.get("A2DLC_REQUEST") or "/in/request.json")
    response_path = Path(os.environ.get("A2DLC_RESPONSE") or "/out/response.json")
    try:
        req = read_request(request_path)
    except (OSError, ValueError) as e:
        print(f"a2dlc-roles: cannot read {request_path}: {e}", file=sys.stderr)
        return 1
    try:
        doc = run(req, request_path, response_path.parent)
        write_response(response_path, doc)
        return 0
    except Failure as f:
        write_response(response_path, response(req, "error", {}, f.reason, f.cost, f.model))
        print(f"a2dlc-roles: error: {f.reason}", file=sys.stderr)
        return 1
    except Exception as e:  # nothing escapes without a response.json: the core would count it as invalid_response with no reason
        write_response(response_path, response(req, "error", {}, f"internal adapter failure: {type(e).__name__}: {clean(str(e))}"))
        print(f"a2dlc-roles: internal failure: {type(e).__name__}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    sys.exit(main())
