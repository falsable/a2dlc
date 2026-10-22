"""a2dlc Python engine, operation fuzz (protocol section 5, Stage 4 tier 1).

Deterministic coverage-guided fuzzing with Atheris (libFuzzer) of the changed public functions of the candidate.
Each target runs in its own child process: libFuzzer ends the process on the first crash, so one interpreter
cannot fuzz two targets. The request seed is passed to libFuzzer, so the input sequence is fixed for a given
seed; the number of runs still depends on how fast the machine is, because the budget is wall time.

A finding is an exception that escapes a changed public function. The engine has no list of exceptions a
function may raise, so every escape counts. Each finding is shrunk in a child process to a smaller input that
raises the same exception type, and it carries the request seed.

A hang also counts as a finding: libFuzzer runs with -timeout=5, so one input that runs more than 5 seconds
(an artifact named timeout-*) or exhausts memory (oom-*) is reported with its hex input. Hangs are not shrunk,
because shrinking would run the same hang again. A child that exits 0 without a status line and without
artifacts is a finding with the input "<no-result>", so a target can never vanish from the reply silently.

Targets are the top-level public functions whose AST changed against the base. A target is fuzzed only when every
parameter is annotated with a type this engine can decode (int, float, bool, str, bytes, None, list, tuple,
dict with a hashable key type, Optional). Any other target is listed in unsupported[] and is not fuzzed.

The budget is split evenly over the targets, with a floor of one second each, so the total can exceed
budget.seconds when there are more targets than seconds.

Contract: engine.py dispatches operation "fuzz" here; the reply is /io/response.json, validated by
contracts/engine-fuzz-response.schema.json.
"""

from __future__ import annotations

import ast
import importlib
import inspect
import json
import re
import subprocess
import sys
import tempfile
import time
import types
import typing
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Callable

SCHEMA_VERSION = 1
IO = Path("/io")
GRACE_S = 60  # wall time a child gets on top of its budget, for interpreter start and imports
SHRINK_TIMEOUT_S = 120
SHRINK_STEPS = 2000
LIST_MAX = 8
STR_MAX = 64
MAX_TEXT = 2000
REPORT_NAME = "exception-report.json"
INPUT_TIMEOUT_S = 5  # libFuzzer -timeout: one input running longer is a hang, reported as a finding
HANG_KINDS = ("timeout", "oom")
RUNS_RE = re.compile(r"stat::number_of_executed_units:\s*(\d+)")
PROGRESS_RE = re.compile(r"^#(\d+)\s", re.MULTILINE)
# libFuzzer's last line when -max_total_time expires. atheris.Fuzz() then exits the child with 0 before the child
# can print its status, so this line is the only sign that a clean run reached the end of its budget.
DONE_RE = re.compile(r"^Done (\d+) runs", re.MULTILINE)

Decoder = Callable[[Any], Any]
Spawn = Callable[[str, dict, float], tuple[int, str, str]]


class Unsupported(Exception):
    """A target whose signature this engine cannot decode into inputs."""


@dataclass(frozen=True)
class Target:
    module: str
    name: str

    @property
    def qualified(self) -> str:
        return f"{self.module}.{self.name}"


@dataclass
class Outcome:
    runs: int = 0
    finding: dict | None = None
    reason: str | None = None


# ---------- target selection (parent process, no imports of candidate code) ----------

def module_name(rel: str) -> str:
    parts = list(Path(rel).with_suffix("").parts)
    if parts and parts[-1] == "__init__":
        parts.pop()
    return ".".join(parts)


def top_level_functions(tree: ast.Module) -> list[ast.FunctionDef]:
    return [node for node in tree.body
            if isinstance(node, ast.FunctionDef) and not node.name.startswith("_")]


def untyped_reason(node: ast.FunctionDef) -> str | None:
    args = node.args
    if args.vararg is not None or args.kwarg is not None or args.kwonlyargs:
        return "variadic or keyword-only parameters are not supported"
    for arg in args.posonlyargs + args.args:
        if arg.annotation is None:
            return f"parameter {arg.arg!r} has no type annotation"
    return None


