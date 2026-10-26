"""classify.py and operation classify_lines: what counts as a defensive handler or declaration-only code, and above all what
does not. The five real cases of bead a2dlc-os3 live in testdata/classify/cases.json."""

import json
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import classify  # noqa: E402
import engine  # noqa: E402

DATA = Path(__file__).resolve().parent / "testdata" / "classify"


def notes_of(source: str, lines: list[int]) -> dict[str, list[int]]:
    return {n["category"]: n["lines"] for n in classify.classify(textwrap.dedent(source), lines)}


def everything(source: str) -> list[int]:
    return list(range(1, textwrap.dedent(source).count("\n") + 1))


class RealCases(unittest.TestCase):
    def test_cases_of_the_bead(self):
        cases = json.loads((DATA / "cases.json").read_text(encoding="utf-8"))["cases"]
        self.assertEqual(len(cases), 9)
        for c in cases:
            with self.subTest(c["id"]):
                got = {n["category"]: n["lines"]
                       for n in classify.classify((DATA / c["fixture"]).read_text(encoding="utf-8"), c["uncovered"])}
                self.assertEqual(got, c["noted"])
                left = sorted(set(c["uncovered"]) - {line for ls in got.values() for line in ls})
                self.assertEqual(left, c["blocking"])


class DefensiveHandlers(unittest.TestCase):
    def check(self, body: str, expect: bool, handler: str = "except Exception:"):
        src = f"def f():\n    try:\n        g()\n    {handler}\n" + textwrap.indent(textwrap.dedent(body), "        ")
        lines = list(range(4, src.count("\n") + 1))
        got = notes_of(src, lines).get(classify.DEFENSIVE, [])
        self.assertEqual(got, lines if expect else [], src)

    def test_swallow_forms(self):
        for body in ("pass\n", "x = None\n", "x = y = None\n", "self.x = {}\n", "return\n", "return None\n",
                     "return {'a': None, 'b': 1}\n", "raise\n", "continue\n", "'doc'\n", "...\n"):
            with self.subTest(body):
                self.check(body, True)

    def test_logging_then_inert(self):
        self.check("logger.warning('x', exc_info=True)\nreturn None\n", True)
        self.check("log.error('failed %s' % str(e))\n", True, "except Exception as e:")
        self.check("self._logger.exception(f'bad {e!r}')\n", True, "except BaseException as e:")

    def test_broad_catches(self):
        for handler in ("except:", "except BaseException:", "except (OSError, Exception):"):
            with self.subTest(handler):
                self.check("pass\n", True, handler)

    def test_real_logic_in_the_handler_is_not_classified(self):
        for body in ("db.rollback()\n", "x = compute()\n", "return build(e)\n", "if x:\n    pass\n",
                     "for i in y:\n    pass\n", "raise HTTPException(502)\n", "raise RuntimeError('x') from e\n",
                     "x = a[0]\n", "x += 1\n",
                     "logger.warning(compute())\n", "print('x')\n", "metrics.warning('x')\n", "x = await y\n"):
            with self.subTest(body):
                self.check(body, False)

    def test_specific_exceptions_are_not_classified(self):
        for handler in ("except ValueError:", "except (KeyError, IndexError):", "except OSError as e:"):
            with self.subTest(handler):
                self.check("pass\n", False, handler)

    def test_a_nested_try_makes_the_outer_handler_real_logic(self):
        src = "def f():\n    try:\n        g()\n    except Exception:\n        try:\n            h()\n        except Exception:\n            pass\n"
        self.assertEqual(notes_of(src, list(range(1, 9))), {classify.DEFENSIVE: [7, 8]})

    def test_try_body_and_header_stay_blocking(self):
        src = "def f():\n    try:\n        g()\n    except Exception:\n        pass\n"
        self.assertEqual(notes_of(src, [2, 3, 4, 5]), {classify.DEFENSIVE: [4, 5]})


