# VARA-2: a2dlc as an execution oracle (coverage + N-tree differential mediated by tests)

Frozen on 2026-10-17 **before** writing the code that measures it (this file's commit is the bar). It comes from the
result of `VARA.md` (`RESULTS.md`: detection 0 %, 66 % empty PASS) and from the strategist's recommendation of
2026-10-17: the symbolic engine over module-level functions has no ceiling for the patches that SWE-ABS rejects
(logic errors 47 % + incomplete fixes 35 %); what a2dlc can be without an LLM is the **deterministic oracle**:
(1) no changed line executed by a test → never PASS; (2) the same test set T over the base, the candidate and the N
trees of other builders, with the repo's runner; red on the candidate and green on another tree → FAIL.

## Question and arms

Same population: the **10 measured instances and the 38 checks** of `RESULTS.md` (`chosen.txt` of each instance, same
ABS labels). Trees per instance: `C0` = the image's HEAD + config + `.gitignore` (commit); `G` = `C0` + gold (commit);
`A_i` = `C0` + the patch of agent *i* (commit, one per chosen agent); candidate = `C0` + the agent's patch in a
worktree. Each arm is one `a2dlc check` per agent (38 per arm):

| arm | command | T (tests that run) | what it measures |
|---|---|---|---|
| **A1 T_orig + coverage** | `check --base C0` | NOP gate: the tests that the agent's patch brings (if it touches `tests.paths`); baseline gate: **T_orig** = the test files touched by SWE-bench's `original_test_patch`, as they exist in `C0` (without the FAIL_TO_PASS tests, which that patch adds) | cut 1: empty PASS must be 0; how many changed lines the original suite executes |
| **A2 T_abs N-trees** | `check --base C0 --peer G --tests-overlay <ABS tests>` | A1 + the files of ABS's `test_patch` as an overlay, run over `C0`, the candidate and `G` | cut 2, **tautological upper bound**: the labels come from these same tests; it measures that the oracle reads them correctly |
| **A3 T_cross** | `check --base C0 --peer A_j…` (the other chosen agents of the instance) | A1 + the tests that the other agents bring (14 of the 38 patches touch `tests.paths`; 9 of the 10 instances have at least one) | what is reachable **without gold and without an LLM**: N builders testing each other |

No function engine: `engines: []` and `tests.source_paths` = the repo's package. What CrossHair gave is already measured
in `RESULTS.md`; here only the test-mediated path is measured.

## Config per repo (goes in `C0`, one per instance; `baseline_command` carries T_orig fixed)

- `tests.image`: image derived from SWE-bench's (`FROM` + `coverage` + `pytest` where missing + the log-to-JUnit
  converter for the django runner). `tests.paths`: django `tests/**`, sympy `sympy/**/tests/**`, sphinx `tests/**`,
  matplotlib `lib/matplotlib/tests/**`. `tests.source_paths`: django `django/**`, sympy `sympy/**` (minus tests), sphinx
  `sphinx/**`, matplotlib `lib/matplotlib/**` (minus tests).
- `command`: copies the exported tree (`/work`) over `/testbed` in the container (the packages are installed
  *editable* in `/testbed`, and matplotlib has `.so` files that git ignores), runs **only** the files in
  `$A2DLC_TESTS` under `coverage run`, writes JUnit to `$A2DLC_JUNIT` and Cobertura XML to `$A2DLC_COVERAGE`. django:
  `tests/runtests.py --parallel 1` + converter; sympy/sphinx/matplotlib: `pytest -p no:cacheprovider -o
  junit_family=xunit1`. `reruns` 1, `timeout_s` 1200, `memory` 4g, `cpus` 2.
- An agent patch that does not apply over `C0` is counted as `no_aplica` (it already happened in VARA 1: none of the 38).

## Core rules being measured (written after this commit)

1. **Coverage precondition.** For each changed file that matches `tests.source_paths` or a `paths` of an engine, every
   changed line must be either inside an engine target with a PASS/FAIL verdict, or executed (hits > 0) by some test
   run on the **candidate**. Lines that the coverage report does not list as statements (blanks, comments, `else:`) do
   not count. A file missing from the report = no line executed. If any line is left unexecuted, the verdict cannot be
   PASS: INCONCLUSIVE with escalation `uncovered_change` (file, lines, `needs: {role: saboteur, artifact: test_files}`
   (up to block 2 it said inquisitor)). Without `config.tests`, or without `$A2DLC_COVERAGE`, the same applies.
