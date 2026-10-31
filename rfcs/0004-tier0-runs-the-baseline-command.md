# RFC 0004: Tier 0 can run the baseline command, so Stage 4 does not run it again on the same tree

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable (bead a2dlc-0y3)
- **Amends:** RFC-0001 §5 Stage 3 (step 4, as RFC-0002 wrote it), `contracts/config.schema.json`; the description of `test_gates.baseline.candidate_cached` in `contracts/verdict.schema.json`
- **Changes `schema_version`:** no

## Problem

In `a2dlc run`, a repo that declares `config.tests` with a `baseline_command` runs its suite twice on the same tree. Step 4 of Tier 0 (RFC-0002) runs `tests.command` with `$A2DLC_TESTS` empty on the tree of each Builder round, which for most repos is the whole suite. The candidate of Stage 3 is the tree of the round Tier 0 accepted: the core commits that tree as it is (`CommitOn(tree, …)` in `internal/app/stage3.go`), so Stage 4 judges the same content. The "no regression against baseline" gate of Stage 4 then runs `baseline_command` on that candidate. When `baseline_command` runs the whole suite too, that second run gives the same answer as the first one.

The suite cache (keyed by tree content, image digest, command and, when the command names it, the selection) does not help, because the two runs use different commands. Tier 0's run is never kept under the key the baseline gate looks up.

Measured on a two-lane `a2dlc run` chain in real Docker with a scripted builder and no model (an `alpine:3.20` repo with three tests of 2 s each, no engine, `baseline_command` running the whole suite): over 3 repetitions Stage 4 took 11 to 13 s per lane, and the baseline gate never reported `candidate_cached`. Most of those seconds are the candidate run that Tier 0 had just made under the other command.

## Proposal

A new optional field in `config.tests`:

```json
"tier0_command": "command" | "baseline"
```

- `command`, the default and what an absent field means: step 4 runs `tests.command` with every changed file in `$A2DLC_CHANGED` and `$A2DLC_TESTS` empty, as today.
- `baseline`: step 4 runs `baseline_command`, with the `$A2DLC_TESTS` (changed test files) and `$A2DLC_CHANGED` (changed sources) that the baseline gate of Stage 4 gives it, split by `tests.paths` the same way. The run goes through the suite cache under the key that gate uses for the same tree. When Stage 4 judges a candidate whose tree is the one of the Builder's last round, the gate's candidate run is a cache hit: it does not run, its coverage is merged as after a run, and the receipt says `candidate_cached: true`.

Everything else in step 4 applies to the run unchanged: it counts as a review only if it is readable, a failed or errored testcase is a `test` finding, and a green run with readable coverage gives the `coverage` findings.

Two cases fall back:

- Without `baseline_command`, `baseline` behaves as `command`: there is nothing else to run.
- When `tests.image` has no digest (a local build that was never pushed), the cache cannot be keyed to the image, so step 4 runs `baseline_command` without keeping the result. Stage 4 refuses that image for its own gate anyway.

The field is read from the base's config, like the rest of `config.tests`, so a diff cannot turn it on for itself. Any other value is refused when the config is read, as `rung` is.

Why it is opt-in: `baseline_command` is meant to run the tests the change affects (`vitest related`, `go test` on the touched packages), which can be fewer than the whole suite. With `baseline`, Tier 0's feedback covers only those tests, and if `baseline_command` writes no coverage, Tier 0 gives no `coverage` finding. A repo whose `baseline_command` runs the whole suite and writes coverage loses nothing and saves one run per accepted lane; a repo with a narrow one decides whether that trade is worth it.

## Implementation ahead of acceptance

The reference implementation carries this behavior behind the opt-in before the RFC is accepted, so that the factory can measure it. A config without the field behaves exactly as before. The protocol text in `docs/A2-DLC_v3.5_protocol.md` is marked as proposed until the 14-day comment period of [README.md](README.md) ends and the maintainer decides. If the RFC is rejected, the commit is reverted, together with the schema field and the protocol sentence.

## Compatibility

No existing document becomes invalid: the field is optional and an absent field keeps today's behavior. Per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) an added optional field does not bump `schema_version`. A binary older than this RFC rejects a config that sets the field, because configs are decoded strictly; that is the pairing rule working as intended. The receipt gains no field: `candidate_cached` already exists, and only its description widens to say where the cached run may come from.

## Alternatives

- **Make Stage 4 reuse Tier 0's run of `tests.command`.** The two commands differ by design (`tests.command` must run only the files of `$A2DLC_TESTS`), so a result of one cannot stand for the other.
- **Turn it on by default.** It would narrow Tier 0's feedback, and lose its `coverage` findings, for every repo whose `baseline_command` is narrower than the whole suite or writes no coverage. The default stays what RFC-0002 specified.
- **Merge the NOP run and the baseline run of Stage 4.** A larger change to Stage 4 with its own tradeoffs; it was deferred, and it does not remove the duplicate with Tier 0.
- **Do nothing.** Each accepted lane keeps paying one run of `baseline_command` on a tree whose answer is already known.

## Verification

- Unit tests (`internal/app/suite_cache_test.go`):
  - with `baseline`, Tier 0 on a candidate, then `check` of the same candidate tree gives `candidate_cached: true`, both with a `baseline_command` that runs the whole suite and with one that names `$A2DLC_CHANGED`;
  - without the field, the same sequence gives `candidate_cached: false`;
  - with `baseline` and no digest, step 4 runs `baseline_command`; with `baseline` and no `baseline_command`, it runs `tests.command`;
  - an unknown value is refused when the config is read.
  - The first two tests fail on the implementation before this RFC.
- Schema examples: valid `config__tests-tier0-baseline.json`; invalid `config__tier0-command-unknown.json`. `contracts/tests/validate.py` and `contracts/tests/check_rfc.py` green.
- The two-lane chain of the Problem section, 3 repetitions per arm, the implementation without the field against it with `baseline`:
  - `candidate_cached: true` on all 6 lanes with the field, and on none without it;
  - Stage 4 per lane: 11 to 13 s without the field (median 12 s), 4 to 7 s with it (median 5 s); Stage 3 stays within the same range (11 to 16 s);
  - the whole chain: median 76.7 s without the field, 61.9 s with it (19 % less);
  - the verdicts are the same in both arms.
- Not measured yet: a factory run at the scale of the sixth freeze with the field on.
