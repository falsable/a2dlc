"""Static listing of the test functions of a Python file (operation list_tests, bead a2dlc-uca).

The core uses it to learn which test ids already existed in the base without running the base. It lists at function level:
every def and async def at module level and inside classes (nested classes included), by class path, whatever the name. It
over-lists on purpose: a name that is not a test never shows up as a testcase, while a test that is missing from the list
would be taken for a new one. A file whose collection cannot be read off its AST is not listable, and the core runs the base
for that file only."""

import ast

# Bases a test class may have without importing methods from somewhere the AST cannot see.
_PLAIN_BASES = {"object", "TestCase", "IsolatedAsyncioTestCase", "SimpleTestCase", "TransactionTestCase", "LiveServerTestCase"}
# A use of one of these can create tests, classes or names that no def shows.
_DYNAMIC = {"globals", "locals", "vars", "setattr", "exec", "eval", "__import__", "compile"}


def _collected(name: str) -> bool:
    return name.lower().startswith("test")


def _base_name(b: ast.expr) -> str | None:
    if isinstance(b, ast.Name):
        return b.id
    if isinstance(b, ast.Attribute):
        return b.attr
    return None


def _blocks(body: list[ast.stmt]):
    """The statements of a body, looking inside if/try/with at the same level (a def there is collected)."""
    for s in body:
        yield s
        if isinstance(s, ast.If):
            yield from _blocks(s.body)
            yield from _blocks(s.orelse)
        elif isinstance(s, ast.Try):
            for part in (s.body, s.orelse, s.finalbody, *[h.body for h in s.handlers]):
                yield from _blocks(part)
        elif isinstance(s, (ast.With, ast.AsyncWith)):
            yield from _blocks(s.body)


def list_tests(text: str) -> tuple[bool, str, list[tuple[str, str]]]:
    """(listable, reason, [(class path, function name)]) of a Python source. The class path is "" at module level and
    "Outer.Inner" for nested classes; methods inherited from classes defined in the same file are listed under the subclass."""
    try:
        tree = ast.parse(text)
    except (SyntaxError, ValueError, RecursionError) as e:
        return False, f"does not parse: {type(e).__name__}", []
    for n in ast.walk(tree):
        if isinstance(n, ast.Name) and n.id in _DYNAMIC:
            return False, f"uses {n.id}: tests may be created dynamically", []
        if isinstance(n, ast.ImportFrom) and any(a.name == "*" for a in n.names):
            return False, "star import: tests may come from another module", []
    if any(isinstance(s, (ast.For, ast.AsyncFor, ast.While)) for s in tree.body):
        return False, "module-level loop: names may be generated", []
    classes: dict[str, ast.ClassDef] = {}
    out: set[tuple[str, str]] = set()

    def own_methods(cls: ast.ClassDef) -> list[str]:
        return [s.name for s in _blocks(cls.body) if isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef))]

    def walk(body: list[ast.stmt], path: str) -> str | None:
        for s in _blocks(body):
            if isinstance(s, ast.ClassDef):
                p = f"{path}.{s.name}" if path else s.name
                classes.setdefault(p, s)
                why = walk(s.body, p)
                if why:
                    return why
            elif isinstance(s, (ast.FunctionDef, ast.AsyncFunctionDef)):
                out.add((path, s.name))
            elif isinstance(s, (ast.Import, ast.ImportFrom)) and not path:
                for a in s.names:
                    bound = (a.asname or a.name).split(".")[0]
                    if _collected(bound):
                        return f"imports {bound}, which pytest would collect here"
            elif isinstance(s, (ast.Assign, ast.AnnAssign, ast.AugAssign)):
                targets = s.targets if isinstance(s, ast.Assign) else [s.target]
                for t in targets:
                    for x in ast.walk(t):
                        if isinstance(x, ast.Name) and _collected(x.id):
                            return f"assigns {x.id}: a test or a class created by an expression"
        return None

    why = walk(tree.body, "")
    if why:
        return False, why, []
    # Inheritance: every base must be a class of this file or a plain one; the methods of a base count for the subclass.
    by_name = {p.rsplit(".", 1)[-1]: c for p, c in classes.items()}

    def inherited(cls: ast.ClassDef, seen: set[str]) -> list[str] | None:
        if any(k.arg == "metaclass" for k in cls.keywords):
            return None
        names: list[str] = []
        for b in cls.bases:
            n = _base_name(b)
            if n in _PLAIN_BASES:
                continue
            if n is None or n not in by_name or n in seen:
                return None
            sub = inherited(by_name[n], seen | {n})
            if sub is None:
                return None
            names += own_methods(by_name[n]) + sub
        return names

    for p, cls in classes.items():
        names = inherited(cls, {cls.name})
        if names is None:
            return False, f"class {p} inherits from a base the AST of this file does not show", []
        for n in names:
            out.add((p, n))
    return True, "", sorted(out)
