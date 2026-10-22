"""Operation capture_traces (Stage 1, refactor and legacy): the I/O and the branches of the base's changed functions.

The base tree is copied, and its tests run in a child process (this file, run with --harness) under sys.settrace. For
each call of a changed function the harness records the input (positional, *args, keyword-only and **kwargs, as
repr), the output (repr, or "raise <Type>" when the call exits by an exception) and the branches the call took: the
arcs that leave a decision line. Decisions are the if, for and while statements of the base function itself.

branch_coverage is the fraction of decision outcomes reached by the traces: each decision has two outcomes, and an
outcome is reached when some call leaves the decision towards its successor line. The denominator covers only the
changed functions, not the whole legacy area; the core decides the threshold. It is omitted when the changed functions
have no decision at all.

One-line decisions (`if x: return 1`, or a while or for whose body starts on the decision line) emit no line event for
their body, so the line arcs cannot tell their outcomes apart. They are traced with sys.monitoring instruction events
instead (Python 3.12 or later; the engine image is 3.12): the conditional jumps of such a line are watched, and the
instruction that runs right after each one says which side was taken. The body side is recorded as the arc from the
line to itself; the other side as the arc to the line of the first statement after the decision. Each watched jump
of a line counts, so a compound test on one line (`if a and b: ...`) may credit a true outcome when only its first
operand held.

The test runner is pytest when the engine image has it, otherwise unittest (a2dlc-engine-py has no pytest). unittest
finds tests only in packages (directories with __init__.py); a run that finds no test is an error, never a pass.
Standard library only: nothing is installed in the engine image.

Limitations: the trace is per call of a top-level function or of a method of a top-level class; exception edges,
comprehensions and short-circuit operators are not decisions. budget.timeout_s is read here as the contract defines it
(engine-request capture_traces: a number above 0, default 300 s).
"""
import ast
import dis
import importlib.util
import inspect
import io as iolib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

from graph import SKIP, read_source

IO = Path("/io")
TIMEOUT_S = 300
REPR_LIMIT = 300
MONITOR_TOOL = 4  # a sys.monitoring tool id no other tracer of the harness uses
NO_TESTS = "no tests discovered: unittest finds tests only in packages (directories with __init__.py) of the base"
PYTEST_NO_TESTS = "pytest collected no tests"
PYTEST_NO_TESTS_EXIT = 5  # pytest's exit code for "no tests collected"
ADDRESS = re.compile(r"0x[0-9a-fA-F]+")


def function_id(file: str, qualname: str) -> str:
    return f"{file}::{qualname}"


def safe_repr(value: object) -> str:
    """repr without memory addresses (they change between runs) and bounded in length."""
    try:
        text = repr(value)
    except Exception:  # a repr that raises must not stop the run
        text = f"<unrepr {type(value).__name__}>"
    text = ADDRESS.sub("0x?", text)
    return text if len(text) <= REPR_LIMIT else text[:REPR_LIMIT] + "..."


def functions_of(tree: ast.Module) -> dict[str, ast.AST]:
    """Top-level functions and methods of top-level classes, by qualified name ('Class.method')."""
    out: dict[str, ast.AST] = {}
    for node in tree.body:
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
            out[node.name] = node
        elif isinstance(node, ast.ClassDef):
            for item in node.body:
                if isinstance(item, (ast.FunctionDef, ast.AsyncFunctionDef)):
                    out[f"{node.name}.{item.name}"] = item
    return out


def decision_nodes(fn: ast.AST) -> list[ast.stmt]:
    """The if, for and while statements of fn itself, not of the functions or classes nested in it."""
    found: list[ast.stmt] = []
    stack = list(fn.body)
    while stack:
        node = stack.pop()
        if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef, ast.ClassDef, ast.Lambda)):
            continue
        if isinstance(node, (ast.If, ast.For, ast.AsyncFor, ast.While)):
            found.append(node)
        stack.extend(ast.iter_child_nodes(node))
    return found


def decisions_of(fn: ast.AST) -> list[int]:
    """Lines of the if, for and while statements of fn itself."""
    return sorted({node.lineno for node in decision_nodes(fn)})


def inline_decisions_of(fn: ast.AST) -> list[int]:
    """Lines of the decisions whose body starts on the decision line (`if x: return 1`)."""
    return sorted({node.lineno for node in decision_nodes(fn) if node.body[0].lineno == node.lineno})


def changed_targets(base_text: str, cand_text: str | None, rel: str) -> list[dict]:
    """Base functions whose AST differs from the candidate's, or that the candidate no longer has."""
    base = functions_of(ast.parse(base_text, filename=rel))
    try:
        cand = functions_of(ast.parse(cand_text, filename=rel)) if cand_text is not None else {}
    except (SyntaxError, ValueError, RecursionError):
        cand = {}  # an unparsable candidate counts every base function as changed: the safe side
    return [
        {"file": rel, "qualname": qual, "decisions": decisions_of(node), "inline": inline_decisions_of(node)}
        for qual, node in sorted(base.items())
        if qual not in cand or ast.dump(cand[qual]) != ast.dump(node)
    ]


