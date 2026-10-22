"""Tests of engines/py/fuzz.py. Fakes replace the child processes, except the two marked as real Atheris runs."""

import contextlib
import io
import json
import subprocess
import sys
import tempfile
import textwrap
import typing
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import fuzz  # noqa: E402

try:
    import atheris  # noqa: F401
    HAVE_ATHERIS = True
except ImportError:
    HAVE_ATHERIS = False


class FakeFdp:
    """Stands in for atheris.FuzzedDataProvider: each Consume* call returns the next scripted value."""

    def __init__(self, script):
        self.script = list(script)
        self.calls = []

    def _next(self, name, *args):
        self.calls.append((name, args))
        return self.script.pop(0)

    def ConsumeBool(self):
        return self._next("ConsumeBool")

    def ConsumeInt(self, size):
        return self._next("ConsumeInt", size)

    def ConsumeIntInRange(self, low, high):
        return self._next("ConsumeIntInRange", low, high)

    def ConsumeFloat(self):
        return self._next("ConsumeFloat")

    def ConsumeUnicodeNoSurrogates(self, n):
        return self._next("ConsumeUnicodeNoSurrogates", n)

    def ConsumeBytes(self, n):
        return self._next("ConsumeBytes", n)


class ModuleName(unittest.TestCase):
    def test_package_init_is_the_package(self):
        self.assertEqual(fuzz.module_name("pkg/__init__.py"), "pkg")

    def test_nested_path_becomes_dotted(self):
        self.assertEqual(fuzz.module_name("a/b/c.py"), "a.b.c")