2. **`tests_diff` gate (N trees).** T = the candidate's changed tests ∪ each `--peer`'s changed tests relative to the
   base ∪ the files of `--tests-overlay`. T runs in full over the base, the candidate and each peer (the overlay of the
   files of T over each clean tree). Per test: red on the candidate and green on some other tree → **FAIL**
   (`behavior_divergence`, evidence `trees`); red on all trees → INCONCLUSIVE (`test_without_teeth`: it discriminates
   nothing); absent from the candidate → INCONCLUSIVE; green on the candidate → PASS (with `characterization` if it is
   also green on the base). A red on the candidate is re-run `reruns` times on the candidate only: if it ever passes,
   INCONCLUSIVE + `flaky_test`, not FAIL. The candidate passing a test that a peer fails does not penalize it (it is
   data for the peer's check).
3. **Empty PASS** = PASS with some changed line in scope that has no PASS/FAIL target and no execution. By rule 1 it must
   be 0; it is counted by reading `verdict.json` (`coverage`), not by trusting the rule.

## Metrics (per arm, table {rejected, passed} × {FAIL, INCONCLUSIVE, PASS, ERROR}, Wilson 95 %)

- **detection** = FAIL / rejected · **FAIL in passed** = FAIL / passed (each one is read and classified: runner bug,
  out-of-spec divergence, or false FAIL) · **inconclusive** = INCONCLUSIVE / all, with causes by frequency
  (`uncovered_change`, red on all trees, flaky, tooling) · **empty PASS** · **coverage**: changed lines in scope executed
  / total, per check · time per check (median, maximum).

## Thresholds (set now)

- **A1**: empty PASS = 0/38. Any empty PASS = cut 1 broken. No FAIL is expected (the agents pass the original tests);
  any one is read and classified.
- **A2**: detection ≥ 90 % or the runner is broken (the tests that label are the ones that run); FAIL in passed ≤ 10.6 %
  (SWE-ABS's false-rejection rate; by construction it should be 0: each one is read). **Refutes** the recommendation:
  detection < 60 % with a healthy runner, or FAIL in passed > 20 %.
- **A3**: no threshold; it is the realistic figure without gold and without an LLM. If A2 ≥ 90 % and A3 < 20 %, the
  bottleneck is test generation, which by decision A lives outside a2dlc (Inquisitor/Saboteur with `uncovered_change`
  and `tests_overlay` as the contract). If A3 ≥ 60 %, the builders' cross tests are enough.
- **Breakthrough** only if A1 empty PASS = 0 **and** A2 detection ≥ 90 % with FAIL in passed ≤ 10.6 %; and even then it
  is stated that A2 is an upper bound and A3 is what is reachable today.

## Limits declared with the result

- A2 is tautological on purpose: it measures the oracle, not the ability to find the tests. The work of finding them is
  left outside (LLM) by design.
- 10 instances, 38 checks, 4 repos; the gold as peer assumes the gold is correct; no flakiness measured (reruns 1).
- The derived images install `coverage`/`pytest` with network access at build time; the checks run with `--network none`.

## Amendment of 2026-10-17 (after the smoke test on sympy-18211, before measuring the 10)

- **Rule 2, precision:** a test that is red on all trees and that **was already red on the base with its own test file**
  (without T on top) is a pre-existing failure of the repo and does not affect the verdict; it is noted with its `trees`
  and the reason "pre-existing". Measured motive: `test_relational.py` carries two XFAILs that sympy's runner reports as
  failures, and they left every check with peers (A2 and A3) INCONCLUSIVE, measuring the repo instead of the candidate.
  `test_without_teeth` is kept for the tests that nobody passes and that had no red on the base. It costs one more base
  run, only when there are reds on all trees.
- **Runner:** coverage is traced only over the files in `$A2DLC_CHANGED` (`--include`), not over the whole package:
  tracing all of sympy cost more than the tests (A1 from 510 s → ~130 s). It does not change what is measured: the
  precondition only looks at those lines.
- **Run order:** A1 and A2 over the 10 instances first (they are the arms with thresholds), A3 afterwards.
