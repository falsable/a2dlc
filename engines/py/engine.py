"""a2dlc Python engine: CrossHair diffbehavior between the base (/base) and the candidate (/cand).

Contract: reads /io/request.json and writes /io/response.json (contracts/engine-*.schema.json).
Targets: top-level functions that exist in both versions and whose AST changed, plus the ones that call them
within the same file (transitive closure). Methods and new functions are out of scope in v0.1.
Both versions are copied to /tmp/a2dlc/a2dlc_base and a2dlc_cand so they can be imported at the same time; absolute
imports of the repo's own modules are rewritten to that prefix.
CrossHair does not use generators: generators is always typed_default, and without type annotations the target is INCONCLUSIVE.
"""
from __future__ import annotations  # the `X | None` annotations must be valid on the repo's Python 3.9 (SWE-bench)

import ast
import bisect
import dataclasses
import difflib
import importlib.util
import inspect
import io
import json
import re
import shutil
import subprocess
import sys
import time
import tokenize
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path

IO = Path("/io")
WORK = Path("/tmp/a2dlc")
CROSSHAIR_VERSION = "0.0.111"
SKIP = {".git", ".a2dlc", "node_modules", ".venv", "venv", "__pycache__", ".beads", ".mypy_cache", ".pytest_cache"}
# Measured on 2026-10-15: below 10 s per function CrossHair gives false PASS results.
FLOOR_TIMEOUT_S = 10


COOKIE = re.compile(r"^[ \t\f]*#.*?coding[:=][ \t]*([-\w.]+)")


def read_py(p: Path) -> str:
    """Text of a .py file according to its encoding (BOM or PEP 263 cookie), not plain UTF-8. If it was not UTF-8,
    the cookie of the returned text becomes utf-8: whatever the engine writes back (stubs, mutants) goes in UTF-8.
    SyntaxError if the declaration is invalid; UnicodeDecodeError if the bytes do not match it."""
    raw = p.read_bytes()
    encoding, _ = tokenize.detect_encoding(io.BytesIO(raw).readline)
    text = raw.decode(encoding)
    if encoding in ("utf-8", "utf-8-sig"):
        return text
    lines = text.split("\n", 2)
    for i, line in enumerate(lines[:2]):
        m = COOKIE.match(line)
        if m:
            lines[i] = line[:m.start(1)] + "utf-8" + line[m.end(1):]
            break
    return "\n".join(lines)


def copy_py(origin: Path, destination: Path) -> None:
    for p in origin.rglob("*.py"):
        rel = p.relative_to(origin)
        if any(part in SKIP for part in rel.parts):
            continue
        d = destination / rel
        d.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(p, d)


def installed(name: str) -> bool:
    """Whether `name` resolves to a module of this image (stdlib or site-packages). The engine's own folder is not one."""
    try:
        spec = importlib.util.find_spec(name)
    except (ImportError, ValueError):
        return False
    origin = spec and (spec.origin or next(iter(spec.submodule_search_locations or []), None))
    return bool(origin) and Path(__file__).resolve().parent not in Path(origin).resolve().parents


def roots_local(tree: Path) -> dict[str, str]:
    """Importable top-level name → prefix inside the tree ('' at the root, 'src.' in a src layout). A folder without
    __init__.py that shares its name with an installed package (migrations in alembic/) is not the repo's own root:
    claiming it would make every `from alembic import command` import the repo's folder instead."""
    roots: dict[str, str] = {}
    for base, prefix in ((tree / "src", "src."), (tree, "")):
        if not base.is_dir():
            continue
        for p in base.iterdir():
            if p.name in SKIP or p.name.startswith("."):
                continue
            if p.suffix == ".py":
                roots.setdefault(p.stem, prefix)
            elif p.is_dir() and any(p.rglob("*.py")) and ((p / "__init__.py").exists() or not installed(p.name)):
                roots.setdefault(p.name, prefix)
    roots.pop("src", None)
    return roots


def rewrite_imports(tree: Path, package: str) -> None:
    roots = roots_local(tree)
    for p in tree.rglob("*.py"):
        text = p.read_text(encoding="utf-8", errors="replace")
        try:
            mod = ast.parse(text)
        except (SyntaxError, RecursionError):
            continue
        lines = text.splitlines(keepends=True)
        changes: list[tuple[int, str, str]] = []
        for node in ast.walk(mod):
            if isinstance(node, ast.ImportFrom) and node.level == 0 and node.module:
                head = node.module.split(".")[0]
                if head in roots:
                    changes.append((node.lineno, f"from {node.module} ", f"from {package}.{roots[head]}{node.module} "))
            elif isinstance(node, ast.Import):
                for a in node.names:
                    head = a.name.split(".")[0]
                    if head not in roots:
                        continue
                    where = f"{package}.{roots[head]}"
                    if "." not in a.name:
                        changes.append((node.lineno, f"import {a.name}", f"import {where}{a.name} as {a.asname or a.name}"))
                    elif a.asname:
                        changes.append((node.lineno, f"import {a.name} as {a.asname}", f"import {where}{a.name} as {a.asname}"))
                    else:  # `import pkg.sub` binds `pkg`: import the submodule, then bind the root under its own name
                        changes.append((node.lineno, f"import {a.name}",
                                        f"import {where}{a.name} as _a2dlc_{a.name.replace('.', '_')}, {where}{head} as {head}"))
        for lineno, old, new in changes:
            lines[lineno - 1] = lines[lineno - 1].replace(old, new, 1)
        if changes:
            p.write_text("".join(lines), encoding="utf-8")


def functions(text: str) -> dict[str, ast.AST]:
    return {n.name: n for n in ast.parse(text).body if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef))}


def targets_of(base_txt: str, cand_txt: str) -> tuple[list[str], list[str]]:
    """(changed, callers): the ones whose AST changed and the ones that call them in the same file (transitive closure)."""
    vb, vc = functions(base_txt), functions(cand_txt)
    common = [n for n in vc if n in vb]
    direct = {n for n in common if ast.dump(vb[n]) != ast.dump(vc[n])}
    closure = set(direct)
    uses = {n: {x.id for x in ast.walk(vc[n]) if isinstance(x, ast.Name)} for n in common}
    grew = True
    while grew:
        grew = False
        for n in common:
            if n not in closure and uses[n] & closure:
                closure.add(n)
                grew = True
    return [n for n in common if n in direct], [n for n in common if n in closure - direct]