class ChangedPublicFunctions(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.base, self.cand = root / "base", root / "cand"
        self.base.mkdir()
        self.cand.mkdir()

    def write(self, root, rel, text):
        path = root / rel
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(textwrap.dedent(text), encoding="utf-8")

    def test_only_changed_or_new_typed_public_functions_are_targets(self):
        self.write(self.base, "m.py", """
            def same(x: int) -> int:
                return x

            def changed(x: int) -> int:
                return x
        """)
        self.write(self.cand, "m.py", """
            def same(x: int) -> int:
                return x

            def changed(x: int) -> int:
                return x + 1

            def new(s: str) -> str:
                return s

            def _private(x: int) -> int:
                return x

            async def coro(x: int) -> int:
                return x
        """)
        targets, unsupported = fuzz.changed_public_functions(self.base, self.cand, ["m.py"])
        self.assertEqual(sorted(t.qualified for t in targets), ["m.changed", "m.new"])
        self.assertEqual(unsupported, [])

    def test_untyped_parameter_is_listed_as_unsupported(self):
        self.write(self.cand, "m.py", """
            def loose(x):
                return x
        """)
        targets, unsupported = fuzz.changed_public_functions(self.base, self.cand, ["m.py"])
        self.assertEqual(targets, [])
        self.assertEqual(unsupported, [{"function": "m.loose", "reason": "parameter 'x' has no type annotation"}])

    def test_variadic_parameters_are_unsupported(self):
        self.write(self.cand, "m.py", """
            def many(*xs: int) -> int:
                return len(xs)
        """)
        _, unsupported = fuzz.changed_public_functions(self.base, self.cand, ["m.py"])
        self.assertEqual(unsupported[0]["function"], "m.many")

    def test_non_python_and_missing_files_are_ignored(self):
        self.write(self.cand, "notes.txt", "def f(x: int): pass\n")
        targets, unsupported = fuzz.changed_public_functions(self.base, self.cand, ["notes.txt", "gone.py"])
        self.assertEqual((targets, unsupported), ([], []))

    def test_unparsable_candidate_is_reported_not_raised(self):
        self.write(self.cand, "bad.py", "def broken(:\n")
        targets, unsupported = fuzz.changed_public_functions(self.base, self.cand, ["bad.py"])
        self.assertEqual(targets, [])
        self.assertEqual(unsupported[0]["function"], "bad.py")


class DecoderFor(unittest.TestCase):
    def test_scalars_use_the_matching_consume_call(self):
        fdp = FakeFdp([True, 7, 1.5, "s", b"b"])
        self.assertIs(fuzz.decoder_for(bool)(fdp), True)
        self.assertEqual(fuzz.decoder_for(int)(fdp), 7)
        self.assertEqual(fuzz.decoder_for(float)(fdp), 1.5)
        self.assertEqual(fuzz.decoder_for(str)(fdp), "s")
        self.assertEqual(fuzz.decoder_for(bytes)(fdp), b"b")
        self.assertEqual([name for name, _ in fdp.calls],
                         ["ConsumeBool", "ConsumeInt", "ConsumeFloat", "ConsumeUnicodeNoSurrogates", "ConsumeBytes"])

    def test_list_draws_a_length_then_items(self):
        fdp = FakeFdp([2, 10, 20])
        self.assertEqual(fuzz.decoder_for(list[int])(fdp), [10, 20])
        self.assertEqual(fdp.calls[0], ("ConsumeIntInRange", (0, fuzz.LIST_MAX)))

    def test_optional_draws_a_flag_first(self):
        self.assertIsNone(fuzz.decoder_for(typing.Optional[str])(FakeFdp([False])))
        self.assertEqual(fuzz.decoder_for(typing.Optional[str])(FakeFdp([True, "x"])), "x")

    def test_pipe_union_with_none_is_optional(self):
        self.assertIsNone(fuzz.decoder_for(int | None)(FakeFdp([False])))

    def test_fixed_tuple_and_dict(self):
        self.assertEqual(fuzz.decoder_for(tuple[int, bool])(FakeFdp([3, True])), (3, True))
        self.assertEqual(fuzz.decoder_for(dict[bool, int])(FakeFdp([1, True, 4])), {True: 4})

    def test_undecodable_type_raises_unsupported(self):
        with self.assertRaises(fuzz.Unsupported):
            fuzz.decoder_for(set[int])
        with self.assertRaises(fuzz.Unsupported):
            fuzz.decoder_for(int | str)

    def test_dict_with_unhashable_key_is_unsupported(self):
        with self.assertRaises(fuzz.Unsupported):
            fuzz.decoder_for(dict[list[int], int])
        with self.assertRaises(fuzz.Unsupported):
            fuzz.decoder_for(dict[tuple[int, list[int]], int])
        with self.assertRaises(fuzz.Unsupported):
            fuzz.decoder_for(dict[typing.Optional[list[int]], int])

    def test_dict_with_hashable_key_still_decodes(self):
        # the script is: entry count, then key parts, then value
        self.assertEqual(fuzz.decoder_for(dict[tuple[int, str], bool])(FakeFdp([1, 3, "k", True])),
                         {(3, "k"): True})
        self.assertEqual(fuzz.decoder_for(dict[str, int])(FakeFdp([1, "k", 5])), {"k": 5})


class RunWithFakeChildren(unittest.TestCase):
    """The parent logic over a scripted spawn: no child process runs."""

    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        self.io, self.base, self.cand = root / "io", root / "base", root / "cand"
        self.io.mkdir()
        self.base.mkdir()
        self.cand.mkdir()
        (self.cand / "m.py").write_text("def f(x: int) -> int:\n    return x\n", encoding="utf-8")
        self.calls = []

    def request(self, seconds=10, seed=42, files=("m.py",)):
        return {"base_dir": str(self.base), "candidate_dir": str(self.cand), "changed_files": list(files),
                "budget": {"seconds": seconds, "seed": seed}}

    def response(self):
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))

    def unsupported(self):
        return self.response()["unsupported"]

    def spawn(self, script):
        def fake(mode, spec, timeout):
            self.calls.append((mode, spec, timeout))
            return script(mode, spec, timeout)
        return fake

    def test_crash_becomes_a_finding_with_shrunk_input_seed_and_runs(self):
        def script(mode, spec, timeout):
            if mode == "fuzz":
                Path(spec["artifact_dir"], "crash-abc").write_bytes(b"\x01\x02\x03")
                Path(spec["artifact_dir"], fuzz.REPORT_NAME).write_text(
                    json.dumps({"exception": "ValueError", "message": "big"}), encoding="utf-8")
                return 77, "", "junk\nstat::number_of_executed_units: 123\n"
            self.assertEqual(spec["exception"], "ValueError")
            self.assertEqual(spec["input_hex"], "010203")
            return 0, json.dumps({"status": "shrunk", "input": "(5000,)", "error": "ValueError: big"}), ""

        fuzz.run(self.request(seconds=10, seed=42), self.io, self.spawn(script))
        resp = self.response()
        self.assertEqual(resp["findings"], [{"function": "m.f", "input": "(5000,)", "error": "ValueError: big",
                                             "seed": 42}])
        self.assertEqual(resp["runs"], 123)
        self.assertEqual(resp["schema_version"], 1)
        self.assertEqual(self.calls[0][1]["seed"], 42)
        self.assertEqual(self.calls[0][1]["seconds"], 10)
        self.assertEqual(self.calls[0][2], 10 + fuzz.GRACE_S)

    def test_failed_shrink_keeps_the_raw_input_as_hex(self):
        def script(mode, spec, timeout):
            if mode == "fuzz":
                Path(spec["artifact_dir"], "crash-x").write_bytes(b"\xff")
                return 77, "", ""
            return 1, "", "boom"

        fuzz.run(self.request(), self.io, self.spawn(script))
        finding = self.response()["findings"][0]
        self.assertEqual(finding["input"], "hex:ff")
        self.assertEqual(finding["error"], "libFuzzer crash")

    def test_clean_run_has_no_findings_and_counts_runs(self):
        def script(mode, spec, timeout):
            return 0, json.dumps({"status": "fuzzed"}), "stat::number_of_executed_units: 9000\n"

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"], [])
        self.assertEqual(self.response()["runs"], 9000)

    def test_unsupported_child_status_is_listed_not_fuzzed(self):
        def script(mode, spec, timeout):
            return 0, json.dumps({"status": "unsupported", "reason": "type set[int] is not decodable"}), ""

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"], [])
        self.assertEqual(self.unsupported(), [{"function": "m.f", "reason": "type set[int] is not decodable"}])

    def test_import_error_is_a_finding_with_the_import_marker(self):
        def script(mode, spec, timeout):
            return 0, json.dumps({"status": "import_error", "error": "ImportError: nope"}), ""

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"],
                         [{"function": "m.f", "input": "<import>", "error": "ImportError: nope", "seed": 42}])

    def test_timeout_is_a_finding(self):
        def script(mode, spec, timeout):
            raise subprocess.TimeoutExpired(cmd="fuzz", timeout=timeout)

        fuzz.run(self.request(seconds=3), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"][0]["input"], "<timeout>")

    def test_libfuzzer_hang_artifact_is_a_finding_and_is_not_shrunk(self):
        def script(mode, spec, timeout):
            if mode == "fuzz":
                Path(spec["artifact_dir"], "timeout-0123").write_bytes(b"\x0a")
                return 77, "", ""
            self.fail("a hang must not be shrunk: shrinking would rerun the hang")

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"], [{
            "function": "m.f", "input": "hex:0a",
            "error": f"libFuzzer timeout: one input ran past the {fuzz.INPUT_TIMEOUT_S} s limit", "seed": 42}])
        self.assertEqual([mode for mode, _, _ in self.calls], ["fuzz"])

    def test_oom_artifact_is_a_finding(self):
        def script(mode, spec, timeout):
            if mode == "fuzz":
                Path(spec["artifact_dir"], "oom-9").write_bytes(b"\x01")
                return 77, "", ""
            self.fail("an oom must not be shrunk")

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"][0]["error"], "libFuzzer oom: one input exhausted memory")

    def test_child_exit_zero_without_status_or_artifacts_is_a_finding(self):
        def script(mode, spec, timeout):
            return 0, "", ""

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"], [{
            "function": "m.f", "input": "<no-result>",
            "error": "fuzz child exited 0 without a status line", "seed": 42}])

    def test_clean_exit_on_libfuzzers_budget_line_is_fuzzed_not_a_finding(self):
        # atheris exits the child with 0 when -max_total_time expires, before the child can print its status
        def script(mode, spec, timeout):
            return 0, "", "INFO: Running\nDone 835172 runs in 3 second(s)\n"

        fuzz.run(self.request(), self.io, self.spawn(script))
        self.assertEqual(self.response()["findings"], [])
        self.assertEqual(self.response()["runs"], 835172)

    def test_unsupported_list_is_always_in_the_response(self):
        fuzz.run(self.request(files=()), self.io, self.spawn(lambda *a: (0, "", "")))
        self.assertEqual(self.response()["unsupported"], [])
        self.assertFalse((self.io / "fuzz_unsupported.json").exists())

    def test_child_that_dies_without_artifact_is_a_finding(self):
        def script(mode, spec, timeout):
            return 2, "", "segfault-like exit"

        fuzz.run(self.request(), self.io, self.spawn(script))
        finding = self.response()["findings"][0]
        self.assertEqual(finding["input"], "<child>")
        self.assertIn("exited 2", finding["error"])

    def test_budget_is_split_over_targets_with_a_one_second_floor(self):
        (self.cand / "m.py").write_text("def f(x: int) -> int:\n    return x\n"
                                        "def g(s: str) -> str:\n    return s\n", encoding="utf-8")

        def script(mode, spec, timeout):
            return 0, json.dumps({"status": "fuzzed"}), ""

        fuzz.run(self.request(seconds=10), self.io, self.spawn(script))
        self.assertEqual([spec["seconds"] for _, spec, _ in self.calls], [5, 5])

        self.calls.clear()
        fuzz.run(self.request(seconds=1), self.io, self.spawn(script))
        self.assertEqual([spec["seconds"] for _, spec, _ in self.calls], [1, 1])

    def test_no_targets_spawns_nothing(self):
        fuzz.run(self.request(files=()), self.io, self.spawn(lambda *a: (0, "", "")))
        self.assertEqual(self.calls, [])
        self.assertEqual(self.response()["runs"], 0)