def targets_of(base: Path, cand: Path, changed: list[str]) -> tuple[list[dict], list[dict]]:
    """(targets, skipped). A changed file that is new in the candidate has no base behaviour and no target."""
    targets: list[dict] = []
    skipped: list[dict] = []
    for rel in sorted(set(changed)):
        if not rel.endswith(".py") or not (base / rel).is_file():
            continue
        try:
            cand_text = read_source(cand / rel) if (cand / rel).is_file() else None
            targets += changed_targets(read_source(base / rel), cand_text, rel)
        except (OSError, SyntaxError, ValueError, RecursionError) as e:
            skipped.append({"file": rel, "reason": f"not parsed ({type(e).__name__})"})
    return targets, skipped


def assemble(out: dict, targets: list[dict]) -> tuple[list[dict], float | None]:
    """Traces in a fixed order (duplicates removed) and the branch coverage of the changed functions."""
    unique = {(c["function"], c["input"], c["output"], tuple(c["branch_ids"])) for c in out["calls"]}
    traces = [{"function": f, "input": i, "output": o, "branch_ids": list(b)} for f, i, o, b in sorted(unique)]
    successors: dict[tuple[str, int], set[int]] = {}
    for fid, source, target in out["arcs"]:
        successors.setdefault((fid, source), set()).add(target)
    covered = total = 0
    for t in targets:
        fid = function_id(t["file"], t["qualname"])
        for line in t["decisions"]:
            total += 2
            covered += min(2, len(successors.get((fid, line), set())))
    return traces, (covered / total if total else None)


def run_tests(base: Path, targets: list[dict], timeout_s: float) -> dict:
    """Copies the base, runs its tests under the harness and returns the response fields for the traces."""
    runner = "pytest" if importlib.util.find_spec("pytest") else "unittest"
    work = Path(tempfile.mkdtemp(prefix="a2dlc-traces-"))
    try:
        tree = work / "tree"
        shutil.copytree(base, tree, ignore=shutil.ignore_patterns(*SKIP))
        spec = {
            "root": str(tree),
            "runner": runner,
            "out": str(work / "out.json"),
            "targets": [
                {"qualname": t["qualname"], "decisions": t["decisions"], "inline": t["inline"],
                 "path": str(tree / t["file"]), "file": t["file"]}
                for t in targets
            ],
        }
        spec_path = work / "spec.json"
        spec_path.write_text(json.dumps(spec, ensure_ascii=False), encoding="utf-8")
        env = {"PATH": "/usr/local/bin:/usr/bin:/bin", "HOME": str(work), "PYTHONDONTWRITEBYTECODE": "1",
               "PYTHONHASHSEED": "0", "PYTHONIOENCODING": "utf-8"}
        try:
            proc = subprocess.run([sys.executable, str(Path(__file__).resolve()), "--harness", str(spec_path)],
                                  cwd=tree, env=env, capture_output=True, timeout=timeout_s)
        except subprocess.TimeoutExpired:
            return {"runner": runner, "error": f"tests did not finish in {timeout_s:g} s"}
        out_path = work / "out.json"
        if not out_path.exists():
            lines = proc.stderr.decode("utf-8", "replace").strip().splitlines()
            last = lines[-1] if lines else ""
            return {"runner": runner, "error": f"harness wrote no output (exit {proc.returncode}): {last}"}
        out = json.loads(out_path.read_text(encoding="utf-8"))
        error = out.get("error")
        # a run that did not test anything reports no traces and no coverage: nothing was measured
        traces, coverage = ([], None) if error else assemble(out, targets)
        result: dict = {"runner": runner, "test_exit": out["exit"], "traces": traces, "error": error}
        if coverage is not None:
            result["branch_coverage"] = coverage
        return result
    finally:
        shutil.rmtree(work, ignore_errors=True)


def capture_traces(req: dict, io: Path = IO) -> int:
    """Writes engine-traces-response to io/response.json. Its traces are the capture.schema traces items.

    budget.timeout_s is read here as the contract defines it (see the module docstring).
    """
    budget = req.get("budget") or {}
    timeout_s = float(budget.get("timeout_s", TIMEOUT_S))
    base, cand = Path(req["base_dir"]), Path(req["candidate_dir"])
    targets, skipped = targets_of(base, cand, req["changed_files"])
    response: dict = {"schema_version": 1, "runner": None, "test_exit": None, "traces": [], "skipped": skipped,
                      "error": None}
    if targets:
        response.update(run_tests(base, targets, timeout_s))
    (io / "response.json").write_text(json.dumps(response, ensure_ascii=False, indent=1), encoding="utf-8")
    return 0


