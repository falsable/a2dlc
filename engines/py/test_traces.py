"""Tests of the trace capture (traces.py). They run in the engine image:

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest discover -s /engine -p "test_*.py"

The end-to-end cases start the harness in a child process on a small base tree whose branch coverage is known by
hand: sign() has one if, boom() has one if, mix() has none. Lines of the base fixture are numbered in BASE below.
"""
import ast
import importlib.util
import json
import subprocess
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import traces  # noqa: E402

# line 1 def sign(x): | 2 if x < 0: (decision) | 3 return -1 | 4 return 1
# line 7 def boom(x): | 8 if x: (decision) | 9 raise ValueError | 10 return 0
# line 13 def mix(a, *rest, k=0, **kw): | 14 return a
BASE = textwrap.dedent("""\
    def sign(x):
        if x < 0:
            return -1
        return 1


    def boom(x):
        if x:
            raise ValueError("no")
        return 0


    def mix(a, *rest, k=0, **kw):
        return a
    """)

# Same functions, each with a changed body (the AST differs, the lines do not).
CAND = BASE.replace("return 1\n", "return 1 + 0\n").replace("return 0\n", "return 0 + 0\n") \
    .replace("return a\n", "return a + 0\n")


class Pure(unittest.TestCase):
    def test_functions_of_lists_top_level_functions_and_methods_only(self):
        tree = ast.parse("def f(): pass\nclass C:\n    def m(self): pass\n    class D:\n        def n(self): pass\n"
                         "def g():\n    def inner(): pass\n")
        self.assertEqual(sorted(traces.functions_of(tree)), ["C.m", "f", "g"])

    def test_decisions_are_the_if_for_while_of_the_function_itself(self):
        tree = ast.parse("def f(x):\n    for i in x:\n        if i:\n            pass\n"
                         "    while x:\n        def inner(z):\n            if z:\n                pass\n        x = 0\n")
        self.assertEqual(traces.decisions_of(tree.body[0]), [2, 3, 5])

    def test_changed_targets_takes_only_the_functions_whose_ast_changed(self):
        base = "def f(y):\n    if y:\n        return 1\n    return 2\n\ndef g(y):\n    return y\n"
        cand = "def f(y):\n    if y:\n        return 1\n    return 3\n\ndef g(y):\n    return y\n"
        self.assertEqual(traces.changed_targets(base, cand, "m.py"),
                         [{"file": "m.py", "qualname": "f", "decisions": [2], "inline": []}])

    def test_a_function_missing_in_the_candidate_is_a_target(self):
        targets = traces.changed_targets("def f(y):\n    return y\n", "x = 1\n", "m.py")
        self.assertEqual([t["qualname"] for t in targets], ["f"])

    def test_nested_function_decisions_belong_to_the_nested_function(self):
        base = "def f(y):\n    def inner(z):\n        while z:\n            z -= 1\n    return inner\n"
        cand = base.replace("return inner\n", "return inner if y else inner\n")
        self.assertEqual(traces.changed_targets(base, cand, "m.py"),
                         [{"file": "m.py", "qualname": "f", "decisions": [], "inline": []}])

    def test_coverage_counts_each_decision_outcome_once(self):
        targets = [{"file": "a.py", "qualname": "f", "decisions": [2, 5]}]
        call = {"function": "a.py::f", "input": "(1)", "output": "1", "branch_ids": ["2->3"]}
        out = {"calls": [call, dict(call)],
               "arcs": [["a.py::f", 2, 3], ["a.py::f", 2, 4], ["a.py::f", 5, 6]]}
        found, coverage = traces.assemble(out, targets)
        self.assertEqual(len(found), 1)
        self.assertEqual(coverage, 0.75)  # decision 2 has both outcomes, decision 5 has one of two

    def test_no_decisions_means_no_branch_coverage(self):
        targets = [{"file": "a.py", "qualname": "f", "decisions": []}]
        self.assertEqual(traces.assemble({"calls": [], "arcs": []}, targets), ([], None))

    def test_safe_repr_hides_addresses_and_bounds_length(self):
        class Odd:
            def __repr__(self):
                return "<Odd at 0x7f3a2b>"

        class Bad:
            def __repr__(self):
                raise ValueError("no repr")

        self.assertEqual(traces.safe_repr(Odd()), "<Odd at 0x?>")
        self.assertEqual(traces.safe_repr(Bad()), "<unrepr Bad>")
        self.assertEqual(len(traces.safe_repr("x" * 1000)), traces.REPR_LIMIT + 3)


