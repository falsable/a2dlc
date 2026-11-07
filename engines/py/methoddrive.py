"""Plain functions that compare one method of a class between the base and the candidate (engine.py, compare).

CrossHair calls plain functions and gives both sides the same arguments. A method needs an instance, and handing both
sides one instance of the base class is not a comparison: the candidate's method would read the base's class attributes,
the base's constructor and the base's other methods (measured: a changed class attribute gave PASS, a method extracted
into a new helper gave FAIL). So `pair(base_cls, cand_cls, name)` returns `old` and `new` taking the constructor's
arguments and then the method's: each side builds its OWN class from the same constructor arguments, calls the method
and returns the result together with a plain snapshot of the instance, so a method that only mutates `self` is
compared too. A constructor target (`__init__`) returns the snapshot of the built object.

When the instance cannot be built from arguments CrossHair can generate, nothing is guessed (a stub instance could hide
a difference): `Unsupported` is raised with the reason, which the engine reads from stderr as INCONCLUSIVE.

Stats go through asyncdrive.sync_of, so the engine refuses a PASS in which no call completed (a constructor that
always raises, a method that always raises). This file is copied into /tmp/a2dlc as a2dlc_methoddrive.py.
"""
import inspect
import sys
import types
import typing

try:
    import a2dlc_asyncdrive as asyncdrive  # the name the engine copies it under
except ImportError:  # imported from engines/py (unit tests)
    import asyncdrive

PREFIX = "A2DLC_METHOD_UNSUPPORTED: "
CTOR = "self_"  # constructor parameters in the wrapper's signature, so they cannot collide with the method's
DEPTH = 6
P = inspect.Parameter


class Unsupported(Exception):
    """The two versions cannot be compared through an instance built from generated arguments."""


def _plain(t) -> bool:
    """A builtin or typing type, which CrossHair can build; Literal is not (CrossHair fails on it)."""
    if isinstance(t, list):  # the argument list of a Callable
        return all(_plain(a) for a in t)
    if t is typing.Any or t is None or t is type(None):
        return True
    origin = typing.get_origin(t)
    if origin is typing.Literal:
        return False
    if origin is not None:
        ok = origin is getattr(types, "UnionType", None) or getattr(origin, "__module__", "") in ("builtins", "typing", "collections.abc")
        return ok and all(_plain(a) for a in typing.get_args(t) if a is not Ellipsis)
    return getattr(t, "__module__", None) in ("builtins", "typing", "collections.abc")


def _norm(t) -> str:
    return str(t).replace("a2dlc_base.", "").replace("a2dlc_cand.", "")


def _hints(fn) -> dict:
    try:
        return typing.get_type_hints(getattr(fn, "__func__", fn))
    except Exception as e:  # noqa: BLE001 - an unresolvable forward reference, a missing name
        raise Unsupported(f"the annotations of {getattr(fn, '__qualname__', fn)} cannot be resolved ({type(e).__name__})")


def snap(x, depth: int = 0, seen: tuple = ()):
    """The state of x as plain data: equal for equal state whatever the class object is (the two sides' classes differ)."""
    if x is None or isinstance(x, (bool, int, float, complex, str, bytes)):
        return x
    if id(x) in seen:
        return "<cycle>"
    if depth > DEPTH:
        return f"<{type(x).__qualname__}>"
    seen, depth = (*seen, id(x)), depth + 1
    if isinstance(x, (list, tuple)):
        return (type(x).__name__, [snap(v, depth, seen) for v in x])
    if isinstance(x, (set, frozenset)):
        return (type(x).__name__, sorted((snap(v, depth, seen) for v in x), key=repr))
    if isinstance(x, dict):
        return ("dict", sorted(((snap(k, depth, seen), snap(v, depth, seen)) for k, v in x.items()), key=repr))
    try:
        state = dict(vars(x))
    except TypeError:
        slots = [s for c in type(x).__mro__ for s in getattr(c, "__slots__", ())]
        state = {s: getattr(x, s) for s in slots if hasattr(x, s)}
    if state or hasattr(x, "__dict__"):
        return (type(x).__qualname__, snap(state, depth, seen))
    # shortcut: an object with no visible attributes is compared by its repr; an address in it makes the versions differ
    return (type(x).__qualname__, repr(x))


def _ctor(cls) -> list:
    """[(name, type)] of the constructor, or Unsupported if the instance cannot be built from generated arguments."""
    if cls.__init__ is object.__init__ and cls.__new__ is object.__new__:
        return []
    try:
        sig = inspect.signature(cls)
    except (TypeError, ValueError) as e:
        raise Unsupported(f"constructor not derivable: {cls.__name__} has no signature ({e})")
    hints = _hints(cls.__init__)
    out, bare, opaque = [], [], []
    for p in sig.parameters.values():
        if p.kind in (P.VAR_POSITIONAL, P.VAR_KEYWORD):
            raise Unsupported(f"constructor not derivable: {cls.__name__} takes *{p.name}, there is nothing to build it from")
        if p.kind is P.POSITIONAL_ONLY:
            raise Unsupported(f"constructor not derivable: parameter {p.name} of {cls.__name__} is positional-only")
        t = hints.get(p.name)
        if p.name not in hints:
            bare.append(p.name)
        elif not _plain(t):
            opaque.append(f"{p.name}: {getattr(t, '__name__', None) or _norm(t)}")
        out.append((p.name, t))
    if bare:
        raise Unsupported(f"constructor not derivable: {cls.__name__} has parameters without annotation ({', '.join(bare)}): "
                          "CrossHair has no domain")
    if opaque:
        raise Unsupported(f"constructor not derivable: {cls.__name__}({', '.join(opaque)}) takes something that is not a "
                          "builtin or typing type, CrossHair cannot build it and a stub could hide a difference")
    return out


