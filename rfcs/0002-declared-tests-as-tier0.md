# RFC 0002: Declared tests as the Tier 0 of files no engine covers

- **Status:** Accepted
- **Author:** Aldemar Hernández, falsable (decision of 2026-10-29, bead a2dlc-eve)
- **Amends:** RFC-0001 §5 Stage 3, and `contracts/engine-tier0-response.schema.json`
- **Changes `schema_version`:** no

## Problem

Tier 0 runs engines and nothing else. On a repo with no engine for its language (`"engines": []`), nothing reviews a Builder's candidate, so Tier 0 reports `reviewed` false, and Stage 3 escalates every candidate: "Tier 0 ran nothing ... nobody reviewed the builder's candidate". The protocol requires that what nobody executed is `INCONCLUSIVE`, never `PASS`, so the escalation is correct. The consequence is that `a2dlc run` can never accept a candidate on such a repo, however good the repo's own tests are.

It happened in the first paid pilot of the factory (2026-10-29, arm 1, target `tinylinks`, config with `config.tests` and no engine): both lanes ended `escalated` with that message and 0 changes were accepted. The core is meant to work for any language, and a language needs a Docker engine before the loop can close, which contradicts that.

A second gap showed in the first full run of the factory (2026-10-29, both arms, same target; bead a2dlc-oer). With step 4 in place the lanes reached Stage 4, and several ended `INCONCLUSIVE` because the Builder had left changed source lines that no test executes (an unreachable `return`, a function nothing calls, an entry file no test loads). The Builder never learned of it: Tier 0 returned only red testcases, the coverage the same run wrote was dropped, and the Builder's instructions made tests optional. Stage 4 then blocks on lines the Builder could have covered, or removed, in the round that costs the least.

## Proposal

Add step 4 to RFC-0001 §5 Stage 3, and qualify its title. The text as it appears in `docs/A2-DLC_v3.5_protocol.md`:

> 4. **The declared tests, for the changed files.** If the repo declares `config.tests`, `tests.command` runs once in `tests.image` on the candidate, in the same hermetic sandbox, with every changed file in `$A2DLC_CHANGED`, the files an engine reviewed in steps 1 to 3 included; the suite runs as the command defines it. The run counts as a review only if it is readable: JUnit XML is present, it has at least one testcase, and a non-zero exit comes with at least one identifiable failed or errored testcase. Each failed or errored testcase is a Tier 0 finding of kind `test` and returns to the Builder with the other findings. When the suite is green and the same run also wrote readable coverage (`$A2DLC_COVERAGE`), each source file in `tests.source_paths` with changed lines that no test executed is one Tier 0 finding of kind `coverage` (its first such line, and all of them in the message), once the lines that Stage 4 notes instead of blocking on (defensive exception handlers, declaration-only code) are left out. This holds for a file an engine covers too: Tier 0 runs no diff, so it cannot know which of its lines an engine will examine as PASS or FAIL at Stage 4, and it reports a line no test executed whatever the engine will say; it can say more than Stage 4 about a line an engine will judge, never less. A run that wrote no readable coverage gives no such finding, and Stage 4 keeps its own coverage precondition. A run that is not readable (no JUnit, no testcase, a red exit with no identifiable failed testcase, a timeout, no runner) is an error of the tool, not a review and not feedback: the stage fails with the reason and spends no round. Step 4 does not run when the repo declares no `config.tests`.

Findings of step 4 spend rounds of `max_inner_iterations` like any other finding, so the existing exhaustion rule (CB-02b) cuts a Builder that does not converge. The files that step 4 covered stay in the list of what Tier 0 did not review, and Stage 4 keeps its coverage precondition.

The title of the stage said "< 2 s". That is the time of steps 1 to 3. Step 4 takes the time of the repo's suite, bounded by `tests.timeout_s`.

Contract: `contracts/engine-tier0-response.schema.json` gets the finding kinds `test` and `coverage`. The core writes them into `tier0_feedback.json`, as it does `lemma`; an engine does not emit them. For `test`, `file` is the testcase's file (its classname, when the runner gives no file). For `coverage`, `file` is the source file, `line` its first changed line that no test executed, and `message` lists all of them as ranges. `reviewed` is also true when step 4 ran with a readable run.

The coverage finding uses the code of Stage 4's coverage precondition, so the two cannot disagree: the files in scope are the changed sources matching an engine path or `tests.source_paths`; a source with no record in the coverage at all counts as unexecuted; and the lines that an engine's `classify_lines` returns as defensive handlers or declaration-only code are left out. It is produced only when the suite is green, because the coverage of a failing run is partial and its testcases come first, and at most 30 files are reported per round. It costs no extra run: it reads what step 4's run already wrote.

Not normative: the Builder's instructions (`roles/prompts/builder_primary.md`) say that a change of behavior comes with a test that executes it, in a new test file, and describe the `coverage` finding; and the factory keeps each lane's `verdict.json` under `<out>/<run id>/verdicts/<issue id>.json` before the lane's worktree is removed, so the reason of an `INCONCLUSIVE` lane can be read afterwards.

