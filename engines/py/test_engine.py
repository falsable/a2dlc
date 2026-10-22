"""Tests of the Python engine (engine.py). They need pyflakes, which is in the engine image:

    docker run --rm --network none -e PYTHONDONTWRITEBYTECODE=1 -v "<repo>/engines/py:/engine:ro" \
        --entrypoint python <engine image> -m unittest discover -s /engine -p "test_*.py"

Each case comes from a review finding; the name says which invariant it guards.
"""
import ast
import itertools
import json
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))
import engine  # noqa: E402

LATIN1 = "# -*- coding: latin-1 -*-\n"


def chain(n: int) -> str:
    return "def f(x):\n    return " + "+".join(["x"] * n) + "\n"


class Directory(unittest.TestCase):
    """One temporary /io, base and candidate; engine.IO points to /io."""

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        root = Path(self.tmp.name)
        self.io, self.base, self.cand = root / "io", root / "base", root / "cand"
        for d in (self.io, self.base, self.cand):
            d.mkdir()
        patcher = mock.patch.object(engine, "IO", self.io)
        patcher.start()
        self.addCleanup(patcher.stop)
        self.addCleanup(self.tmp.cleanup)

    def req(self, files, **extra):
        return {"base_dir": str(self.base), "candidate_dir": str(self.cand), "changed_files": files, **extra}

    def response(self):
        return json.loads((self.io / "response.json").read_text(encoding="utf-8"))


class Tier0(unittest.TestCase):
    def test_base_already_broken_does_not_trap_loop(self):
        # README: «a file that was already broken does not trap the loop»
        base, cand = "x = 1\nprint 'hello'\n", "x = 2\nprint 'hello'\n"
        self.assertEqual(engine.findings_of(base, cand, "old.py"), [])

    def test_new_compile_is_reported(self):
        r = engine.findings_of("x = 1\n", "x = (\n", "a.py")
        self.assertEqual([h["kind"] for h in r], ["compile"])

    def test_broken_base_other_error_does_not_mute_new(self):
        r = engine.findings_of("print 'hello'\n", "def f(:\n    pass\n", "a.py")
        self.assertEqual([h["kind"] for h in r], ["compile"])

    def test_only_new_lint_findings_count(self):
        base = "def f():\n    return old_name\n"
        cand = "def f():\n    return old_name\n\n\ndef g():\n    return new_name\n"
        r = engine.findings_of(base, cand, "m.py")
        self.assertEqual([(h["kind"], h["line"], h["message"]) for h in r], [("lint", 6, "undefined name 'new_name'")])

    def test_long_expression_does_not_crash_pyflakes(self):
        self.assertIsInstance(engine.findings_of(chain(1500), chain(1500), "g.py"), list)

    def test_pyflakes_without_stack_reports_nothing_and_does_not_crash(self):
        with mock.patch.object(engine, "_pyflakes", side_effect=RecursionError):
            self.assertEqual(engine.findings_of(None, "x = 1\n", "a.py"), [])


class Policy(unittest.TestCase):
    """Tier 0 AST policy (protocol §5 Stage 3 step 2): forbidden imports, unbounded loops, reflection, complexity
    and header. Only what is new with respect to the base, except the header."""

    def messages(self, text, base=None, **policy):
        return [(h["kind"], h["line"], h["message"]) for h in
                engine.findings_policy(base, text, "m.py", engine.policy_of(policy))]

    def test_forbidden_import_covers_submodules_and_from(self):
        r = self.messages("import os.path\nfrom subprocess import run\nimport json\n", forbidden_imports=["os", "subprocess"])
        self.assertEqual([(k, l) for k, l, _ in r], [("policy", 1), ("policy", 2)])
        self.assertIn("os.path (os)", r[0][2])

    def test_import_already_in_base_is_not_builders_fault(self):
        base = "import pickle\n"
        self.assertEqual(self.messages("import pickle\nimport pickle\n", base, forbidden_imports=["pickle"])[0][1], 2)
        self.assertEqual(self.messages("import pickle\nx = 1\n", base, forbidden_imports=["pickle"]), [])

    def test_while_true_without_break_is_unbounded_loop(self):
        self.assertEqual([k for k, _, _ in self.messages("while True:\n    pass\n")], ["policy"])
        self.assertEqual(self.messages("while True:\n    if x:\n        break\n"), [])
        self.assertEqual(self.messages("while x:\n    pass\n"), [])

    def test_nested_loop_break_does_not_bound_outer(self):
        r = self.messages("while 1:\n    for i in y:\n        break\n")
        self.assertEqual([(k, l) for k, l, _ in r], [("policy", 1)])

    def test_reflection_eval_exec_import_and_dynamic_getattr(self):
        r = self.messages("eval('1')\nexec(s)\n__import__('os')\nimportlib.import_module(n)\ngetattr(o, name)\ngetattr(o, 'fixed')\n")
        self.assertEqual([l for _, l, _ in r], [1, 2, 3, 4, 5])
        self.assertTrue(all(k == "policy" and m.startswith("reflection") for k, _, m in r))

    def test_complexity_over_max_per_function(self):
        fn = "def f(x):\n" + "".join(f"    if x == {i}:\n        return {i}\n" for i in range(4)) + "    return 0\n"
        self.assertEqual(self.messages(fn, max_complexity=4)[0][2][:37], "cyclomatic complexity 5 of f exceeds ")
        self.assertEqual(self.messages(fn, max_complexity=5), [])
        self.assertEqual(self.messages(fn), [])  # 15 by default

    def test_required_header_not_filtered_against_base(self):
        missing = [("header", 1, f"missing header @a2dlc:{k} (require_header)") for k in engine.HEADER_KEYS]
        self.assertEqual(self.messages("x = 1\n", "x = 0\n", require_header=True), missing)
        self.assertEqual(self.messages(FULL_HEADER + "x = 1\n", require_header=True), [])
        self.assertEqual(self.messages("x = 1\n"), [])

    def test_unparsable_text_does_not_duplicate_compile(self):
        self.assertEqual(self.messages("def f(:\n", forbidden_imports=["os"]), [])

    def test_request_policy_fills_defaults(self):
        policy = engine.policy_of({"forbidden_imports": ["os"], "max_complexity": None})
        self.assertEqual((policy["forbidden_imports"], policy["require_header"], policy["max_complexity"]), (["os"], False, 15))