class Harness(unittest.TestCase):
    """End to end: a base tree with tests, a candidate that changes three functions, the real child process."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.base, self.cand, self.io = root / "base", root / "cand", root / "io"
        for d in (self.base, self.cand, self.io):
            d.mkdir()
        (self.base / "calc.py").write_text(BASE, encoding="utf-8")
        (self.cand / "calc.py").write_text(CAND, encoding="utf-8")

    def capture(self, test_source: str, budget: dict | None = None) -> dict:
        (self.base / "test_calc.py").write_text(test_source, encoding="utf-8")
        request = {"base_dir": str(self.base), "candidate_dir": str(self.cand), "changed_files": ["calc.py"],
                   "budget": budget or {}}
        self.assertEqual(traces.capture_traces(request, self.io), 0)
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))

    PARTIAL = textwrap.dedent("""\
        import unittest
        from calc import sign, boom, mix


        class T(unittest.TestCase):
            def test_sign(self):
                self.assertEqual(sign(-5), -1)

            def test_boom(self):
                with self.assertRaises(ValueError):
                    boom(1)

            def test_mix(self):
                self.assertEqual(mix(1, 2, k=3, z=4), 1)
        """)

    def test_partial_tests_record_inputs_outputs_and_the_branch_taken(self):
        response = self.capture(self.PARTIAL)
        self.assertEqual(response["error"], None)
        self.assertIn(response["runner"], ("pytest", "unittest"))
        self.assertEqual(response["test_exit"], 0)
        self.assertEqual(response["traces"], [
            {"function": "calc.py::boom", "input": "(1)", "output": "raise ValueError", "branch_ids": ["8->9"]},
            {"function": "calc.py::mix", "input": "(1, *(2,), k=3, **{'z': 4})", "output": "1", "branch_ids": []},
            {"function": "calc.py::sign", "input": "(-5)", "output": "-1", "branch_ids": ["2->3"]},
        ])
        # two decisions, four outcomes; sign reached one, boom reached one
        self.assertEqual(response["branch_coverage"], 0.5)

    FULL = textwrap.dedent("""\
        import unittest
        from calc import sign, boom, mix


        class T(unittest.TestCase):
            def test_sign(self):
                self.assertEqual(sign(-5), -1)
                self.assertEqual(sign(3), 1)

            def test_boom(self):
                boom(0)
                with self.assertRaises(ValueError):
                    boom(1)

            def test_mix(self):
                self.assertEqual(mix(1, 2, k=3, z=4), 1)
        """)

    def test_full_tests_reach_every_outcome_and_the_run_is_repeatable(self):
        full = self.FULL
        first = self.capture(full)
        second = self.capture(full)
        self.assertEqual(first, second)
        self.assertEqual(first["branch_coverage"], 1.0)
        self.assertEqual([t["function"] for t in first["traces"]],
                         ["calc.py::boom", "calc.py::boom", "calc.py::mix", "calc.py::sign", "calc.py::sign"])

    def test_no_changed_function_runs_nothing(self):
        (self.cand / "calc.py").write_text(BASE, encoding="utf-8")
        response = self.capture(self.PARTIAL)
        self.assertEqual(response["traces"], [])
        self.assertIsNone(response["runner"])
        self.assertNotIn("branch_coverage", response)
        self.assertIsNone(response["error"])

    def test_tests_over_budget_are_an_error_not_a_pass(self):
        response = self.capture(self.PARTIAL, {"timeout_s": 0.001})
        self.assertTrue(response["error"].startswith("tests did not finish"))
        self.assertEqual(response["traces"], [])


class InlineWatch(unittest.TestCase):
    def test_inline_decisions_are_those_whose_body_starts_on_the_decision_line(self):
        tree = ast.parse("def f(x):\n    if x: return 1\n    if x:\n        return 2\n    return 0\n")
        self.assertEqual(traces.decisions_of(tree.body[0]), [2, 3])
        self.assertEqual(traces.inline_decisions_of(tree.body[0]), [2])

    def test_inline_watch_names_the_body_and_exit_sides_of_a_one_line_if(self):
        namespace: dict = {}
        exec("def f(x):\n    if x: return 1\n    return 0\n", namespace)
        watch = traces.inline_watch(namespace["f"].__code__, {2})
        self.assertEqual(len(watch), 1)
        ((line, body, exit_, exit_line),) = watch.values()
        self.assertEqual((line, exit_line), (2, 3))
        self.assertIsNotNone(body)
        self.assertNotEqual(body, exit_)


class InlineDecisions(unittest.TestCase):
    """One-line decisions end to end: their outcomes come from opcode events in the real child process."""

    BASE = textwrap.dedent("""\
        def f(x):
            if x: return 1
            return 0


        def w(n):
            while n: n -= 1
            return n


        def g(xs):
            t = 0
            for x in xs: t += x
            return t
        """)
    # same functions with changed bodies (the AST differs, the decision lines do not)
    CAND = BASE.replace("return 1", "return 10").replace("n -= 1", "n -= 2").replace("t += x", "t += x * 1")
    TESTS = textwrap.dedent("""\
        import unittest
        from calc import f, w, g


        class T(unittest.TestCase):
            def test_f_true(self):
                self.assertEqual(f(1), 1)

            def test_f_false(self):
                self.assertEqual(f(0), 0)

            def test_w_exit_at_once(self):
                self.assertEqual(w(0), 0)

            def test_w_body_then_exit(self):
                self.assertEqual(w(4), 0)

            def test_g_empty(self):
                self.assertEqual(g([]), 0)

            def test_g_body(self):
                self.assertEqual(g([1]), 1)
        """)

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.base, self.cand, self.io = root / "base", root / "cand", root / "io"
        for d in (self.base, self.cand, self.io):
            d.mkdir()
        (self.base / "calc.py").write_text(self.BASE, encoding="utf-8")
        (self.cand / "calc.py").write_text(self.CAND, encoding="utf-8")

    def capture(self, tests: str) -> dict:
        (self.base / "test_calc.py").write_text(tests, encoding="utf-8")
        return self.run_capture()

    def run_capture(self) -> dict:
        request = {"base_dir": str(self.base), "candidate_dir": str(self.cand), "changed_files": ["calc.py"],
                   "budget": {}}
        self.assertEqual(traces.capture_traces(request, self.io), 0)
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))

    @staticmethod
    def branches(response: dict, function: str, given: str) -> list[list[str]]:
        return [t["branch_ids"] for t in response["traces"] if t["function"] == function and t["input"] == given]

    def test_each_one_line_decision_reaches_both_outcomes(self):
        response = self.capture(self.TESTS)
        self.assertIsNone(response["error"])
        self.assertEqual(response["branch_coverage"], 1.0)
        self.assertEqual(self.branches(response, "calc.py::f", "(1)"), [["2->2"]])
        self.assertEqual(self.branches(response, "calc.py::f", "(0)"), [["2->3"]])
        self.assertEqual(self.branches(response, "calc.py::w", "(0)"), [["7->8"]])
        self.assertEqual(self.branches(response, "calc.py::w", "(4)"), [["7->7", "7->8"]])
        self.assertEqual(self.branches(response, "calc.py::g", "([])"), [["13->14"]])
        self.assertEqual(self.branches(response, "calc.py::g", "([1])"), [["13->13", "13->14"]])

    def test_partial_tests_count_only_the_outcomes_they_reach(self):
        partial = self.TESTS.replace("def test_f_false", "def _skip_f_false").replace(
            "def test_w_body_then_exit", "def _skip_w").replace("def test_g_body", "def _skip_g")
        response = self.capture(partial)
        self.assertIsNone(response["error"])
        self.assertEqual(response["branch_coverage"], 0.5)

    def test_no_tests_outside_a_package_is_an_error_not_a_pass(self):
        if importlib.util.find_spec("pytest"):
            self.skipTest("the runner is pytest here, and pytest collects tests outside packages")
        (self.base / "tests").mkdir()
        (self.base / "tests" / "test_calc.py").write_text(self.TESTS, encoding="utf-8")
        response = self.run_capture()
        self.assertEqual(response["runner"], "unittest")
        self.assertTrue(response["error"].startswith("no tests discovered"))
        self.assertEqual(response["test_exit"], 1)
        self.assertEqual(response["traces"], [])
        self.assertNotIn("branch_coverage", response)


class HarnessRunner(unittest.TestCase):
    """The child process of the harness: its exit code and its error, per runner."""

    def run_spec(self, root: Path, runner: str) -> dict:
        spec_path = root / "spec.json"
        out_path = root / "out.json"
        spec = {"root": str(root), "runner": runner, "out": str(out_path), "targets": []}
        spec_path.write_text(json.dumps(spec), encoding="utf-8")
        proc = subprocess.run([sys.executable, traces.__file__, "--harness", str(spec_path)], cwd=root,
                              capture_output=True)
        self.assertEqual(proc.returncode, 0, proc.stderr.decode("utf-8", "replace"))
        return json.loads(out_path.read_text(encoding="utf-8"))

    def test_unittest_with_a_package_test_passes(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root, "test_m.py").write_text("import unittest\n\n\nclass T(unittest.TestCase):\n"
                                               "    def test_ok(self):\n        pass\n", encoding="utf-8")
            out = self.run_spec(Path(root), "unittest")
        self.assertEqual((out["exit"], out["error"]), (0, None))

    def test_unittest_without_a_package_finds_nothing_and_says_so(self):
        with tempfile.TemporaryDirectory() as root:
            Path(root, "tests").mkdir()
            Path(root, "tests", "test_m.py").write_text("import unittest\n", encoding="utf-8")
            out = self.run_spec(Path(root), "unittest")
        self.assertEqual(out["exit"], 1)
        self.assertEqual(out["error"], traces.NO_TESTS)

    def test_pytest_exit_codes_are_mapped(self):
        for code in (0, 1, traces.PYTEST_NO_TESTS_EXIT):
            with self.subTest(exit_code=code), tempfile.TemporaryDirectory() as root:
                # a stub pytest module next to the harness: the harness imports it from the base's root
                Path(root, "pytest.py").write_text(f"def main(args):\n    return {code}\n", encoding="utf-8")
                out = self.run_spec(Path(root), "pytest")
                self.assertEqual(out["exit"], code)
                expected = traces.PYTEST_NO_TESTS if code == traces.PYTEST_NO_TESTS_EXIT else None
                self.assertEqual(out["error"], expected)


if __name__ == "__main__":
    unittest.main()
