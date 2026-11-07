"""Methods of module-level classes are judged targets (bead a2dlc-zs7). These tests run the real CrossHair, so they only
run where it is installed (the engine image):

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest test_methods -v   (from /engine)

Each case says which invariant it guards. The soundness cases (class attribute, constructor, extracted helper, mutation)
are the ones the first design got wrong: it handed both sides one instance of the BASE class.
"""
import importlib.util
import json
import sys
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine  # noqa: E402
from test_engine import Directory  # noqa: E402

HAS_CROSSHAIR = importlib.util.find_spec("crosshair") is not None
BUDGET = {"per_condition_timeout_s": 10, "workers": 2}


def code(*lines):
    return "\n".join(lines) + "\n"


def module(v: int) -> str:
    """One module with a class per scenario; v=0 is the base, v=1 the candidate."""
    new = v == 1
    return code(
        "class Scaler:                      # plain class, behavior changes",
        "    def __init__(self, factor: int):",
        "        self.factor = factor",
        "",
        "    def scale(self, x: int) -> int:",
        f"        return x * self.factor{' + 1' if new else ''}",
        "",
        "",
        "class Equal:                        # same behavior, other expression",
        "    def __init__(self, factor: int):",
        "        self.factor = factor",
        "",
        "    def scale(self, x: int) -> int:",
        f"        return {'self.factor * x' if new else 'x * self.factor'}",
        "",
        "",
        "class Limit:                        # class attribute changes, the method only looks different",
        f"    LIMIT = {10 if new else 5}",
        "",
        "    def under(self, n: int) -> bool:",
        f"        return n < self.LIMIT{' + 0' if new else ''}",
        "",
        "",
        "class Ctor:                         # constructor changes, scale reads the same attribute",
        "    def __init__(self, factor: int):",
        f"        self.factor = factor{' + 1' if new else ''}",
        "",
        "    def scale(self, x: int) -> int:",
        f"        return {'self.factor * x' if new else 'x * self.factor'}",
        "",
        "",
        "class Helper:                       # a helper extracted into a method that only the candidate has",
        "    def pub(self, n: int) -> int:",
        f"        return {'self.twice(n) // 2' if new else 'n'}",
        "",
        *(["    def twice(self, n: int) -> int:", "        return n * 2", ""] if new else [""]),
        "",
        "class Session:",
        "    def execute(self, q: str) -> int:",
        "        return len(q)",
        "",
        "",
        "class Repo:                         # the constructor needs a session-like object",
        "    def __init__(self, session: Session):",
        "        self.session = session",
        "",
        "    def count(self, q: str) -> int:",
        f"        return self.session.execute(q){' + 1' if new else ''}",
        "",
        "",
        "class Svc:                          # async method",
        "    async def run(self, x: int) -> int:",
        f"        return x{' + 1' if new else ''}",
        "",
        "",
        "class Util:                         # staticmethod and classmethod (the classmethod reads a class attribute)",
        "    BASE = 1",
        "",
        "    @staticmethod",
        "    def twice(n: int) -> int:",
        f"        return n * 2{' + 1' if new else ''}",
        "",
        "    @classmethod",
        "    def plus(cls, n: int) -> int:",
        f"        return n + cls.BASE{' + 1' if new else ''}",
        "",
        "",
        "class Priv:                         # a private method",
        "    def pub(self, n: int) -> int:",
        "        return self._h(n)",
        "",
        "    def _h(self, n: int) -> int:",
        f"        return n{' + 1' if new else ''}",
        "",
        "",
        "class Counter:                      # returns None: only the state tells the versions apart",
        "    def __init__(self, total: int):",
        "        self.total = total",
        "",
        "    def add(self, x: int) -> None:",
        f"        self.total += x{' + 1' if new else ''}",
        "",
        "",
        "class _Hidden:                      # a private class is outside the declared scope",
        "    def get(self, n: int) -> int:",
        f"        return n{' + 1' if new else ''}",
    )


@unittest.skipUnless(HAS_CROSSHAIR, "needs CrossHair (the engine image)")
class ClassMethodsJudged(Directory):
    @classmethod
    def setUpClass(cls):
        cls.engine_ran = False

    def setUp(self):
        super().setUp()
        if not ClassMethodsJudged.engine_ran:  # one run for all the scenarios: each target is a CrossHair process
            (self.base / "m.py").write_text(module(0), encoding="utf-8")
            (self.cand / "m.py").write_text(module(1), encoding="utf-8")
            engine.diff(self.req(["m.py"], budget=BUDGET))
            ClassMethodsJudged.result = self.response()
            ClassMethodsJudged.engine_ran = True

    def target(self, name):
        (t,) = [t for t in self.result["targets"] if t["target"] == f"m.py::{name}"]
        return t

    def test_a_method_that_changed_behavior_is_FAIL_with_a_counterexample(self):
        t = self.target("Scaler.scale")
        self.assertEqual(t["verdict"], "FAIL")
        self.assertIn("self_factor", t["counterexample"]["args"])

    def test_an_equivalent_method_is_PASS(self):
        self.assertEqual(self.target("Equal.scale")["verdict"], "PASS")

    def test_a_changed_class_attribute_is_seen_by_the_method_that_reads_it(self):
        self.assertEqual(self.target("Limit.under")["verdict"], "FAIL")

    def test_a_changed_constructor_is_FAIL_not_PASS(self):
        self.assertEqual(self.target("Ctor.__init__")["verdict"], "FAIL")
        self.assertEqual(self.target("Ctor.scale")["verdict"], "FAIL")  # same text of scale, other instance

    def test_an_extracted_helper_is_not_a_false_FAIL(self):
        self.assertEqual(self.target("Helper.pub")["verdict"], "PASS")

    def test_a_constructor_that_needs_a_session_is_INCONCLUSIVE_naming_it(self):
        t = self.target("Repo.count")
        self.assertEqual(t["verdict"], "INCONCLUSIVE")
        self.assertIn("constructor not derivable", t["reason"])
        self.assertIn("session: Session", t["reason"])

    def test_an_async_method_is_judged(self):
        self.assertEqual(self.target("Svc.run")["verdict"], "FAIL")

    def test_static_and_class_methods_are_judged(self):
        self.assertEqual(self.target("Util.twice")["verdict"], "FAIL")
        self.assertEqual(self.target("Util.plus")["verdict"], "FAIL")

    def test_a_private_method_is_judged_like_a_private_function(self):
        self.assertEqual(self.target("Priv._h")["verdict"], "FAIL")

    def test_a_method_that_only_mutates_self_is_compared_by_the_state(self):
        self.assertEqual(self.target("Counter.add")["verdict"], "FAIL")

    def test_nothing_is_out_of_scope_and_a_private_class_is_not_a_target(self):
        names = [t["target"] for t in self.result["targets"]]
        self.assertFalse([n for n in names if "_Hidden" in n])
        self.assertEqual(self.result["out_of_scope"], [])  # the file has judged targets


