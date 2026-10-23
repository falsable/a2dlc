"""a2dlc lemmas operation (Stage 2 of Critical slices; protocol v3.4 §5, decision 12).

Each lemma is a top-level function of the candidate with a declared postcondition over its arguments and its result.
The engine writes a wrapper module per lemma. The wrapper binds the whole module namespace of the candidate, calls the
lemma and carries the postcondition as a PEP 316 docstring (`post: ...`, where `_` is the return value): it is the
contract kind CrossHair checks without an extra library. In refactor mode (diffbehavior) the same-named lemma of the
base is also compared with the candidate's.

Statuses:
  pass          CrossHair confirmed the postcondition over all paths (not just "no counterexample found") and, in
                refactor, found no difference from the base lemma.
  fail          a counterexample exists. The counterexample shape tells the core which case it is, and the shape is
                part of this contract:
                  {"args", "returned"}  the postcondition is false for these arguments (Tier 0 feedback to the builder)
                  {"args", "raised"}    the lemma raises for these arguments (Tier 0 feedback to the builder)
                  {"args", "old", "new"} refactor: the candidate's lemma diverges from the base's (a probe)
  inconclusive  the engine could not decide within its budget, or the lemma is outside what it can check.
  no_pair       refactor only: the base has no same-named lemma to diff against.

A pass never raises anything: the core applies that rule. The postcondition check uses a fixed budget of
FLOOR_TIMEOUT_S seconds of CrossHair CPU time per lemma (the measured floor, engine.py).
Limitation: only top-level functions with annotations on every parameter are checked. Async functions are inconclusive.
"""
from __future__ import annotations

import ast
import builtins
import json
import re
import shutil
import sys
from dataclasses import dataclass
from pathlib import Path

import engine as core

ERROR = re.compile(r": error: (?P<rest>.*)$")
CALLED = re.compile(r"^(?P<msg>.*?) when calling \w+\((?P<args>.*?)\)(?: \(which returns (?P<ret>.*)\))?$")
CONFIRMED = "info: Confirmed over all paths."
NO_CHECKABLE = "contain no checkable functions"
SIDE_EFFECT = "SideEffectDetected"


@dataclass
class Context:
    base: Path
    cand: Path
    changed: list
    refactor: bool


def run_lemmas(req: dict) -> int:
    budget = req.get("budget") or {}
    ctx = Context(base=Path(req["base_dir"]), cand=Path(req["candidate_dir"]),
                  changed=list(req.get("changed_files") or []), refactor=budget.get("mode") == "diffbehavior")
    shutil.rmtree(core.WORK, ignore_errors=True)
    core.WORK.mkdir(parents=True, exist_ok=True)
    core.copy_py(ctx.cand, core.WORK / "a2dlc_cand")
    core.rewrite_imports(core.WORK / "a2dlc_cand", "a2dlc_cand")
    if ctx.refactor:
        core.copy_py(ctx.base, core.WORK / "a2dlc_base")
        core.rewrite_imports(core.WORK / "a2dlc_base", "a2dlc_base")
        core.install_import_shims()
    lemmas = [_lemma(i, spec, ctx) for i, spec in enumerate(budget.get("functions") or [])]
    resp = {"schema_version": 1, "lemmas": lemmas}
    (core.IO / "response.json").write_text(json.dumps(resp, ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


def _lemma(i: int, spec: dict, ctx: Context) -> dict:
    status, message, counterexample = _verdict(i, spec, ctx)
    out = {"name": spec["name"], "status": status, "message": message}
    if status == "fail":
        out["counterexample"] = counterexample
    return out


def _verdict(i: int, spec: dict, ctx: Context) -> tuple:
    """(status, message, counterexample) of one lemma."""
    name = spec["name"]
    found = _locate(ctx.cand, ctx.changed, name)
    if found is None:
        return "inconclusive", f"no top-level function named {name} in the candidate tree", None
    file, fn, module_names = found
    if isinstance(fn, ast.AsyncFunctionDef):
        return "inconclusive", "async function: not supported yet", None
    if not core.typed(fn):
        return "inconclusive", "no type annotations on the parameters: CrossHair has no domain", None
    post, why = _postcondition(fn, spec["postcondition"], module_names)
    if post is None:
        return "inconclusive", why, None
    (core.WORK / f"a2dlc_lemma_{i}.py").write_text(_wrapper("a2dlc_cand", core.module(file), fn, post),
                                                  encoding="utf-8")
    check = _check(f"a2dlc_lemma_{i}.{name}")
    if not ctx.refactor or check[0] == "fail":
        return check
    pair = _locate(ctx.base, ctx.changed, name)
    if pair is None:
        return "no_pair", f"the base has no lemma named {name}, so there is nothing to diff against " \
                           f"(postcondition: {check[1]})", None
    bfile, bfn, _ = pair
    if _signature(bfn) != _signature(fn):
        return "inconclusive", f"the base lemma {name} has signature {_signature(bfn)} and the candidate has " \
                               f"{_signature(fn)}: diffbehavior would compare different functions", None
    if not core.typed(bfn) or not core.typed(fn):
        diff = ("inconclusive", "no type annotations on the parameters: CrossHair has no domain", None)
    else:
        diff = _diff(f"a2dlc_base.{core.module(bfile)}.{name}", f"a2dlc_cand.{core.module(file)}.{name}")
    if diff[0] == "fail":
        return diff
    if check[0] == "inconclusive" or diff[0] == "inconclusive":
        return "inconclusive", f"postcondition: {check[1]}; diffbehavior: {diff[1]}", None
    return "pass", "postcondition confirmed and no difference from the base lemma", None


def _signature(fn: ast.FunctionDef | ast.AsyncFunctionDef) -> str:
    """Parameter list and return annotation, normalized by ast.unparse: two lemmas match only if these are equal."""
    return f"({ast.unparse(fn.args)})" + (f" -> {ast.unparse(fn.returns)}" if fn.returns is not None else "")


def _sources(root: Path, changed: list) -> list:
    """Candidate-relative .py paths: the changed files first, then the rest of the tree."""
    everything = sorted(p.relative_to(root).as_posix() for p in root.rglob("*.py")
                        if not set(p.relative_to(root).parts) & core.SKIP)
    ordered: list = []
    for file in [*changed, *everything]:
        if file.endswith(".py") and file not in ordered and (root / file).is_file():
            ordered.append(file)
    return ordered


def _locate(root: Path, changed: list, name: str):
    """(file, function node, top-level names) of the last top-level definition called name, or None."""
    for file in _sources(root, changed):
        try:
            tree = ast.parse(core.read_py(root / file))
        except (OSError, SyntaxError, ValueError, RecursionError):
            continue
        found = None
        for node in tree.body:
            if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)) and node.name == name:
                found = node
        if found is not None:
            return file, found, _top_level_names(tree)
    return None