@unittest.skipUnless(HAVE_ATHERIS, "needs atheris in the image")
class ChildLoadsTargets(unittest.TestCase):
    """The child decides, before Atheris starts, whether a target can be fuzzed. Real imports, no fuzzing."""

    def child_status(self, source, name):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        cand = Path(tmp.name)
        module = "mod_" + name
        (cand / (module + ".py")).write_text(textwrap.dedent(source), encoding="utf-8")
        spec = {"candidate_dir": str(cand), "module": module, "name": name,
                "artifact_dir": str(cand), "seed": 1, "seconds": 1}
        out = io.StringIO()
        with contextlib.redirect_stdout(out):
            self.assertEqual(fuzz.child_fuzz(spec), 0)
        return json.loads(out.getvalue().strip().splitlines()[-1])

    def test_annotation_imported_only_under_type_checking_is_unsupported(self):
        # with the lazy-annotations future import the module imports fine and only get_type_hints raises NameError;
        # without it the def itself raises at import, which is a broken candidate and stays an import_error
        status = self.child_status("""
            from __future__ import annotations
            from typing import TYPE_CHECKING
            if TYPE_CHECKING:
                from collections import OrderedDict

            def counts(table: OrderedDict[str, int]) -> int:
                return len(table)
        """, "counts")
        self.assertEqual(status["status"], "unsupported")
        self.assertIn("OrderedDict", status["reason"])

    def test_dict_with_unhashable_key_is_unsupported_not_a_crash(self):
        status = self.child_status("""
            def by_key(table: dict[list[int], int]) -> int:
                return len(table)
        """, "by_key")
        self.assertEqual(status["status"], "unsupported")


