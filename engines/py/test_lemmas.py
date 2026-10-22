"""Tests of the lemmas operation (lemmas.py). They need crosshair, which is in the engine image:

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest discover -s /engine -p "test_*.py"

Most cases fake the CrossHair runs at engine.run, so they test the mapping from CrossHair's output to statuses.
The Real class runs CrossHair on fixtures that finish in seconds (a confirmed postcondition, a counterexample and a
divergence); nothing waits for the 10 s budget.
"""
import ast
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine  # noqa: E402
import lemmas  # noqa: E402


def fake_crosshair(answers):
    """engine.run stand-in: answers maps "check:<target>" or "diffbehavior" to (rc, output). It records the commands."""
    calls = []

    def run(cmd, timeout):
        calls.append(cmd)
        kind = "check:" + cmd[-1] if "check" in cmd else "diffbehavior"
        return answers[kind]

    run.calls = calls
    return run


CONFIRMED = "/io/a2dlc_lemma_0.py:3: info: Confirmed over all paths.\n"


class Directory(unittest.TestCase):
    """A temporary /io, base and candidate; engine.IO and engine.WORK point into the temporary directory."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.io, self.base, self.cand, self.work = root / "io", root / "base", root / "cand", root / "work"
        for d in (self.io, self.base, self.cand):
            d.mkdir()
        for name, value in (("IO", self.io), ("WORK", self.work)):
            patcher = mock.patch.object(engine, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)
        self.addCleanup(self.tmp.cleanup)

    def put(self, side, rel, text):
        path = (self.base if side == "base" else self.cand) / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text, encoding="utf-8")

    def request(self, functions, mode="check", changed=("mod.py",)):
        return {"base_dir": str(self.base), "candidate_dir": str(self.cand), "changed_files": list(changed),
                "budget": {"functions": functions, "mode": mode}}

    def response(self):
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))

    def verdict(self, text, post="result >= 0", base_text=None, mode="check", fake=None):
        """Status, message and counterexample of one lemma named f, with the candidate (and base) text given."""
        self.put("cand", "mod.py", text)
        if base_text is not None:
            self.put("base", "mod.py", base_text)
        with mock.patch.object(engine, "run", fake or fake_crosshair({})):
            lemmas.run_lemmas(self.request([{"name": "f", "postcondition": post}], mode=mode))
        (one,) = self.response()["lemmas"]
        return one["status"], one["message"], one.get("counterexample")


F_INT = "def f(x: int) -> int:\n    return x + 1\n"


class Postcondition(unittest.TestCase):
    def fn(self, source):
        return ast.parse(source).body[0]

    def test_result_is_renamed_to_the_return_value(self):
        text, why = lemmas._postcondition(self.fn(F_INT), "result >= x", set())
        self.assertEqual((text, why), ("_ >= x", ""))

    def test_unparsable_postcondition_is_not_checkable(self):
        text, why = lemmas._postcondition(self.fn(F_INT), "result >=", set())
        self.assertIsNone(text)
        self.assertIn("does not parse", why)

    def test_name_outside_arguments_and_module_is_not_checkable(self):
        text, why = lemmas._postcondition(self.fn(F_INT), "helper(result)", set())
        self.assertIsNone(text)
        self.assertIn("helper", why)

    def test_private_module_name_is_checkable(self):
        # A private helper of the candidate module is in scope in the wrapper (the whole namespace is bound).
        text, why = lemmas._postcondition(self.fn(F_INT), "_is_sorted(result)", {"_is_sorted"})
        self.assertEqual((text, why), ("_is_sorted(_)", ""))

    def test_comprehension_and_lambda_variables_are_not_free(self):
        text, why = lemmas._postcondition(self.fn(F_INT), "all(v >= 0 for v in result)", set())
        self.assertEqual(why, "")
        text, why = lemmas._postcondition(self.fn(F_INT), "(lambda v: v >= 0)(result)", set())
        self.assertEqual(why, "")

    def test_argument_named_result_is_not_checkable(self):
        text, why = lemmas._postcondition(self.fn("def f(result: int) -> int:\n    return result\n"), "result >= 0", set())
        self.assertIsNone(text)

    def test_wrapper_forwards_every_parameter_kind(self):
        source = "def f(a: int, /, b: int = 1, *args: int, c: int, **kw: int) -> int:\n    return a\n"
        wrapper = lemmas._wrapper("a2dlc_cand", "pkg.mod", self.fn(source), "_ >= 0")
        ast.parse(wrapper)
        self.assertIn("return a2dlc_target(a, b, *args, c=c, **kw)", wrapper)
        self.assertIn("def f(a: int, /, b: int=1, *args: int, c: int, **kw: int) -> int:", wrapper)
        self.assertIn("post: _ >= 0", wrapper)

    def test_wrapper_binds_the_module_namespace(self):
        wrapper = lemmas._wrapper("a2dlc_cand", "pkg.mod", self.fn(F_INT), "_ >= 0")
        self.assertIn("globals().update(", wrapper)
        self.assertIn("import a2dlc_cand.pkg.mod as a2dlc_module", wrapper)


class Verdicts(Directory):
    def test_confirmed_postcondition_passes(self):
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED)})
        status, _, counterexample = self.verdict(F_INT, fake=fake)
        self.assertEqual((status, counterexample), ("pass", None))

    def test_no_counterexample_without_proof_is_inconclusive(self):
        out = "/io/a2dlc_lemma_0.py:3: info: Not confirmed.\n"
        status, message, _ = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (0, out)}))
        self.assertEqual(status, "inconclusive")
        self.assertIn("did not confirm", message)

    def test_counterexample_fails_with_args_and_returned_value(self):
        out = "/io/a2dlc_lemma_0.py:3: error: false when calling f(-1) (which returns -2)\n"
        status, _, counterexample = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)}))
        self.assertEqual(status, "fail")
        self.assertEqual(counterexample, {"args": "-1", "returned": "-2"})

    def test_counterexample_with_a_tuple_argument_keeps_its_parentheses(self):
        out = "/io/a2dlc_lemma_0.py:3: error: false when calling f((1, 2)) (which returns 0)\n"
        _, _, counterexample = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)}))
        self.assertEqual(counterexample, {"args": "(1, 2)", "returned": "0"})

    def test_exception_of_the_lemma_fails_with_raised(self):
        out = "/io/a2dlc_lemma_0.py:3: error: ValueError: boom when calling f(3)\n"
        _, _, counterexample = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)}))
        self.assertEqual(counterexample, {"args": "3", "raised": "ValueError: boom"})

    def test_side_effect_is_inconclusive_not_a_failure(self):
        out = "/io/a2dlc_lemma_0.py:3: error: SideEffectDetected: open\n"
        status, _, _ = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)}))
        self.assertEqual(status, "inconclusive")

    def test_no_checkable_function_is_inconclusive(self):
        out = "WARNING: Targets found, but contain no checkable functions.\n"
        status, message, _ = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (0, out)}))
        self.assertEqual(status, "inconclusive")
        self.assertIn("nothing checkable", message)

    def test_timeout_is_inconclusive(self):
        status, message, _ = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (None, "")}))
        self.assertEqual(status, "inconclusive")
        self.assertIn("exceeded", message)

    def test_crosshair_error_exit_is_inconclusive(self):
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (2, "Traceback\nModuleNotFoundError: nope\n")})
        status, message, _ = self.verdict(F_INT, fake=fake)
        self.assertEqual(status, "inconclusive")
        self.assertIn("rc=2", message)

    def test_missing_function_is_inconclusive_and_runs_nothing(self):
        fake = fake_crosshair({})
        status, message, _ = self.verdict("def g(x: int) -> int:\n    return x\n", fake=fake)
        self.assertEqual(status, "inconclusive")
        self.assertIn("no top-level function named f", message)
        self.assertEqual(fake.calls, [])

    def test_untyped_function_is_inconclusive_and_runs_nothing(self):
        fake = fake_crosshair({})
        status, message, _ = self.verdict("def f(x):\n    return x\n", fake=fake)
        self.assertEqual(status, "inconclusive")
        self.assertIn("no type annotations", message)
        self.assertEqual(fake.calls, [])

    def test_unparsable_postcondition_is_inconclusive_and_runs_nothing(self):
        fake = fake_crosshair({})
        status, _, _ = self.verdict(F_INT, post="result >=", fake=fake)
        self.assertEqual(status, "inconclusive")
        self.assertEqual(fake.calls, [])

    def test_refactor_without_base_pair_is_no_pair_and_skips_the_diff(self):
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED)})
        status, message, counterexample = self.verdict(F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual((status, counterexample), ("no_pair", None))
        self.assertIn("nothing to diff against", message)
        self.assertEqual(len(fake.calls), 1)

    def test_refactor_divergence_fails_with_the_diff_counterexample(self):
        diff = ("/io/x.py:1: note\nGiven: (2),\n  a2dlc_base.mod.f : 3\n  a2dlc_cand.mod.f : 4\n")
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED), "diffbehavior": (1, diff)})
        status, _, counterexample = self.verdict(F_INT, base_text=F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual(status, "fail")
        self.assertEqual(counterexample, {"args": "2", "old": "3", "new": "4"})

    def test_refactor_pass_needs_the_postcondition_and_the_diff(self):
        diff = "No differences found. (attempted 12 iterations)\n"
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED), "diffbehavior": (0, diff)})
        status, _, _ = self.verdict(F_INT, base_text=F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual(status, "pass")

    def test_refactor_unconcluded_diff_is_inconclusive_not_pass(self):
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED), "diffbehavior": (0, "something else\n")})
        status, _, _ = self.verdict(F_INT, base_text=F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual(status, "inconclusive")

    def test_refactor_postcondition_failure_wins_over_no_pair(self):
        out = "/io/a2dlc_lemma_0.py:3: error: false when calling f(-1) (which returns -2)\n"
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)})
        status, _, counterexample = self.verdict(F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual(status, "fail")
        self.assertEqual(counterexample, {"args": "-1", "returned": "-2"})

    def test_refactor_with_a_changed_signature_is_not_diffed(self):
        # Same name, different parameters: the base lemma is not the candidate's, so no diff and no pass.
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED)})
        status, message, counterexample = self.verdict("def f(x: int, y: int) -> int:\n    return x + y\n",
                                                       base_text=F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual((status, counterexample), ("inconclusive", None))
        self.assertIn("signature", message)
        self.assertIn("(x: int, y: int) -> int", message)
        self.assertEqual(len(fake.calls), 1)

    def test_refactor_with_a_changed_return_annotation_is_not_diffed(self):
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (0, CONFIRMED)})
        status, _, _ = self.verdict("def f(x: int) -> float:\n    return x + 1.0\n",
                                    base_text=F_INT, mode="diffbehavior", fake=fake)
        self.assertEqual(status, "inconclusive")
        self.assertEqual(len(fake.calls), 1)

    def test_rc_1_with_an_unrelated_error_line_is_inconclusive_not_fail(self):
        out = "/io/a2dlc_lemma_0.py:3: error: internal setup failure\n"
        status, message, counterexample = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)}))
        self.assertEqual((status, counterexample), ("inconclusive", None))
        self.assertIn("internal setup failure", message)

    def test_counterexample_line_is_found_after_an_unrelated_error_line(self):
        out = ("/io/a2dlc_lemma_0.py:3: error: internal setup failure\n"
               "/io/a2dlc_lemma_0.py:3: error: false when calling f(-1) (which returns -2)\n")
        status, _, counterexample = self.verdict(F_INT, fake=fake_crosshair({"check:a2dlc_lemma_0.f": (1, out)}))
        self.assertEqual(status, "fail")
        self.assertEqual(counterexample, {"args": "-1", "returned": "-2"})

    def test_star_import_makes_a_module_name_unknown_so_the_lemma_is_inconclusive(self):
        # The star import may supply helper, so helper is not excused as a module name: never a false pass.
        text = "from os.path import *\n\n\ndef helper(x: int) -> int:\n    return x\n\n\ndef f(x: int) -> int:\n" \
               "    return helper(x)\n"
        fake = fake_crosshair({})
        status, message, _ = self.verdict(text, post="helper(result) >= 0", fake=fake)
        self.assertEqual(status, "inconclusive")
        self.assertIn("helper", message)
        self.assertEqual(fake.calls, [])


class Response(Directory):
    def test_lemmas_keep_input_order_and_counterexample_only_on_fail(self):
        self.put("cand", "mod.py", "def g(x: int) -> int:\n    return x\n\ndef f(x: int) -> int:\n    return x - 5\n")
        out = "/io/a2dlc_lemma_0.py:3: error: false when calling f(0) (which returns -5)\n"
        fake = fake_crosshair({"check:a2dlc_lemma_0.f": (1, out),
                               "check:a2dlc_lemma_1.g": (0, "/io/a2dlc_lemma_1.py:3: info: Confirmed over all paths.\n")})
        request = self.request([{"name": "f", "postcondition": "result >= 0"},
                                {"name": "g", "postcondition": "result >= 0"}])
        with mock.patch.object(engine, "run", fake):
            self.assertEqual(lemmas.run_lemmas(request), 0)
        resp = self.response()
        self.assertEqual(resp["schema_version"], 1)
        self.assertEqual([x["name"] for x in resp["lemmas"]], ["f", "g"])
        self.assertEqual(resp["lemmas"][0]["status"], "fail")
        self.assertEqual(resp["lemmas"][0]["counterexample"], {"args": "0", "returned": "-5"})
        self.assertEqual(resp["lemmas"][1]["status"], "pass")
        self.assertNotIn("counterexample", resp["lemmas"][1])


class Real(Directory):
    """Real CrossHair on fixtures that finish in seconds."""

    def test_confirmed_postcondition_passes(self):
        self.put("cand", "mod.py", "def f(x: int) -> int:\n    return x * 2\n")
        lemmas.run_lemmas(self.request([{"name": "f", "postcondition": "result % 2 == 0"}]))
        self.assertEqual(self.response()["lemmas"][0]["status"], "pass")

    def test_counterexample_fails_with_returned_value(self):
        self.put("cand", "mod.py", "def f(x: int) -> int:\n    return x + 1\n")
        lemmas.run_lemmas(self.request([{"name": "f", "postcondition": "result >= 2"}]))
        (one,) = self.response()["lemmas"]
        self.assertEqual(one["status"], "fail")
        self.assertIn("returned", one["counterexample"])

    def test_private_helper_of_the_module_is_in_scope_of_the_postcondition(self):
        # A NameError inside the wrapper would come back as fail: the helper must resolve, so the status is not fail.
        self.put("cand", "mod.py", "def _sq(x: int) -> int:\n    return x * x\n\n\ndef f(x: int) -> int:\n    return _sq(x)\n")
        lemmas.run_lemmas(self.request([{"name": "f", "postcondition": "_sq(result) >= 0"}]))
        (one,) = self.response()["lemmas"]
        self.assertNotEqual(one["status"], "fail", one["message"])

    def test_refactor_divergence_fails_with_old_and_new(self):
        self.put("base", "mod.py", "def f(x: int) -> int:\n    return x + 1\n")
        self.put("cand", "mod.py", "def f(x: int) -> int:\n    return x + 2\n")
        lemmas.run_lemmas(self.request([{"name": "f", "postcondition": "True"}], mode="diffbehavior"))
        (one,) = self.response()["lemmas"]
        self.assertEqual(one["status"], "fail")
        self.assertEqual(set(one["counterexample"]), {"args", "old", "new"})


if __name__ == "__main__":
    unittest.main()