FULL_HEADER = ('# @a2dlc:slice_id = "billing.ledger.v1"\n'
               '# @a2dlc:mode = "greenfield"\n'
               '# @a2dlc:contract_hash = null\n'
               '# @a2dlc:rationale = "Integer cents, so the totals are exact."\n'
               '# @a2dlc:rejected_alternatives = ["float64"]\n'
               '# @a2dlc:protected_invariants = ["INV-01"]\n'
               '# @a2dlc:do_not_refactor = []\n')


class HeaderLint(unittest.TestCase):
    """The seven keys of builder_header, each once; rationale not empty; the list keys JSON arrays of strings."""

    def test_complete_header_passes_with_null_or_string_contract_hash(self):
        self.assertEqual(engine.header_problems(FULL_HEADER), [])
        self.assertEqual(engine.header_problems(FULL_HEADER.replace("null", '"sha256:ab"')), [])

    def test_missing_rationale_is_a_finding(self):
        text = FULL_HEADER.replace('# @a2dlc:rationale = "Integer cents, so the totals are exact."\n', "")
        self.assertEqual(engine.header_problems(text), ["missing header @a2dlc:rationale (require_header)"])

    def test_empty_rationale_is_a_finding(self):
        text = FULL_HEADER.replace("Integer cents, so the totals are exact.", "   ")
        self.assertEqual(engine.header_problems(text), ["header @a2dlc:rationale is empty"])

    def test_list_keys_must_be_json_arrays_of_strings(self):
        text = FULL_HEADER.replace('["float64"]', '"float64"').replace("[\"INV-01\"]", "[1]")
        self.assertEqual(engine.header_problems(text), [
            "header @a2dlc:rejected_alternatives is not a JSON array of strings",
            "header @a2dlc:protected_invariants is not a JSON array of strings"])

    def test_repeated_key_is_a_finding(self):
        self.assertEqual(engine.header_problems(FULL_HEADER + '# @a2dlc:mode = "legacy"\n'),
                         ["repeated header @a2dlc:mode: declared 2 times"])

    def test_value_that_is_not_json_is_a_finding(self):
        self.assertEqual(engine.header_problems(FULL_HEADER.replace('"billing.ledger.v1"', "billing")),
                         ["header @a2dlc:slice_id is not valid JSON"])

    def test_contract_hash_must_be_string_or_null(self):
        self.assertEqual(engine.header_problems(FULL_HEADER.replace("null", "12")),
                         ["header @a2dlc:contract_hash is neither a JSON string nor null"])


class SideEffects(unittest.TestCase):
    """forbidden_side_effects (intent.boundary): heuristics per category, each one with the false negatives its comment names."""

    def hits(self, text, *categories):
        return engine.policy_violations(text, {"forbidden_side_effects": list(categories)})

    def test_direct_clock_reads_only_when_the_category_is_forbidden(self):
        text = "import time\nstamp = time.time()\nwhen = datetime.now()\nok = time.monotonic()\n"
        self.assertEqual(self.hits(text), [])
        self.assertEqual(self.hits(text, "time.system_clock_direct"), [
            (2, "side effect time.system_clock_direct: call to time.time"),
            (3, "side effect time.system_clock_direct: call to datetime.now")])

    def test_writes_are_open_in_write_mode_pathlib_writes_and_os_mutations(self):
        text = ("open('a.txt', 'w')\nopen('b.txt', mode='ab')\nopen('c.txt')\nopen('d.txt', m)\n"
                "Path(p).write_text('x')\nos.remove(p)\n")
        self.assertEqual([line for line, _ in self.hits(text, "fs.write")], [1, 2, 5, 6])

    def test_outbound_http_imports_and_calls(self):
        text = "import requests\nrequests.get(u)\nurllib.request.urlopen(u)\nimport json\n"
        self.assertEqual([line for line, _ in self.hits(text, "network.http.outbound")], [1, 2, 3])

    def test_sql_built_by_fstring_percent_or_concatenation(self):
        text = "cur.execute(f'select {x}')\ncur.execute('select ' + x)\ncur.execute('select %s' % x)\ncur.execute('select ?', (x,))\n"
        self.assertEqual([line for line, _ in self.hits(text, "db.raw_sql_unparameterized")], [1, 2, 3])

    def test_more_fs_writes_are_flagged(self):
        text = "os.unlink(p)\nos.replace(a, b)\nshutil.rmtree(d)\nshutil.copy2(a, b)\nPath(p).unlink()\nq.mkdir()\nos.chmod(p, 0)\n"
        self.assertEqual([line for line, _ in self.hits(text, "fs.write")], [1, 2, 3, 4, 5, 6])  # chmod is a known gap

    def test_format_sql_only_as_the_argument_itself(self):
        text = "cur.execute('select {}'.format(x))\nq = f'select {x}'\ncur.execute(q)\n"
        self.assertEqual([line for line, _ in self.hits(text, "db.raw_sql_unparameterized")], [1])  # variable: a known gap

    def test_only_new_side_effects_are_findings(self):
        text = "import requests\n"
        policy = engine.policy_of({"forbidden_side_effects": ["network.http.outbound"]})
        self.assertEqual(engine.findings_policy(text, text, "m.py", policy), [])
        self.assertEqual([h["kind"] for h in engine.findings_policy(None, text, "m.py", policy)], ["policy"])


