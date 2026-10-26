"""listing.py and operation list_tests: the static ids of a test file, and what stops it from being listable. The last test
compares the listing with what a real pytest reports (junit_family=xunit1); it is skipped where pytest is not installed."""

import json
import subprocess
import sys
import tempfile
import textwrap
import unittest
import xml.etree.ElementTree as ET
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine  # noqa: E402
import listing  # noqa: E402


def listed(source: str) -> tuple[bool, str, set[tuple[str, str]]]:
    ok, why, tests = listing.list_tests(textwrap.dedent(source))
    return ok, why, set(tests)


class ListingTest(unittest.TestCase):
    def test_functions_classes_and_nested_classes(self):
        ok, _, ts = listed("""
            import pytest
            def test_a(): pass
            async def test_b(): pass
            @pytest.mark.parametrize("x", [1, 2])
            def test_p(x): pass
            class TestA:
                def test_c(self): pass
                class TestInner:
                    def test_d(self): pass
            """)
        self.assertTrue(ok)
        self.assertEqual(ts, {("", "test_a"), ("", "test_b"), ("", "test_p"), ("TestA", "test_c"), ("TestA.TestInner", "test_d")})

    def test_lists_every_def_not_only_test_names(self):
        # over-listing is safe: a helper never shows up as a testcase
        _, _, ts = listed("def helper(): pass\ndef test_a(): pass\n")
        self.assertEqual(ts, {("", "helper"), ("", "test_a")})

    def test_defs_under_if_and_try_are_collected(self):
        _, _, ts = listed("""
            import sys
            if sys.platform == "win32":
                def test_win(): pass
            else:
                def test_nix(): pass
            try:
                import x
            except ImportError:
                def test_noimport(): pass
            """)
        self.assertEqual({n for _, n in ts}, {"test_win", "test_nix", "test_noimport"})

    def test_inherits_methods_of_a_base_in_the_same_file(self):
        ok, _, ts = listed("""
            class Base:
                def test_shared(self): pass
            class TestSub(Base):
                def test_own(self): pass
            """)
        self.assertTrue(ok)
        self.assertIn(("TestSub", "test_shared"), ts)
        self.assertIn(("TestSub", "test_own"), ts)

    def test_unittest_testcase_is_a_plain_base(self):
        ok, _, ts = listed("""
            import unittest
            class T(unittest.TestCase):
                def test_a(self): pass
            """)
        self.assertTrue(ok)
        self.assertEqual(ts, {("T", "test_a")})

    def test_what_cannot_be_read_off_the_ast_is_not_listable(self):
        cases = {
            "a base from another module": "from base import BaseSuite\nclass TestX(BaseSuite):\n    def test_a(self): pass\n",
            "an unparseable file": "def test_a(:\n",
            "setattr": "def make(i): pass\nfor i in range(3):\n    setattr(__import__('sys').modules[__name__], f'test_{i}', make(i))\n",
            "globals": "globals()['test_x'] = lambda: None\n",
            "star import": "from helpers import *\ndef test_a(): pass\n",
            "an imported test": "from helpers import test_shared\ndef test_a(): pass\n",
            "an assigned test": "test_alias = other\n",
            "a module-level loop": "for i in range(3):\n    pass\n",
            "a metaclass": "class TestM(metaclass=Meta):\n    def test_a(self): pass\n",
        }
        for name, src in cases.items():
            with self.subTest(name):
                ok, why, ts = listing.list_tests(src)
                self.assertFalse(ok)
                self.assertTrue(why)
                self.assertEqual(ts, [])

    def test_operation_answers_per_file_and_never_lists_what_is_not_python(self):
        with tempfile.TemporaryDirectory() as d, tempfile.TemporaryDirectory() as io:
            base = Path(d)
            (base / "tests").mkdir()
            (base / "tests" / "test_a.py").write_text("def test_a(): pass\n", encoding="utf-8")
            (base / "tests" / "test_d.py").write_text("from x import *\n", encoding="utf-8")
            (base / "tests" / "t.js").write_text("test('a', () => {})\n", encoding="utf-8")
            req = {"operation": "list_tests", "base_dir": str(base), "changed_files": [
                "tests/test_a.py", "tests/test_d.py", "tests/t.js", "tests/missing.py"]}
            with mock.patch.object(engine, "IO", Path(io)):
                self.assertEqual(engine.list_tests_op(req), 0)
            resp = json.loads((Path(io) / "response.json").read_text(encoding="utf-8"))
        by = {f["file"]: f for f in resp["files"]}
        self.assertEqual(resp["schema_version"], 1)
        self.assertTrue(by["tests/test_a.py"]["listable"])
        self.assertEqual(by["tests/test_a.py"]["tests"], [{"class": "", "name": "test_a"}])
        for f in ("tests/test_d.py", "tests/t.js", "tests/missing.py"):
            self.assertFalse(by[f]["listable"], f)
            self.assertTrue(by[f]["reason"])
            self.assertEqual(by[f]["tests"], [])


try:
    import pytest  # noqa: F401
    HAVE_PYTEST = True
except ImportError:
    HAVE_PYTEST = False


@unittest.skipUnless(HAVE_PYTEST, "pytest is not installed")
class AgainstRealPytest(unittest.TestCase):
    """Every testcase pytest reports for a file must be found by the listing, parametrize suffix stripped, and the
    JUnit classname must be the module of the file followed by the class path (the way the core matches them)."""

    SOURCE = textwrap.dedent("""
        import pytest
        import unittest

        def test_plain(): pass

        @pytest.mark.parametrize("a,b", [(1, 2), (3, 4)])
        def test_param(a, b): pass

        @pytest.mark.parametrize("v", ["x[0]", "y"])
        def test_bracket(v): pass

        class TestBox:
            def test_in_class(self): pass

            @pytest.mark.parametrize("n", [1, 2, 3])
            def test_in_class_param(self, n): pass

            class TestNested:
                def test_deep(self): pass

        class Base:
            def test_inherited(self): pass

        class TestChild(Base):
            def test_child(self): pass

        class UnitCase(unittest.TestCase):
            def test_unit(self): pass
        """)

    def test_every_reported_testcase_is_listed(self):
        with tempfile.TemporaryDirectory() as d:
            root = Path(d)
            (root / "tests").mkdir()
            (root / "tests" / "test_sample.py").write_text(self.SOURCE, encoding="utf-8")
            for family in ("xunit1", "xunit2"):
                out = root / f"{family}.xml"
                subprocess.run([sys.executable, "-m", "pytest", "-q", "-p", "no:cacheprovider", "-o", f"junit_family={family}",
                                f"--junitxml={out}", "tests/test_sample.py"], cwd=root, capture_output=True, check=False)
                cases = [(c.get("classname"), c.get("name")) for c in ET.parse(out).getroot().iter("testcase")]
                self.assertGreaterEqual(len(cases), 12, cases)
                ok, why, tests = listing.list_tests(self.SOURCE)
                self.assertTrue(ok, why)
                module = "tests.test_sample"
                for classname, name in cases:
                    self.assertTrue(classname == module or classname.startswith(module + "."), (family, classname))
                    path = classname[len(module) + 1:] if classname != module else ""
                    self.assertIn((path, name.split("[", 1)[0]), tests, (family, classname, name))


if __name__ == "__main__":
    unittest.main()
