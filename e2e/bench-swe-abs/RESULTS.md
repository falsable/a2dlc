# SWE-ABS benchmark results (run 2026-10-16 → 2026-10-17)

Bar: `VARA.md` (commit `fbfde8b`, sha of `order.json` corrected in `dff1273`). Measured engine: `engines/py/engine.py`
of v0.2.0 plus the Python 3.9 fix (`0f08719`, only `from __future__ import annotations`).

## Verdict: refuted for this form of the engine

| | FAIL | INCONCLUSIVE | PASS | (empty PASS) | ERROR | n |
|---|---|---|---|---|---|---|
| rejected (ABS tests fail) | 0 | 7 | 11 | 11 | 0 | 18 |
| passed (ABS tests pass) | 0 | 6 | 14 | 14 | 0 | 20 |

- **Detection** (FAIL / rejected): **0 %** [0–18] (Wilson 95 %). The bar required ≥ 60 %.
- **Inconclusive**: 34 % [21–50]. **FAIL in passed**: 0 % [0–16]: there were no counterexamples to classify.
- **Empty PASS** (0 targets, everything out of scope): 25 of 38 checks (66 %). a2dlc approved **11 rejected patches
  without looking at them**. The table counts them as PASS (per the bar); this is the main finding.
- Time per `check`: median 55 s, maximum 211 s (django: exporting and copying/rewriting ~6000 `.py` files); 3390 s in total.

10 instances measured, in order: sympy-18211, sphinx-9230, django-15315, django-14787, sphinx-9711, sympy-13031,
django-15563, sympy-14711, matplotlib-24026, sphinx-8056. 18 examined (positions 2–17 plus 2 pilot): 3 `sin_pares`
(astropy-13453, django-15731, django-16454: all 20 agents pass ABS; django-15957 too) and pylint-7080 measured at
position 11, outside the 10. 89 ABS labels computed: 62 passed, 27 rejected (30 %, higher than the 19.78 % of the paper
because the population is instances with ≥ 5 agents and Python ≥ 3.9).

## Causes, by frequency (targets and files of the 38 checks)

| cause | n | what it is |
|---|---|---|
| out of scope `no_engine` | 69 | test files and loose scripts left by the agent (`test_*.py`, `demonstrate_fix.py`): no engine looks at them |
| out of scope `not_exported` | 30 | the change is **inside a class method**; the engine only compares module-level functions (`funciones()` reads `ast.parse(...).body`) |
| no type annotations | 9 | `django/utils/decorators.py::method_decorator`, `_multi_decorate`; `sympy/physics/vector/vector.py::_check_vector`: CrossHair has no domain |
| CrossHair cannot determine the signature | 8 | `sphinx/extension.py::setup`, `verify_needs_extensions` (`**kwargs`, decorators) |
| could not import | 4 | `lib/matplotlib/stackplot.py`: circular import (`_version`) when the package is renamed to `a2dlc_base.*` |

According to the paper (manual analysis of 100), the patches rejected by SWE-ABS are: logic errors 47 %, incomplete fixes
35 %, edge cases/types/off-by-one 14 %. They are divergences in the behaviour of the API on inputs that the original
tests do not cover, inside methods with framework state: exactly what a symbolic differential over pure, typed
functions cannot reach.

## Pilot (positions 0 and 1; not counted)

- pytest-5631: 4 checks, 4 INCONCLUSIVE (the two changed functions of `src/_pytest/compat.py` have no annotations).
  Labels: 6 rejected / 2 passed out of 8 agents.
- django-16100: 4 checks, 4 empty PASS (`ModelAdmin.changelist_view`, a method). Labels: 2 rejected / 10 passed.

Adjustments made during the pilot (all committed before the measurements, except where noted):
1. ABS acceptance rule (`7c4bb20`): ABS's `test_patch` files are **new** test files and the dataset's
   `FAIL_TO_PASS`/`PASS_TO_PASS` columns are still the original ones; accepted = all the new tests pass.