def changed_public_functions(base: Path, cand: Path, changed_files: list[str]) -> tuple[list[Target], list[dict]]:
    targets: list[Target] = []
    unsupported: list[dict] = []
    for rel in changed_files:
        if not rel.endswith(".py") or not (cand / rel).is_file():
            continue
        try:
            tree = ast.parse((cand / rel).read_text(encoding="utf-8"), filename=rel)
        except (SyntaxError, UnicodeDecodeError) as exc:
            unsupported.append({"function": rel, "reason": f"unparsable candidate file: {exc}"})
            continue
        base_path = base / rel
        base_defs: dict[str, str] = {}
        if base_path.is_file():
            try:
                base_tree = ast.parse(base_path.read_text(encoding="utf-8"), filename=rel)
                base_defs = {node.name: ast.dump(node) for node in top_level_functions(base_tree)}
            except (SyntaxError, UnicodeDecodeError):
                base_defs = {}
        module = module_name(rel)
        for node in top_level_functions(tree):
            if base_defs.get(node.name) == ast.dump(node):
                continue
            target = Target(module, node.name)
            reason = untyped_reason(node)
            if reason is not None:
                unsupported.append({"function": target.qualified, "reason": reason})
                continue
            targets.append(target)
    return targets, unsupported


# ---------- typed input decoding (child processes only) ----------

UNHASHABLE_TYPES = (list, dict, set, bytearray)


def _hashable(hint: Any) -> bool:
    """False when a value of this annotation cannot be a dict key (a list inside a tuple included)."""
    origin = typing.get_origin(hint) or hint
    if isinstance(origin, type) and issubclass(origin, UNHASHABLE_TYPES):
        return False
    if origin is tuple:
        return all(_hashable(a) for a in typing.get_args(hint) if a is not Ellipsis)
    if origin is typing.Union or origin is types.UnionType:
        return all(_hashable(a) for a in typing.get_args(hint))
    return True


def decoder_for(hint: Any) -> Decoder:
    """Map a type annotation to a function that consumes bytes from an Atheris FuzzedDataProvider.

    Raises Unsupported for any annotation the engine cannot decode, dict keys that are not hashable included.
    """
    if hint is None or hint is type(None):
        return lambda fdp: None
    if hint is bool:
        return lambda fdp: fdp.ConsumeBool()
    if hint is int:
        return lambda fdp: fdp.ConsumeInt(8)
    if hint is float:
        return lambda fdp: fdp.ConsumeFloat()
    if hint is str:
        return lambda fdp: fdp.ConsumeUnicodeNoSurrogates(STR_MAX)
    if hint is bytes:
        return lambda fdp: fdp.ConsumeBytes(STR_MAX)
    origin = typing.get_origin(hint)
    args = typing.get_args(hint)
    if origin is list and len(args) == 1:
        item = decoder_for(args[0])
        return lambda fdp: [item(fdp) for _ in range(fdp.ConsumeIntInRange(0, LIST_MAX))]
    if origin is tuple and len(args) == 2 and args[1] is Ellipsis:
        item = decoder_for(args[0])
        return lambda fdp: tuple(item(fdp) for _ in range(fdp.ConsumeIntInRange(0, LIST_MAX)))
    if origin is tuple and args:
        parts = [decoder_for(a) for a in args]
        return lambda fdp: tuple(part(fdp) for part in parts)
    if origin is dict and len(args) == 2:
        if not _hashable(args[0]):
            raise Unsupported(f"dict key type {args[0]!r} is not hashable")
        key, value = decoder_for(args[0]), decoder_for(args[1])
        return lambda fdp: {key(fdp): value(fdp) for _ in range(fdp.ConsumeIntInRange(0, LIST_MAX))}
    if origin is typing.Union or origin is types.UnionType:
        present = [a for a in args if a is not type(None)]
        if len(present) == 1 and len(args) == 2:
            inner = decoder_for(present[0])
            return lambda fdp: inner(fdp) if fdp.ConsumeBool() else None
    raise Unsupported(f"type {hint!r} is not decodable")


def _load_target(spec: dict) -> tuple[Callable[..., Any], list[Decoder]]:
    """Import the candidate module under Atheris instrumentation and decode the target's signature."""
    import atheris

    sys.path.insert(0, spec["candidate_dir"])
    with atheris.instrument_imports():
        module = importlib.import_module(spec["module"])
    fn = getattr(module, spec["name"])
    try:
        hints = typing.get_type_hints(fn)
    except Exception as exc:  # e.g. NameError: an annotation imported only under TYPE_CHECKING
        raise Unsupported(f"annotation cannot be resolved at runtime: {_describe(exc)}") from exc
    decoders = [decoder_for(hints[p]) for p in inspect.signature(fn).parameters]
    return fn, decoders


