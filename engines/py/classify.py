"""Operation classify_lines: which unexamined changed lines are defensive exception handlers or declaration-only code.

The core hands the engine the changed lines that no engine target examined and no test executed (budget.lines, file ->
lines of the candidate). The engine answers, per file, the subset that falls in one of two categories; the core records
them as notes in the receipt and does not let them turn the verdict INCONCLUSIVE by themselves. Every other line stays
unexamined and blocks as before. When in doubt a line is NOT classified: this module only ever lowers what blocks, so each
rule below is as narrow as the case it was written for (docs/COVERAGE_NOTES.md).

defensive_handler: an `except` clause that catches broadly (bare, Exception or BaseException, alone or in a tuple) and whose
whole body is inert: pass, `...`, a docstring, continue, break, a bare `raise`, assignments of inert values, `return` of an
inert value, and one-line logging calls. The header and every line of the body. One statement that does anything else (a
call that is not logging, an `if`, a loop, `raise Something(...)`, a nested try) makes the handler real logic, and none of
its lines is classified.

declarative_only: imports; `__all__ = [...]` of string literals; an `if TYPE_CHECKING:` block holding only imports; a class
skeleton, that is a class whose body holds only a docstring, `pass`/`...`, annotated or plain assignments of inert values,
nested skeletons and stub methods (a body of only a docstring, `pass`, `...` or `raise NotImplementedError`), with
decorators from a short list (runtime_checkable, abstractmethod, dataclass, ...). A Protocol or ABC with real code in one
method is not a skeleton.

An inert value is a literal, a name, an attribute chain, a unary minus or `not`, or a tuple, list, set, dict or f-string of
inert values. A call is never inert, except str(), repr() and type() inside the arguments of a logging call.
"""
from __future__ import annotations

import ast

DEFENSIVE = "defensive_handler"
DECLARATIVE = "declarative_only"

_BROAD = {"Exception", "BaseException"}
_LOG_METHODS = {"debug", "info", "warning", "warn", "error", "exception", "critical", "fatal", "log"}
_ARG_CALLS = {"str", "repr", "type"}
_DECORATORS = {"runtime_checkable", "abstractmethod", "abstractproperty", "dataclass", "final", "overload", "override",
               "property", "staticmethod", "classmethod", "total_ordering"}


def _inert(n: ast.AST | None, calls: bool = False) -> bool:
    """A value that computes nothing: no call (unless calls and it is str/repr/type), no subscript, no comprehension."""
    if n is None or isinstance(n, (ast.Constant, ast.Name)):
        return True
    if isinstance(n, ast.Attribute):
        return _inert(n.value)
    if isinstance(n, ast.UnaryOp):
        return isinstance(n.op, (ast.USub, ast.Not)) and _inert(n.operand, calls)
    if isinstance(n, (ast.Tuple, ast.List, ast.Set)):
        return all(_inert(e, calls) for e in n.elts)
    if isinstance(n, ast.Dict):
        return all(k is not None and _inert(k, calls) for k in n.keys) and all(_inert(v, calls) for v in n.values)
    if isinstance(n, ast.JoinedStr):
        return all(_inert(v, calls) for v in n.values)
    if isinstance(n, ast.FormattedValue):
        return _inert(n.value, calls)
    if isinstance(n, ast.BinOp):
        return isinstance(n.op, (ast.Add, ast.Mod)) and _inert(n.left, calls) and _inert(n.right, calls)
    if isinstance(n, ast.Call) and calls:
        return (isinstance(n.func, ast.Name) and n.func.id in _ARG_CALLS
                and all(_inert(a, calls) for a in n.args) and all(_inert(k.value, calls) for k in n.keywords))
    return False


def _last_name(n: ast.AST) -> str:
    if isinstance(n, ast.Name):
        return n.id
    if isinstance(n, ast.Attribute):
        return n.attr
    return ""


def _is_logging(s: ast.stmt) -> bool:
    """`logger.warning("...", exc)`: a call on something named like a logger, with inert arguments."""
    if not (isinstance(s, ast.Expr) and isinstance(s.value, ast.Call) and isinstance(s.value.func, ast.Attribute)):
        return False
    c = s.value
    receiver = _last_name(c.func.value).lower()
    if c.func.attr not in _LOG_METHODS or "log" not in receiver:
        return False
    return all(_inert(a, True) for a in c.args) and all(_inert(k.value, True) for k in c.keywords)


def _assign_target(t: ast.AST) -> bool:
    return isinstance(t, ast.Name) or isinstance(t, ast.Attribute) and _inert(t.value)


def _inert_in_handler(s: ast.stmt) -> bool:
    if isinstance(s, (ast.Pass, ast.Continue, ast.Break)):
        return True
    if isinstance(s, ast.Expr):
        return isinstance(s.value, ast.Constant) or _is_logging(s)
    if isinstance(s, ast.Raise):
        return s.exc is None and s.cause is None
    if isinstance(s, ast.Return):
        return _inert(s.value)
    if isinstance(s, ast.Assign):
        return all(_assign_target(t) for t in s.targets) and _inert(s.value)
    if isinstance(s, ast.AnnAssign):
        return _assign_target(s.target) and _inert(s.value)
    return False