class Declarative(unittest.TestCase):
    def test_imports_all_and_type_checking(self):
        src = """\
            from __future__ import annotations
            import os
            from a import (
                b,
                c,
            )
            if TYPE_CHECKING:
                from x import Y
            __all__ = ["b", "c"]
        """
        self.assertEqual(notes_of(src, everything(src)), {classify.DECLARATIVE: list(range(1, 10))})

    def test_all_with_a_computed_entry_is_not_declarative(self):
        self.assertEqual(notes_of('__all__ = ["a"] + other()\n', [1]), {})
        self.assertEqual(notes_of("__all__ = [name]\n", [1]), {})

    def test_type_checking_with_code_is_not_declarative(self):
        src = "if TYPE_CHECKING:\n    from x import Y\n    setup()\n"
        self.assertEqual(notes_of(src, [1, 2, 3]), {classify.DECLARATIVE: [2]})
        self.assertEqual(notes_of("if TYPE_CHECKING:\n    pass\nelse:\n    import x\n", [1, 2, 4]), {classify.DECLARATIVE: [4]})

    def test_protocol_skeleton(self):
        src = """\
            @runtime_checkable
            class P(Protocol[T], metaclass=ABCMeta):
                '''doc'''
                name: str
                limit: int = 3
                @abstractmethod
                async def f(self, x: int = 1) -> int:
                    ...
                def g(self):
                    raise NotImplementedError('later')
                class Inner:
                    pass
        """
        self.assertEqual(notes_of(src, everything(src)), {classify.DECLARATIVE: list(range(1, 13))})

    def test_class_with_one_real_method_is_not_a_skeleton(self):
        src = """\
            class P(Protocol):
                def f(self):
                    ...
                def g(self):
                    return 1
        """
        self.assertEqual(notes_of(src, everything(src)), {})

    def test_other_class_shapes_are_not_skeletons(self):
        for src in ("class A:\n    x = compute()\n", "class A(make_base()):\n    pass\n",
                    "@app.route('/x')\nclass A:\n    pass\n", "class A:\n    def f(self, x=g()):\n        ...\n",
                    "class A:\n    @router.get('/x')\n    def f(self):\n        ...\n",
                    "class A:\n    def f(self):\n        raise HTTPException(404)\n",
                    "class A:\n    x: int = g()\n"):
            with self.subTest(src):
                self.assertEqual(notes_of(src, [1, 2, 3, 4]), {}, src)

    def test_logic_is_never_declarative(self):
        for src in ("x = 1\n", "x = f()\n", "def f():\n    return 1\n", "async with db() as s:\n    pass\n",
                    "if a:\n    b = 1\n", "for i in y:\n    pass\n", "logger = logging.getLogger(__name__)\n"):
            with self.subTest(src):
                self.assertEqual(notes_of(src, [1, 2]), {}, src)

    def test_function_decorators_outside_a_skeleton_stay_blocking(self):
        self.assertEqual(notes_of("@app.get('/x')\ndef f():\n    ...\n", [1, 2, 3]), {})


class Robustness(unittest.TestCase):
    def test_unparseable_source_classifies_nothing(self):
        self.assertEqual(classify.classify("def f(:\n", [1]), [])

    def test_only_the_requested_lines_come_back(self):
        src = "import a\nimport b\n"
        self.assertEqual(classify.classify(src, [2]), [{"category": classify.DECLARATIVE, "lines": [2]}])
        self.assertEqual(classify.classify(src, []), [])

    def test_lines_are_sorted_and_unique(self):
        self.assertEqual(classify.classify("import a\nimport b\n", [2, 1, 2]),
                         [{"category": classify.DECLARATIVE, "lines": [1, 2]}])


class Operation(unittest.TestCase):
    def test_classify_lines_answers_the_engine_classify_response(self):
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            io, cand = root / "io", root / "cand"
            io.mkdir()
            cand.mkdir()
            (cand / "m.py").write_text("import a\nx = f()\n", encoding="utf-8")
            (cand / "notes.md").write_text("# x\n", encoding="utf-8")
            request = {"operation": "classify_lines", "base_dir": str(root / "base"), "candidate_dir": str(cand),
                       "changed_files": ["m.py", "notes.md", "gone.py"],
                       "budget": {"lines": {"m.py": [1, 2], "notes.md": [1], "gone.py": [1]}}}
            (io / "request.json").write_text(json.dumps(request), encoding="utf-8")
            with mock.patch.object(engine, "IO", io):
                self.assertEqual(engine.main(), 0)
            resp = json.loads((io / "response.json").read_text(encoding="utf-8"))
        self.assertEqual(resp, {"schema_version": 1, "error": None, "files": [
            {"file": "m.py", "notes": [{"category": "declarative_only", "lines": [1]}]},
            {"file": "notes.md", "notes": []}, {"file": "gone.py", "notes": []}]})


if __name__ == "__main__":
    unittest.main()