def _binds(stmt: ast.stmt) -> set[str]:
    """The module-level names a top-level statement binds."""
    if isinstance(stmt, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
        return {stmt.name}
    out = set()
    for x in ast.walk(stmt):  # also the defs and imports inside a top-level if/try/with
        if isinstance(x, ast.Name) and isinstance(x.ctx, ast.Store):
            out.add(x.id)
        elif isinstance(x, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            out.add(x.name)
        elif isinstance(x, (ast.Import, ast.ImportFrom)):
            out |= {(a.asname or a.name).split(".")[0] for a in x.names}
    return out


def identical_of(base_txt: str, cand_txt: str) -> list[str]:
    """Public symbols (signatures_of names) that no input can tell apart in base and candidate: the same AST, and every
    module-level name they read, transitively, bound by the same statements in both. A name bound by an import never
    qualifies (the imported module may differ), nor does anything in a module with `from x import *` or whose
    module-level statements that bind no name (`Externo.x = 5`, a call) differ."""
    def bindings(text: str) -> tuple[dict[str, list[str]], dict[str, ast.stmt], list[str]]:
        by_name: dict[str, list[str]] = {}
        defs: dict[str, ast.stmt] = {}
        loose: list[str] = []
        for stmt in ast.parse(text).body:
            if any(isinstance(x, ast.ImportFrom) and any(a.name == "*" for a in x.names) for x in ast.walk(stmt)):
                raise ValueError("star import")
            dump = "import" if any(isinstance(x, (ast.Import, ast.ImportFrom)) for x in ast.walk(stmt)) else ast.dump(stmt)
            names = _binds(stmt)
            if not names:
                loose.append(ast.dump(stmt))
            for n in names:
                by_name.setdefault(n, []).append(dump)
                if isinstance(stmt, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
                    defs[n] = stmt
        return by_name, defs, loose

    try:
        (bb, _, lb), (bc, defs, lc) = bindings(base_txt), bindings(cand_txt)
    except ValueError:
        return []
    if lb != lc:
        return []
    same = {n for n in bc if bb.get(n) == bc[n] and "import" not in bc[n]}
    reads = {n: {x.id for x in ast.walk(defs[n]) if isinstance(x, ast.Name) and isinstance(x.ctx, ast.Load)}
             for n in defs}
    grew = True
    while grew:  # a symbol that reads a module-level name that changed (or reads one that does) can differ
        grew = False
        for n in list(same):
            if n in reads and any(m in bc and m not in same for m in reads[n]):
                same.discard(n)
                grew = True
    out = []
    for s in signatures_of(cand_txt):
        if s["name"].split(".")[0] in same and s["name"].split(".")[0] in defs:
            out.append(s["name"])
    return out


def typed(fn: ast.AST) -> bool:
    a = fn.args
    params = [*a.posonlyargs, *a.args, *a.kwonlyargs]
    return all(p.annotation is not None for p in params) and (a.vararg is None or a.vararg.annotation is not None) \
        and (a.kwarg is None or a.kwarg.annotation is not None)


def module(file: str) -> str:
    return file[:-3].replace("/", ".").removesuffix(".__init__")


# Base and candidate are imported in one process, so a module that registers a metric at import registers the same
# name twice in prometheus_client's default REGISTRY ("Duplicated timeseries") and every target in a file that
# imports it came back INCONCLUSIVE (a2dlc-gdp). Python imports sitecustomize from PYTHONPATH (WORK) at startup, so
# both copies run with the same patch: the duplicate is not registered, the metric object still counts.
IMPORT_SHIMS = '''\
import importlib.util

if importlib.util.find_spec("prometheus_client") is not None:
    from prometheus_client import registry as _registry

    _register = _registry.CollectorRegistry.register

    def _register_once(self, collector):
        try:
            _register(self, collector)
        except ValueError as e:
            if "Duplicated timeseries" not in str(e):
                raise

    _registry.CollectorRegistry.register = _register_once
'''


def install_import_shims() -> None:
    (WORK / "sitecustomize.py").write_text(IMPORT_SHIMS, encoding="utf-8")


def run(cmd: list[str], timeout: float) -> tuple[int | None, str]:
    try:
        r = subprocess.run(cmd, cwd=WORK, capture_output=True, text=True, timeout=timeout,
                           env={"PYTHONPATH": str(WORK), "PYTHONDONTWRITEBYTECODE": "1", "PATH": "/usr/local/bin:/usr/bin:/bin",
                                "HOME": "/tmp"})
        return r.returncode, (r.stdout + r.stderr)
    except subprocess.TimeoutExpired as e:
        output = (e.stdout or b"") + (e.stderr or b"") if isinstance(e.stdout, bytes) else (e.stdout or "") + (e.stderr or "")
        return None, output if isinstance(output, str) else output.decode("utf-8", "replace")


DIFFERENCE = re.compile(r"^Given: \((?P<args>.*)\),\s*$\n^\s*\S+ : (?P<old>.*)$\n^\s*\S+ : (?P<new>.*)$", re.M)
WITHOUT_DIFFERENCE = re.compile(r"No differences found\. \(attempted (\d+) iterations\)")

# What CrossHair prints as "raises X(...)" is not always the function's behavior. These are failures of CrossHair or of the
# harness that builds the arguments: a divergence where either side is one of them says nothing about the two functions,
# so the target is INCONCLUSIVE, never FAIL with a counterexample (a2dlc-qb1: agencia-ia 60f5c6b, build_audit_package).
# The rule is by exception name or by exact message, not "anything unfamiliar": an exception the function raises itself
# (ValueError, its own classes) stays behavior.
HARNESS_EXCEPTIONS = frozenset({
    "SideEffectDetected",  # CrossHair refused an operation of the function with side effects (fcntl.ioctl, open, ...)
    "CrossHairInternal", "CrosshairUnsupported", "UnexploredPath", "UnknownSatisfiability", "PathTimeout",
    "IgnoreAttempt", "NotDeterministic"})
# typing.Literal[...] reaching inspect when CrossHair builds an argument from the annotation.
HARNESS_TYPING_ERROR = re.compile(r"""^TypeError\(["'](?:typing|typing_extensions)\..* is not a module, class, method, or function""")


def harness_error(outcome: str) -> str | None:
    """A short description if a CrossHair outcome line ("raises X(...)") is a failure of CrossHair or the harness, not of the
    function; None for a return value and for any exception the function itself can raise."""
    r = _result(outcome)
    if r is None or r[0] != "raises":
        return None
    if r[1].split("(", 1)[0] in HARNESS_EXCEPTIONS or HARNESS_TYPING_ERROR.match(r[1]):
        return r[1][:100]
    return None


def new_target(target: str, timeout_s: float) -> dict:
    return {"target": target, "engine": "crosshair-diffbehavior", "engine_version": CROSSHAIR_VERSION,
            "verdict": "PASS", "search_budget": {"iterations": 0, "per_condition_timeout_s": timeout_s},
            "generators": "typed_default", "typed": True, "domain_guard": None, "equivalence": "strict",
            "counterexample": None}


def lines_of(fn: ast.AST) -> list[int]:
    """[first, last] line of the symbol in the candidate, decorators included: the core subtracts those lines from the
    coverage precondition; any other changed lines in the file must be executed by a test."""
    first = min([fn.lineno, *(d.lineno for d in getattr(fn, "decorator_list", []))])
    return [first, getattr(fn, "end_lineno", None) or fn.lineno]


def compare(file: str, mod: str, n: str, fn: ast.AST, rc_imp: int | None, out_imp: str, timeout_s: float,
             rel: dict | None = None, remaining: float | None = None) -> dict:
    """One target. remaining: the seconds the diff budget has left when the target starts (None: no budget); the shrink of
    a counterexample runs within it, so it is counted against the budget."""
    t = new_target(f"{file}::{n}", timeout_s)
    t["lines"] = lines_of(fn)

    def inconclusive(reason: str) -> dict:
        t["verdict"], t["reason"] = "INCONCLUSIVE", reason[:300]
        return t

    if rc_imp != 0:
        last = (out_imp or "").strip().splitlines()[-1:] or ["no output"]
        return inconclusive(f"could not import: {last[0]}")
    if isinstance(fn, ast.AsyncFunctionDef):
        return inconclusive("async function: not supported yet")
    if not typed(fn):
        t["typed"] = False
        return inconclusive("no type annotations on the parameters: CrossHair has no domain")
    limit = timeout_s * 4 + 60
    rc, out = run([sys.executable, "-m", "crosshair", "diffbehavior", "--per_condition_timeout", str(timeout_s),
                      f"a2dlc_base.{mod}.{n}", f"a2dlc_cand.{mod}.{n}"], limit)
    m_dif, m_ok = DIFFERENCE.search(out), WITHOUT_DIFFERENCE.search(out)
    harness = [f"{side}: {h}" for side, h in (("old", harness_error(m_dif["old"].strip())),
                                               ("new", harness_error(m_dif["new"].strip()))) if h] if m_dif else []
    if harness:
        inconclusive(f"CrossHair/harness error, not the function's behavior ({'; '.join(harness)})")
    elif m_dif and rel and equivalents(m_dif["old"].strip(), m_dif["new"].strip(), rel):
        # CrossHair stops at the first difference. If that one vanishes under π_S, it does not mean there is no other one,
        # and PASS would claim something nobody searched for.
        inconclusive(f"the difference CrossHair found ({m_dif['args']}) disappears under π_S ({', '.join(rel['_requested'])}); "
                     "CrossHair stops at the first difference, so no other one was searched for")
    elif m_dif:
        t["verdict"] = "FAIL"
        found = {"args": m_dif["args"], "old": m_dif["old"].strip(), "new": m_dif["new"].strip()}
        shrink_s = SHRINK_BUDGET_S if remaining is None else min(SHRINK_BUDGET_S, remaining)
        t["counterexample"] = shrink_counterexample(mod, n, found, rel or {}, shrink_s)
    elif m_ok:
        t["search_budget"]["iterations"] = int(m_ok.group(1))
    elif rc is None:
        inconclusive(f"CrossHair exceeded {limit:.0f} s")
    elif "SideEffectDetected" in out:
        inconclusive("side effects detected while running")
    elif "usage:" in out:
        inconclusive(f"the function read sys.argv and ended the process (rc={rc})")
    else:
        last = out.strip().splitlines()[-1:] or ["no output"]
        inconclusive(f"CrossHair did not conclude (rc={rc}): {last[0]}")
    return t


# ---------- counterexample shrinking (CB-01, protocol §5 Stage 1) ----------
# CrossHair stops at the first difference, which is often a large or noisy input. The shrink is greedy and bounded:
# each top-level argument is replaced by a smaller literal (toward zero, shorter, fewer elements) and the replacement is
# kept only if the two functions still disagree on it. The checks run in a subprocess inside the engine container, like
# CrossHair: the candidate's code is user code, and the container has no network. Only literal arguments are shrunk;
# anything else keeps CrossHair's counterexample as it was.

# argv of the script: base module, candidate module, function, arguments, relaxations (rel), seconds it may run.
# The script runs under the budget left by the diff (at most SHRINK_BUDGET_S); its subprocess timeout is that plus 1 s.
SHRINK_BUDGET_S = 10
SHRINK_SCRIPT_BODY = """
import ast, contextlib, copy, importlib, io, json, math, sys, time
deadline = time.monotonic() + float(sys.argv[6])
old = getattr(importlib.import_module(sys.argv[1]), sys.argv[3])
new = getattr(importlib.import_module(sys.argv[2]), sys.argv[3])
best = ast.literal_eval(sys.argv[4])
rel = ast.literal_eval(sys.argv[5])
MAX_CHECKS = 300
#EQUIVALENCE#


def outcome(f, kw):
    try:
        with contextlib.redirect_stdout(io.StringIO()), contextlib.redirect_stderr(io.StringIO()):
            return "returns " + repr(f(**copy.deepcopy(kw)))
    except Exception as e:
        return "raises " + repr(e)


def smaller(v):
    if isinstance(v, bool) or v is None:
        return []
    if isinstance(v, int):
        if v == 0:
            return []
        sign, mag = (1, v) if v > 0 else (-1, -v)
        out, step = [0], 1 << (mag.bit_length() - 1)
        while step >= 1:
            out.append(sign * (mag - step))
            step //= 2
        return [x for x in dict.fromkeys(out) if x != v]
    if isinstance(v, float):
        return [] if not math.isfinite(v) else [x for x in (0.0, float(int(v))) if x != v]
    if isinstance(v, str):
        return [x for x in dict.fromkeys(["", v[:len(v) // 2], v[:-1]]) if x != v] if v else []
    if isinstance(v, (list, tuple)):
        return ([type(v)()] if v else []) + [type(v)(v[:i] + v[i + 1:]) for i in range(len(v))]
    return []


def diverges(kw):
    # Under the relaxations, a pair that is equivalent under them is not a divergence: the search goes on for another one.
    a, b = outcome(old, kw), outcome(new, kw)
    return a != b and not (rel and equivalents(a, b, rel))


checks = 0
while checks < MAX_CHECKS and time.monotonic() < deadline:
    for name in sorted(best):
        for value in smaller(best[name]):
            trial = {**best, name: value}
            checks += 1
            if checks > MAX_CHECKS or time.monotonic() >= deadline:
                break
            if diverges(trial):
                best = trial
                break
        else:
            continue
        break
    else:
        break
print(json.dumps({"args": ", ".join(f"{k}={v!r}" for k, v in best.items()), "old": outcome(old, best),
                  "new": outcome(new, best)}))
"""


def shrink_counterexample(mod: str, name: str, found: dict, rel: dict, budget_s: float = SHRINK_BUDGET_S) -> dict:
    """The counterexample with its literal arguments shrunk while the two functions still disagree; CrossHair's own
    counterexample if the arguments are not literal keywords, the subprocess fails, there is less than a second of budget
    left, or the shrunk pair is equivalent under the requested relaxations. Keys: args, old, new (the same shape as
    CrossHair's). The args are written as CrossHair writes them: `x=1001`."""
    try:
        call = ast.parse(f"f({found['args']})", mode="eval").body
        if not isinstance(call, ast.Call) or call.args or any(k.arg is None for k in call.keywords):
            return found
        kwargs = {k.arg: ast.literal_eval(k.value) for k in call.keywords}
    except (SyntaxError, ValueError, TypeError, MemoryError, RecursionError):
        return found
    if not kwargs or budget_s < 1:
        return found
    rc, out = run([sys.executable, "-c", SHRINK_SCRIPT, f"a2dlc_base.{mod}", f"a2dlc_cand.{mod}", name, repr(kwargs),
                   repr(rel), repr(budget_s)], budget_s + 1)
    if rc != 0:
        return found
    try:
        shrunk = json.loads(out.strip().splitlines()[-1])
    except (ValueError, IndexError):
        return found
    if not isinstance(shrunk, dict) or set(shrunk) != {"args", "old", "new"} or not all(isinstance(v, str) for v in shrunk.values()):
        return found
    if shrunk["old"] == shrunk["new"] or equivalents(shrunk["old"], shrunk["new"], rel):
        return found
    return shrunk


# ---------- π_S: relaxations of equivalence (block 7, protocol §5 Stage 1.5 step 3) ----------
# The ones this engine knows how to apply when judging the difference CrossHair found; any other one marks the result
# equivalence_unsupported and it is compared strictly (the safe side):
#   ignore_order               lists and tuples are compared sorted (by repr)
#   set_semantics              also without duplicates
#   float_tolerance:<eps>      two numbers are equal if |a-b| <= eps * max(1, |a|, |b|)
#   ignore_fields:<f1>,<f2>    those dict keys are ignored, at any depth
#   errors_by_type             two exceptions are equal if they have the same type, even if the message changes
# Only values that ast.literal_eval understands: an object repr, nan or inf is not normalized and the difference stays FAIL.

def relaxations(requested: list[str] | None) -> tuple[dict, list[str]]:
    rel: dict = {"_requested": []}
    unsupported: list[str] = []
    for r in requested or []:
        if r in ("ignore_order", "set_semantics", "errors_by_type"):
            rel[r] = True
        elif r.startswith("float_tolerance:"):
            try:
                eps = float(r.split(":", 1)[1])
            except ValueError:
                eps = -1.0
            if not eps >= 0 or eps == float("inf"):
                unsupported.append(r)
                continue
            rel["float_tolerance"] = eps
        elif r.startswith("ignore_fields:") and any(c.strip() for c in r.split(":", 1)[1].split(",")):
            rel.setdefault("ignore_fields", set()).update(c.strip() for c in r.split(":", 1)[1].split(",") if c.strip())
        else:
            unsupported.append(r)
            continue
        rel["_requested"].append(r)
    return (rel if rel["_requested"] else {}), unsupported


def _norm(v, rel: dict):
    if isinstance(v, dict):
        return ("dict", {k: _norm(x, rel) for k, x in v.items() if k not in rel.get("ignore_fields", ())})
    if isinstance(v, (list, tuple, set, frozenset)):
        xs = [_norm(x, rel) for x in v]
        if rel.get("set_semantics"):
            xs = list({repr(x): x for x in xs}.values())
        if rel.get("ignore_order") or rel.get("set_semantics") or isinstance(v, (set, frozenset)):
            xs.sort(key=repr)
        return (type(v).__name__ if not isinstance(v, frozenset) else "set", xs)
    return v


def _equal(a, b, eps: float | None) -> bool:
    number = (int, float)
    if eps is not None and isinstance(a, number) and isinstance(b, number) and not isinstance(a, bool) and not isinstance(b, bool):
        return abs(a - b) <= eps * max(1.0, abs(a), abs(b))
    if isinstance(a, tuple) and isinstance(b, tuple) and len(a) == len(b) == 2 and a[0] == b[0] and isinstance(a[1], (dict, list)):
        if isinstance(a[1], dict):
            return a[1].keys() == b[1].keys() and all(_equal(a[1][k], b[1][k], eps) for k in a[1])
        return len(a[1]) == len(b[1]) and all(_equal(x, y, eps) for x, y in zip(a[1], b[1]))
    return type(a) is type(b) and a == b


def _result(text: str):
    """("returns", value) or ("raises", repr of the exception) from a CrossHair line; None if it is not understood."""
    if text.startswith("returns "):
        try:
            return "returns", ast.literal_eval(text[len("returns "):])
        except (ValueError, SyntaxError, MemoryError, RecursionError):
            return None
    if text.startswith("raises "):
        return "raises", text[len("raises "):].strip()
    return None


def equivalents(old: str, new: str, rel: dict) -> bool:
    a, b = _result(old), _result(new)
    if a is None or b is None or a[0] != b[0]:
        return False
    if a[0] == "raises":
        return a[1].split("(", 1)[0] == b[1].split("(", 1)[0] if rel.get("errors_by_type") else a[1] == b[1]
    return _equal(_norm(a[1], rel), _norm(b[1], rel), rel.get("float_tolerance"))


# The shrink script checks the relaxations with this file's own functions, copied from their source: one definition.
SHRINK_SCRIPT = SHRINK_SCRIPT_BODY.replace("#EQUIVALENCE#", "\n\n".join(
    inspect.getsource(f) for f in (_norm, _equal, _result, equivalents)))


# ---------- operation: stub (NOP gate, protocol §5 Stage 4) ----------
# Ported from refutar.py (~/.claude/skills/chain, decision of 2026-10-15): the base gets stubs of what the candidate
# added, so that a new test fails because of the test (NotImplementedError) and not because of an ImportError.

STUB_DECORATORS = {"staticmethod", "classmethod", "property"}
DEFS = (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)


def _literal(value: ast.AST) -> bool:
    try:
        ast.literal_eval(value)
        return True
    except Exception:  # noqa: BLE001 - anything that is not a pure literal
        return False


def _only_names(t: ast.AST) -> bool:
    return all(isinstance(x, (ast.Name, ast.Tuple, ast.List, ast.Store)) for x in ast.walk(t))


def _nullify(body: list, nulls: set[str]) -> None:
    """`NAME = <value>` → `NAME = None` when the value is a literal or uses an already-nulled name (`B = A * 2`):
    the candidate's values must not reach the base. Anything computed by other means (`Base = declarative_base()`)
    stays as it is, because nulling it would break the import. `A, B = X` with X nulled becomes `A = B = None`
    (unpacking None would fail). An AnnAssign with a value becomes an Assign, because its annotation may name something
    the base does not have."""
    for i, n in enumerate(body):
        if isinstance(n, ast.AnnAssign) and n.value is not None and isinstance(n.target, ast.Name):
            assign_targets, value = [n.target], n.value
        elif isinstance(n, ast.Assign) and all(_only_names(t) for t in n.targets):
            assign_targets, value = n.targets, n.value
        else:
            continue
        names = [x.id for t in assign_targets for x in ast.walk(t) if isinstance(x, ast.Name)]
        if all(x.startswith("__") and x.endswith("__") for x in names):
            continue
        if _literal(value) or any(isinstance(x, ast.Name) and x.id in nulls for x in ast.walk(value)):
            body[i] = ast.copy_location(ast.Assign(targets=[ast.Name(x, ast.Store()) for x in names],
                                                     value=ast.Constant(None)), n)
            nulls.update(names)


def _nullify_class(c: ast.ClassDef, nulls: set[str] | None = None) -> None:
    """Attributes of a top-level class WITHOUT bases or metaclass. With bases (Enum, IntEnum, BaseModel, models.Model)
    the base class uses the attribute's value when it creates the class: nulling it would break the import and the test
    would go from a false FAIL to INCONCLUSIVE. Nested classes (`class Meta`) are not touched either."""
    if not c.bases and not c.keywords:
        _nullify(c.body, set(nulls or ()))


def _stub(node: ast.AST) -> ast.AST:
    """The same def/class with each function body replaced by `raise NotImplementedError`. No annotations or decorators
    (except staticmethod/classmethod/property): they may name things that do not exist in the base. Parameter defaults
    become None (the candidate might name something the base does not have), and so do the attributes of a class without
    bases that have a literal value (see _nullify_class)."""
    if isinstance(node, ast.ClassDef):
        _nullify_class(node)
    for n in ast.walk(node):
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
            n.body = [ast.parse("raise NotImplementedError('a2dlc stub')").body[0]]
            n.returns = None
            for a in n.args.posonlyargs + n.args.args + n.args.kwonlyargs:
                a.annotation = None
            for a in (n.args.vararg, n.args.kwarg):
                if a is not None:
                    a.annotation = None
            n.args.defaults = [ast.Constant(None) for _ in n.args.defaults]
            n.args.kw_defaults = [None if d is None else ast.Constant(None) for d in n.args.kw_defaults]
        if isinstance(n, DEFS):
            n.decorator_list = [d for d in n.decorator_list if isinstance(d, ast.Name) and d.id in STUB_DECORATORS]
    return node


def _bound(module: ast.Module) -> set[str]:
    """Names a module binds at its top level (def, class, import, assignment)."""
    names = set()
    for n in module.body:
        if isinstance(n, DEFS):
            names.add(n.name)
        elif isinstance(n, (ast.Import, ast.ImportFrom)):
            names.update((a.asname or a.name).split(".")[0] for a in n.names)
        elif isinstance(n, (ast.Assign, ast.AnnAssign, ast.AugAssign)):
            for t in (n.targets if isinstance(n, ast.Assign) else [n.target]):
                names.update(x.id for x in ast.walk(t) if isinstance(x, ast.Name))
    return names


def _def_names(node: ast.AST) -> set[str]:
    return {n.name for n in ast.walk(node) if isinstance(n, DEFS)}


def stub_of(base_txt: str | None, cand_txt: str) -> tuple[str | None, list[str], set[str]]:
    """(text of the base with stubs or None if not needed, symbols with a stub, new names without a stub).

    - New module: the candidate's version with each function body stubbed and the top-level literal values and the
      attributes of classes without bases set to None (see _nullify); its symbols are the def/class and the assigned
      names. Without symbols no stub is written.
    - Module that already existed: the base text intact and, at the end, as stubs: the new imports that do not
      overwrite a name of the base, the new top-level def/class, the new methods of an existing top-level class
      (`K.m = <stub>`) and a `NAME = None` for each new top-level assigned name. The base's bodies and values are not
      touched: they may be the fix itself and would leak into the base.
    - What is left without a stub (a new method in a nested class) is returned separately: if a test fails because of
      it, the gate treats it as an invalid failure."""
    impl = ast.parse(cand_txt)
    if base_txt is None:
        assigned = _bound(ast.Module(body=[n for n in impl.body if isinstance(n, (ast.Assign, ast.AnnAssign, ast.AugAssign))],
                                        type_ignores=[]))
        symbols = sorted(_def_names(impl) | {x for x in assigned if not (x.startswith("__") and x.endswith("__"))})
        if not symbols:  # only imports or loose statements: nothing a new test could name (the contract requires symbols to be empty)
            return None, [], set()
        nulls: set[str] = set()
        _nullify(impl.body, nulls)
        for n in impl.body:
            if isinstance(n, ast.ClassDef):
                _nullify_class(n, nulls)
        return ast.unparse(_stub(impl)) + "\n", symbols, set()
    previous = ast.parse(base_txt)
    bound, defined = _bound(previous), _def_names(previous)
    new = _def_names(impl) - defined
    # an import that rebinds a name of the base (from x import a -> from y import a) would bring the fix in
    add = [n for n in impl.body if isinstance(n, (ast.Import, ast.ImportFrom))
               and not (isinstance(n, ast.ImportFrom) and n.module == "__future__")
               and not ({(a.asname or a.name).split(".")[0] for a in n.names} & bound)]
    symbols, covered, defs = [], set(), []
    for n in impl.body:
        if isinstance(n, DEFS) and n.name not in bound:
            defs.append(_stub(n))
            symbols.append(n.name)
            covered |= _def_names(n)
    before = {n.name: {m.name for m in n.body if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef))}
             for n in previous.body if isinstance(n, ast.ClassDef)}
    for n in impl.body:
        if isinstance(n, ast.ClassDef) and n.name in before:
            for m in n.body:
                if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef)) and m.name not in before[n.name]:
                    method, m.name = m.name, f"_a2dlc_stub_{n.name}_{m.name}"
                    defs += [_stub(m), ast.parse(f"{n.name}.{method} = {m.name}").body[0]]
                    symbols.append(f"{n.name}.{method}")
                    covered.add(method)
    for name in sorted(_bound(impl) - bound):
        if any(isinstance(n, DEFS) and n.name == name for n in impl.body):
            continue
        if any(name in {(a.asname or a.name).split(".")[0] for a in n.names} for n in add):
            continue
        defs.append(ast.parse(f"{name} = None").body[0])
        symbols.append(name)
    unstubbed = new - covered
    if not defs:
        return None, [], unstubbed
    stubs = ast.unparse(ast.Module(body=add + defs, type_ignores=[]))
    return base_txt.rstrip("\n") + "\n\n# --- a2dlc stubs (NOP gate) ---\n" + stubs + "\n", symbols, unstubbed