# Harness: runs in the child process started by run_tests. Everything below is the trace hook.

def call_input(frame) -> str:
    """The arguments of a call as the signature orders them: positional, *args, keyword-only, **kwargs."""
    code, local = frame.f_code, frame.f_locals
    names = code.co_varnames

    def value(name: str) -> str:
        return safe_repr(local[name]) if name in local else "<unbound>"

    positional = names[: code.co_argcount]
    kwonly = names[code.co_argcount: code.co_argcount + code.co_kwonlyargcount]
    index = code.co_argcount + code.co_kwonlyargcount
    varargs = names[index] if code.co_flags & inspect.CO_VARARGS else None
    if varargs:
        index += 1
    varkw = names[index] if code.co_flags & inspect.CO_VARKEYWORDS else None
    parts = [value(n) for n in positional]
    if varargs:
        parts.append("*" + value(varargs))
    parts += [f"{n}={value(n)}" for n in kwonly]
    if varkw:
        parts.append("**" + value(varkw))
    return "(" + ", ".join(parts) + ")"


def is_conditional(opname: str) -> bool:
    """A jump that decides an if, while or for: the loop's FOR_ITER or a POP_JUMP_IF_* (any variant)."""
    return opname == "FOR_ITER" or (opname.startswith("POP_JUMP") and "_IF_" in opname)


def inline_watch(code, inline: set[int]) -> dict[int, tuple[int, int | None, int | None, int | None]]:
    """The conditional jumps of the one-line decisions of code, by offset.

    Each maps to (decision line, body offset, exit offset, exit line). The body is the successor on the side where the
    decision runs its body: the jump target after a jump on a true test (POP_JUMP_IF_TRUE, POP_JUMP_IF_NONE), the next
    instruction otherwise (a for loop enters its body on the fall-through). The exit line is the first line after the
    exit offset that is not the decision line: a for loop exits to END_FOR, which carries the decision line.
    """
    instrs = list(dis.get_instructions(code))
    position = {ins.offset: i for i, ins in enumerate(instrs)}
    lines: list[int | None] = []
    current = None
    for ins in instrs:
        if ins.starts_line is not None:
            current = ins.starts_line
        lines.append(current)
    watch: dict[int, tuple[int, int | None, int | None, int | None]] = {}
    for i, ins in enumerate(instrs):
        line = lines[i]
        if line is None or line not in inline or not is_conditional(ins.opname):
            continue
        fall = instrs[i + 1].offset if i + 1 < len(instrs) else None
        target = ins.argval if isinstance(ins.argval, int) else None
        taken_is_body = "IF_TRUE" in ins.opname or ("IF_NONE" in ins.opname and "NOT_NONE" not in ins.opname)
        body, exit_ = (target, fall) if taken_is_body else (fall, target)
        if exit_ in position and instrs[position[exit_]].opname == "END_FOR":
            # a for loop exhausted by FOR_ITER jumps over its END_FOR: the next instruction runs
            exit_ = instrs[position[exit_] + 1].offset if position[exit_] + 1 < len(instrs) else None
        exit_line = None
        if exit_ is not None and exit_ in position:
            for k in range(position[exit_], len(instrs)):
                if lines[k] != line:
                    exit_line = lines[k]
                    break
        watch[ins.offset] = (line, body, exit_, exit_line)
    return watch