class TypeDeadBranches(Directory):
    """Type-guaranteed dead branches: mypy --warn-unreachable, one run per mutate call (protocol §5 Stage 5, step 2)."""
    NARROWED = "def f(x: int) -> int:\n    if isinstance(x, int):\n        return x + 1\n    return x - 1\n"

    def mutate(self, text, lines):
        (self.cand / "g.py").write_text(text, encoding="utf-8")
        engine.mutate(self.req(["g.py"], budget={"changed_lines": {"g.py": lines}}))
        return self.response()

    def test_branch_narrowed_to_never_is_pruned_with_its_reason(self):
        # line 3 is the reachable return, line 4 the one narrowing made dead
        r = self.mutate(self.NARROWED, [3, 4])
        self.assertEqual(r["pruned"], [{"file": "g.py", "line": 4, "reason": engine.REASON_TYPE}])
        self.assertEqual({m["line"] for m in r["mutants"]}, {3})
        self.assertNotIn(4, {m["line"] for m in r["mutants"]})

    def test_code_that_is_only_never_called_is_not_pruned(self):
        r = self.mutate("def f(x: int) -> int:\n    return x + 1\n\n\ndef g(x: int) -> int:\n    return x - 1\n", [2, 6])
        self.assertEqual(r["pruned"], [])
        self.assertEqual({m["line"] for m in r["mutants"]}, {2, 6})

    def test_span_runs_from_the_first_unreachable_statement_to_the_end_of_its_block(self):
        text = "def f(x: int) -> int:\n    if isinstance(x, int):\n        return x + 1\n    y = x - 1\n    return y * 2\n"
        r = self.mutate(text, [4, 5])
        self.assertEqual(r["pruned"], [{"file": "g.py", "line": 4, "reason": engine.REASON_TYPE},
                                       {"file": "g.py", "line": 5, "reason": engine.REASON_TYPE}])
        self.assertEqual(r["mutants"], [])

    def test_mypy_that_does_not_run_leaves_the_mutants_unpruned(self):
        with mock.patch.object(engine, "_mypy", return_value=None):
            r = self.mutate(self.NARROWED, [3, 4])
        self.assertEqual(r["pruned"], [])
        self.assertEqual({m["line"] for m in r["mutants"]}, {3, 4})

    def test_reason_is_the_contract_prose(self):
        self.assertEqual(engine.REASON_TYPE, "equivalent: type-guaranteed dead branch")


class Shrink(Directory):
    """CB-01 minimal counterexample: the literal arguments shrink while the two functions still disagree (real subprocess)."""

    def setUp(self):
        super().setUp()
        patcher = mock.patch.object(engine, "WORK", self.io.parent / "work")
        patcher.start()
        self.addCleanup(patcher.stop)
        for package in ("a2dlc_base", "a2dlc_cand"):
            (engine.WORK / package).mkdir(parents=True)

    def test_shrinks_to_the_smallest_input_that_still_diverges(self):
        (engine.WORK / "a2dlc_base" / "m.py").write_text("def f(x: int) -> int:\n    return x\n", encoding="utf-8")
        (engine.WORK / "a2dlc_cand" / "m.py").write_text("def f(x: int) -> int:\n    return x + (1 if x > 1000 else 0)\n", encoding="utf-8")
        found = {"args": "x = 5000", "old": "returns 5000", "new": "returns 5001"}
        self.assertEqual(engine.shrink_counterexample("m", "f", found, {}),
                         {"args": "x=1001", "old": "returns 1001", "new": "returns 1002"})

    def test_arguments_that_are_not_literals_keep_crosshairs_counterexample(self):
        found = {"args": "x = g()", "old": "returns 1", "new": "returns 2"}
        self.assertEqual(engine.shrink_counterexample("m", "f", found, {}), found)

    def test_relaxations_judge_the_shrink_too(self):
        """Under ignore_order the pair is equivalent for every x: the shrink must not stop on it, and CrossHair's stays."""
        (engine.WORK / "a2dlc_base" / "m.py").write_text("def f(x: int) -> list:\n    return [x, 1]\n", encoding="utf-8")
        (engine.WORK / "a2dlc_cand" / "m.py").write_text(
            "def f(x: int) -> list:\n    return [1, x] if x > 1000 else [x, 1]\n", encoding="utf-8")
        found = {"args": "x = 5000", "old": "returns [5000, 1]", "new": "returns [1, 5000]"}
        self.assertEqual(engine.shrink_counterexample("m", "f", found, {}),
                         {"args": "x=1001", "old": "returns [1001, 1]", "new": "returns [1, 1001]"})
        self.assertEqual(engine.shrink_counterexample("m", "f", found, engine.relaxations(["ignore_order"])[0]), found)

    def test_shrink_without_budget_left_keeps_crosshairs_counterexample(self):
        found = {"args": "x = 5000", "old": "returns 5000", "new": "returns 5001"}
        self.assertEqual(engine.shrink_counterexample("m", "f", found, {}, 0.5), found)


