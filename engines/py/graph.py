"""Operation deps_graph (decision 15, Stage 6): the Python import graph of the candidate, read by the AST.

An edge goes from each file to every file its imports execute: the module itself and each package __init__.py on the
way (Python runs those too). Absolute and relative imports both count, including the ones inside functions, try
blocks and TYPE_CHECKING guards. An import that maps to no file of the repository is reported in unresolved as
"<file>: <name>", never dropped: standard library and third-party modules appear there too. Dynamic imports
(importlib, __import__ with a computed name) are not seen. Stage 6 inverts the edges to find the consumers of the
changed files.
"""
import ast
import json
import os
import tokenize
from pathlib import Path

IO = Path("/io")
SKIP = {".git", ".a2dlc", "node_modules", ".venv", "venv", "__pycache__", ".beads", ".mypy_cache", ".pytest_cache"}


def read_source(path: Path) -> str:
    """Text of a file as Python reads it: a BOM or a PEP 263 cookie is honoured."""
    with tokenize.open(path) as f:
        return f.read()


def sources(root: Path) -> list[str]:
    """Repo-relative POSIX paths of every .py file under root, outside SKIP directories, sorted."""
    found = []
    for base, dirs, names in os.walk(root):
        dirs[:] = sorted(d for d in dirs if d not in SKIP)
        for name in names:
            if name.endswith(".py"):
                found.append((Path(base) / name).relative_to(root).as_posix())
    return sorted(found)


def module_of(rel: str) -> list[str] | None:
    """Dotted name of a file as parts. A src/ prefix is not part of the name (src layout). None if it has no name."""
    parts = rel[: -len(".py")].split("/")
    if len(parts) > 1 and parts[0] == "src":
        parts = parts[1:]
    if parts[-1] == "__init__":
        parts = parts[:-1]
    return parts or None


def package_of(rel: str) -> list[str]:
    """Parts of the package a file belongs to: its own name for an __init__.py, its parent otherwise."""
    parts = module_of(rel) or []
    return parts if Path(rel).name == "__init__.py" else parts[:-1]


class Index:
    """Dotted module names of the repository. A package __init__.py wins over a module of the same name."""

    def __init__(self, files: list[str]):
        self.files: dict[str, str] = {}
        self.known: set[str] = set()
        for rel in files:
            parts = module_of(rel)
            if parts is None:
                continue
            for i in range(1, len(parts) + 1):
                self.known.add(".".join(parts[:i]))
            name = ".".join(parts)
            if Path(rel).name == "__init__.py":
                self.files[name] = rel
            else:
                self.files.setdefault(name, rel)

    def chain(self, dotted: str) -> list[str]:
        """Files Python executes to import dotted: each existing package on the way, then the module itself."""
        out: list[str] = []
        parts = dotted.split(".")
        for i in range(1, len(parts) + 1):
            found = self.files.get(".".join(parts[:i]))
            if found and found not in out:
                out.append(found)
        return out


def imports_of(rel: str, tree: ast.AST, index: Index) -> tuple[set[str], set[str]]:
    """(files imported by rel, unresolved imports of rel as '<file>: <name>')."""
    files: set[str] = set()
    unresolved: set[str] = set()
    package = package_of(rel)
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            for alias in node.names:
                if alias.name in index.known:
                    files.update(index.chain(alias.name))
                else:
                    unresolved.add(f"{rel}: {alias.name}")
        elif isinstance(node, ast.ImportFrom):
            module = node.module or ""
            if node.level == 0:
                dotted: str | None = module
            else:
                up = node.level - 1
                dotted = None
                if up <= len(package):
                    base = package[: len(package) - up]
                    dotted = ".".join(base + (module.split(".") if module else []))
            label = "." * node.level + module
            if dotted is None or (dotted and dotted not in index.known):
                unresolved.add(f"{rel}: {label}")
                continue
            if dotted:
                files.update(index.chain(dotted))
            for alias in node.names:
                full = f"{dotted}.{alias.name}" if dotted else alias.name
                if full in index.files:
                    files.update(index.chain(full))
    return files, unresolved


def deps_graph(req: dict, io: Path = IO) -> int:
    """Writes engine-deps-response (edges, unresolved) to io/response.json."""
    root = Path(req["candidate_dir"])
    files = sources(root)
    index = Index(files)
    edges: set[tuple[str, str]] = set()
    unresolved: set[str] = set()
    for rel in files:
        try:
            tree = ast.parse(read_source(root / rel), filename=rel)
        except (OSError, SyntaxError, ValueError, RecursionError) as e:
            unresolved.add(f"{rel}: not parsed ({type(e).__name__})")
            continue
        found, missing = imports_of(rel, tree, index)
        edges.update((rel, target) for target in found if target != rel)
        unresolved.update(missing)
    response = {
        "schema_version": 1,
        "edges": [{"from": a, "to": b} for a, b in sorted(edges)],
        "unresolved": sorted(unresolved),
    }
    (io / "response.json").write_text(json.dumps(response, ensure_ascii=False, indent=1), encoding="utf-8")
    return 0