2. Python 3.9 engine (`0f08719`): `int | None` in annotations broke in SWE-bench's `testbed`; `from __future__
   import annotations`; verified with 9/9 cases of `measure_gates.sh`.
3. Script (`0f08719`, `93cd14d`): repos in a short path (`Filename too long` on Windows), `core.longpaths`, fallback
   `patch --fuzz=5` (SWE-bench does the same), S3 `patch.diff` without a final newline, `eval.sh` generated with network
   once (`make_test_spec` downloads requirements from GitHub), grader without `make_test_spec`.
4. **During the measurements** (`d0c62cb`): sphinx runs pytest without `-rA` in those images, and swebench's parser does
   not see the tests; the grader reads the final summary (`N passed, M failed`). It affected only the `abs_tooling` label of
   sphinx-9230 and sphinx-9711, which were re-run and entered measured in their position in the order. It does not touch
   the engine.

## Limits

- It measures the engine's divergence detection with the gold as the oracle; in production there is no gold (N ≥ 2 builders).
- 20 of 30 agents (the `mini-*` do not publish patches); Python ≥ 3.9; 10 instances, ≤ 4 checks each.
- The population has 30 % rejected because it requires ≥ 5 agents that resolved it; it is not the paper's rate.
- Raw data and logs: the session's scratchpad (`bench/work/<instance>/{labels,logs,runs}`), repos copied to
  `%LOCALAPPDATA%/a2dlc-bench/`. Reproducible with `run_all.sh` from `order.json`.

## What comes next (decided separately, with the strategist)

The benchmark does not refute the idea of the gate; it refutes "CrossHair over typed module-level functions" as the
central piece for real repos. Minimum non-negotiable: "everything out of scope" must be INCONCLUSIVE, never PASS.

# VARA-2 (10 of 10 instances, run 2026-10-17)

Bar in `VARA-2.md`. Arms A1 (T_orig + coverage) and A2 (T_abs with the gold as `--peer`); A3 was not run (Aldemar's
decision). Measured instances, in `order.json` order: sympy-18211, sphinx-9230, django-15315, django-14787,
sphinx-9711, sympy-13031, django-15563, sympy-14711, matplotlib-24026, sphinx-8056 (38 checks per arm: 18 rejected,
20 accepted). The run finished on its own at 22:54 (the last instance took 1208 s); an earlier cut of this file had it
stopped at 9/10 because the log was read before it finished. Raw table: `tabulate2.py` over `work/`.

| arm | population | FAIL | INCONCLUSIVE | PASS | empty PASS | ERROR | n |
|---|---|---|---|---|---|---|---|
| A1 | rejected | 0 | 12 | 6 | 0 | 0 | 18 |
| A1 | passed | 0 | 11 | 8 | 0 | 1 | 20 |
| A2 | rejected | 18 | 0 | 0 | 0 | 0 | 18 |
| A2 | passed | 0 | 5 | 15 | 0 | 0 | 20 |

- **A1**: empty PASS 0/38 (cut 1 holds); no FAIL; inconclusive 61 % [45–74] (23 `uncovered_change`, 8 `tooling` for tests
  that do not appear in the base because of import or collection, 3 `tooling` for a test that fails on the base because of
  an import or a symbol without a stub); 1 check ended in ERROR (no verdict); coverage of the changed lines in
  scope 74 % (312/423); median 155 s per check, maximum 1255 s.
- **A2**: detection 100 % [82–100] (18/18 rejected in FAIL); FAIL in passed 0 % [0–16]; inconclusive 13 % [6–27] (8
  `tooling` for ABS tests that do not collect in the base, 5 `uncovered_change`, 1 `tooling` for a test that does not
  collect in the candidate, 1 other `tooling`); 3 pre-existing red tests discounted; coverage 92 % (378/411); median 289 s,
  maximum 2827 s. All the FAILs are ABS tests that the candidate fails and the gold (peer) passes; in 7 of the 10
  instances at least one of those tests also fails on the base (the test teaches something new) and it still counts,
  because the peer passes it.
- **Against the thresholds**: A1 empty PASS = 0 and A2 detection ≥ 90 % with FAIL in passed ≤ 10.6 %: the breakthrough
  condition of `VARA-2.md` is met in all 10 instances. A2 is an upper bound (the tests that label are the ones that
  run); A3, the figure reachable without gold, was not measured.
- **Engine changed mid-run**: `run_instance.sh` builds the engine image from the hash of the live repo's
  `engines/py/engine.py`, and the protocol review committed an engine fix (dcd0546, 18:52) while the run was going.
  Instances 1–4 (up to django-14787) used the previous engine; 5–9 used the new one (tier0, mutate, and a module stub
  without def/class: none of the three operations enters `check`). It is declared here instead of repeating the
  measurements.