class Tracer:
    """sys.settrace hook: follows the calls of the target functions and the arcs their decisions take.

    The one-line decisions are followed by sys.monitoring instruction events instead (see _instruction).
    """

    def __init__(self, targets: list[dict]):
        self.targets = {
            (os.path.realpath(t["path"]), t["qualname"]):
                (function_id(t["file"], t["qualname"]), set(t["decisions"]), set(t.get("inline", [])))
            for t in targets
        }
        self.seen: dict = {}
        self.watches: dict = {}
        self.states: dict = {}  # frame -> state, for the frames that have one-line decisions
        self.monitored: list = []
        self.pending = None  # (state, line, body, exit, exit_line) of the watched jump that ran last
        self.calls: list[dict] = []
        self.arcs: set[tuple[str, int, int]] = set()

    def _target(self, code):
        if code not in self.seen:
            qualname = getattr(code, "co_qualname", code.co_name)
            self.seen[code] = self.targets.get((os.path.realpath(code.co_filename), qualname))
        return self.seen[code]

    def _watch(self, code, inline: set[int]) -> dict:
        if code not in self.watches:
            self.watches[code] = inline_watch(code, inline) if inline else {}
        return self.watches[code]

    def global_trace(self, frame, event, arg):
        if event != "call":
            return None
        target = self._target(frame.f_code)
        if target is None:
            return None
        watch = self._watch(frame.f_code, target[2])
        state = {"target": target, "code": frame.f_code, "input": call_input(frame), "prev": None,
                 "raised": None, "branches": set()}
        if watch:
            self.states[frame] = state
            self._monitor(frame.f_code)

        def local(frame, event, arg):
            self._local(state, frame, event, arg)
            return local

        return local

    def _monitor(self, code) -> None:
        if code not in self.monitored:
            sys.monitoring.set_local_events(MONITOR_TOOL, code, sys.monitoring.events.INSTRUCTION)
            self.monitored.append(code)

    def attach(self) -> None:
        """Registers the instruction callback. Needs sys.monitoring (Python 3.12 or later)."""
        sys.monitoring.use_tool_id(MONITOR_TOOL, "a2dlc-traces")
        sys.monitoring.register_callback(MONITOR_TOOL, sys.monitoring.events.INSTRUCTION, self._instruction)

    def detach(self) -> None:
        for code in self.monitored:
            sys.monitoring.set_local_events(MONITOR_TOOL, code, 0)
        sys.monitoring.register_callback(MONITOR_TOOL, sys.monitoring.events.INSTRUCTION, None)
        sys.monitoring.free_tool_id(MONITOR_TOOL)

    def _arc(self, state: dict, source: int, target: int) -> None:
        self.arcs.add((state["target"][0], source, target))
        state["branches"].add(f"{source}->{target}")

    def _instruction(self, code, offset):
        """Instruction event of a watched code. Nothing runs between a watched jump and the instruction after it, so
        the next instruction event is that successor, and it tells which side of the one-line decision was taken."""
        pending = self.pending
        if pending is not None:
            state, line, body, exit_, exit_line = pending
            self.pending = None
            if code is state["code"]:
                if offset == body:
                    self._arc(state, line, line)
                elif offset == exit_ and exit_line is not None:
                    self._arc(state, line, exit_line)
        jump = self.watches.get(code, {}).get(offset)
        if jump is not None:
            state = self.states.get(sys._getframe(1))  # the frame that executes the jump
            if state is not None:
                self.pending = (state, *jump)

    def _local(self, state: dict, frame, event: str, arg) -> None:
        fid, decisions, inline = state["target"]
        if event == "line":
            line = frame.f_lineno
            prev = state["prev"]
            # the arcs of a one-line decision come from instruction events: its line events cannot tell them apart
            if prev is not None and prev in decisions and prev not in inline:
                self._arc(state, prev, line)
            state["prev"] = line
            state["raised"] = None  # a caught exception does not leak into the output of the call
        elif event == "exception":
            state["raised"] = arg[0].__name__
        elif event == "return":
            self.states.pop(frame, None)
            output = f"raise {state['raised']}" if state["raised"] else safe_repr(arg)
            self.calls.append({"function": fid, "input": state["input"], "output": output,
                               "branch_ids": sorted(state["branches"])})


def harness(spec_path: str) -> int:
    spec = json.loads(Path(spec_path).read_text(encoding="utf-8"))
    os.chdir(spec["root"])
    sys.path.insert(0, spec["root"])
    tracer = Tracer(spec["targets"])
    error = None
    attached = False
    if any(t.get("inline") for t in spec["targets"]):
        if hasattr(sys, "monitoring"):
            tracer.attach()
            attached = True
        else:
            error = "one-line decisions need sys.monitoring (Python 3.12 or later)"
    sys.settrace(tracer.global_trace)
    try:
        if spec["runner"] == "pytest":
            import pytest
            exit_code = int(pytest.main(["-q", "-p", "no:cacheprovider", "--tb=no"]))
            if exit_code == PYTEST_NO_TESTS_EXIT:
                error = PYTEST_NO_TESTS
        else:
            import unittest
            suite = unittest.defaultTestLoader.discover(".", top_level_dir=".")
            result = unittest.TextTestRunner(stream=iolib.StringIO(), verbosity=0).run(suite)
            exit_code = 0 if result.wasSuccessful() else 1
            if result.testsRun == 0:  # a pass with nothing run would hide a base that was never tested
                exit_code = 1
                error = NO_TESTS
    finally:
        sys.settrace(None)
        if attached:
            tracer.detach()
    out = {"runner": spec["runner"], "exit": exit_code, "error": error, "calls": tracer.calls,
           "arcs": sorted([fid, a, b] for fid, a, b in tracer.arcs)}
    Path(spec["out"]).write_text(json.dumps(out, ensure_ascii=False, sort_keys=True), encoding="utf-8")
    return 0


if __name__ == "__main__":
    if len(sys.argv) == 3 and sys.argv[1] == "--harness":
        sys.exit(harness(sys.argv[2]))
    sys.stderr.write("usage: traces.py --harness <spec.json> (the engine calls capture_traces)\n")
    sys.exit(2)
