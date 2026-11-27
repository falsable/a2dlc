# RFC 0012: A Tier 0 round the engines or the checks flag does not wait for the tests

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §5 Stage 3 (steps 4 and 5), RFC-0002, RFC-0011
- **Changes `schema_version`:** no

## Problem

In Stage 3 the declared tests (step 4) run on every builder round, in parallel with the engines' tier0, and the blocking
checks of step 5 run after them. With `tests.tier0_command: "baseline"` step 4 is the impacted test set, the slowest part
of a round. Measured on a factory run over a Python repo (three builder rounds): the builder ran 407 s, and the three Tier 0
rounds after it took 449, 454 and 338 s. Round 1's only finding was one type error from a check, and it still paid the
whole test run, whose result was thrown away when the builder changed the tree for round 2.

## Proposal

The order of a round becomes: the engines (step 3) and the tests (step 4) start together; the blocking checks of step 5
run while the tests are still running. If the engines or the checks give at least one finding, the test run is cancelled
and the round's feedback is those findings alone. Only a round they leave clean waits for the tests, and its feedback is
the tests' findings (failed testcases, coverage, a feature without a test). Without engines for the changed files, the
checks run first and the tests start only if the checks are clean.

A cancelled run is a tool run that did not finish: it is not cached and it is not feedback. The next round's tree gets its
own run.

## Compatibility

No contract changes. A builder may now receive the tests' findings one round later than before, after it fixed what the
engines or the checks flagged. The tree Stage 4 takes is a round's tree that passed all three steps, as before.

## Alternatives

- Run the tests only on the round that would be the last. Rejected: a builder that cannot see a red test until its last
  round has no round left to fix it.
- Run the engines and the checks first and start the tests after them. Rejected: on a clean round it adds the engines'
  and the checks' time to the tests' instead of overlapping them.

## Verification

- `go test -count=1 ./internal/app -run TestTier0CheapFindingsDoNotWaitForTheTests`: an engine finding returns within
  10 s while the test run would take 30 s, with the run stopped before Tier 0 returns; a check finding with no engine
  starts no test run; a clean check lets the tests run.
- `go test -count=1 ./internal/app -run 'TestTier0RunsTheTests'`: with the engine green, the tests' red is the feedback.