def _top_level_names(tree: ast.Module) -> set:
    """Names bound at the top level of a module. "*" marks a star import, whose names are not known statically."""
    names: set = set()
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef)):
            names.add(node.name)
        elif isinstance(node, ast.ImportFrom) and any(a.name == "*" for a in node.names):
            names.add("*")
        elif isinstance(node, (ast.Import, ast.ImportFrom)):
            names.update((a.asname or a.name).split(".")[0] for a in node.names)
        elif isinstance(node, ast.Assign):
            names.update(t.id for t in node.targets if isinstance(t, ast.Name))
        elif isinstance(node, ast.AnnAssign) and isinstance(node.target, ast.Name):
            names.add(node.target.id)
    return names


def _params(fn: ast.FunctionDef | ast.AsyncFunctionDef) -> set:
    a = fn.args
    names = {x.arg for x in [*a.posonlyargs, *a.args, *a.kwonlyargs]}
    for extra in (a.vararg, a.kwarg):
        if extra is not None:
            names.add(extra.arg)
    return names


class _ResultToReturn(ast.NodeTransformer):
    """PEP 316 names the return value `_`; the declared postconditions say `result`."""

    def visit_Name(self, node: ast.Name) -> ast.Name:
        return ast.copy_location(ast.Name(id="_", ctx=node.ctx), node) if node.id == "result" else node


def _postcondition(fn: ast.FunctionDef | ast.AsyncFunctionDef, expr: str, module_names: set) -> tuple:
    """(PEP 316 text, "") or (None, why it cannot be checked)."""
    try:
        tree = ast.parse(expr.strip(), mode="eval")
    except SyntaxError as e:
        return None, f"postcondition does not parse: {e.msg}"
    params = _params(fn)
    if params & {"result", "_"}:
        return None, "an argument named result or _ clashes with the postcondition"
    stored = {n.id for n in ast.walk(tree) if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Store)}
    stored |= {a.arg for n in ast.walk(tree) if isinstance(n, ast.Lambda) for a in n.args.args}
    loaded = {n.id for n in ast.walk(tree) if isinstance(n, ast.Name) and isinstance(n.ctx, ast.Load)}
    free = loaded - params - stored - {"result"} - set(dir(builtins))
    # A star import supplies names the file does not show, so no free name can be excused: the lemma is then
    # inconclusive (never a false pass). Subtracting the known module names is safe only without it.
    if "*" not in module_names:
        free -= module_names
    if free:
        return None, f"postcondition uses names outside the arguments and the module: {', '.join(sorted(free))}"
    text = ast.unparse(ast.fix_missing_locations(_ResultToReturn().visit(tree)).body)
    if '"""' in text:
        return None, "postcondition cannot be written in a docstring"
    return text, ""