class Budget(Directory):
    """The diff operation: budget.total_s cuts the targets that start after it, in priority order; budget.workers does not
    change the response. CrossHair is faked; the clock is a counter, so the cut is the same on every run."""

    BASE = ("def leaf(x: int) -> int:\n    return x\n\ndef hub(x: int) -> int:\n    return x\n\n"
            "def caller1(x: int) -> int:\n    return hub(x)\n\ndef caller2(x: int) -> int:\n    return hub(x)\n")
    CAND = ("def leaf(x: int) -> int:\n    return x + 0\n\ndef hub(x: int) -> int:\n    return x + 0\n\n"
            "def caller1(x: int) -> int:\n    return hub(x) + 0\n\ndef caller2(x: int) -> int:\n    return hub(x) + 0\n")

    def setUp(self):
        super().setUp()
        patcher = mock.patch.object(engine, "WORK", self.io.parent / "work")
        patcher.start()
        self.addCleanup(patcher.stop)
        patcher = mock.patch.object(engine, "run", side_effect=self.fake_run)
        patcher.start()
        self.addCleanup(patcher.stop)

    @staticmethod
    def fake_run(cmd, timeout):
        if cmd[1] == "-c":  # the import check
            return 0, ""
        time.sleep(0.001 * (len(cmd[-1]) % 3))  # with workers, the runs finish out of order
        return 0, "No differences found. (attempted 3 iterations)"

    def diff(self, base, cand, budget=None, clock=None):
        (self.base / "m.py").write_text(base, encoding="utf-8")
        (self.cand / "m.py").write_text(cand, encoding="utf-8")
        kwargs = {} if clock is None else {"clock": clock}
        engine.diff(self.req(["m.py"], budget=budget or {}), **kwargs)
        return self.response()

    def test_targets_run_by_callers_then_name_and_the_rest_are_cut_with_a_reason(self):
        ticks = itertools.count()  # start 0, deadline 1.5, then one tick per job: only the first job starts in time
        r = self.diff(self.BASE, self.CAND, budget={"total_s": 1.5}, clock=lambda: next(ticks))
        by_name = {t["target"].split("::")[1]: t for t in r["targets"]}
        self.assertEqual(by_name["hub"]["verdict"], "PASS")
        for name in ("leaf", "caller1", "caller2"):
            self.assertEqual((by_name[name]["verdict"], by_name[name]["reason"]), ("INCONCLUSIVE", "not run: budget"))
        self.assertEqual(list(by_name), ["leaf", "hub", "caller1", "caller2"])  # request order, nothing omitted

    def test_without_total_s_every_target_runs(self):
        r = self.diff(self.BASE, self.CAND)
        self.assertEqual({t["verdict"] for t in r["targets"]}, {"PASS"})
        self.assertEqual(len(r["targets"]), 4)

    def test_workers_do_not_change_the_response_bytes(self):
        self.diff(self.BASE, self.CAND, budget={"workers": 1})
        one = (self.io / "response.json").read_bytes()
        self.diff(self.BASE, self.CAND, budget={"workers": 2})
        two = (self.io / "response.json").read_bytes()
        self.assertEqual(one, two)

    def test_callers_are_compared_only_when_a_changed_function_is_inconclusive(self):
        untyped = "def helper(x):\n    return x + 0\n\ndef user(x: int) -> int:\n    return helper(x)\n"
        base = "def helper(x):\n    return x\n\ndef user(x: int) -> int:\n    return helper(x)\n"
        r = self.diff(base, untyped)
        self.assertEqual([(t["target"], t["verdict"]) for t in r["targets"]],
                         [("m.py::helper", "INCONCLUSIVE"), ("m.py::user", "PASS")])
        typed = "def helper(x: int) -> int:\n    return x + 0\n\ndef user(x: int) -> int:\n    return helper(x)\n"
        r = self.diff(base.replace("def helper(x):", "def helper(x: int) -> int:"), typed)
        self.assertEqual([t["target"] for t in r["targets"]], ["m.py::helper"])

    def test_diff_budget_names_every_target_the_budget_cut(self):
        ticks = itertools.count()  # start 0, deadline 1.5; hub starts at tick 1, the rest after it; the end is tick 5
        r = self.diff(self.BASE, self.CAND, budget={"total_s": 1.5}, clock=lambda: next(ticks))
        self.assertEqual(r["diff_budget"], {"total_s": 1.5, "used_s": 5, "cut": [
            {"target": "m.py::leaf", "reason": "not run: budget"},
            {"target": "m.py::caller1", "reason": "not run: budget"},
            {"target": "m.py::caller2", "reason": "not run: budget"}]})

    def test_without_total_s_there_is_no_diff_budget(self):
        self.assertNotIn("diff_budget", self.diff(self.BASE, self.CAND))

    def test_workers_are_clamped_to_the_memory_bound(self):
        self.assertEqual([engine.workers_of({"workers": w}) for w in (None, 0, 1, 2, 100)], [1, 1, 1, 2, engine.MAX_WORKERS])
        self.assertEqual(engine.MAX_WORKERS, 2)


class Typecheck(Directory):
    """mypy --strict per changed file: only errors that are new with respect to the base; if mypy does not run, tooling."""

    def write(self, root, file, text):
        p = root / file
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text, encoding="utf-8")

    def test_new_unannotated_function_is_typecheck_and_base_one_is_not(self):
        self.write(self.base, "src/m.py", "def old_fn(x):\n    return x\n")
        self.write(self.cand, "src/m.py", "def old_fn(x):\n    return x\n\n\ndef new_fn(y):\n    return y\n")
        r = engine.findings_typecheck(self.base, self.cand, ["src/m.py"])
        self.assertEqual([(h["kind"], h["line"]) for h in r], [("typecheck", 5)])
        self.assertIn("[no-untyped-def]", r[0]["message"])

    def test_annotated_and_correct_reports_nothing(self):
        self.write(self.cand, "src/m.py", "def subtract(a: int, b: int) -> int:\n    return a - b\n")
        self.assertEqual(engine.findings_typecheck(self.base, self.cand, ["src/m.py"]), [])

    def test_new_type_error_is_reported_with_its_line(self):
        self.write(self.cand, "src/m.py", "def f(a: int) -> int:\n    return a\n\n\nx: int = f('no')\n")
        r = engine.findings_typecheck(self.base, self.cand, ["src/m.py"])
        self.assertEqual([(h["kind"], h["line"]) for h in r], [("typecheck", 5)])

    def test_mypy_down_is_tooling_not_clean(self):
        self.write(self.cand, "src/m.py", "x = 1\n")
        with mock.patch.object(engine, "_mypy", return_value=None):
            r = engine.findings_typecheck(self.base, self.cand, ["src/m.py"])
        self.assertEqual([(h["kind"], h["file"], h["line"]) for h in r], [("tooling", "src/m.py", None)])

    def test_tier0_joins_compile_policy_and_typecheck(self):
        self.write(self.cand, "a.py", "import pickle\n\n\ndef f(x):\n    return x\n")
        engine.tier0(self.req(["a.py"], budget={"probes": [], "policy": {"forbidden_imports": ["pickle"]}}))
        r = self.response()
        self.assertFalse(r["ok"])
        self.assertEqual(sorted(h["kind"] for h in r["findings"]), ["policy", "typecheck"])