def _attr(cls, name: str) -> str:
    """The attribute a method is stored under: a `__private` name is mangled with the class name."""
    return f"_{cls.__name__.lstrip('_')}{name}" if name.startswith("__") and not name.endswith("__") else name


def _kind(cls, name: str) -> str:
    if name == "__init__":
        return "init"
    raw = inspect.getattr_static(cls, _attr(cls, name))
    if isinstance(raw, staticmethod):
        return "static"
    if isinstance(raw, classmethod):
        return "class"
    if isinstance(raw, property) or type(raw).__name__ == "cached_property":
        return "property"
    return "instance"


def pair(base_cls, cand_cls, name: str):
    """(old, new): functions of the constructor's arguments (prefixed with CTOR) followed by the method's own."""
    try:
        return _pair(base_cls, cand_cls, name)
    except Unsupported as e:
        sys.stderr.write(PREFIX + str(e)[:280].replace("\n", " ") + "\n")
        raise


def _pair(base_cls, cand_cls, name: str):
    kind = _kind(base_cls, name)
    if _kind(cand_cls, name) != kind:
        raise Unsupported(f"{base_cls.__name__}.{name} is a {kind} method in the base and another kind in the candidate")
    stateless = kind in ("static", "class")
    ctor = [] if stateless else _ctor(base_cls)
    if not stateless and [(n, _norm(t)) for n, t in ctor] != [(n, _norm(t)) for n, t in _ctor(cand_cls)]:
        raise Unsupported(f"the constructor of {base_cls.__name__} changed its parameters: both sides cannot be built from "
                          "the same arguments")
    mparams: list = []
    if kind == "init":
        params = [P(n, P.POSITIONAL_OR_KEYWORD, annotation=t) for n, t in ctor]
    else:
        if kind != "property":
            fn = getattr(base_cls, _attr(base_cls, name))  # a function; bound for a classmethod, so `cls` is already gone
            mparams = list(inspect.signature(fn).parameters.values())
            if kind == "instance":
                mparams = mparams[1:]  # self
            hints = _hints(fn)
            mparams = [p.replace(annotation=hints.get(p.name, p.annotation)) for p in mparams]
        if not stateless and any(p.name.startswith(CTOR) for p in mparams):
            raise Unsupported("a method parameter collides with the generated constructor parameters")
        params = [P(CTOR + n, P.POSITIONAL_OR_KEYWORD, annotation=t) for n, t in ctor]
        params += [p.replace(kind=P.POSITIONAL_OR_KEYWORD) if p.kind is P.POSITIONAL_ONLY else p for p in mparams]
    signature = inspect.Signature(params)
    target = getattr(base_cls, _attr(base_cls, name), None) if kind in ("instance", "static", "class") else None
    asynchronous = inspect.iscoroutinefunction(target) or inspect.isasyncgenfunction(target)
    return tuple(_side(cls, name, kind, mparams, signature, asynchronous, side)
                 for cls, side in ((base_cls, "old"), (cand_cls, "new")))


def _side(cls, name, kind, mparams, signature, asynchronous, side):
    prefix = "" if kind == "init" else CTOR
    method_names = {p.name for p in mparams}
    attr = _attr(cls, name)

    def split(args, kwargs):
        bound = signature.bind(*args, **kwargs).arguments
        ctor_kw = {k[len(prefix):]: v for k, v in bound.items() if k not in method_names and k.startswith(prefix)}
        a, k = [], {}
        for p in mparams:
            if p.name not in bound:
                continue
            v = bound[p.name]
            if p.kind in (P.POSITIONAL_ONLY, P.POSITIONAL_OR_KEYWORD):
                a.append(v)
            elif p.kind is P.VAR_POSITIONAL:
                a.extend(v)
            elif p.kind is P.KEYWORD_ONLY:
                k[p.name] = v
            else:
                k.update(v)
        return ctor_kw, a, k

    def attach(fn):
        fn.__signature__ = signature
        fn.__annotations__ = {p.name: p.annotation for p in signature.parameters.values() if p.annotation is not P.empty}
        fn.__name__ = fn.__qualname__ = side
        return fn

    if asynchronous:
        async def go(*args, **kwargs):
            ctor_kw, a, k = split(args, kwargs)
            obj = cls if kind in ("static", "class") else cls(**ctor_kw)
            out = getattr(obj, attr)(*a, **k)
            value = await asyncdrive._collect(out) if inspect.isasyncgen(out) else await out
            return value if kind in ("static", "class") else (value, snap(obj))
        return asyncdrive.sync_of(attach(go), side)

    def call(*args, **kwargs):
        ctor_kw, a, k = split(args, kwargs)
        if kind == "init":
            return snap(cls(**ctor_kw))
        if kind in ("static", "class"):
            return getattr(cls, attr)(*a, **k)
        obj = cls(**ctor_kw)
        value = getattr(obj, attr) if kind == "property" else getattr(obj, attr)(*a, **k)
        return value, snap(obj)
    return asyncdrive.sync_of(attach(call), side, drive=False)
