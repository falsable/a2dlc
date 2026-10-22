"""Each operation main() routes to another module, run for real over a tiny tree (no fakes), through engine.main().

The reply must have the keys its contract schema requires and no others. The schemas live in contracts/, which the
image does not carry, so the key sets are written here from them.
"""

import json
import sys
import tempfile
import textwrap
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine  # noqa: E402

try:
    import atheris  # noqa: F401
    HAVE_ATHERIS = True
except ImportError:
    HAVE_ATHERIS = False


class Operation(unittest.TestCase):
    """engine.IO, engine.WORK and the trees live in one temporary directory."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.io, self.base, self.cand, self.work = root / "io", root / "base", root / "cand", root / "work"
        for d in (self.io, self.base, self.cand):
            d.mkdir()
        for name, value in (("IO", self.io), ("WORK", self.work)):
            patcher = mock.patch.object(engine, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def run_main(self, request: dict) -> dict:
        (self.io / "request.json").write_text(json.dumps(request), encoding="utf-8")
        self.assertEqual(engine.main(), 0)
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))

    def assertKeys(self, resp: dict, required: set, optional: set = frozenset()):
        self.assertEqual(set(resp) - required - optional, set(), resp)
        self.assertEqual(required - set(resp), set(), resp)


class LemmasEndToEnd(Operation):
    def test_lemmas_answers_engine_lemmas_response(self):
        (self.cand / "m.py").write_text("def f(x: int) -> int:\n    return x\n", encoding="utf-8")
        resp = self.run_main({
            "operation": "lemmas", "base_dir": str(self.base), "candidate_dir": str(self.cand),
            "changed_files": ["m.py"],
            "budget": {"mode": "check", "functions": [{"name": "absent", "postcondition": "True"}]}})
        self.assertKeys(resp, {"schema_version", "lemmas"})
        self.assertNotIn("targets", resp)
        self.assertEqual(resp["schema_version"], 1)
        self.assertEqual(len(resp["lemmas"]), 1, resp)
        lemma = resp["lemmas"][0]
        self.assertEqual(lemma["name"], "absent")
        self.assertEqual(lemma["status"], "inconclusive")
        self.assertIn("absent", lemma["message"])


class FuzzEndToEnd(Operation):
    def test_fuzz_answers_engine_fuzz_response_when_nothing_is_fuzzable(self):
        # an untyped parameter is listed as unsupported and no child process is spawned
        (self.cand / "m.py").write_text("def f(x):\n    return x\n", encoding="utf-8")
        resp = self.run_main({
            "operation": "fuzz", "base_dir": str(self.base), "candidate_dir": str(self.cand),
            "changed_files": ["m.py"], "budget": {"seconds": 2, "seed": 1}})
        self.assertKeys(resp, {"schema_version", "findings", "runs", "seconds", "unsupported"})
        self.assertEqual(resp["findings"], [])
        self.assertEqual(resp["runs"], 0)
        self.assertEqual([u["function"] for u in resp["unsupported"]], ["m.f"], resp)

    @unittest.skipUnless(HAVE_ATHERIS, "needs atheris in the image")
    def test_fuzz_with_atheris_answers_engine_fuzz_response(self):
        (self.cand / "m.py").write_text("def f(x: int) -> int:\n    return x\n", encoding="utf-8")
        resp = self.run_main({
            "operation": "fuzz", "base_dir": str(self.base), "candidate_dir": str(self.cand),
            "changed_files": ["m.py"], "budget": {"seconds": 2, "seed": 1}})
        self.assertKeys(resp, {"schema_version", "findings", "runs", "seconds"}, {"unsupported"})
        self.assertEqual(resp["findings"], [], resp)
        self.assertGreater(resp["runs"], 0, resp)


class DepsGraphEndToEnd(Operation):
    def test_deps_graph_answers_engine_deps_response(self):
        (self.cand / "a.py").write_text("import b\nimport nowhere_at_all\n", encoding="utf-8")
        (self.cand / "b.py").write_text("X = 1\n", encoding="utf-8")
        resp = self.run_main({"operation": "deps_graph", "candidate_dir": str(self.cand)})
        self.assertKeys(resp, {"schema_version", "edges", "unresolved"}, {"error"})
        self.assertEqual(resp["schema_version"], 1)
        self.assertIn({"from": "a.py", "to": "b.py"}, resp["edges"])
        self.assertTrue(any("nowhere_at_all" in u for u in resp["unresolved"]), resp)


class CaptureTracesEndToEnd(Operation):
    SOURCE = textwrap.dedent("""\
        def sign(x: int) -> int:
            if x < 0:
                return -1
            return 1
        """)
    TESTS = textwrap.dedent("""\
        import unittest
        from calc import sign


        class T(unittest.TestCase):
            def test_sign(self):
                self.assertEqual(sign(-5), -1)
                self.assertEqual(sign(5), 1)
        """)

    def test_capture_traces_answers_engine_traces_response(self):
        (self.base / "calc.py").write_text(self.SOURCE, encoding="utf-8")
        # the candidate changes sign's body without changing what it returns, so sign is a changed function
        (self.cand / "calc.py").write_text(self.SOURCE.replace("return 1\n", "return 1 + 0\n"), encoding="utf-8")
        (self.base / "test_calc.py").write_text(self.TESTS, encoding="utf-8")
        resp = self.run_main({
            "operation": "capture_traces", "base_dir": str(self.base), "candidate_dir": str(self.cand),
            "changed_files": ["calc.py"], "budget": {"timeout_s": 60}})
        self.assertKeys(resp, {"schema_version", "runner", "test_exit", "traces", "skipped", "error"},
                        {"branch_coverage"})
        self.assertIsNone(resp["error"], resp)
        self.assertEqual(resp["test_exit"], 0, resp)
        self.assertIn(resp["runner"], ("unittest", "pytest"))
        self.assertGreater(len(resp["traces"]), 0, resp)
        self.assertNotIn("targets", resp)


if __name__ == "__main__":
    unittest.main()