def _wrapper(package: str, mod: str, fn: ast.FunctionDef | ast.AsyncFunctionDef, post: str) -> str:
    """The module CrossHair checks: the whole candidate module namespace, then the lemma with its postcondition."""
    a = fn.args
    call = [x.arg for x in [*a.posonlyargs, *a.args]]
    if a.vararg is not None:
        call.append(f"*{a.vararg.arg}")
    call += [f"{x.arg}={x.arg}" for x in a.kwonlyargs]
    if a.kwarg is not None:
        call.append(f"**{a.kwarg.arg}")
    returns = f" -> {ast.unparse(fn.returns)}" if fn.returns is not None else ""
    return (
        f"import {package}.{mod} as a2dlc_module\n"
        "globals().update({k: v for k, v in vars(a2dlc_module).items() if not k.startswith('__')})\n"
        f"a2dlc_target = a2dlc_module.{fn.name}\n\n\n"
        f"def {fn.name}({ast.unparse(a)}){returns}:\n"
        f'    r"""\n    post: {post}\n    """\n'
        f"    return a2dlc_target({', '.join(call)})\n"
    )


def _counterexample(rest: str) -> dict:
    """The shape of a counterexample line; rest must already match CALLED."""
    m = CALLED.match(rest)
    if m["ret"] is not None:
        return {"args": m["args"], "returned": m["ret"]}
    return {"args": m["args"], "raised": m["msg"]}


def _check(target: str) -> tuple:
    limit = 4 * core.FLOOR_TIMEOUT_S + 60
    rc, out = core.run([sys.executable, "-m", "crosshair", "check", "--report_all", "--analysis_kind", "PEP316",
                        "--per_condition_timeout", str(core.FLOOR_TIMEOUT_S), target], limit)
    if rc is None:
        return "inconclusive", f"CrossHair exceeded {limit:.0f} s", None
    if SIDE_EFFECT in out:
        return "inconclusive", "side effects detected while running the lemma", None
    if rc == 1:
        # A counterexample is an error line in the "when calling f(args)" form. Any other error line (an internal or
        # setup error that exits with 1) is not a counterexample and must not reach the builder as one.
        for line in out.splitlines():
            m = ERROR.search(line)
            if m is not None and CALLED.match(m["rest"]) is not None:
                return "fail", "CrossHair found an input for which the lemma breaks its postcondition", \
                    _counterexample(m["rest"])
        raw = next((line for line in out.splitlines() if ERROR.search(line)), "no error line")
        return "inconclusive", f"CrossHair exited with 1 without a counterexample: {raw}", None
    if rc == 0 and CONFIRMED in out:
        return "pass", "postcondition confirmed over all paths", None
    if rc == 0 and NO_CHECKABLE in out:
        return "inconclusive", "CrossHair found nothing checkable in the generated wrapper", None
    if rc == 0:
        return "inconclusive", f"CrossHair did not confirm the postcondition within {core.FLOOR_TIMEOUT_S} s", None
    last = (out.strip().splitlines() or ["no output"])[-1]
    return "inconclusive", f"CrossHair did not conclude (rc={rc}): {last}", None


def _diff(base_target: str, cand_target: str) -> tuple:
    limit = 4 * core.FLOOR_TIMEOUT_S + 60
    rc, out = core.run([sys.executable, "-m", "crosshair", "diffbehavior", "--per_condition_timeout",
                        str(core.FLOOR_TIMEOUT_S), base_target, cand_target], limit)
    m_dif = core.DIFFERENCE.search(out)
    if m_dif is not None:
        counterexample = {"args": m_dif["args"], "old": m_dif["old"].strip(), "new": m_dif["new"].strip()}
        return "fail", "the candidate's lemma diverges from the base's lemma", counterexample
    if core.WITHOUT_DIFFERENCE.search(out) is not None:
        return "pass", "no difference from the base lemma", None
    if rc is None:
        return "inconclusive", f"CrossHair exceeded {limit:.0f} s", None
    last = (out.strip().splitlines() or ["no output"])[-1]
    return "inconclusive", f"CrossHair did not conclude (rc={rc}): {last}", None