class Mutate(unittest.TestCase):
    def test_operator_on_unchanged_line_is_not_mutated(self):
        text = "def f(a, b):\n    x = (a\n         < b)\n    return x\n"
        self.assertEqual([m for m in engine.mutants_of(text, {2}) if m[1] == "comparison"], [])

    def test_operator_on_requested_line_is_mutated_and_declared(self):
        text = "def f(a, b):\n    x = (a\n         < b)\n    return x\n"
        r = [m for m in engine.mutants_of(text, {3}) if m[1] == "comparison"]
        self.assertEqual([(m[0], m[2]) for m in r], [(3, "comparison < changed to <=")])
        self.assertEqual(r[0][3], "def f(a, b):\n    x = (a\n         <= b)\n    return x\n")

    def test_arithmetic_and_multiline_if(self):
        text = "def f(a, b):\n    if (a >\n            b):\n        return a +\\\n            b\n"
        for line, op in ((2, "comparison"), (4, "arithmetic")):
            r = engine.mutants_of(text, {line})
            self.assertTrue([m for m in r if m[1] == op and m[0] == line], (line, op))
        for line, op in ((3, "comparison"), (5, "arithmetic")):
            self.assertEqual([m for m in engine.mutants_of(text, {line}) if m[1] == op], [])

    def test_mutant_does_not_shift_lines(self):
        text = "def f(a, b):\n    x = (a\n         + b)\n    return x\n"
        for _, _, _, mutated in engine.mutants_of(text, {3}):
            self.assertEqual(len(mutated.splitlines()), len(text.splitlines()))

    def test_power_to_product_keeps_structure(self):
        mutated = [m for m in engine.mutants_of("y = a / b ** c\n", {1}) if m[2].startswith("arithmetic operator **")][0][3]
        self.assertEqual(ast.dump(ast.parse(mutated)), ast.dump(ast.parse("y = a / (b * c)")))

    def test_two_word_comparisons(self):
        r = {m[2]: m[3] for m in engine.mutants_of("y = a is not b\nz = a not in b\n", {1, 2}) if m[1] == "comparison"}
        self.assertEqual(r, {"comparison is not changed to is": "y = a is b\nz = a not in b\n",
                             "comparison not in changed to in": "y = a is not b\nz = a in b\n"})

    def test_aug_assign_changes_only_the_operator(self):
        r = [m for m in engine.mutants_of("def f(x):\n    x += (1 +\n        2)\n    return x\n", {2}) if m[1] == "arithmetic"]
        self.assertIn("    x -= (1 +\n        2)\n", [m[3].split("def f(x):\n")[1].split("    return")[0] for m in r])

    def test_long_chain_no_longer_deepcopies(self):
        self.assertTrue(engine.mutants_of(chain(400), {2}))

    def test_logging_line_not_mutated_and_listed(self):
        text = "def f(a, b):\n    logger.info('x %s', a + b)\n    return a\n"
        self.assertEqual(engine.mutants_of(text, {2}), [])
        self.assertEqual(engine.pruned_of(text, {2}), [(2, engine.REASON_LOG)])

    def test_logging_of_other_object_not_pruned(self):
        text = "def f(a):\n    self.logger.info(a + 1)\n    return a\n"
        self.assertTrue(engine.mutants_of(text, {2}))
        self.assertEqual(engine.pruned_of(text, {2}), [])

    def test_assert_not_mutated_and_listed(self):
        text = "def f(a):\n    assert a > 0, 'bad'\n    return a\n"
        self.assertEqual(engine.mutants_of(text, {2}), [])
        self.assertEqual(engine.pruned_of(text, {2}), [(2, engine.REASON_ASSERT)])

    def test_dead_branch_if_false_not_mutated_but_condition_is(self):
        text = "def f(a):\n    if False:\n        return a + 1\n    return a\n"
        self.assertEqual(engine.mutants_of(text, {3}), [])
        self.assertEqual(engine.pruned_of(text, {3}), [(3, engine.REASON_BRANCH)])
        self.assertEqual(engine.pruned_of(text, {2}), [])
        self.assertTrue(engine.mutants_of(text, {2}))

    def test_else_of_if_true_is_dead_branch(self):
        text = "def f(a):\n    if True:\n        return a\n    else:\n        return a - 1\n"
        self.assertEqual(engine.mutants_of(text, {5}), [])
        self.assertEqual(engine.pruned_of(text, {5}), [(5, engine.REASON_BRANCH)])
        self.assertEqual(engine.pruned_of(text, {3}), [])

    def test_while_false_and_if_zero_are_dead_branches(self):
        for text in ("def f(a):\n    while False:\n        a += 1\n    return a\n",
                      "def f(a):\n    if 0:\n        a += 1\n    return a\n"):
            self.assertEqual(engine.mutants_of(text, {3}), [], text)
            self.assertEqual(engine.pruned_of(text, {3}), [(3, engine.REASON_BRANCH)], text)

    def test_non_constant_condition_not_pruned(self):
        text = "def f(a):\n    if 1:\n        return a + 1\n    return a\n"
        self.assertTrue(engine.mutants_of(text, {3}))
        self.assertEqual(engine.pruned_of(text, {3}), [])


