# RFC 0002: Declared tests as the Tier 0 of files no engine covers

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable (decision of 2026-10-29, bead a2dlc-eve)
- **Amends:** RFC-0001 §5 Stage 3, and `contracts/engine-tier0-response.schema.json`
- **Changes `schema_version`:** no

## Problem

Tier 0 runs engines and nothing else. On a repo with no engine for its language (`"engines": []`), nothing reviews a Builder's candidate, so Tier 0 reports `reviewed` false, and Stage 3 escalates every candidate: "Tier 0 ran nothing ... nobody reviewed the builder's candidate". The protocol requires that what nobody executed is `INCONCLUSIVE`, never `PASS`, so the escalation is correct. The consequence is that `a2dlc run` can never accept a candidate on such a repo, however good the repo's own tests are.

It happened in the first paid pilot of the factory (2026-10-29, arm 1, target `tinylinks`, config with `config.tests` and no engine): both lanes ended `escalated` with that message and 0 changes were accepted. The core is meant to work for any language, and a language needs a Docker engine before the loop can close, which contradicts that.

## Proposal

Add step 4 to RFC-0001 §5 Stage 3, and qualify its title. The text as it appears in `docs/A2-DLC_v3.5_protocol.md`:

> 4. **The declared tests, for the changed files no engine reviewed.** If the repo declares `config.tests`, `tests.command` runs in `tests.image` on the candidate, in the same hermetic sandbox, with the files that no engine reviewed in steps 1 to 3 in `$A2DLC_CHANGED`; the suite runs as the command defines it. The run counts as a review only if it is readable: JUnit XML is present, it has at least one testcase, and a non-zero exit comes with at least one identifiable failed or errored testcase. Each failed or errored testcase is a Tier 0 finding of kind `test` and returns to the Builder with the other findings. A run that is not readable (no JUnit, no testcase, a red exit with no identifiable failed testcase, a timeout, no runner) is an error of the tool, not a review and not feedback: the stage fails with the reason and spends no round. Step 4 does not run when the engines reviewed every changed file or when the repo declares no `config.tests`.

Findings of step 4 spend rounds of `max_inner_iterations` like any other finding, so the existing exhaustion rule (CB-02b) cuts a Builder that does not converge. The files that step 4 covered stay in the list of what Tier 0 did not review, and Stage 4 keeps its coverage precondition.

The title of the stage said "< 2 s". That is the time of steps 1 to 3. Step 4 takes the time of the repo's suite, bounded by `tests.timeout_s`.

Contract: `contracts/engine-tier0-response.schema.json` gets the finding kind `test`. The core writes it into `tier0_feedback.json`, as it does `lemma`; an engine does not emit it, and `file` is the testcase's file (its classname, when the runner gives no file). `reviewed` is also true when step 4 ran with a readable run.

Implementation notes that are not normative: the review of a working tree that no Builder wrote (probes executed on the working tree) does not run step 4; the feedback of one round carries at most 30 failed testcases and one line for the rest.

## Implementation ahead of acceptance

The reference implementation carries this behavior before the RFC is accepted (commit `be5dccd`), because without it `a2dlc run` could not accept any builder candidate on a repo with no engine, which blocked the factory pilot. The text in `docs/A2-DLC_v3.5_protocol.md` is marked as proposed until the 14-day comment period of [README.md](README.md) ends and the maintainer accepts the RFC. If the RFC is rejected, that commit is reverted, together with the finding kind `test` and the protocol text.

## Compatibility

No existing document becomes invalid. `test` is a new enum value in a field that `lemma` was added to the same way; per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) an added enum value does not bump `schema_version`. A consumer with the older schema rejects a `tier0_feedback.json` that carries it, which is the pairing rule working as intended. A repo with at least one engine covering every changed file behaves as before. A repo with engines that leave some changed files uncovered (for example test files) and a `config.tests` now runs the declared command for those files, which can be slower than before and can fail on a suite that is already red on the base.

## Alternatives

- **An engine for every language.** Rejected: the core knows no languages, and the factory and the global-protocol goals need a repo to close the loop with the tests it already has.
- **Accept the candidate without a review when no engine ran.** Rejected: it breaks the rule that what nobody executed is `INCONCLUSIVE`.
- **Run only the changed test files.** Rejected: a change to a source file is judged by the tests that exist, and the Builder's own new test says little about a regression.
- **Subtract the failures that are already red on the base**, as the engines do for their findings. Not adopted: it doubles the cost of every round. A suite that is red on the base makes each round fail until the Builder fixes it or the rounds run out and the stage escalates, which is safe but wasteful. It can be added later without changing the contract.

## Verification

- Contract: `contracts/examples/valid/engine-tier0-response__tests-feedback.json` and the invalid example `engine-tier0-response__kind-unknown.json`; `python contracts/tests/validate.py contracts` ends in `0 failures`.
- Unit tests with a fake test runner: `internal/app/tier0_tests_test.go` (no engine and green JUnit is reviewed; a red testcase is feedback and a later green round accepts; no JUnit, no testcase, a red exit without a failed testcase, a timeout and no runner are tool errors; engines covering every file run no tests) and `internal/run/stage3_tests_test.go` (Stage 3 accepts, spends one feedback round, exhausts the rounds, fails on no JUnit).
- A lane through Stage 3 with a scripted Builder and the real test image on a copy of the factory target ends `PASS` (`e2e/stage3_tests_tier0.sh`).
- Conformance: the kit under `conformance/` does not cover Tier 0, so there are no vectors to add.