def _describe(exc: BaseException) -> str:
    return f"{type(exc).__name__}: {exc}"[:MAX_TEXT]


def _emit(obj: dict) -> int:
    print(json.dumps(obj))
    return 0


# ---------- child: fuzz one target with Atheris ----------

def child_fuzz(spec: dict) -> int:
    import atheris

    try:
        fn, decoders = _load_target(spec)
    except Unsupported as exc:
        return _emit({"status": "unsupported", "reason": str(exc)})
    except BaseException as exc:  # the candidate may raise anything at import time
        return _emit({"status": "import_error", "error": _describe(exc)})

    report = Path(spec["artifact_dir"]) / REPORT_NAME

    def one_input(data: bytes) -> None:
        fdp = atheris.FuzzedDataProvider(data)
        args = [decode(fdp) for decode in decoders]
        try:
            fn(*args)
        except BaseException as exc:
            report.write_text(json.dumps({"exception": type(exc).__name__, "message": str(exc)[:MAX_TEXT]}),
                              encoding="utf-8")
            raise

    atheris.Setup([
        "fuzz",
        f"-seed={spec['seed']}",
        f"-max_total_time={spec['seconds']}",
        f"-timeout={INPUT_TIMEOUT_S}",
        f"-artifact_prefix={Path(spec['artifact_dir']).as_posix()}/",
    ], one_input)
    atheris.Fuzz()
    return _emit({"status": "fuzzed"})


# ---------- child: shrink a crashing input, keeping the exception type ----------