class Stub(unittest.TestCase):
    def test_new_module_with_constants_reports_symbols_and_nullifies(self):
        text, symbols, unstubbed = engine.stub_of(None, "LIMIT = 10\nTABLE = {'a': 1}\n")
        self.assertEqual((symbols, unstubbed), (["LIMIT", "TABLE"], set()))
        self.assertEqual(text, "LIMIT = None\nTABLE = None\n")

    def test_new_module_with_nothing_to_name_writes_no_stub(self):
        self.assertEqual(engine.stub_of(None, "import os\n"), (None, [], set()))

    def test_new_module_keeps_computed_and_propagates_none(self):
        text, symbols, _ = engine.stub_of(None, "__all__ = ['f']\nA = 2\nB = A * 2\nBase = make_base()\n\n\ndef f(x=A):\n    return x\n")
        self.assertEqual(symbols, ["A", "B", "Base", "f"])
        self.assertIn("A = None\nB = None\nBase = make_base()", text)
        self.assertIn("__all__ = ['f']", text)
        self.assertIn("def f(x=None):\n    raise NotImplementedError('a2dlc stub')", text)

    def test_new_class_does_not_leak_attributes_or_defaults(self):
        text, symbols, _ = engine.stub_of("def f():\n    return 1\n",
                                            "def f():\n    return 1\n\n\nclass Config:\n    LIMIT = 10\n    DOUBLE = LIMIT * 2\n"
                                            "    name: str = 'x'\n\n    def g(self, x=99, *, k=3):\n        return x\n")
        self.assertEqual(symbols, ["Config"])
        self.assertIn("LIMIT = None\n    DOUBLE = None\n    name = None", text)
        self.assertIn("def g(self, x=None, *, k=None):", text)
        self.assertNotIn("10", text)
        self.assertNotIn("99", text)

    def import_(self, base, cand):
        text, _, _ = engine.stub_of(base, cand)
        space: dict = {}
        exec(compile(text, "stub", "exec"), space)
        return space

    def test_classes_with_bases_keep_attributes_and_stub_imports(self):
        e = self.import_(None, "from enum import Flag, IntEnum\n\n\nclass S(IntEnum):\n    OK = 0\n\n\nclass F(Flag):\n"
                               "    A = 1\n    B = 2\n\n\nclass M(dict, metaclass=type):\n    abstract = True\n")
        self.assertEqual((int(e["S"].OK), e["F"].B.value, e["M"].abstract), (0, 2, True))

    def test_nested_and_based_class_in_existing_module_untouched(self):
        text, _, _ = engine.stub_of("x = 1\n", "x = 1\n\n\nclass K(dict):\n    N = 3\n\n    class Meta:\n        abstract = True\n")
        self.assertIn("N = 3", text)
        self.assertIn("abstract = True", text)

    def test_unpacking_nullified_name_does_not_blow_up(self):
        e = self.import_(None, "X = (1, 2)\nA, B = X\nC, D = 3, 4\n\n\nclass K:\n    P = A\n    Q = X[0]\n")
        self.assertEqual((e["A"], e["B"], e["C"], e["D"], e["K"].P, e["K"].Q), (None,) * 6)

    def test_stub_module_is_importable(self):
        text, _, _ = engine.stub_of(None, "class C:\n    A = 1\n    B = A + 1\n\n    def m(self, x=B):\n        return x\n")
        space: dict = {}
        exec(compile(text, "stub", "exec"), space)
        self.assertIsNone(space["C"].B)


class Dispatch(Directory):
    """main() routes each operation to its module; the modules are other packages' files, so they are faked here."""

    def run_main(self, request):
        (self.io / "request.json").write_text(json.dumps(request), encoding="utf-8")
        return engine.main()

    def test_operations_reach_their_module_and_the_others_still_diff(self):
        cases = [("lemmas", "lemmas", "run_lemmas", {"operation": "lemmas"}, ()),
                 ("fuzz", "fuzz", "run", {"operation": "fuzz"}, (engine.IO,)),
                 ("deps_graph", "graph", "deps_graph", {"operation": "deps_graph"}, (engine.IO,)),
                 ("capture_traces", "traces", "capture_traces", {"operation": "capture_traces"}, (engine.IO,))]
        for op, name, fn, request, extra in cases:
            module = mock.MagicMock(name=name)
            getattr(module, fn).return_value = 0
            with mock.patch.dict(sys.modules, {name: module}), mock.patch.object(engine, "diff", return_value=9) as diff:
                self.assertEqual(self.run_main(request), 0, op)
                called = getattr(module, fn)
                called.assert_called_once()
                self.assertEqual(called.call_args.args[0]["operation"], op)
                if extra:
                    self.assertEqual(called.call_args.args[1:], extra)
                diff.assert_not_called()
        with mock.patch.object(engine, "diff", return_value=7) as diff:
            self.assertEqual(self.run_main({"operation": "diff"}), 7)
            diff.assert_called_once()

    def test_a_missing_module_fails_the_request_instead_of_diffing(self):
        with mock.patch.dict(sys.modules, {"fuzz": None}), mock.patch.object(engine, "diff") as diff:
            with self.assertRaises(ImportError):
                self.run_main({"operation": "fuzz"})
            diff.assert_not_called()


class ModifiedTests(Directory):
    """stub response: the test ids whose body changed (the NOP gate judges them as new tests)."""

    def test_changed_test_bodies_are_listed_by_id(self):
        base = ("def test_a():\n    assert 1\n\nclass TestB:\n    def test_m(self):\n        assert 2\n\n"
                "def helper():\n    return 1\n")
        cand = ("def test_a():\n    assert 1 + 0\n\nclass TestB:\n    def test_m(self):\n        assert 2\n\n"
                "def helper():\n    return 2\n\ndef test_new():\n    assert True\n")
        (self.base / "test_x.py").write_text(base, encoding="utf-8")
        (self.cand / "test_x.py").write_text(cand, encoding="utf-8")
        engine.stub(self.req(["test_x.py"]))
        self.assertEqual(self.response()["modified_tests"], ["test_x.py::test_a"])

    def test_method_bodies_and_new_files_are_listed_or_not(self):
        (self.base / "tests").mkdir()
        (self.cand / "tests").mkdir()
        (self.base / "tests" / "t.py").write_text("class TestB:\n    def test_m(self):\n        assert 2\n", encoding="utf-8")
        (self.cand / "tests" / "t.py").write_text("class TestB:\n    def test_m(self):\n        assert 3\n", encoding="utf-8")
        (self.cand / "test_new.py").write_text("def test_n():\n    assert 1\n", encoding="utf-8")
        engine.stub(self.req(["tests/t.py", "test_new.py"]))
        self.assertEqual(self.response()["modified_tests"], ["tests/t.py::TestB.test_m"])

    def test_source_files_have_no_modified_tests(self):
        (self.base / "m.py").write_text("def test_like():\n    return 1\n", encoding="utf-8")
        (self.cand / "m.py").write_text("def test_like():\n    return 2\n", encoding="utf-8")
        engine.stub(self.req(["m.py"]))
        self.assertEqual(self.response()["modified_tests"], [])


