"""Tests of the import graph (graph.py). They run in the engine image:

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest discover -s /engine -p "test_*.py"

Each case writes a small tree to a temporary directory and checks the response of deps_graph.
"""
import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import graph  # noqa: E402


class Repo(unittest.TestCase):
    """A temporary candidate tree and an io directory; deps_graph writes its response there."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name) / "cand"
        self.io = Path(self.tmp.name) / "io"
        self.root.mkdir()
        self.io.mkdir()

    def write(self, rel: str, text: str = "") -> None:
        path = self.root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def deps(self) -> dict:
        self.assertEqual(graph.deps_graph({"candidate_dir": str(self.root)}, self.io), 0)
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))

    def edges(self) -> set[tuple[str, str]]:
        return {(e["from"], e["to"]) for e in self.deps()["edges"]}


class Absolute(Repo):
    def test_absolute_import_links_the_module_and_the_packages_on_the_way(self):
        self.write("pkg/__init__.py")
        self.write("pkg/b.py")
        self.write("pkg/a.py", "import pkg.b\n")
        self.assertEqual(self.edges(), {("pkg/a.py", "pkg/__init__.py"), ("pkg/a.py", "pkg/b.py")})

    def test_from_import_of_a_submodule_links_it(self):
        self.write("pkg/__init__.py")
        self.write("pkg/b.py")
        self.write("pkg/a.py", "from pkg import b\n")
        self.assertIn(("pkg/a.py", "pkg/b.py"), self.edges())

    def test_from_import_of_an_attribute_links_the_package_only(self):
        self.write("pkg/__init__.py", "VALUE = 1\n")
        self.write("pkg/a.py", "from pkg import VALUE\n")
        self.assertEqual(self.edges(), {("pkg/a.py", "pkg/__init__.py")})
        self.assertEqual(self.deps()["unresolved"], [])

    def test_import_inside_a_function_and_a_try_block_counts(self):
        self.write("b.py")
        self.write("a.py", "def f():\n    import b\n\ntry:\n    from b import x\nexcept ImportError:\n    pass\n")
        self.assertEqual(self.edges(), {("a.py", "b.py")})

    def test_self_import_is_not_an_edge(self):
        self.write("a.py", "import a\n")
        self.assertEqual(self.edges(), set())
        self.assertEqual(self.deps()["unresolved"], [])


class Relative(Repo):
    def test_package_init_relative_import(self):
        self.write("pkg/__init__.py", "from . import b\n")
        self.write("pkg/b.py")
        self.assertIn(("pkg/__init__.py", "pkg/b.py"), self.edges())

    def test_two_dots_climb_to_the_parent_package(self):
        self.write("pkg/__init__.py")
        self.write("pkg/b.py")
        self.write("pkg/sub/__init__.py")
        self.write("pkg/sub/c.py", "from ..b import x\n")
        self.assertIn(("pkg/sub/c.py", "pkg/b.py"), self.edges())

    def test_relative_import_of_a_module_in_the_same_package(self):
        self.write("pkg/__init__.py")
        self.write("pkg/a.py", "from .b import x\n")
        self.write("pkg/b.py")
        self.assertIn(("pkg/a.py", "pkg/b.py"), self.edges())

    def test_relative_import_past_the_root_is_unresolved(self):
        self.write("a.py", "from ...x import y\n")
        self.assertEqual(self.deps()["unresolved"], ["a.py: ...x"])

    def test_relative_import_of_a_missing_module_is_unresolved(self):
        self.write("pkg/__init__.py")
        self.write("pkg/a.py", "from .missing import x\n")
        self.assertEqual(self.deps()["unresolved"], ["pkg/a.py: .missing"])


class Layout(Repo):
    def test_src_layout_maps_names_without_the_src_prefix(self):
        self.write("src/pkg/__init__.py")
        self.write("src/pkg/b.py")
        self.write("src/pkg/a.py", "from pkg import b\n")
        self.assertIn(("src/pkg/a.py", "src/pkg/b.py"), self.edges())

    def test_namespace_package_without_init_resolves_its_modules(self):
        self.write("ns/mod.py")
        self.write("a.py", "import ns.mod\n")
        self.assertEqual(self.edges(), {("a.py", "ns/mod.py")})
        self.assertEqual(self.deps()["unresolved"], [])

    def test_package_init_wins_over_a_module_of_the_same_name(self):
        self.write("pkg.py")
        self.write("pkg/__init__.py")
        self.write("a.py", "import pkg\n")
        self.assertEqual(self.edges(), {("a.py", "pkg/__init__.py")})

    def test_test_files_are_importers_like_any_other(self):
        self.write("calc.py")
        self.write("tests/test_calc.py", "import calc\n")
        self.assertEqual(self.edges(), {("tests/test_calc.py", "calc.py")})


class Unresolved(Repo):
    def test_third_party_and_standard_imports_are_reported_not_dropped(self):
        self.write("a.py", "import os\nimport numpy.linalg\nfrom requests import get\n")
        self.assertEqual(self.edges(), set())
        self.assertEqual(self.deps()["unresolved"],
                         ["a.py: numpy.linalg", "a.py: os", "a.py: requests"])

    def test_missing_submodule_of_a_repo_package_is_unresolved(self):
        self.write("pkg/__init__.py")
        self.write("a.py", "import pkg.missing\n")
        self.assertEqual(self.deps()["unresolved"], ["a.py: pkg.missing"])

    def test_syntax_error_is_reported_and_its_file_has_no_edges(self):
        self.write("b.py")
        self.write("a.py", "import b\ndef broken(:\n")
        response = self.deps()
        self.assertEqual(response["edges"], [])
        self.assertEqual(len(response["unresolved"]), 1)
        self.assertTrue(response["unresolved"][0].startswith("a.py: not parsed (SyntaxError)"))

    def test_skipped_directories_are_neither_sources_nor_targets(self):
        self.write("b.py")
        self.write(".venv/lib/x.py", "import b\nimport nothere\n")
        self.write("a.py", "import b\n")
        self.assertEqual(self.edges(), {("a.py", "b.py")})
        self.assertEqual(self.deps()["unresolved"], [])


class Output(Repo):
    def test_response_is_sorted_deduplicated_and_stable(self):
        self.write("pkg/__init__.py")
        self.write("pkg/b.py")
        self.write("pkg/c.py")
        self.write("pkg/a.py", "import pkg.c\nimport pkg.b\nimport pkg.b\n")
        first = self.deps()
        second = self.deps()
        self.assertEqual(first, second)
        self.assertEqual(first["schema_version"], 1)
        pairs = [(e["from"], e["to"]) for e in first["edges"]]
        self.assertEqual(pairs, sorted(pairs))
        self.assertEqual(len(pairs), len(set(pairs)))

    def test_empty_repo_gives_empty_graph(self):
        self.assertEqual(self.deps(), {"schema_version": 1, "edges": [], "unresolved": []})


if __name__ == "__main__":
    unittest.main()
