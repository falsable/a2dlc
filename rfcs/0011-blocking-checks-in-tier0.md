# RFC 0011: Blocking checks that fire at stage:3 feed back in Tier 0

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §5 (Stage 3 and Checks), `contracts/engine-tier0-response.schema.json` (finding kind `check`)
- **Changes `schema_version`:** no

## Problem

`config.checks` already accepts `fire_on: ["stage:3"]`, but `a2dlc run` never fires a check outside Stage 4. A blocking
check such as a formatter (`black --check`) therefore reaches the builder only as a `check_failed` escalation after its
last round. Measured on a factory end-to-end run over a Python repo: the builder's fix passed Tier 0 in one round, Stage
4 failed on `black` for one test file, and the run escalated. The builder never saw the finding it could have fixed in a
second round.

## Proposal

In Stage 3, after step 4 (the declared tests), new step 5:

> 5. **The blocking checks of this stage** (RFC-0011). Each `config.checks` entry with `severity: "blocking"` whose
> `fire_on` lists `stage:3` runs on the candidate of the round, with the same execution as in §5 Checks (base at
> `/base:ro`, candidate at `/cand:ro`, `--network none`, the matched changed files). Each finding of a check that `ran`
> is one Tier 0 finding of kind `check`: `file` is the finding's file, `line` the `n` of `<file>::L<n>` (else null), and
> the message starts with the check id. A check in `error` or `timeout` gives no finding: Stage 4 judges it again with
> the truth table of §5 Checks. Advisory checks do not run here. A finding of step 5 spends a round like any other.

In §5 Checks, `fire_on` adds: a blocking check that fires at `stage:3` runs in the builder's Tier 0 and its findings
return to the builder; listing `check` too keeps it in Stage 4 and in the pre-push gate.

The finding kind enum of `engine-tier0-response.schema.json` gains `check`. The core writes it; an engine never emits it.

Waivers are not read in Tier 0: an `accept_finding` waiver still applies in Stage 4, and the builder may receive a finding
that Stage 4 would accept.

## Compatibility

Additive. A config without `stage:3` in any `fire_on` behaves as before. A Tier 0 response reader that rejects unknown
kinds must accept `check`; `schema_version` does not change ([docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md): a new
enum value written only by the core).

## Alternatives

- Run every blocking check in Tier 0 regardless of `fire_on`. Rejected: a slow check (a type checker over the whole repo)
  would run on every round, and the repo already has a field to say where a check fires.
- Return the Stage 4 `check_failed` finding to the builder and rerun from Stage 3, as the divergence finding does.
  Rejected: it costs a full Stage 4 per round, where a formatter takes seconds.

## Verification

- `go test -count=1 ./internal/app -run TestTier0RunsTheBlockingChecksOfStage3`: blocking at `stage:3` gives `check`
  findings with the line from `::L<n>`; clean, only at `check`, advisory, outside the builder's loop and in error give none.
- `go test -count=1 ./internal/run -run TestStage3BlockingCheckFeedsTheBuilder`: round 1 leaves what the check flags,
  round 2 receives it as a `check` finding and fixes it, and Stage 4 records the check as `ran` with no findings.
- `contracts/examples/valid/engine-tier0-response__check-feedback.json` validates against the schema.