def is_test_file(file: str) -> bool:
    p = Path(file)
    return p.name.startswith("test_") or p.name.endswith("_test.py") or "tests" in p.parts


def modified_tests_of(base_txt: str | None, cand_txt: str, file: str) -> list[str]:
    """Test ids (file::Class.method or file::func) of a test file whose body changed: the test exists in both trees and its
    AST differs. Names are compared without their line numbers, so moving a test does not count. New tests are not listed:
    the NOP gate already judges them."""
    if base_txt is None or not is_test_file(file):
        return []
    try:
        before, after = _definitions(base_txt), _definitions(cand_txt)
    except (SyntaxError, RecursionError):
        return []
    return [f"{file}::{name}" for name, fn in after.items()
            if name.rsplit(".", 1)[-1].startswith("test") and name in before and ast.dump(before[name]) != ast.dump(fn)]


def stub(req: dict) -> int:
    base, cand = Path(req["base_dir"]), Path(req["candidate_dir"])
    stubbed, unstubbed, modified = [], set(), []
    for file in req["changed_files"]:
        c = cand / file
        if not file.endswith(".py") or not c.exists():
            continue
        b = base / file
        try:
            base_txt = read_py(b) if b.exists() else None
            cand_txt = read_py(c)
            text, symbols, missing = stub_of(base_txt, cand_txt)
        except (SyntaxError, ValueError, RecursionError):  # this file cannot be read or parsed: the others continue
            continue
        modified += modified_tests_of(base_txt, cand_txt, file)
        unstubbed |= missing
        if text is None:
            continue
        destination = IO / "stubs" / file
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(text, encoding="utf-8")
        stubbed.append({"file": file, "symbols": symbols})
    (IO / "response.json").write_text(json.dumps({"schema_version": 1, "stubbed": stubbed, "unstubbed": sorted(unstubbed),
                                                   "modified_tests": sorted(set(modified)), "error": None},
                                                  ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


# ---------- operation: signatures (Saboteur, protocol §3) ----------
# The candidate's public signatures, without bodies: the only part of the code the Saboteur can see.


def _public(name: str) -> bool:
    return not name.startswith("_") or (name.startswith("__") and name.endswith("__"))


def _signature(n: ast.AST) -> str:
    if isinstance(n, ast.ClassDef):
        bases = ", ".join(ast.unparse(b) for b in n.bases)
        return f"class {n.name}({bases})" if bases else f"class {n.name}"
    pre = "async def " if isinstance(n, ast.AsyncFunctionDef) else "def "
    ret = f" -> {ast.unparse(n.returns)}" if n.returns is not None else ""
    return f"{pre}{n.name}({ast.unparse(n.args)}){ret}"


def _doc(n: ast.AST) -> str | None:
    d = ast.get_docstring(n)
    return d.strip().split("\n\n")[0][:300] if d else None


def _symbol(n: ast.AST, name: str) -> dict:
    return {"name": name, "signature": _signature(n), "doc": _doc(n), "lines": [n.lineno, n.end_lineno or n.lineno]}


def signatures_of(text: str) -> list[dict]:
    """Top-level public functions and classes, and the public methods of those classes."""
    out = []
    for n in ast.parse(text).body:
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and _public(n.name):
            out.append(_symbol(n, n.name))
        elif isinstance(n, ast.ClassDef) and _public(n.name):
            out.append(_symbol(n, n.name))
            out += [_symbol(m, f"{n.name}.{m.name}") for m in n.body
                    if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef)) and _public(m.name)]
    return out