@unittest.skipUnless(HAS_CROSSHAIR, "needs CrossHair (the engine image)")
class RealisticLayout(Directory):
    """A package where the changed file is imported by a file nobody asked about (relative to the changed set)."""

    def test_a_method_of_a_class_imported_elsewhere_is_judged(self):
        for root, limit in ((self.base, "n"), (self.cand, "n if n < 100 else 100")):
            pkg = root / "pkg"
            pkg.mkdir()
            (pkg / "__init__.py").write_text("", encoding="utf-8")
            (pkg / "util.py").write_text("def clamp(n: int) -> int:\n    return n\n", encoding="utf-8")
            (pkg / "service.py").write_text("from pkg.repo import Repo\n\n\ndef make() -> Repo:\n    return Repo(1)\n",
                                            encoding="utf-8")
            (pkg / "repo.py").write_text(code(
                "from pkg.util import clamp", "",
                "class Repo:",
                "    def __init__(self, scale: int):",
                "        self.scale = scale", "",
                "    def value(self, n: int) -> int:",
                f"        return clamp({limit}) * self.scale"), encoding="utf-8")
        engine.diff(self.req(["pkg/repo.py"], budget=BUDGET))
        r = self.response()
        (t,) = r["targets"]
        self.assertEqual((t["target"], t["verdict"]), ("pkg/repo.py::Repo.value", "FAIL"))
        self.assertEqual(r["out_of_scope"], [])


@unittest.skipUnless(HAS_CROSSHAIR, "needs CrossHair (the engine image)")
class EquivalentByAstNeedsTheClassToBeTheSame(Directory):
    def test_a_docstring_only_method_change_with_a_changed_class_attribute_is_judged_not_PASS_by_AST(self):
        def src(doc, limit):
            return code("class K:", f"    LIMIT = {limit}", "",
                        "    def under(self, n: int) -> bool:", f'        """{doc}"""', "        return n < self.LIMIT")
        (self.base / "m.py").write_text(src("old", 5), encoding="utf-8")
        (self.cand / "m.py").write_text(src("new", 10), encoding="utf-8")
        engine.diff(self.req(["m.py"], budget=BUDGET))
        (t,) = self.response()["targets"]
        self.assertEqual((t["target"], t["verdict"]), ("m.py::K.under", "FAIL"))


class SelectionWithoutCrossHair(unittest.TestCase):
    """Which methods are targets, from the AST alone."""

    def test_functions_lists_private_async_and_property_methods_of_public_classes_but_not_of_private_ones(self):
        text = code("class C:", "    def a(self): ...", "    def _b(self): ...", "    async def c(self): ...",
                    "    @property", "    def d(self): ...", "", "class _P:", "    def e(self): ...")
        self.assertEqual(sorted(engine.functions(text)), ["C._b", "C.a", "C.c", "C.d"])

    def test_a_method_defined_twice_or_a_setter_is_unjudged_with_its_reason_when_it_changes(self):
        base = code("class C:", "    @property", "    def p(self): return 1", "",
                    "    @p.setter", "    def p(self, v): ...", "", "    def x(self): ...", "    def x(self, y): ...")
        cand = base.replace("return 1", "return 2")
        unjudged = engine.unjudged_of(base, cand)
        self.assertEqual(sorted(unjudged), ["C.p"])  # x did not change
        self.assertIn("more than once", unjudged["C.p"][1])

    def test_a_class_level_change_makes_every_method_of_the_class_a_target(self):
        base = code("class C:", "    N = 1", "    def a(self, x: int) -> int: return x", "    def b(self, x: int) -> int: return x")
        cand = base.replace("N = 1", "N = 2")
        direct, _ = engine.targets_of(base, cand)
        self.assertEqual(sorted(direct), ["C.a", "C.b"])
        self.assertEqual(engine.equivalent_of(base, cand), [])

    def test_a_caller_through_self_is_a_caller(self):
        base = code("class C:", "    def a(self, x: int) -> int: return self.b(x)", "    def b(self, x: int) -> int: return x")
        cand = base.replace("def b(self, x: int) -> int: return x", "def b(self, x: int) -> int: return x + 1")
        direct, callers = engine.targets_of(base, cand)
        self.assertEqual((direct, callers), (["C.b"], ["C.a"]))


if __name__ == "__main__":
    unittest.main()