@unittest.skipUnless(HAVE_ATHERIS, "needs atheris in the image")
class RealAtherisRun(unittest.TestCase):
    """Real child processes: a planted bug is found and shrunk. Runs only where atheris is installed.

    Finding the planted bug depends on machine speed and on the seed: libFuzzer stops at max_total_time, so a
    slow machine may run fewer inputs. The seed fixes the input sequence, and 5 s was enough in the three
    consecutive runs recorded for this change (75 OK each time), but it is not a guarantee.
    """

    def test_clean_target_runs_its_budget_with_no_finding(self):
        # the child exits 0 without a status line once the budget runs out; libFuzzer's Done line makes it fuzzed
        root = Path(tempfile.mkdtemp(prefix="a2dlc-fuzz-test-"))
        base, cand, io = root / "base", root / "cand", root / "io"
        for d in (base, cand, io):
            d.mkdir()
        (cand / "clean.py").write_text("def check(x: int) -> int:\n    return x\n", encoding="utf-8")
        request = {"base_dir": str(base), "candidate_dir": str(cand), "changed_files": ["clean.py"],
                   "budget": {"seconds": 2, "seed": 7}}
        self.assertEqual(fuzz.run(request, io), 0)
        resp = json.loads((io / "response.json").read_text(encoding="utf-8"))
        self.assertEqual(resp["findings"], [], resp)
        self.assertEqual(resp.get("unsupported", []), [], resp)
        self.assertGreater(resp["runs"], 0)

    def test_planted_bug_is_found_and_shrunk(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        root = Path(tmp.name)
        base, cand, io = root / "base", root / "cand", root / "io"
        base.mkdir()
        cand.mkdir()
        io.mkdir()
        (cand / "planted.py").write_text(textwrap.dedent("""
            def check(x: int) -> int:
                if x > 2 ** 40:
                    raise ValueError("big")
                return x
        """), encoding="utf-8")
        request = {"base_dir": str(base), "candidate_dir": str(cand), "changed_files": ["planted.py"],
                   "budget": {"seconds": 5, "seed": 7}}
        self.assertEqual(fuzz.run(request, io), 0)
        resp = json.loads((io / "response.json").read_text(encoding="utf-8"))
        self.assertEqual(len(resp["findings"]), 1, resp)
        finding = resp["findings"][0]
        self.assertEqual(finding["function"], "planted.check")
        self.assertEqual(finding["error"], "ValueError: big")
        self.assertEqual(finding["seed"], 7)
        # the shrunk input is the decoded argument tuple, not the raw-bytes fallback
        self.assertTrue(finding["input"].startswith("("), finding)
        self.assertGreater(int(finding["input"].strip("(),")), 2 ** 40)
        self.assertGreater(resp["runs"], 0)


if __name__ == "__main__":
    unittest.main()
