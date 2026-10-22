# Benchmark: a2dlc's differential against patches from agents rejected by SWE-ABS

Bar frozen on 2026-10-16 **before** running anything (this file's commit is the bar; whatever is measured afterwards
does not move it). Question: does a2dlc's Python engine (CrossHair `diffbehavior`, N=2 with the gold patch as second
builder) mark FAIL on the agent patches that pass SWE-bench Verified's original tests but fail SWE-ABS's augmented
tests (arXiv 2603.00520)? Control: what does it do with the patches that SWE-ABS also accepts?

## Data (all downloaded on 2026-10-16; nothing is installed on Windows, third-party code runs in Docker)

- Dataset `OpenAgentLab/SWE-Bench_Verified_ABS` (HF, Apache-2.0, 500 instances, revision `79fc07a8…`), parquet
  sha256 `12df02a43cb624a88bbfadf25493f18b7daf551a5d6061d47109cd5b88bc6f97`. Fields used: `base_commit`, `patch`
  (gold), `test_patch` (ABS, augmented), `FAIL_TO_PASS`/`PASS_TO_PASS` (from ABS), `version`.
- Which agent resolved which instance with the **original** tests: the official `results.json` of
  `SWE-bench/experiments` (`evaluation/verified/<agent>/results/results.json`). It matches SWE-ABS's `oldCount` for 17
  of 20 agents and differs by ≤1 for the other 3.
- Agent patches: S3 `swe-bench-submissions` (`all_preds.jsonl` or `logs/<instance>/patch.diff`). Of the 30 agents in
  SWE-ABS's top 30, **20 are usable** (`usable_agents.txt`, sha256 `82908452…`); the 9 `mini-*` and one more have no
  public patches (404 on both paths) and are left out.
- SWE-ABS does not publish the rejected/accepted label per patch: **it is computed here** by running ABS's `test_patch`
  over each agent patch with the `eval.sh` that `swebench==4.1.0` generates (`make_test_spec`), and grading it with
  that library's log parser (`grade.py`). It is the same harness the paper uses. Acceptance rule (clarified in the
  pilot, 2026-10-16: ABS's `test_patch` files are **new** test files and the `FAIL_TO_PASS`/`PASS_TO_PASS` columns are
  still the original ones, so they cannot judge it): **accepted if all the tests in ABS's `test_patch` pass and there
  is at least one; rejected if any of them fails**.
- Images: `swebench/sweb.eval.x86_64.<instance with __→_1776_>:latest`; the engine is a derived image (`FROM` that one +
  `crosshair-tool==0.0.111` in the `testbed` env + `engines/py/engine.py`, sha256
  `94ed05ab2059f3a36bd023fe3126abae11cbbf1e3fe46084db08f51ee15e0ebf`, commit `052e3b2`).

## Population and order (deterministic, `selection.py`)

500 → Python ≥ 3.9 per `MAP_REPO_VERSION_TO_SPECS_PY` of swebench 4.1.0 (CrossHair requires it; 315) → gold touches
only `.py` (314) → at least 5 usable agents resolved it (258) → sorted by `sha256(instance_id)` → **first 24**
(`order.json`, sha256 of the committed blob, with LF: `8fc27bba5cc9088fa9f59712675f7f0a605d6eee20a721de66a767c34a9a8bde`).

- **Pilot**: positions 0 and 1 (`pytest-dev__pytest-5631`, `django__django-16100`). They serve to fix the tooling; they
  are reported separately and **do not count**. Any change to the engine or the scripts after the pilot is noted in
  RESULTS.md with its commit.
- **Measurements**: from position 2, in order, until **10 instances are measured** or the 22 remaining are exhausted.

## Procedure per instance (`run_instance.sh`)

1. Tooling control: gold + ABS `test_patch` → must come out `resolved`. If not, the instance is discarded as
   `abs_tooling` and counted.
2. Labels: the agents that resolved it (results.json), in alphabetical key order; each one: apply its patch in
   `/testbed`, run ABS's `eval.sh`, grade it. `rejected` = not resolved under ABS; `passed` = resolved. A patch that does
   not apply is counted as `no_aplica` and not labelled. It stops once it has **2 rejected and 2 passed**.
3. Instance **measured** (status `medida`) if it has ≥ 1 rejected and ≥ 1 passed; otherwise `sin_pares`, counted.
4. Repo = `/testbed` of the image. `C0` = base_commit + `.a2dlc/config.json` + `.gitignore`; `G` = `C0` + gold
   (commit); candidate = `C0` + the agent's patch in the working tree (git worktree). `a2dlc check --base G`.
   Config: engine `python`, derived image, `paths` = the repo's package (django `django/**`, pytest `src/**`, sympy
   `sympy/**`, sphinx `sphinx/**`, astropy `astropy/**`, matplotlib `lib/**`, pylint `pylint/**`),
   `per_condition_timeout_s` 10, `memory` 4g, `cpus` 2. **No `tests` section**: SWE-bench's patches carry no tests by
   the benchmark's design (NOP/Oracle does not apply), and the baseline is covered by SWE-bench.
5. Verdict = `verdict` in `.a2dlc/verdict.json` (FAIL / INCONCLUSIVE / PASS); if the core writes no verdict, it is
   `ERROR` and counted. The time of each `check` is saved.

## Metrics and thresholds (fixed before running)

Table {rejected, passed} × {FAIL, INCONCLUSIVE, PASS, ERROR}, with a 95 % Wilson interval (small sample).

- **detection** = FAIL / rejected · **inconclusive** = INCONCLUSIVE / all runs · **FAIL in passed** = FAIL / passed.
  A FAIL in passed is not "false" in itself: two correct patches can diverge outside what the tests pin down. Each
  counterexample is read and hand-classified as *out-of-spec divergence* or *false FAIL*, saying which is which.
- **Breakthrough signal**: detection ≥ 60 % with inconclusive < 30 %.
- **Refuted for this form of the engine**: inconclusive > 50 %, or false FAILs (already classified) > 20 % of the passed.
- **Partial**: everything else. In every case the causes of INCONCLUSIVE are listed by frequency: that decides the next
  step (types, imports, side effects, timeouts).

## Limits declared with the result

- It measures the engine's divergence detection with the gold as the oracle (assumption: the gold is correct and
  complete), not the real deployment (N ≥ 2 LLM builders without gold).
- The current engine requires type annotations on the parameters and the module to import in isolation; SWE-bench's
  repos (2019–2023) barely annotate types. If that dominates INCONCLUSIVE, the result refutes the form of the engine,
  not the idea.
- 20 of 30 agents; instances with Python ≥ 3.9; a sample of ≤ 10 instances and ≤ 4 runs per instance.