class Encoding(Directory):
    def write(self, d: Path, name: str, body: str) -> None:
        (d / name).write_bytes((LATIN1 + body).encode("latin-1"))

    def test_read_py_honours_cookie_and_converts_to_utf8(self):
        self.write(self.cand, "lat.py", "def f():\n    return '\xe9'\n")
        text = engine.read_py(self.cand / "lat.py")
        self.assertIn("\xe9", text)
        self.assertTrue(text.startswith("# -*- coding: utf-8 -*-\n"))

    def test_stub_signatures_and_mutants_of_latin1_file(self):
        self.write(self.base, "lat.py", "def f():\n    return '\xe9'\n")
        self.write(self.cand, "lat.py", "def f():\n    return '\xe9'\n\n\ndef g():\n    return '\xe9\xe9'\n")
        self.assertEqual(engine.stub(self.req(["lat.py"])), 0)
        r = self.response()
        self.assertEqual([s["symbols"] for s in r["stubbed"]], [["g"]])
        space: dict = {}
        exec(compile((self.io / "stubs" / "lat.py").read_bytes(), "stub", "exec"), space)
        self.assertEqual(space["f"](), "\xe9")  # the base with stubs keeps its strings, without mojibake

        self.assertEqual(engine.signatures(self.req(["lat.py"])), 0)
        self.assertEqual([s["name"] for s in self.response()["files"][0]["symbols"]], ["f", "g"])

        engine.mutate(self.req(["lat.py"], budget={"changed_lines": {"lat.py": [7]}}))
        r = self.response()
        self.assertEqual(r["not_generated"], [])
        self.assertTrue(r["mutants"])
        for m in r["mutants"]:
            compile((self.io / "mutants" / m["id"] / "lat.py").read_bytes(), "m", "exec")

    def test_non_utf8_bytes_without_cookie_report_unsupported_encoding(self):
        (self.cand / "bad.py").write_bytes(b"x = 1\ny = '\xe9'\n")
        engine.mutate(self.req(["bad.py"], budget={"changed_lines": {"bad.py": [1]}}))
        self.assertTrue(self.response()["not_generated"][0]["reason"].startswith("unsupported encoding"))

    def test_unreadable_file_does_not_drag_others(self):
        (self.cand / "bad.py").write_bytes(b"x = 1\ny = '\xe9'\n")
        (self.cand / "good.py").write_text("def f():\n    return 1\n", encoding="utf-8")
        engine.signatures(self.req(["bad.py", "good.py"]))
        self.assertEqual([f["file"] for f in self.response()["files"]], ["good.py"])


class Depth(Directory):
    def test_complexity_with_too_deep_tree_gives_null(self):
        with mock.patch.object(engine.ast, "dump", side_effect=RecursionError):
            r = engine.measure("g.py", "def f(x):\n    return x\n", "def f(x):\n    return x + 1\n")
        self.assertEqual((r["cyclomatic"], r["ast_nodes"]), (None, None))

    def test_mutate_with_recursion_leaves_it_in_not_generated(self):
        (self.cand / "g.py").write_text("x = 1 + 2\n", encoding="utf-8")
        with mock.patch.object(engine, "_mutants", side_effect=RecursionError):
            engine.mutate(self.req(["g.py"], budget={"changed_lines": {"g.py": [1]}}))
        r = self.response()
        self.assertEqual((r["mutants"], r["not_generated"][0]["file"]), ([], "g.py"))

    def test_mutate_of_long_chain_responds(self):
        (self.cand / "g.py").write_text(chain(400), encoding="utf-8")
        engine.mutate(self.req(["g.py"], budget={"changed_lines": {"g.py": [2]}, "max_mutants": 5}))
        self.assertEqual(len(self.response()["mutants"]), 5)