Implementation notes that are not normative: the review of a working tree that no Builder wrote (probes executed on the working tree) does not run step 4; the feedback of one round carries at most 30 failed testcases and one line for the rest.

## Implementation ahead of acceptance

The reference implementation carries this behavior before the RFC is accepted (commit `7f77772`), because without it `a2dlc run` could not accept any builder candidate on a repo with no engine, which blocked the factory pilot. The text in `docs/A2-DLC_v3.5_protocol.md` is marked as proposed until the 14-day comment period of [README.md](README.md) ends and the maintainer accepts the RFC. If the RFC is rejected, that commit is reverted, together with the finding kind `test` and the protocol text.

## Amendment: engine-covered files (bead a2dlc-l4s)

Step 4 first ran only for the files no engine reviewed. A run of the factory on `tinylinks` (sixth freeze, engine paths `src/**`) showed the gap: Stage 4 rejected two lanes for "changed lines no test executed" in `src/server.ts`, while their Tier 0 feedback was empty, because that file was engine-covered and the tests did not run for it. The Builder never heard of lines that Stage 4 then counted against it. Since the rule "an engine target that cannot judge a changed line the tests executed does not make the diff `INCONCLUSIVE`" (note `inconclusive_covered_by_tests`), the tests examine the lines of engine targets that stay `INCONCLUSIVE`, so the Builder has to be told about the ones they leave unexecuted.

Step 4 now runs once per Tier 0 over every changed file, and the coverage finding is produced for every changed source in scope, with or without an engine. Tier 0 runs no diff, so it does not know which lines an engine will examine as `PASS` or `FAIL` at Stage 4, and it does not subtract them: for a line an engine will judge, Tier 0 can ask for a test that Stage 4 would not have required, and it never asks for less. The cost is the one run the step already had. The text of step 4 above and in `docs/A2-DLC_v3.5_protocol.md` is the amended one; no schema changes.

## Compatibility

No existing document becomes invalid. `test` and `coverage` are new enum values in a field that `lemma` was added to the same way; per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) an added enum value does not bump `schema_version`. A consumer with the older schema rejects a `tier0_feedback.json` that carries it, which is the pairing rule working as intended. A repo with no `config.tests` behaves as before. A repo with `config.tests` runs the declared command once per Tier 0 for every changed file, as the amendment above says; before it, the command ran only for the files no engine covered (for example test files), which can be slower than before and can fail on a suite that is already red on the base.

## Alternatives

- **An engine for every language.** Rejected: the core knows no languages, and the factory and the global-protocol goals need a repo to close the loop with the tests it already has.
- **Accept the candidate without a review when no engine ran.** Rejected: it breaks the rule that what nobody executed is `INCONCLUSIVE`.
- **Run only the changed test files.** Rejected: a change to a source file is judged by the tests that exist, and the Builder's own new test says little about a regression.
- **Leave the unexecuted lines to Stage 4 and the Saboteur.** Rejected for the repos this RFC is about: the factory runs with no Saboteur, so Stage 4 only blocks, after the Builder's rounds are spent.
- **One sentence in the Builder's goal** ("every changed line must run under the tests"). Kept as the prompt change above, but it is weaker alone: nothing tells the Builder which line it missed.
- **Subtract the failures that are already red on the base**, as the engines do for their findings. Not adopted: it doubles the cost of every round. A suite that is red on the base makes each round fail until the Builder fixes it or the rounds run out and the stage escalates, which is safe but wasteful. It can be added later without changing the contract.

## Verification

- Coverage finding (bead a2dlc-oer): `contracts/examples/valid/engine-tier0-response__coverage-feedback.json`; `internal/app/tier0_coverage_test.go` with a fake runner (an unexecuted changed line gives one `coverage` finding and one round, and a green second round accepts; fully executed lines, no coverage and unreadable coverage give none; sources out of scope and test files give none; a source no test loaded is all unexecuted; a red suite gives only its `test` findings; declaration-only lines classified by the engine give none); `e2e/stage3_tests_tier0.sh` with a candidate that leaves a function nothing calls: one `coverage` round, then accepted, zero cost.
- Contract: `contracts/examples/valid/engine-tier0-response__tests-feedback.json` and the invalid example `engine-tier0-response__kind-unknown.json`; `python contracts/tests/validate.py contracts` ends in `0 failures`.
- Unit tests with a fake test runner: `internal/app/tier0_tests_test.go` (no engine and green JUnit is reviewed; a red testcase is feedback and a later green round accepts; no JUnit, no testcase, a red exit without a failed testcase, a timeout and no runner are tool errors; engines covering every file still run the tests once, over all of them) and `internal/run/stage3_tests_test.go` (Stage 3 accepts, spends one feedback round, exhausts the rounds, fails on no JUnit).
- A lane through Stage 3 with a scripted Builder and the real test image on a copy of the tinylinks target ends `PASS` (`e2e/stage3_tests_tier0.sh`, which drives `a2dlc run` in a worktree per issue, with a fake claude and no model).
- Conformance: the kit under `conformance/` does not cover Tier 0, so there are no vectors to add.