def signatures(req: dict) -> int:
    cand = Path(req["candidate_dir"])
    files = []
    for file in req["changed_files"]:
        c = cand / file
        if not file.endswith(".py") or not c.exists():
            continue
        try:
            symbols = signatures_of(read_py(c))
        except (SyntaxError, ValueError, RecursionError):  # this file cannot be read or parsed: the others continue
            continue
        files.append({"file": file, "symbols": symbols})
    (IO / "response.json").write_text(json.dumps({"schema_version": 1, "files": files, "error": None},
                                                  ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


# ---------- operation: tier0 (Builder, protocol §5 Stage 3) ----------
# Compile, strict typecheck (mypy --strict), lint (pyflakes) and the AST policy (budget.policy: forbidden imports,
# unbounded loops, reflection, complexity per function, @a2dlc header) of each changed file in the candidate, plus the
# blocking probes of budget.probes. Only findings that are new with respect to the same file in the base count: what was
# already wrong in the base belongs to the Builder and would block the loop. The header is filtered against the base:
# the Builder writes it into every file it touches. If mypy does not run, the finding is "tooling": what was not
# measured does not count as clean.

# pyflakes messages that are errors (the code fails when run), not style: an unused import must not block the Builder.
ERRORS_PYFLAKES = {"UndefinedName", "UndefinedLocal", "UndefinedExport", "DuplicateArgument", "ReturnOutsideFunction",
                    "YieldOutsideFunction", "ContinueOutsideLoop", "BreakOutsideLoop", "DefaultExceptNotLast",
                    "TwoStarredExpressions", "TooManyExpressionsInStarredAssignment", "ForwardAnnotationSyntaxError"}
TIMEOUT_PROBE_S = 30


def _pyflakes(text: str, file: str) -> list[tuple[int, str]]:
    """(line, message) of the pyflakes errors in the text."""
    from pyflakes import checker
    tree = ast.parse(text, filename=file)
    w = checker.Checker(tree, filename=file)
    out = []
    for m in w.messages:
        if type(m).__name__ in ERRORS_PYFLAKES:
            out.append((m.lineno, m.message % m.message_args))
    return sorted(out)


def findings_of(base_txt: str | None, cand_txt: str, file: str) -> list[dict]:
    """Compile/lint findings of the candidate that the base of the same file did not have (by message, without the line)."""
    try:
        compile(cand_txt, file, "exec", dont_inherit=True)
    except (SyntaxError, RecursionError) as e:
        msg = (getattr(e, "msg", None) or type(e).__name__)[:300]
        # a base that already failed to compile with the same message belongs to the Builder; otherwise it continues to pyflakes (it parses)
        if base_txt is not None:
            try:
                compile(base_txt, file, "exec", dont_inherit=True)
            except (SyntaxError, RecursionError) as b:
                if (getattr(b, "msg", None) or type(b).__name__)[:300] == msg:
                    return []
        line = getattr(e, "lineno", None)
        return [{"kind": "compile", "file": file, "line": line if line and line > 0 else None, "message": msg}]
    previous: dict[str, int] = {}
    if base_txt is not None:
        try:
            for _, msg in _pyflakes(base_txt, file):
                previous[msg] = previous.get(msg, 0) + 1
        except (SyntaxError, RecursionError):
            pass
    try:
        found = _pyflakes(cand_txt, file)
    except RecursionError:  # it compiles but pyflakes cannot walk a tree this deep: no lint for this file
        return []
    out = []
    for line, msg in found:
        if previous.get(msg, 0) > 0:
            previous[msg] -= 1
            continue
        out.append({"kind": "lint", "file": file, "line": line, "message": msg[:300]})
    return out


POLICY_DEFAULTS = {"forbidden_imports": [], "require_header": False, "max_complexity": 15}
REFLECTION = {"eval", "exec", "__import__"}
DYNAMIC_ATTRIBUTE = {"getattr", "setattr", "delattr"}
MYPY_TIMEOUT_S = 120
MYPY_LINE = re.compile(r"^(?P<file>[^:\n]+):(?P<line>\d+)(?::\d+)?: error: (?P<msg>.*)$", re.M)


def policy_of(req_policy: dict | None) -> dict:
    """The request policy with the engine defaults for whatever is missing (contracts/defs.schema.json#/$defs/tier0_policy)."""
    policy = dict(POLICY_DEFAULTS)
    policy.update({k: v for k, v in (req_policy or {}).items() if v is not None})
    return policy


def _forbidden(module: str | None, forbidden: list[str]) -> str | None:
    """The forbidden entry that covers module ('os' covers 'os' and 'os.path'), or None."""
    if not module:
        return None
    for p in forbidden:
        if module == p or module.startswith(p + "."):
            return p
    return None


def _breaks(node: ast.AST) -> bool:
    """Whether a loop body has its own break (those of nested loops and inner functions do not count)."""
    stack = list(getattr(node, "body", [])) + list(getattr(node, "orelse", []))
    while stack:
        x = stack.pop()
        if isinstance(x, ast.Break):
            return True
        if isinstance(x, (ast.While, ast.For, ast.AsyncFor, ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef, ast.Lambda)):
            continue
        stack.extend(ast.iter_child_nodes(x))
    return False


def _constant_true(test: ast.AST) -> bool:
    return isinstance(test, ast.Constant) and bool(test.value)


def _called_name(call: ast.Call) -> str:
    f = call.func
    if isinstance(f, ast.Name):
        return f.id
    if isinstance(f, ast.Attribute):
        parts = []
        while isinstance(f, ast.Attribute):
            parts.append(f.attr)
            f = f.value
        if isinstance(f, ast.Name):
            parts.append(f.id)
        return ".".join(reversed(parts))
    return ""


def policy_violations(text: str, policy: dict) -> list[tuple[int, str]]:
    """(line, message) of the AST policy (§5 Stage 3 step 2) over a text that parses: forbidden imports, unbounded loops
    (while over a true constant without break), reflection (eval, exec, __import__, importlib.import_module,
    getattr/setattr/delattr with a non-literal name) and cyclomatic complexity per function against max_complexity."""
    tree = ast.parse(text)
    out: list[tuple[int, str]] = []
    forbidden = list(policy.get("forbidden_imports") or [])
    for n in ast.walk(tree):
        if isinstance(n, ast.Import):
            for a in n.names:
                p = _forbidden(a.name, forbidden)
                if p:
                    out.append((n.lineno, f"forbidden import per policy: {a.name} ({p})"))
        elif isinstance(n, ast.ImportFrom) and n.level == 0:
            p = _forbidden(n.module, forbidden)
            if p:
                out.append((n.lineno, f"forbidden import per policy: {n.module} ({p})"))
        elif isinstance(n, ast.While) and _constant_true(n.test) and not _breaks(n):
            out.append((n.lineno, "unbounded loop: while over a true constant with no break"))
        elif isinstance(n, ast.Call):
            called = _called_name(n)
            if called in REFLECTION or called == "importlib.import_module":
                out.append((n.lineno, f"reflection: call to {called}"))
            elif called in DYNAMIC_ATTRIBUTE and len(n.args) >= 2 and not isinstance(n.args[1], ast.Constant):
                out.append((n.lineno, f"reflection: {called} with a non-literal attribute name"))
        elif isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
            c = _cyclomatic(n)
            if c > int(policy.get("max_complexity") or POLICY_DEFAULTS["max_complexity"]):
                out.append((n.lineno, f"cyclomatic complexity {c} of {n.name} exceeds max_complexity {policy.get('max_complexity')}"))
        if isinstance(n, (ast.Import, ast.ImportFrom, ast.Call)):
            found = side_effect_of(n, set(policy.get("forbidden_side_effects") or ()))
            if found:
                out.append((n.lineno, found))
    return sorted(out)


# ---------- forbidden side effects (intent.boundary.forbidden_side_effects) ----------
# Heuristics over the AST, one category at a time. Each one has false negatives it does not try to fix: an alias of the
# module (`import time as t`, `from time import time`), a receiver whose type is not visible (`sock.connect`, a
# `cursor` built elsewhere), a mode or an SQL string that is not a literal, and any call through getattr. Also not
# flagged: fs writes not listed (os.chmod, os.symlink, shutil.copytree, archive extraction), SQL built in a variable
# first (`q = f"..."; cur.execute(q)`). `.format` counts only when it is the argument itself (`execute("...".format(x))`).
# Pathlib methods (`.unlink`, `.mkdir`, `.touch`, ...) are flagged by name, so a method of another object with that name
# is flagged too. A hit is a policy finding of the new code (the same message in the base does not count). Messages
# carry no line number.

SIDE_EFFECT_CLOCK = {"time.time", "datetime.now", "datetime.utcnow", "datetime.datetime.now", "datetime.datetime.utcnow"}
SIDE_EFFECT_FS = {"os.remove", "os.rename", "os.makedirs", "os.unlink", "os.replace", "os.mkdir", "os.rmdir", "os.removedirs",
                  "shutil.rmtree", "shutil.copy", "shutil.copy2", "shutil.copyfile", "shutil.move"}
# Methods of pathlib.Path (and the like) are matched by name only: the receiver's type is not visible.
SIDE_EFFECT_FS_METHODS = ("write_text", "write_bytes", "unlink", "mkdir", "rmdir", "touch")
SIDE_EFFECT_NET = ("requests.", "httpx.", "urllib.request.")
SIDE_EFFECT_WRITE_MODES = set("wax+")


def _open_mode(call: ast.Call) -> str | None:
    """The literal mode of open(), or None if it is absent or not a literal (the read mode is the default)."""
    node = call.args[1] if len(call.args) > 1 else next((k.value for k in call.keywords if k.arg == "mode"), None)
    if node is None:
        return "r"
    return node.value if isinstance(node, ast.Constant) and isinstance(node.value, str) else None


def side_effect_of(n: ast.AST, enabled: set[str]) -> str | None:
    """The message of a forbidden side effect that node n produces, if its category is enabled; None otherwise."""
    if not enabled:
        return None
    if isinstance(n, ast.Import):
        for a in n.names:
            if "network.http.outbound" in enabled and a.name.split(".")[0] in ("requests", "httpx"):
                return f"side effect network.http.outbound: import of {a.name}"
    if isinstance(n, ast.ImportFrom) and n.level == 0 and n.module and "network.http.outbound" in enabled:
        if n.module.split(".")[0] in ("requests", "httpx") or n.module == "urllib.request":
            return f"side effect network.http.outbound: import from {n.module}"
    if not isinstance(n, ast.Call):
        return None
    called = _called_name(n)
    if "time.system_clock_direct" in enabled and called in SIDE_EFFECT_CLOCK:
        return f"side effect time.system_clock_direct: call to {called}"
    if "fs.write" in enabled:
        mode = _open_mode(n) if called == "open" else None
        if mode is not None and set(mode) & SIDE_EFFECT_WRITE_MODES:
            return f"side effect fs.write: open with write mode {mode!r}"
        if called in SIDE_EFFECT_FS:
            return f"side effect fs.write: call to {called}"
        if called.rsplit(".", 1)[-1] in SIDE_EFFECT_FS_METHODS:
            return f"side effect fs.write: call to .{called.rsplit('.', 1)[-1]}"
    if "network.http.outbound" in enabled and called.startswith(SIDE_EFFECT_NET):
        return f"side effect network.http.outbound: call to {called}"
    if "db.raw_sql_unparameterized" in enabled and called.rsplit(".", 1)[-1] in ("execute", "executemany") and n.args:
        sql = n.args[0]
        built = isinstance(sql, ast.JoinedStr) or (isinstance(sql, ast.BinOp) and isinstance(sql.op, (ast.Mod, ast.Add)))
        built = built or (isinstance(sql, ast.Call) and isinstance(sql.func, ast.Attribute) and sql.func.attr == "format")
        if built:
            return f"side effect db.raw_sql_unparameterized: {called} with a built SQL string"
    return None


def findings_policy(base_txt: str | None, cand_txt: str, file: str, policy: dict) -> list[dict]:
    """New policy findings of the candidate (by message, without the line), plus the header if the policy requires it."""
    out: list[dict] = []
    try:
        found = policy_violations(cand_txt, policy)
    except (SyntaxError, RecursionError):  # compile already reported it, or the tree is too deep to walk
        return out
    previous: dict[str, int] = {}
    if base_txt is not None:
        try:
            for _, msg in policy_violations(base_txt, policy):
                previous[msg] = previous.get(msg, 0) + 1
        except (SyntaxError, RecursionError):
            pass
    for line, msg in found:
        if previous.get(msg, 0) > 0:
            previous[msg] -= 1
            continue
        out.append({"kind": "policy", "file": file, "line": line, "message": msg[:300]})
    if policy.get("require_header"):
        out += [{"kind": "header", "file": file, "line": 1, "message": m} for m in header_problems(cand_txt)]
    return out


# The @a2dlc header (protocol §6.3, builder_header in role-payloads.schema.json): seven keys, each once. Scalars are JSON
# strings (contract_hash may be null in lite mode); the three list keys are JSON arrays of strings; rationale is not empty.
HEADER_KEYS = ("slice_id", "mode", "contract_hash", "rationale", "rejected_alternatives", "protected_invariants",
               "do_not_refactor")
HEADER_LISTS = ("rejected_alternatives", "protected_invariants", "do_not_refactor")
HEADER_LINE = re.compile(r"^\s*#\s*@a2dlc:(\w+)\s*=\s*(.*?)\s*$", re.M)


def header_problems(text: str) -> list[str]:
    """Messages for the @a2dlc header of a file that does not declare it right: a missing or repeated key, a value that
    is not valid JSON, a wrong type for its key, or an empty rationale. Empty when the header is complete."""
    values: dict[str, list[str]] = {}
    for key, raw in HEADER_LINE.findall(text):
        values.setdefault(key, []).append(raw)
    out: list[str] = []
    for key in HEADER_KEYS:
        found = values.get(key, [])
        if not found:
            out.append(f"missing header @a2dlc:{key} (require_header)")
            continue
        if len(found) > 1:
            out.append(f"repeated header @a2dlc:{key}: declared {len(found)} times")
            continue
        try:
            value = json.loads(found[0])
        except ValueError:
            out.append(f"header @a2dlc:{key} is not valid JSON")
            continue
        if key in HEADER_LISTS:
            if not (isinstance(value, list) and all(isinstance(x, str) for x in value)):
                out.append(f"header @a2dlc:{key} is not a JSON array of strings")
        elif key == "contract_hash":
            if value is not None and not isinstance(value, str):
                out.append("header @a2dlc:contract_hash is neither a JSON string nor null")
        elif not isinstance(value, str):
            out.append(f"header @a2dlc:{key} is not a JSON string")
        elif key == "rationale" and not value.strip():
            out.append("header @a2dlc:rationale is empty")
    return out


def _mypy(root: Path, files: list[str], flags: tuple[str, ...] = ()) -> dict[str, list[tuple[int, str]]] | None:
    """{file: [(line, message)]} from mypy --strict (plus flags) over files inside root; None if mypy did not run."""
    if not files:
        return {}
    cmd = [sys.executable, "-m", "mypy", "--strict", *flags, "--no-error-summary", "--no-color-output", "--show-error-codes",
           "--hide-error-context", "--no-incremental", "--cache-dir=/dev/null", "--follow-imports=silent",
           "--ignore-missing-imports", "--no-pretty", *files]
    try:
        r = subprocess.run(cmd, cwd=root, capture_output=True, text=True, timeout=MYPY_TIMEOUT_S,
                           env={"PYTHONDONTWRITEBYTECODE": "1", "PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": "/tmp"})
    except (subprocess.TimeoutExpired, OSError):
        return None
    if r.returncode not in (0, 1):  # 0 clean, 1 with errors; anything else means mypy is down (or missing)
        return None
    out: dict[str, list[tuple[int, str]]] = {f: [] for f in files}
    for m in MYPY_LINE.finditer(r.stdout):
        f = m.group("file").replace("\\", "/")
        if f in out:
            out[f].append((int(m.group("line")), m.group("msg").strip()))
    return out


def findings_typecheck(base: Path, cand: Path, files: list[str]) -> list[dict]:
    """mypy --strict errors of the candidate that the base did not have in the same file (by message, without the line).
    If mypy did not run, a tooling finding on the first file: what was not measured does not count as clean."""
    if not files:
        return []
    cand_result = _mypy(cand, files)
    if cand_result is None:
        return [{"kind": "tooling", "file": files[0], "line": None,
                 "message": f"mypy --strict did not run (missing, down, or over {MYPY_TIMEOUT_S} s): no typecheck in this round"}]
    in_base = [f for f in files if (base / f).exists()]
    base_result = _mypy(base, in_base) if in_base else {}
    if base_result is None:
        base_result = {}
    out: list[dict] = []
    for f in files:
        previous: dict[str, int] = {}
        for _, msg in base_result.get(f, []):
            previous[msg] = previous.get(msg, 0) + 1
        for line, msg in sorted(cand_result.get(f, [])):
            if previous.get(msg, 0) > 0:
                previous[msg] -= 1
                continue
            out.append({"kind": "typecheck", "file": f, "line": line, "message": msg[:300]})
    return out


PROBE_SCRIPT = """
import importlib, sys
def raises(exc, f, *a, **k):
    try:
        f(*a, **k)
    except exc:
        return True
    return False
m = importlib.import_module(sys.argv[1])
ns = dict(vars(m))
ns["raises"] = raises
sys.exit(0 if eval(compile(sys.argv[2], "<probe>", "eval"), ns) else 3)
"""


def probe(p: dict) -> dict | None:
    """Runs a probe in its own process against the candidate imported as a2dlc_cand; None if it passes."""
    rc, out = run([sys.executable, "-c", PROBE_SCRIPT, f"a2dlc_cand.{module(p['file'])}", p["expr"]], TIMEOUT_PROBE_S)
    if rc == 0:
        return None
    if rc is None:
        reason = f"exceeded {TIMEOUT_PROBE_S} s"
    elif rc == 3:
        reason = "returned false"
    else:
        last = out.strip().splitlines()[-1:] or [f"rc={rc}"]
        reason = f"raised {last[0]}"
    return {"kind": "probe", "file": p["file"], "line": None, "message": f"{p['id']}: {p['expr']} {reason}"[:300]}


def tier0(req: dict) -> int:
    base, cand = Path(req["base_dir"]), Path(req["candidate_dir"])
    probes = [p for p in (req.get("budget") or {}).get("probes") or [] if p.get("file") and p.get("expr")]
    try:
        import pyflakes  # noqa: F401
    except ImportError:
        (IO / "response.json").write_text(json.dumps({"schema_version": 1, "ok": False, "findings": [],
                                                      "error": "pyflakes is not in the image"}), encoding="utf-8")
        return 0
    policy = policy_of((req.get("budget") or {}).get("policy"))
    findings: list[dict] = []
    typed_list: list[str] = []
    for file in req["changed_files"]:
        c = cand / file
        if not file.endswith(".py") or not c.exists():
            continue
        b = base / file
        base_txt = b.read_text(encoding="utf-8", errors="replace") if b.exists() else None
        cand_txt = c.read_text(encoding="utf-8", errors="replace")
        compiles = findings_of(base_txt, cand_txt, file)
        findings += compiles
        findings += findings_policy(base_txt, cand_txt, file, policy)
        if not any(h["kind"] == "compile" for h in compiles):
            typed_list.append(file)
    findings += findings_typecheck(base, cand, typed_list)
    if probes:
        shutil.rmtree(WORK, ignore_errors=True)
        copy_py(cand, WORK / "a2dlc_cand")
        rewrite_imports(WORK / "a2dlc_cand", "a2dlc_cand")
        findings += [f for f in map(probe, probes) if f is not None]
    (IO / "response.json").write_text(json.dumps({"schema_version": 1, "ok": not findings, "findings": findings,
                                                  "error": None}, ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


# ---------- operation: mutate (Stage 5, protocol §5 Tier 2) ----------
# Mutants only of the changed lines, with the AST and with no dependencies. Each mutant changes the text of a single node
# (splice by position, in UTF-8 bytes as col_offset): the rest of the file stays the same, comments included.
# The descriptions go to the Saboteur, who does not see the code: they name the operator (add_operator), never identifiers or literals.

SYMBOL = {ast.Add: "+", ast.Sub: "-", ast.Mult: "*", ast.Div: "/", ast.FloorDiv: "//", ast.Mod: "%", ast.Pow: "**",
           ast.Lt: "<", ast.LtE: "<=", ast.Gt: ">", ast.GtE: ">=", ast.Eq: "==", ast.NotEq: "!=", ast.Is: "is",
           ast.IsNot: "is not", ast.In: "in", ast.NotIn: "not in"}
ARITHMETIC = {ast.Add: ast.Sub, ast.Sub: ast.Add, ast.Mult: ast.Div, ast.Div: ast.Mult, ast.FloorDiv: ast.Mult,
              ast.Mod: ast.FloorDiv, ast.Pow: ast.Mult}
COMPARISON = {ast.Lt: ast.LtE, ast.LtE: ast.Lt, ast.Gt: ast.GtE, ast.GtE: ast.Gt, ast.Eq: ast.NotEq, ast.NotEq: ast.Eq,
               ast.Is: ast.IsNot, ast.IsNot: ast.Is, ast.In: ast.NotIn, ast.NotIn: ast.In}
CONDITION = {ast.If: "an if", ast.While: "a while", ast.IfExp: "a conditional expression", ast.Assert: "an assert"}


def _excluded(mod: ast.Module) -> set[int]:
    """ids of the nodes that are not mutated: docstrings and loose strings, parts of f-strings and annotations."""
    outside: set[int] = set()
    for n in ast.walk(mod):
        sub: list[ast.AST] = []
        if isinstance(n, ast.Expr) and isinstance(n.value, ast.Constant) and isinstance(n.value.value, str):
            sub.append(n.value)
        elif isinstance(n, ast.JoinedStr):
            sub.append(n)
        elif isinstance(n, ast.arg) and n.annotation is not None:
            sub.append(n.annotation)
        elif isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)) and n.returns is not None:
            sub.append(n.returns)
        elif isinstance(n, ast.AnnAssign):
            sub.append(n.annotation)
        for s in sub:
            outside.update(id(x) for x in ast.walk(s))
    return outside


NEGATE = object()  # new text of an edit: `(not (<original text>))`

Edit = tuple[int, int, int, int, "bytes | object"]  # (row, byte column) of start and end, new text


def _tokens(text: str) -> list[tuple[int, int, str]]:
    """(row, UTF-8 byte column, text) of each OP or NAME token, in order. The AST does not position the operator of a
    BinOp, a Compare or an AugAssign: it is searched between the positions of its operands."""
    lines = text.split("\n")
    out: list[tuple[int, int, str]] = []
    try:
        for t in tokenize.generate_tokens(io.StringIO(text).readline):
            if t.type in (tokenize.OP, tokenize.NAME):
                out.append((t.start[0], len(lines[t.start[0] - 1][:t.start[1]].encode("utf-8")), t.string))
    except (tokenize.TokenError, SyntaxError):
        pass  # without tokens there are no add_operator mutants; the others carry on
    return out


def _operator(tokens: list[tuple[int, int, str]], start: tuple[int, int], end: tuple[int, int],
              words: list[str]) -> tuple[int, int, int, int] | None:
    """(row, col, row, col) of the first `words` operator (one or two tokens: `is not`) between the start and end positions."""
    i, n = bisect.bisect_left(tokens, (start[0], start[1], "")), len(words)
    while i < len(tokens) and (tokens[i][0], tokens[i][1]) < end:
        if [t[2] for t in tokens[i:i + n]] == words:
            row, col, last = tokens[i + n - 1]
            return tokens[i][0], tokens[i][1], row, col + len(last.encode("utf-8"))
        i += 1
    return None


def _node(n: ast.AST, text: bytes | object) -> Edit:
    return n.lineno, n.col_offset, n.end_lineno, n.end_col_offset, text


def _changes(mod: ast.Module, lines: set[int], tokens: list[tuple[int, int, str]]
             ) -> list[tuple[int, int, str, str, list[Edit]]]:
    """(declared line, column, operator, description, edits) of the requested lines. An operator mutant (arithmetic,
    comparison, `+=`) changes only the operator token and declares the line of that token; the others replace the text
    of their node and declare the line where it starts."""
    outside = _excluded(mod)
    out: list[tuple[int, int, str, str, list[Edit]]] = []

    def add_operator(op_name: str, description: str, start: ast.AST, end: ast.AST, words: list[str], new: str,
                 wrap: ast.AST | None = None) -> None:
        pos = _operator(tokens, (start.end_lineno, start.end_col_offset), (end.lineno, end.col_offset), words)
        if pos is None or pos[0] not in lines:
            return
        edits: list[Edit] = [(*pos, new.encode("utf-8"))]
        if wrap is not None:  # Pow → Mult lowers the precedence: the whole node goes in parentheses
            edits += [(wrap.lineno, wrap.col_offset, wrap.lineno, wrap.col_offset, b"("),
                          (wrap.end_lineno, wrap.end_col_offset, wrap.end_lineno, wrap.end_col_offset, b")")]
        out.append((pos[0], pos[1], op_name, description, edits))

    for n in ast.walk(mod):
        if id(n) in outside or getattr(n, "end_col_offset", None) is None:
            continue
        on_requested_line = getattr(n, "lineno", None) in lines
        if isinstance(n, ast.BinOp) and type(n.op) in ARITHMETIC:
            a, c = SYMBOL[type(n.op)], SYMBOL[ARITHMETIC[type(n.op)]]
            add_operator("arithmetic", f"arithmetic operator {a} changed to {c}", n.left, n.right, [a], c,
                     n if isinstance(n.op, ast.Pow) else None)
        elif isinstance(n, ast.AugAssign) and type(n.op) in ARITHMETIC:
            a, c = SYMBOL[type(n.op)], SYMBOL[ARITHMETIC[type(n.op)]]
            add_operator("arithmetic", f"arithmetic operator {a}= changed to {c}=", n.target, n.value, [a + "="], c + "=")
        elif isinstance(n, ast.Compare):
            for i, op in enumerate(n.ops):
                if type(op) in COMPARISON:
                    a, c = SYMBOL[type(op)], SYMBOL[COMPARISON[type(op)]]
                    add_operator("comparison", f"comparison {a} changed to {c}", n.left if i == 0 else n.comparators[i - 1],
                             n.comparators[i], a.split(), c)
        elif on_requested_line and isinstance(n, ast.Constant) and not isinstance(n.value, (bytes, complex)) and n.value is not None \
                and n.value is not Ellipsis:
            v = n.value
            if isinstance(v, bool):
                out.append((n.lineno, n.col_offset, "constant", "boolean inverted", [_node(n, repr(not v).encode())]))
            elif isinstance(v, (int, float)):
                out.append((n.lineno, n.col_offset, "constant", "numeric constant changed: +1", [_node(n, repr(v + 1).encode())]))
                out.append((n.lineno, n.col_offset, "constant", "numeric constant changed: -1", [_node(n, repr(v - 1).encode())]))
            elif isinstance(v, str):
                out.append((n.lineno, n.col_offset, "constant",
                            "non-empty string changed to empty" if v else "empty string changed to non-empty",
                            [_node(n, b"''" if v else b"'a2dlc'")]))
        elif on_requested_line and isinstance(n, ast.Return) and n.value is not None \
                and not (isinstance(n.value, ast.Constant) and n.value.value is None):
            out.append((n.lineno, n.col_offset, "return_none", "return <value> changed to return None", [_node(n, b"return None")]))
        elif on_requested_line and isinstance(n, ast.Expr) and isinstance(n.value, (ast.Call, ast.Await)):
            out.append((n.lineno, n.col_offset, "remove_call", "call used as a statement removed", [_node(n, b"pass")]))
        if isinstance(n, tuple(CONDITION)) and id(n.test) not in outside and n.test.lineno in lines:
            out.append((n.test.lineno, n.test.col_offset, "negate_condition", f"condition of {CONDITION[type(n)]} negated",
                        [_node(n.test, NEGATE)]))
    out.sort(key=lambda c: (c[0], c[1], c[2]))
    return out


# ---------- pruning of equivalent mutants (protocol §5 Stage 5, step 2) ----------
# Conservative: only what the AST proves without running. A mutant inside a logging or an assert statement changes
# nothing the tests could observe (or the assert kills it by the execution itself); a branch with a constant condition
# never runs. Any other node is mutated as usual: an extra pruning would hide a gap in the suite.

REASON_LOG = "equivalent: logging-only statement"
REASON_ASSERT = "equivalent: assert"
REASON_BRANCH = "equivalent: dead branch (constant condition)"
PREFIXES_LOG = ("logging.", "logger.", "log.")


def _dotted(f: ast.AST) -> str | None:
    """Dotted name of a callable (`logging.info`), or None if it is not a dotted name."""
    parts = []
    while isinstance(f, ast.Attribute):
        parts.append(f.attr)
        f = f.value
    if not isinstance(f, ast.Name):
        return None
    parts.append(f.id)
    return ".".join(reversed(parts))


def _only_log(n: ast.AST) -> bool:
    """Statement that is only a logging call: logging.*, logger.*, log.*, print or warnings.warn."""
    if not (isinstance(n, ast.Expr) and isinstance(n.value, ast.Call)):
        return False
    d = _dotted(n.value.func)
    return d is not None and (d == "print" or d == "warnings.warn" or d.startswith(PREFIXES_LOG))


def _constant(test: ast.AST) -> bool | None:
    """Truth value of a literal condition True, False or 0; None for anything else."""
    if not isinstance(test, ast.Constant):
        return None
    v = test.value
    if type(v) is bool:
        return v
    if type(v) is int and v == 0:
        return False
    return None


def _span(n: ast.AST) -> tuple[int, int, int, int]:
    return n.lineno, n.col_offset, n.end_lineno, n.end_col_offset


def _prunes(mod: ast.Module) -> list[tuple[tuple[int, int, int, int], str]]:
    """(span, reason) of each statement that is not mutated. A mutant whose position falls inside a span is discarded."""
    out = []
    for n in ast.walk(mod):
        dead: list[ast.AST] = []
        if isinstance(n, ast.If):
            c = _constant(n.test)
            dead = n.body if c is False else n.orelse if c is True else []
        elif isinstance(n, ast.While) and _constant(n.test) is False:
            dead = n.body
        out += [(_span(s), REASON_BRANCH) for s in dead]
        if isinstance(n, ast.Assert):
            out.append((_span(n), REASON_ASSERT))
        elif _only_log(n):
            out.append((_span(n), REASON_LOG))
    return out


def _inside(row: int, col: int, span: tuple[int, int, int, int]) -> bool:
    return (span[0], span[1]) <= (row, col) < (span[2], span[3])


def _mutants(text: str, lines: set[int], extra: list[tuple[tuple[int, int, int, int], str]] | None = None
             ) -> tuple[list[tuple[int, str, str, str]], list[tuple[int, str]]]:
    """(mutants, pruned): the mutants of the requested lines and (line, reason) for each mutant discarded as equivalent.
    `extra` are more spans to prune (the type-guaranteed ones); the AST reasons come first and win where both apply."""
    mod = ast.parse(text)
    prunes = _prunes(mod) + list(extra or [])
    raw = text.encode("utf-8")
    starts, pos = [], 0
    for line in raw.splitlines(keepends=True):
        starts.append(pos)
        pos += len(line)
    starts.append(pos)
    out, pruned = [], []
    for line, col, add_operator, description, edits in _changes(mod, lines, _tokens(text)):
        reason = next((r for span, r in prunes if _inside(line, col, span)), None)
        if reason is not None:
            if (line, reason) not in pruned:
                pruned.append((line, reason))
            continue
        mutated = raw
        for f1, c1, f2, c2, new in sorted(edits, key=lambda e: (e[0], e[1]), reverse=True):  # from back to front
            a, b = starts[f1 - 1] + c1, starts[f2 - 1] + c2
            if new is NEGATE:
                new = b"(not (" + raw[a:b] + b"))"
            mutated = mutated[:a] + new + mutated[b:]
        mutated = mutated.decode("utf-8")
        if mutated == text:
            continue
        try:
            compile(mutated, "<mutant>", "exec")
        except (SyntaxError, ValueError):
            continue
        out.append((line, add_operator, description, mutated))
    return out, pruned


def mutants_of(text: str, lines: set[int]) -> list[tuple[int, str, str, str]]:
    """(line, operator, description, mutated text) of the requested lines, in order; each one compiles and differs.
    For the tests only: mutate calls _mutants, which also returns the pruned ones."""
    return _mutants(text, lines)[0]


def pruned_of(text: str, lines: set[int]) -> list[tuple[int, str]]:
    """(line, reason) of the requested lines where pruning discarded some mutant as equivalent."""
    return _mutants(text, lines)[1]


# Type-guaranteed dead branches (protocol §5 Stage 5, step 2): mypy --warn-unreachable proves a statement can never run,
# because a narrowing leaves nothing for it. Reason: contract.PruneDeadBranchTypes. A mypy timeout or failure prunes nothing
# by types; the AST rules still apply.
REASON_TYPE = "equivalent: type-guaranteed dead branch"
UNREACHABLE_FLAGS = ("--warn-unreachable",)
STATEMENT_BLOCKS = ("body", "orelse", "finalbody")


UNREACHABLE_STATEMENT = "Statement is unreachable"  # mypy's statement-level message (its message starts with this)
EXIT_CALLS = {"sys.exit", "os._exit", "exit", "quit"}


def _unreachable_lines(root: Path, files: list[str]) -> dict[str, set[int]]:
    """{file: lines} where mypy says a statement is unreachable over files inside root; {} if mypy did not run. The
    expression-level messages (`Right operand of "or" is never evaluated`) are left out: they name a part of a statement,
    and pruning the whole statement for them would discard mutants that can still change the behaviour."""
    result = _mypy(root, files, UNREACHABLE_FLAGS)
    if result is None:
        return {}
    return {f: {line for line, msg in found if msg.startswith(UNREACHABLE_STATEMENT)} for f, found in result.items()}


def _terminates(s: ast.stmt) -> bool:
    """Whether control never goes on past statement s. A statement after one of these is dead by control flow, which mypy
    also reports as unreachable: it is not dead by types, so it is not pruned as a type-guaranteed branch. Not known here:
    a call to a user function that ends in NoReturn; mypy still reports the statement after it."""
    if isinstance(s, (ast.Return, ast.Raise, ast.Continue, ast.Break)):
        return True
    if isinstance(s, ast.Expr) and isinstance(s.value, ast.Call):
        return _dotted(s.value.func) in EXIT_CALLS
    if isinstance(s, ast.While):
        return _constant_true(s.test) and not _breaks(s)
    if isinstance(s, ast.If):
        return bool(s.orelse) and any(_terminates(x) for x in s.body) and any(_terminates(x) for x in s.orelse)
    return False


def _type_dead(mod: ast.Module, reported: set[int]) -> list[tuple[tuple[int, int, int, int], str]]:
    """Spans proven dead by mypy. mypy reports the first unreachable statement of a block and not the rest of it, so the
    span runs from that statement to the end of its block. A reported statement that follows a terminating one in its block
    is dead by control flow and gives no span. A reported line that starts no statement (inside a multi-line expression)
    gives the innermost statement that contains it."""
    out: list[tuple[tuple[int, int, int, int], str]] = []
    handled: set[int] = set()
    for n in ast.walk(mod):
        for field in STATEMENT_BLOCKS:
            block = getattr(n, field, None)
            if not isinstance(block, list) or not block:
                continue
            i = next((k for k, s in enumerate(block) if isinstance(s, ast.stmt) and s.lineno in reported), None)
            if i is None:
                continue
            first = block[i]
            handled.add(first.lineno)
            if any(_terminates(s) for s in block[:i]):
                continue
            out.append(((first.lineno, first.col_offset, block[-1].end_lineno, block[-1].end_col_offset), REASON_TYPE))
    for line in sorted(reported - handled):
        inside = [s for s in ast.walk(mod) if isinstance(s, ast.stmt) and s.lineno <= line <= (s.end_lineno or s.lineno)]
        if inside:
            s = max(inside, key=lambda x: x.lineno)
            out.append(((s.lineno, s.col_offset, s.end_lineno or s.lineno, s.end_col_offset), REASON_TYPE))
    return out


def mutate(req: dict) -> int:
    budget = req.get("budget") or {}
    requested = budget.get("changed_lines") or {}
    limit = int(budget.get("max_mutants") or 30)
    cand = Path(req["candidate_dir"])
    mutants, not_generated, pruned = [], [], []
    sources: list[tuple[str, str, set[int]]] = []
    for file in req["changed_files"]:
        lines = set(requested.get(file) or [])
        c = cand / file
        if not lines or not c.exists():
            continue
        if not file.endswith(".py"):
            not_generated.append({"file": file, "reason": "not Python: this engine does not mutate it"})
            continue
        try:
            text = read_py(c)
            ast.parse(text)
        except UnicodeDecodeError as e:
            not_generated.append({"file": file, "reason": f"unsupported encoding: {e}"[:300]})
            continue
        except (SyntaxError, ValueError) as e:
            not_generated.append({"file": file, "reason": f"does not parse: {e}"[:300]})
            continue
        except RecursionError:
            not_generated.append({"file": file, "reason": "expression too deep for the AST (RecursionError)"})
            continue
        sources.append((file, text, lines))
    unreachable = _unreachable_lines(cand, [file for file, _, _ in sources])  # one mypy run per call
    for file, text, lines in sources:
        try:
            own, here = _mutants(text, lines, _type_dead(ast.parse(text), unreachable.get(file, set())))
        except (SyntaxError, ValueError) as e:
            not_generated.append({"file": file, "reason": f"does not parse: {e}"[:300]})
            continue
        except RecursionError:
            not_generated.append({"file": file, "reason": "expression too deep for the AST (RecursionError)"})
            continue
        pruned += [{"file": file, "line": line, "reason": reason} for line, reason in here]
        fits = own[:max(limit - len(mutants), 0)]
        for line, add_operator, description, text in fits:
            mid = f"m{len(mutants) + 1:03d}"
            destination = IO / "mutants" / mid / file
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_text(text, encoding="utf-8")
            mutants.append({"id": mid, "file": file, "line": line, "operator": add_operator, "description": description})
        if len(own) > len(fits):
            rest = sorted({x[0] for x in own[len(fits):]})
            not_generated.append({"file": file, "reason": f"max_mutants={limit}: {len(own) - len(fits)} mutants not generated "
                                                         f"(lines {', '.join(map(str, rest))})"[:300]})
    (IO / "response.json").write_text(json.dumps({"schema_version": 1, "mutants": mutants, "not_generated": not_generated,
                                                  "pruned": pruned, "error": None}, ensure_ascii=False, indent=1),
                                     encoding="utf-8")
# ---------- operation: complexity (Occam selection between builders, protocol §5 Stage 5) ----------
# Per changed file: lines of the diff against the base, and McCabe and AST nodes of the functions (and methods) that the
# candidate added or whose AST changed. null if the file is not Python or does not parse: the core counts it as zero.

DECISIONS = (ast.If, ast.For, ast.AsyncFor, ast.While, ast.IfExp, ast.ExceptHandler, ast.Assert, ast.comprehension)


def _cyclomatic(fn: ast.AST) -> int:
    """McCabe: 1 plus each decision point (if, loops, except, assert, ternary, each for of a comprehension and each of its
    ifs, each extra operand of and/or and each case of match)."""
    n = 1
    for x in ast.walk(fn):
        if isinstance(x, DECISIONS):
            n += 1 + (len(x.ifs) if isinstance(x, ast.comprehension) else 0)
        elif isinstance(x, ast.BoolOp):
            n += len(x.values) - 1
        elif hasattr(ast, "match_case") and isinstance(x, ast.match_case):
            n += 1
    return n


def _definitions(text: str) -> dict[str, ast.AST]:
    """Top-level functions and methods of top-level classes (Class.method)."""
    out: dict[str, ast.AST] = {}
    for n in ast.parse(text).body:
        if isinstance(n, (ast.FunctionDef, ast.AsyncFunctionDef)):
            out[n.name] = n
        elif isinstance(n, ast.ClassDef):
            for m in n.body:
                if isinstance(m, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    out[f"{n.name}.{m.name}"] = m
    return out


def measure(file: str, base_txt: str | None, cand_txt: str | None) -> dict:
    a, b = (base_txt or "").splitlines(), (cand_txt or "").splitlines()
    added = removed = 0
    for op, i1, i2, j1, j2 in difflib.SequenceMatcher(a=a, b=b, autojunk=False).get_opcodes():
        if op != "equal":
            removed += i2 - i1
            added += j2 - j1
    out = {"file": file, "loc_added": added, "loc_removed": removed, "cyclomatic": None, "ast_nodes": None}
    if not file.endswith(".py"):
        return out
    try:
        vc = _definitions(cand_txt) if cand_txt is not None else {}
        vb = _definitions(base_txt) if base_txt is not None else {}
        changed = [fn for name, fn in vc.items() if name not in vb or ast.dump(vb[name]) != ast.dump(fn)]
    except (SyntaxError, RecursionError):  # does not parse, or the tree is too deep for ast.dump: null, counted as zero
        return out
    out["cyclomatic"] = sum(_cyclomatic(fn) for fn in changed)
    out["ast_nodes"] = sum(sum(1 for _ in ast.walk(fn)) for fn in changed)
    return out


def complexity(req: dict) -> int:
    base, cand = Path(req["base_dir"]), Path(req["candidate_dir"])
    files = []
    for file in req["changed_files"]:
        b, c = base / file, cand / file
        files.append(measure(file, b.read_text(encoding="utf-8", errors="replace") if b.exists() else None,
                           c.read_text(encoding="utf-8", errors="replace") if c.exists() else None))
    (IO / "response.json").write_text(json.dumps({"schema_version": 1, "files": files, "error": None},
                                                  ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


# ---------- operation: classify_lines (coverage notes, docs/COVERAGE_NOTES.md) ----------
# budget.lines maps each file to the changed lines nobody examined. Per file, the ones that are a defensive handler or
# declaration-only code, by category (classify.py). Anything else is left out, and the core keeps it blocking.

def classify_lines(req: dict) -> int:
    import classify  # imported here like the other operations' modules: this file loads without it
    cand = Path(req["candidate_dir"])
    wanted = (req.get("budget") or {}).get("lines") or {}
    files = []
    for file in req["changed_files"]:
        c = cand / file
        notes = []
        if file.endswith(".py") and c.is_file():
            notes = classify.classify(c.read_text(encoding="utf-8", errors="replace"), wanted.get(file) or [])
        files.append({"file": file, "notes": notes})
    (IO / "response.json").write_text(json.dumps({"schema_version": 1, "files": files, "error": None},
                                                  ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


# ---------- operation: diff (the differential gate, protocol §5 Stage 4) ----------
# budget.workers threads run the targets (default 1, at most MAX_WORKERS). Each target starts in order of priority: more
# callers in its own file first, then by name. Results are assembled in the request's order, so the targets do not depend
# on the number of workers (search_budget.iterations is CrossHair's own count and varies with timing). budget.total_s bounds the run: a target that starts after it has run out is
# INCONCLUSIVE with the reason NOT_RUN_BUDGET, and one that already runs is not stopped (the budget is soft by one
# CrossHair run); the shrink of its counterexample runs within what is left. The response carries diff_budget (verdict).
# budget.critical is not read here: the contract gives it as seconds for critical slices, not as an order of functions.
# The callers of a changed function are compared only if one of the changed ones was INCONCLUSIVE: with FAIL it already
# blocks; with PASS the callers would add signal and also noise (CrossHair does not support many real signatures, and
# running a main reads sys.argv).

NOT_RUN_BUDGET = "not run: budget"


@dataclasses.dataclass
class Job:
    file: str
    mod: str
    name: str
    fn: ast.AST
    rc_imp: int | None
    out_imp: str
    callers: int
    result: dict | None = None


@dataclasses.dataclass
class Block:
    """The targets of one changed file: the changed functions, and their callers (compared only if one of them is INCONCLUSIVE)."""
    direct: list[Job]
    indirect: list[Job]


def intra_callers(fns: dict[str, ast.AST]) -> dict[str, int]:
    """How many other top-level functions of the same file use each name."""
    uses = {n: {x.id for x in ast.walk(f) if isinstance(x, ast.Name)} for n, f in fns.items()}
    return {n: sum(1 for m in fns if m != n and n in uses[m]) for n in fns}


def workers_of(budget: dict) -> int:
    """budget.workers, at least 1 and at most MAX_WORKERS. Each worker runs its own CrossHair process, and the engine
    container has 1 GB in total (engine.go). MAX_WORKERS is 2: the largest count that one real pair ran with (verdicts
    equal to workers=1). No memory was measured per worker, so a higher bound needs that measurement first."""
    return min(max(int(budget.get("workers") or 1), 1), MAX_WORKERS)


MAX_WORKERS = 2


def diff(req: dict, clock=time.monotonic) -> int:
    start = clock()
    budget = req.get("budget") or {}
    timeout_s = max(float(budget.get("per_condition_timeout_s", 20)), FLOOR_TIMEOUT_S)
    workers = workers_of(budget)
    total = budget.get("total_s")
    deadline = None if total is None else start + float(total)
    base_dir, cand_dir = Path(req["base_dir"]), Path(req["candidate_dir"])
    outside: list[dict] = []
    identical: list[str] = []  # engine-response.identical: not judged because nothing about them differs
    rel, unsupported = relaxations(req.get("equivalence_relaxations"))

    shutil.rmtree(WORK, ignore_errors=True)
    for origin, package in ((base_dir, "a2dlc_base"), (cand_dir, "a2dlc_cand")):
        copy_py(origin, WORK / package)
        rewrite_imports(WORK / package, package)
    install_import_shims()

    layout: list[dict | Block] = []  # output order: a finished target, or the jobs of one changed file
    for file in req["changed_files"]:
        if not file.endswith(".py"):
            outside.append({"file": file, "reason": "not_exported"})
            continue
        bp, cp = base_dir / file, cand_dir / file
        if not bp.exists():
            outside.append({"file": file, "reason": "added"})
            continue
        if not cp.exists():
            outside.append({"file": file, "reason": "deleted"})
            continue
        try:
            base_txt, cand_txt = read_py(bp), read_py(cp)
            direct, callers = targets_of(base_txt, cand_txt)
            identical += [f"{file}::{s}" for s in identical_of(base_txt, cand_txt)]
        except (SyntaxError, ValueError, RecursionError) as e:  # ValueError includes UnicodeDecodeError
            t = new_target(f"{file}::{file.rsplit('/', 1)[-1]}", timeout_s)
            t["verdict"], t["reason"] = "INCONCLUSIVE", f"could not read or parse: {type(e).__name__}: {e}"[:300]
            layout.append(t)
            continue
        if not direct:
            outside.append({"file": file, "reason": "not_exported"})
            continue
        mod = module(file)
        vc = functions(cand_txt)
        counts = intra_callers(vc)
        rc_imp, out_imp = run([sys.executable, "-c", f"import a2dlc_base.{mod}, a2dlc_cand.{mod}"], 120)
        layout.append(Block(
            direct=[Job(file, mod, n, vc[n], rc_imp, out_imp, counts[n]) for n in direct],
            indirect=[Job(file, mod, n, vc[n], rc_imp, out_imp, counts[n]) for n in callers]))

    def run_job(j: Job) -> dict:
        now = clock()
        if deadline is not None and now >= deadline:
            t = new_target(f"{j.file}::{j.name}", timeout_s)
            t["lines"] = lines_of(j.fn)
            t["verdict"], t["reason"] = "INCONCLUSIVE", NOT_RUN_BUDGET
            return t
        return compare(j.file, j.mod, j.name, j.fn, j.rc_imp, j.out_imp, timeout_s, rel,
                       remaining=None if deadline is None else deadline - now)

    def execute(jobs: list[Job]) -> None:
        ordered = sorted(jobs, key=lambda j: (-j.callers, j.name, j.file))
        with ThreadPoolExecutor(max_workers=workers) as pool:
            for j, result in zip(ordered, pool.map(run_job, ordered)):
                j.result = result

    blocks = [item for item in layout if isinstance(item, Block)]
    execute([j for b in blocks for j in b.direct])
    execute([j for b in blocks if any(j.result["verdict"] == "INCONCLUSIVE" for j in b.direct) for j in b.indirect])

    targets: list[dict] = []
    for item in layout:
        if isinstance(item, dict):
            targets.append(item)
            continue
        targets += [j.result for j in item.direct]
        if any(j.result["verdict"] == "INCONCLUSIVE" for j in item.direct):
            targets += [j.result for j in item.indirect]

    resp = {"schema_version": 1, "targets": targets, "out_of_scope": outside, "error": None}
    if identical:
        resp["identical"] = identical
    if req.get("equivalence_relaxations"):
        resp["equivalence_unsupported"] = unsupported
    if total is not None and float(total) > 0:  # verdict.diff_budget: every target the budget cut is named, none omitted
        resp["diff_budget"] = {"total_s": float(total), "used_s": round(clock() - start, 3),
                               "cut": [{"target": t["target"], "reason": t["reason"]} for t in targets
                                       if t.get("reason") == NOT_RUN_BUDGET]}
    (IO / "response.json").write_text(json.dumps(resp, ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


def main() -> int:
    req = json.loads((IO / "request.json").read_text(encoding="utf-8"))
    if req.get("operation") == "complexity":
        return complexity(req)
    if req.get("operation") == "classify_lines":
        return classify_lines(req)
    if req.get("operation") == "stub":
        return stub(req)
    if req.get("operation") == "signatures":
        return signatures(req)
    if req.get("operation") == "tier0":
        return tier0(req)
    if req.get("operation") == "mutate":
        return mutate(req)
    # The modules below are other packages' files (lemmas.py, fuzz.py, graph.py, traces.py). They are imported only by the
    # operation that needs them, so this file loads without them; a missing one fails the request, which the core reads
    # as no response.json: every changed file INCONCLUSIVE, never a silent diff.
    if req.get("operation") == "lemmas":
        import lemmas
        return lemmas.run_lemmas(req)
    if req.get("operation") == "fuzz":
        import fuzz
        return fuzz.run(req, IO)
    if req.get("operation") == "deps_graph":
        import graph
        return graph.deps_graph(req, IO)
    if req.get("operation") == "capture_traces":
        import traces
        return traces.capture_traces(req, IO)
    return diff(req)


if __name__ == "__main__":
    sys.exit(main())