def child_shrink(spec: dict) -> int:
    import atheris

    fn, decoders = _load_target(spec)
    wanted = spec["exception"]

    def run(data: bytes) -> tuple[list[Any], BaseException | None]:
        fdp = atheris.FuzzedDataProvider(data)
        args = [decode(fdp) for decode in decoders]
        try:
            fn(*args)
        except Exception as exc:
            return args, exc
        return args, None

    def fails(data: bytes) -> bool:
        _, exc = run(data)
        return exc is not None and type(exc).__name__ == wanted

    best = bytes.fromhex(spec["input_hex"])
    steps = 0
    chunk = max(len(best) // 2, 1)
    while chunk >= 1 and steps < SHRINK_STEPS:
        index, shrunk = 0, False
        while index < len(best) and steps < SHRINK_STEPS:
            candidate = best[:index] + best[index + chunk:]
            steps += 1
            if fails(candidate):
                best, shrunk = candidate, True
            else:
                index += chunk
        if not shrunk:
            chunk //= 2
    args, exc = run(best)
    if exc is None:
        return _emit({"status": "not_reproduced"})
    return _emit({"status": "shrunk", "input": repr(tuple(args))[:MAX_TEXT], "error": _describe(exc)})


CHILDREN: dict[str, Callable[[dict], int]] = {"fuzz": child_fuzz, "shrink": child_shrink}


# ---------- parent: orchestration and reply ----------

def _spawn_child(mode: str, spec: dict, timeout: float) -> tuple[int, str, str]:
    proc = subprocess.run(
        [sys.executable, str(Path(__file__).resolve()), mode, json.dumps(spec)],
        capture_output=True, text=True, encoding="utf-8", errors="replace", timeout=timeout,
    )
    return proc.returncode, proc.stdout, proc.stderr


def _child_status(stdout: str) -> dict | None:
    for line in reversed(stdout.splitlines()):
        line = line.strip()
        if line.startswith("{"):
            try:
                obj = json.loads(line)
            except json.JSONDecodeError:
                continue
            if isinstance(obj, dict) and "status" in obj:
                return obj
    return None


def _runs_from(stderr: str) -> int:
    """Executions libFuzzer reports. A crash prints no final stat line, so the last progress line counts."""
    counts = [int(n) for n in RUNS_RE.findall(stderr)] + [int(n) for n in PROGRESS_RE.findall(stderr)]
    counts += [int(n) for n in DONE_RE.findall(stderr)]
    return max(counts, default=0)


def fuzz_target(target: Target, cand: Path, seconds: int, seed: int, spawn: Spawn) -> Outcome:
    artifact_dir = Path(tempfile.mkdtemp(prefix="a2dlc-fuzz-"))
    base_spec = {"candidate_dir": str(cand), "module": target.module, "name": target.name,
                 "artifact_dir": str(artifact_dir), "seed": seed, "seconds": seconds}
    try:
        rc, out, err = spawn("fuzz", base_spec, seconds + GRACE_S)
    except subprocess.TimeoutExpired:
        return Outcome(finding={"function": target.qualified, "input": "<timeout>",
                                "error": f"no return within {seconds + GRACE_S}s", "seed": seed})
    status = _child_status(out)
    if status and status.get("status") == "unsupported":
        return Outcome(reason=status.get("reason", "unsupported"))
    if status and status.get("status") == "import_error":
        return Outcome(finding={"function": target.qualified, "input": "<import>",
                                "error": status.get("error", "import failed"), "seed": seed})

    outcome = Outcome(runs=_runs_from(err))
    artifacts = sorted(p for p in artifact_dir.iterdir() if p.is_file() and p.name != REPORT_NAME)
    if not artifacts:
        if rc != 0:
            outcome.finding = {"function": target.qualified, "input": "<child>",
                               "error": f"fuzz child exited {rc}: {err[-MAX_TEXT:]}", "seed": seed}
        elif status is None and not DONE_RE.search(err):
            # exit 0 without a status and without libFuzzer's end-of-budget line: the child never reached the fuzzer
            outcome.finding = {"function": target.qualified, "input": "<no-result>",
                               "error": "fuzz child exited 0 without a status line", "seed": seed}
        return outcome

    kind = artifacts[0].name.split("-")[0]
    data = artifacts[0].read_bytes()
    if kind in HANG_KINDS:
        outcome.finding = {"function": target.qualified, "input": f"hex:{data.hex()}"[:MAX_TEXT],
                           "error": f"libFuzzer {kind}: one input ran past the {INPUT_TIMEOUT_S} s limit"
                           if kind == "timeout" else "libFuzzer oom: one input exhausted memory",
                           "seed": seed}
        return outcome
    report = _read_report(artifact_dir)
    error = f"{report['exception']}: {report['message']}" if report else f"libFuzzer {kind}"
    shrink_spec = dict(base_spec, input_hex=data.hex(),
                       exception=report["exception"] if report else "")
    try:
        _, shrink_out, _ = spawn("shrink", shrink_spec, SHRINK_TIMEOUT_S)
        shrunk = _child_status(shrink_out)
    except subprocess.TimeoutExpired:
        shrunk = None
    if shrunk and shrunk.get("status") == "shrunk":
        outcome.finding = {"function": target.qualified, "input": shrunk["input"],
                           "error": shrunk["error"], "seed": seed}
    else:
        outcome.finding = {"function": target.qualified, "input": f"hex:{data.hex()}"[:MAX_TEXT],
                           "error": error, "seed": seed}
    return outcome


def _read_report(artifact_dir: Path) -> dict | None:
    path = artifact_dir / REPORT_NAME
    if not path.is_file():
        return None
    try:
        obj = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return obj if isinstance(obj, dict) and "exception" in obj else None


def run(req: dict, io_dir: Path = IO, spawn: Spawn = _spawn_child) -> int:
    budget = req.get("budget") or {}
    seconds, seed = int(budget["seconds"]), int(budget["seed"])
    base, cand = Path(req["base_dir"]), Path(req["candidate_dir"])
    targets, unsupported = changed_public_functions(base, cand, req["changed_files"])
    per_target = max(1, seconds // len(targets)) if targets else 0

    started = time.monotonic()
    findings: list[dict] = []
    runs = 0
    for target in targets:
        outcome = fuzz_target(target, cand, per_target, seed, spawn)
        runs += outcome.runs
        if outcome.finding is not None:
            findings.append(outcome.finding)
        if outcome.reason is not None:
            unsupported.append({"function": target.qualified, "reason": outcome.reason})

    response = {"schema_version": SCHEMA_VERSION, "findings": findings, "runs": runs,
                "seconds": round(time.monotonic() - started, 3), "unsupported": unsupported}
    io_dir.mkdir(parents=True, exist_ok=True)
    (io_dir / "response.json").write_text(json.dumps(response, ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(CHILDREN[sys.argv[1]](json.loads(sys.argv[2])))
