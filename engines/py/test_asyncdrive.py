"""Tests of coroutine support (asyncdrive.py and the async branch of engine.compare). The diff cases run real CrossHair,
which is in the engine image:

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest test_asyncdrive -v   (from /engine)
"""
import asyncio
import json
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import asyncdrive  # noqa: E402
import engine  # noqa: E402


class Driver(unittest.TestCase):
    def setUp(self):
        patcher = mock.patch.dict(asyncdrive.stats, {side: {"returned": 0, "suspended": 0, "raised": {}}
                                                     for side in ("old", "new")})
        patcher.start()
        self.addCleanup(patcher.stop)

    def test_a_coroutine_runs_to_completion_through_an_await_that_sleeps(self):
        async def f(x: int) -> int:
            await asyncio.sleep(0.01)
            return x + 1
        self.assertEqual(asyncdrive.sync_of(f, "old")(2), 3)
        self.assertEqual(asyncdrive.stats["old"]["returned"], 1)

    def test_the_loop_is_there_for_the_code_that_needs_one(self):
        async def f(x: int) -> int:
            async def child(n: int) -> int:
                return n * 2
            a, b = await asyncio.gather(asyncio.create_task(child(x)), child(1))
            return a + b
        self.assertEqual(asyncdrive.sync_of(f, "new")(5), 12)

    def test_signature_and_annotations_are_the_originals(self):
        async def f(x: "int", *, y: str = "a") -> int:
            return x
        g = asyncdrive.sync_of(f, "old")
        self.assertEqual(g.__annotations__, f.__annotations__)
        self.assertIs(g.__wrapped__, f)

    def test_an_exception_is_raised_again_and_counted_by_type(self):
        async def f(x: int) -> int:
            raise KeyError(x)
        with self.assertRaises(KeyError):
            asyncdrive.sync_of(f, "old")(1)
        self.assertEqual(asyncdrive.stats["old"], {"returned": 0, "suspended": 0, "raised": {"KeyError": 1}})

    def test_a_future_nobody_sets_is_suspended_and_does_not_hang(self):
        async def f(x: int) -> int:
            await asyncio.get_running_loop().create_future()
            return x
        with mock.patch.object(asyncdrive, "STEP_TIMEOUT_S", 0.2), self.assertRaises(asyncdrive.Suspended):
            asyncdrive.sync_of(f, "old")(1)
        self.assertEqual(asyncdrive.stats["old"]["suspended"], 1)

    def test_an_async_generator_returns_what_it_yields(self):
        async def f(n: int):
            for i in range(n):
                yield i
        self.assertEqual(asyncdrive.sync_of(f, "old")(3), [0, 1, 2])

    def test_an_unbounded_async_generator_is_suspended(self):
        async def f(n: int):
            while True:
                yield n
        with self.assertRaises(asyncdrive.Suspended):
            asyncdrive.sync_of(f, "old")(1)

    def test_a_decorated_function_that_is_no_longer_a_coroutine_is_called_as_it_is(self):
        self.assertEqual(asyncdrive.sync_of(lambda x: x + 1, "old")(1), 2)

    def doubt(self, old, new):
        return asyncdrive.doubt_of(asyncdrive.STATS_PREFIX + json.dumps({"old": old, "new": new}))

    def test_a_pass_needs_completed_calls_on_both_sides(self):
        done = {"returned": 3, "suspended": 0, "raised": {}}
        self.assertIsNone(self.doubt(done, done))
        self.assertIn("no call of the new coroutine returned (ValueError)",
                      self.doubt(done, {"returned": 0, "suspended": 0, "raised": {"ValueError": 4}}))
        self.assertIn("did not finish", self.doubt({**done, "suspended": 1}, done))
        self.assertIn("reported no counts", asyncdrive.doubt_of("No differences found."))


class Diff(unittest.TestCase):
    """engine.diff over one module of async functions with real CrossHair."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        self.io, self.base, self.cand = root / "io", root / "base", root / "cand"
        for d in (self.io, self.base, self.cand):
            d.mkdir()
        for name, value in (("IO", self.io), ("WORK", root / "work")):
            patcher = mock.patch.object(engine, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    def judge(self, base: str, cand: str) -> dict:
        (self.base / "m.py").write_text(base, encoding="utf-8")
        (self.cand / "m.py").write_text(cand, encoding="utf-8")
        engine.diff({"base_dir": str(self.base), "candidate_dir": str(self.cand), "changed_files": ["m.py"],
                     "budget": {"per_condition_timeout_s": 10}})
        targets = json.loads((self.io / "response.json").read_text(encoding="utf-8"))["targets"]
        self.assertEqual(len(targets), 1, targets)
        return targets[0]

    HEAD = "import asyncio\n\n\n"

    def test_a_difference_after_an_await_is_a_counterexample(self):
        t = self.judge(self.HEAD + "async def f(x: int) -> int:\n    await asyncio.sleep(0.01)\n    return x\n",
                       self.HEAD + "async def f(x: int) -> int:\n    await asyncio.sleep(0.01)\n    return x + 1 if x == 5 else x\n")
        self.assertEqual(t["verdict"], "FAIL", t)
        self.assertEqual(t["counterexample"], {"args": "x=5", "old": "returns 5", "new": "returns 6"})

    def test_equivalent_coroutines_that_use_the_loop_pass(self):
        base = self.HEAD + ("async def child(n: int) -> int:\n    await asyncio.sleep(0)\n    return n + 1\n\n\n"
                            "async def f(x: int) -> int:\n    a, b = await asyncio.gather(child(x), child(1))\n    return a + b\n")
        t = self.judge(base, base.replace("a + b", "b + a"))
        self.assertEqual((t["verdict"], t["typed"]), ("PASS", True), t)
        self.assertGreater(t["search_budget"]["iterations"], 0)

    def test_string_annotations_are_typed(self):
        base = "from __future__ import annotations\nimport asyncio\n\n\nasync def f(x: int) -> int:\n    await asyncio.sleep(0)\n    return x\n"
        self.assertEqual(self.judge(base, base.replace("return x", "return x + 0"))["verdict"], "PASS")

    def test_a_call_that_never_finishes_is_not_a_pass(self):
        base = self.HEAD + "async def f(x: int) -> int:\n    await asyncio.get_running_loop().create_future()\n    return x\n"
        t = self.judge(base, base.replace("return x", "return x + 1"))
        self.assertEqual(t["verdict"], "INCONCLUSIVE", t)
        self.assertIn("did not finish", t["reason"])

    def test_a_pair_that_only_raises_is_not_a_pass(self):
        base = self.HEAD + "async def f(x: int) -> int:\n    await asyncio.sleep(0)\n    raise ValueError('no')\n"
        t = self.judge(base, base.replace("await asyncio.sleep(0)", "await asyncio.sleep(0.001)"))
        self.assertEqual(t["verdict"], "INCONCLUSIVE", t)
        self.assertIn("no call of the old coroutine returned (ValueError)", t["reason"])

    def test_an_async_generator_is_compared_by_what_it_yields(self):
        base = "async def f(n: int):\n    yield n\n"
        t = self.judge(base, base.replace("yield n", "yield n + 1"))
        self.assertEqual(t["verdict"], "FAIL", t)

    def test_untyped_parameters_are_inconclusive_for_the_missing_types(self):
        t = self.judge("async def f(x):\n    return x\n", "async def f(x):\n    return x + 1\n")
        self.assertEqual((t["verdict"], t["typed"]), ("INCONCLUSIVE", False))
        self.assertIn("no type annotations", t["reason"])


if __name__ == "__main__":
    unittest.main()