class Prune(Directory):
    """The full mutate: equivalent mutants do not reach the core and go in "pruned" with their reason."""

    def test_pruned_lists_logging_assert_and_dead_branch_without_mutants(self):
        text = ("def f(a, b):\n"
                 "    logging.info('hello %s', a + b)\n"
                 "    assert a >= b, 'order'\n"
                 "    if False:\n"
                 "        b = a * 2\n"
                 "    return a - b\n")
        (self.cand / "p.py").write_text(text, encoding="utf-8")
        engine.mutate(self.req(["p.py"], budget={"changed_lines": {"p.py": [2, 3, 5, 6]}}))
        r = self.response()
        self.assertEqual(r["pruned"], [
            {"file": "p.py", "line": 2, "reason": engine.REASON_LOG},
            {"file": "p.py", "line": 3, "reason": engine.REASON_ASSERT},
            {"file": "p.py", "line": 5, "reason": engine.REASON_BRANCH},
        ])
        self.assertEqual({m["line"] for m in r["mutants"]}, {6})
        self.assertEqual(r["not_generated"], [])

    def test_without_equivalents_pruned_is_empty(self):
        (self.cand / "p.py").write_text("def f(a):\n    return a + 1\n", encoding="utf-8")
        engine.mutate(self.req(["p.py"], budget={"changed_lines": {"p.py": [2]}}))
        self.assertEqual(self.response()["pruned"], [])

    def test_expression_level_unreachable_prunes_no_statement(self):
        """mypy's `Right operand of "or" is never evaluated` names part of a line: the two mutants of that line stay."""
        text = "def f(a: int) -> int:\n    if isinstance(a, int) or a > 3:\n        return a + 1\n    return a - 1\n"
        (self.cand / "t.py").write_text(text, encoding="utf-8")
        engine.mutate(self.req(["t.py"], budget={"changed_lines": {"t.py": [2, 3]}}))
        r = self.response()
        self.assertEqual(r["pruned"], [])
        descriptions = {m["description"] for m in r["mutants"]}
        self.assertIn("comparison > changed to >=", descriptions)
        self.assertIn("arithmetic operator + changed to -", descriptions)
        self.assertTrue(any(d.startswith("condition") for d in descriptions), descriptions)

    def test_statement_after_return_is_not_type_guaranteed(self):
        """Code after a return is unreachable for mypy too, but by control flow: it is mutated, not pruned by type."""
        text = "def f(a: int) -> int:\n    return a\n    return a + 1\n"
        (self.cand / "t.py").write_text(text, encoding="utf-8")
        engine.mutate(self.req(["t.py"], budget={"changed_lines": {"t.py": [3]}}))
        r = self.response()
        self.assertEqual(r["pruned"], [])
        self.assertIn("arithmetic operator + changed to -", {m["description"] for m in r["mutants"]})

    def test_type_guaranteed_branch_is_still_pruned(self):
        """An if that always takes its body makes the following return dead by types (not by control flow)."""
        text = "def f(a: int) -> int:\n    if isinstance(a, int):\n        return a + 1\n    return a - 1\n"
        (self.cand / "t.py").write_text(text, encoding="utf-8")
        engine.mutate(self.req(["t.py"], budget={"changed_lines": {"t.py": [4]}}))
        r = self.response()
        self.assertEqual(r["mutants"], [])
        self.assertEqual(r["pruned"], [{"file": "t.py", "line": 4, "reason": engine.REASON_TYPE}])


class TypeDead(unittest.TestCase):
    """_type_dead on synthetic mypy lines: no mypy needed."""

    def dead(self, text: str, reported: set[int]) -> list[int]:
        return [span[0] for span, reason in engine._type_dead(ast.parse(text), reported) if reason == engine.REASON_TYPE]

    def test_type_dead_statement_gives_a_span(self):
        self.assertEqual(self.dead("def f(a: int):\n    if isinstance(a, int):\n        return 1\n    return 2\n", {4}), [4])

    def test_statement_after_return_raise_or_exit_gives_none(self):
        cases = ["def f(a):\n    return 1\n    return 2\n",
                 "def f(a):\n    raise ValueError\n    return 2\n",
                 "def f(a):\n    import sys\n    sys.exit(1)\n    return 2\n",
                 "def f(a):\n    exit(1)\n    return 2\n",
                 "def f(a):\n    import os\n    os._exit(1)\n    return 2\n",
                 "def f(a):\n    for _ in a:\n        continue\n        return 2\n",
                 "def f(a):\n    while True:\n        pass\n    return 2\n"]
        for text in cases:
            last = len(text.splitlines())  # the statement after the terminator is always the last line
            self.assertEqual(self.dead(text, {last}), [], text)

    def test_statement_after_if_that_always_returns_gives_none(self):
        text = "def f(a):\n    if a:\n        return 1\n    else:\n        return 2\n    return 3\n"
        self.assertEqual(self.dead(text, {6}), [])

    def test_statement_after_if_with_one_returning_branch_is_type_dead(self):
        text = "def f(a):\n    if a:\n        return 1\n    return 3\n"
        self.assertEqual(self.dead(text, {4}), [4])

    def test_while_true_with_break_does_not_terminate(self):
        text = "def f(a):\n    while True:\n        if a:\n            break\n    return 3\n"
        self.assertEqual(self.dead(text, {5}), [5])


class Identical(Directory):
    """engine-response.identical: the public symbols no input can tell apart, so the core does not count them as
    unjudged. Found with N=2 on calc.py: the four untouched functions were INCONCLUSIVE in every two-candidate run.
    CrossHair is faked as in Budget."""

    fake_run, diff = staticmethod(Budget.fake_run), Budget.diff

    def setUp(self):
        super().setUp()
        for name, value in (("WORK", self.io.parent / "work"), ("run", mock.Mock(side_effect=self.fake_run))):
            patcher = mock.patch.object(engine, name, value)
            patcher.start()
            self.addCleanup(patcher.stop)

    MODULE = ("LIMIT = 10\n\ndef suma(a: int, b: int) -> int:\n    return a + b\n\n"
              "def tope(a: int) -> int:\n    return min(a, LIMIT)\n\n"
              "def usa(a: int) -> int:\n    return resta(a, 1)\n\n"
              "class Externo:\n    def viejo(self) -> int:\n        return 0\n\n"
              "def resta(a: int, b: int) -> int:\n    return {}\n")

    def test_untouched_symbols_are_identical_and_the_changed_one_and_its_callers_are_not(self):
        r = self.diff(self.MODULE.format("a - b"), self.MODULE.format("a + -b"))
        self.assertEqual(r["identical"], ["m.py::suma", "m.py::tope", "m.py::Externo", "m.py::Externo.viejo"])
        self.assertIn("m.py::resta", [t["target"] for t in r["targets"]])

    def test_a_changed_module_level_name_makes_its_readers_differ(self):
        r = self.diff(self.MODULE.format("a - b"), self.MODULE.format("a - b").replace("LIMIT = 10", "LIMIT = 11"))
        self.assertNotIn("m.py::tope", r["identical"])
        self.assertIn("m.py::suma", r["identical"])

    def test_a_name_bound_by_an_import_never_qualifies(self):
        text = "from os import sep\n\ndef une(a: str) -> str:\n    return a + sep\n"
        r = self.diff(text, text)
        self.assertNotIn("identical", r)

    def test_a_changed_statement_that_binds_no_name_makes_nothing_identical(self):
        base = self.MODULE.format("a - b")
        r = self.diff(base, base + "\nExterno.viejo = lambda self: 1\n")
        self.assertNotIn("identical", r)


if __name__ == "__main__":
    unittest.main()