def _broad(t: ast.expr | None) -> bool:
    if t is None:
        return True
    if isinstance(t, ast.Tuple):
        return any(_broad(e) for e in t.elts)
    return isinstance(t, ast.Name) and t.id in _BROAD


def _defensive(h: ast.ExceptHandler) -> bool:
    return _broad(h.type) and bool(h.body) and all(_inert_in_handler(s) for s in h.body)


def _safe_decorator(d: ast.expr) -> bool:
    if isinstance(d, ast.Call):
        return (_last_name(d.func) in _DECORATORS and all(_inert(a) for a in d.args)
                and all(_inert(k.value) for k in d.keywords))
    return _last_name(d) in _DECORATORS


def _stub_body(body: list[ast.stmt]) -> bool:
    for s in body:
        if isinstance(s, ast.Pass) or isinstance(s, ast.Expr) and isinstance(s.value, ast.Constant):
            continue
        if isinstance(s, ast.Raise) and s.cause is None and s.exc is not None:
            e = s.exc.func if isinstance(s.exc, ast.Call) else s.exc
            if isinstance(e, ast.Name) and e.id == "NotImplementedError" and (
                    not isinstance(s.exc, ast.Call) or all(_inert(a) for a in s.exc.args)):
                continue
        return False
    return True


def _stub_function(f: ast.FunctionDef | ast.AsyncFunctionDef) -> bool:
    a = f.args
    defaults = [*a.defaults, *(d for d in a.kw_defaults if d is not None)]
    return all(_safe_decorator(d) for d in f.decorator_list) and all(_inert(d) for d in defaults) and _stub_body(f.body)


def _type_param(n: ast.expr) -> bool:
    """Bases like Protocol, Protocol[T], Generic[T], metaclass=ABCMeta."""
    if isinstance(n, ast.Subscript):
        return _type_param(n.value) and _type_param(n.slice)
    if isinstance(n, ast.Tuple):
        return all(_type_param(e) for e in n.elts)
    return _inert(n)


def _skeleton(c: ast.ClassDef) -> bool:
    if not (all(_safe_decorator(d) for d in c.decorator_list) and all(_type_param(b) for b in c.bases)
            and all(_type_param(k.value) for k in c.keywords)):
        return False
    for s in c.body:
        if isinstance(s, (ast.Pass, ast.Expr)) and (isinstance(s, ast.Pass) or isinstance(s.value, ast.Constant)):
            continue
        if isinstance(s, ast.AnnAssign) and isinstance(s.target, ast.Name) and _inert(s.value):
            continue
        if isinstance(s, ast.Assign) and all(isinstance(t, ast.Name) for t in s.targets) and _inert(s.value):
            continue
        if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef)) and _stub_function(s):
            continue
        if isinstance(s, ast.ClassDef) and _skeleton(s):
            continue
        return False
    return True


def _type_checking(test: ast.expr) -> bool:
    return _last_name(test) == "TYPE_CHECKING"


def _span(n: ast.stmt) -> range:
    first = min([n.lineno, *(d.lineno for d in getattr(n, "decorator_list", []))])
    return range(first, n.end_lineno + 1)


def _is_all(s: ast.stmt) -> bool:
    if isinstance(s, ast.Assign):
        names, value = s.targets, s.value
    elif isinstance(s, ast.AnnAssign):
        names, value = [s.target], s.value
    else:
        return False
    return (all(isinstance(t, ast.Name) and t.id == "__all__" for t in names)
            and isinstance(value, (ast.List, ast.Tuple))
            and all(isinstance(e, ast.Constant) and isinstance(e.value, str) for e in value.elts))


def categories(tree: ast.AST) -> dict[int, str]:
    """Line -> category for every line of the module that falls in one of the two."""
    out: dict[int, str] = {}
    for n in ast.walk(tree):
        if isinstance(n, ast.ExceptHandler) and _defensive(n):
            for line in range(n.lineno, n.end_lineno + 1):
                out[line] = DEFENSIVE
        elif isinstance(n, (ast.Import, ast.ImportFrom)):
            out.update({line: DECLARATIVE for line in _span(n)})
        elif isinstance(n, ast.ClassDef) and _skeleton(n):
            out.update({line: DECLARATIVE for line in _span(n)})
        elif isinstance(n, ast.If) and _type_checking(n.test) and not n.orelse and all(
                isinstance(s, (ast.Import, ast.ImportFrom, ast.Pass)) for s in n.body):
            out.update({line: DECLARATIVE for line in _span(n)})
        elif isinstance(n, (ast.Assign, ast.AnnAssign)) and _is_all(n):
            out.update({line: DECLARATIVE for line in _span(n)})
    return out


def classify(text: str, lines: list[int]) -> list[dict]:
    """The notes for `lines` of a Python source, one per category, sorted. A source that does not parse has none."""
    try:
        by_line = categories(ast.parse(text))
    except (SyntaxError, ValueError, RecursionError):
        return []
    notes: dict[str, list[int]] = {}
    for line in sorted(set(lines)):
        if line in by_line:
            notes.setdefault(by_line[line], []).append(line)
    return [{"category": c, "lines": notes[c]} for c in sorted(notes)]
