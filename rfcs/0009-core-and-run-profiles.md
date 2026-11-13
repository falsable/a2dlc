# RFC 0009: Two profiles, Core (the gate) and Run (the orchestrator)

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 (a new section "Profiles" after §1), `conformance/README.md`
- **Changes `schema_version`:** no

## Decision

Owner decision of 2026-11-13: the Run profile has a normative core (R1 to R6 below) made only of behaviour the reference
implementation has and a test pins. This RFC stays Draft as a document until its comment period ends; what changes is
what it proposes, which is no longer "Draft as a whole until §12 validates it".

## Problem

RFC-0001 marks only §6.4 (the receipt) as normative; the rest is pending validation. The text has about 13,400 words and
`contracts/` has 52 schemas, most of them for `a2dlc run` (roles, ledger, plans, stages −1 to 7). Someone who wants to
write a second gate, or to read A2-DLC receipts in a CI, cannot tell which part they must implement. The conformance kit
already splits its vectors into four classes (`verifier`, `producer`, `engine`, `run`), but no text says that the first
three are enough to claim anything.

## Proposal

New section of RFC-0001, after §1:

> ### Profiles
>
> An implementation claims one or both profiles, with the RFC version and `schema_version` it handles.
>
> **Core** is the gate: given a base and a candidate tree, decide PASS, FAIL or INCONCLUSIVE and write a receipt a
> verifier can check. It is normative and consists of:
>
> - the meaning of PASS, FAIL and INCONCLUSIVE (§2, §5 "Checks", §6.4): PASS is "no divergence found within the recorded
>   budget", anything the gate could not decide is INCONCLUSIVE, and a check lowers a verdict and never raises it;
> - the engine protocol: `contracts/engine-request.schema.json` and `contracts/engine-response.schema.json` for the
>   `diff` operation, which a Core engine must implement; the other operations `a2dlc check` sends (`classify_lines`,
>   `list_tests`, `stub`) are optional. An engine without them is still a Core engine, at a cost the receipt shows: without
>   `classify_lines` the lines it would have noted stay unexamined (so the verdict can be INCONCLUSIVE), and
>   without `stub` the test gate has no stubbed base and records that in the receipt;
> - the receipt (§6.4): `verdict`, `waivers`, `approvals`, the `tree_hash` rules and the excluded paths;
> - the configuration it reads: `config`, `deps-allow`, `check-response` (for `config.checks`), and the
>   `escalation` documents it writes for an agent;
> - the exit codes of `check` and `verify` (`docs/COMPATIBILITY.md`).
>
> **Run** is the orchestrator: stages −1 to 7, the roles and their isolation, the hash-chained ledger, intents, plans
> and pre-authorizations. It builds on Core. Part of it is normative: the items below, each of which the reference
> implementation already has and a test pins. Everything else in Run stays Draft until §12 validates it, and the text
> says so where it names such a part.
>
> The normative Run core:
>
> - **R1, isolation.** A role runs in its own container. What it sees is decided by the core, not by `roles.json`
>   (`internal/run/policy.go`): `roles.json` chooses the command and cannot widen the mounts, and an image or env entry
>   that would (`--volume=...`, a core variable) is refused. The network is `none` or `provider`
>   (`contracts/roles.schema.json`); `provider` goes through the egress proxy and the ledger records `proxy-allowlist`
>   with the proxy's digest. Tests: `TestLauncherIsolatesMounts`, `TestImageThatIsFlagIsRejected`,
>   `TestLauncherRejectsEnvOverridingCore`, `TestEgressProxyRunsByResolvedImageAndReportsIt`,
>   `TestEgressProxyThatCannotBeIdentifiedFailsClosed`, `TestEgressSetupFailuresAreErrorsAndRoleDoesNotRun`
>   (`internal/run/launcher_test.go`).
> - **R2, the ledger.** Every launch appends one entry to `.a2dlc/run/<slice>/ledger.json`
>   (`contracts/run-ledger.schema.json`), chained as `hash = sha256(prev_hash + canonical JSON of the entry without
>   hash)`, with `seq` from 1 and `prev_hash` null in the first. `a2dlc verify-run --slice <id>` (`Verify` in
>   `internal/run/ledger.go`, `VerifyRun` in `internal/run/machine.go`) fails a broken link, an edited or reordered entry,
>   a ledger of another run, a stage artifact that changed, an authorization use the chain does not bind, and a ledger
>   that records less spend than `state.json` (deleted or truncated). Limits, stated in the doc comment of `VerifyRun`:
>   the chain has no anchor outside the folder, so whoever rewrites `ledger.json` and `state.json` together is not
>   detected, and neither is removing entries that recorded no cost. Tests:
>   `TestVerifyRunDetectsTamperedLedgerAndArtifacts`, `TestVerifyRunRejectsAHiddenOrEditedAuthorizationUse`,
>   `TestLedgerWithLessTokensIsReported`; conformance vectors `V-run-004` to `V-run-032`.
> - **R3, budget (CB-03).** `max_cost_usd` and `budget.max_tokens` of the intent (`contracts/intent.schema.json`) are
>   checked before each stage and between launches: a run whose reported spend is over a limit launches nothing more and
>   escalates with a reason that starts `CB-03` (`exceeded` in `internal/run/intent.go`). The check is on what the
>   role CLIs reported; a role with no report adds nothing and is counted in `unreported_roles`. A value of
>   `roles.<role>.env` may carry `{remaining_usd}`, replaced at each launch with the USD left under `max_cost_usd`, four
>   decimals, and `{round_usd}`, half of the same USD while more Stage 3 builder rounds follow (the `tier_up`
>   round included) and all of it in the last round (`{remaining_usd}` outside Stage 3); a run with no cost cap, or with nothing left, refuses the launch
>   (`withRemaining`, `contracts/roles.schema.json`). Tests: `TestCB03CutsAtEachStage`,
>   `TestCostChainFourCountersAddUpAndCB03CutsByTokensAndByCost`, `TestTokenCapTripsAndIsNamed`,
>   `TestRemainingUSDIsFilledAtEachLaunch`, `TestRoundUSDSplitsTheRemainingOverTheRoundsLeft`.
> - **R4, builder exhaustion (CB-02b).** When Stage 3's builder spends `max_inner_iterations` rounds without Tier 0
>   green, the stage runs one more round with the role's `tier_up` if `roles.json` declares one, and otherwise
>   escalates with kind `builder_exhaustion` (`internal/run/stage3.go`, `internal/run/cb02b.go`). If two consecutive
>   rounds return the same Tier 0 findings (same kind, file, line and message), the remaining normal rounds are skipped
>   and the run goes to the `tier_up` round or the escalation, with "no progress" in the reason; a round that passes
>   Tier 0 resets the comparison. Tests: `TestCB02bExhaustedWithoutTierUpEscalatesWithItem`,
>   `TestCB02bNoProgressStopsEarlyWithoutTierUp`, `TestCB02bNoProgressJumpsToTierUp`,
>   `TestCB02bChangingFindingsUseAllRounds`, `TestCB02bGreenTier0RoundBetweenEqualFindingsIsNotAStall`
>   (`internal/run/stage3_cb02b_test.go`).
> - **R5, escalations and exit codes.** A stop that has a kind in `contracts/escalation.schema.json`
>   (`builder_exhaustion`, `spec_divergence` and the rest) is written for an agent to
>   `.a2dlc/run/<slice>/escalations.json` in that shape (the examples in `contracts/examples/` decode against the Go
>   structs in `TestExamplesOfContractDecode`). A CB-03 stop has no kind there: it is recorded only as the stage
>   reason (`CB-03 ...`) in `state.json`, with outcome `escalated`. `a2dlc run` exits 0 on PASS, 1 on FAIL, 2 on
>   INCONCLUSIVE, on escalation, or when a2dlc itself failed, and 64 on bad usage (`cmd/a2dlc/main.go`; the single list
>   is `exitTable` in `cmd/a2dlc/exitcodes.go`, checked by `TestExitTableMatchesWhatTheCommandsReturn` and printed in
>   `docs/COMPATIBILITY.md`). `verify-run` exits 0, 1 or 64 the same way.
> - **R6, no raising.** The verdict of a run is the Core gate's. Stage 4 calls the gate (`checkWith` in
>   `internal/run/machine.go`); Stage 5 starts only when its verdict is PASS (inline check in `internal/run/stage5.go`), and
>   Stages 6 and 7 only when Stage 4, and Stage 5 if it ran, were PASS (`withoutPass` in `internal/run/stage6.go`), so a run
>   is not PASS above what the gate said. A stage that cannot judge ends INCONCLUSIVE, not PASS. Tests: `TestStage5SkipsWithoutPassOrTests`,
>   `TestJudgingAnInconclusiveMutantDoesNotPass`, `TestStage5CapBelowListLeavesItNotRunAndNotPass`,
>   `TestStages6And7DoNotRunWhenStage4IsNotPass` (green consumers, Stage 4 INCONCLUSIVE: no consumer runs, both stages skip).
>
> Not normative, still Draft: the content and order of stages −1 to 6 beyond what R1 to R6 say; the payloads of each role
> (`contracts/role-payloads.schema.json`); the router and the N2 dual-builder mode; the Saboteur and the mutation
> thresholds; plans and the plan machine (RFC-0008) except `verify-run --plan`, which the `run` class pins; the
> `tier_up` choice of a family; the spend lines `a2dlc run` prints. No test of the reference pins them as a contract
> yet; a second implementation may differ there and still claim the Run core.
>
> A Core implementation does not have to implement Run. A Run implementation must implement Core.

`conformance/README.md` gains one line: the `verifier`, `producer` and `engine` classes are the Core profile; the
`run` class pins the ledger part (R2) and the acceptance of a plan run of the Run profile. R1, R3, R4, R5 and R6 are
pinned by the reference's own tests, not yet by vectors.

## Compatibility

No document changes shape and no implementation stops working. The reference binary implements both profiles. The
sentence of `docs/NAME_AND_CONFORMANCE.md` an implementation may use becomes "Implements A2-DLC RFC-0001 v3.5, Core
profile, `schema_version` 1", with "Not tested against a conformance suite" when it has not run the three Core classes.

## Alternatives

- **Keep one profile.** Every implementation would have to carry the orchestrator to call itself A2-DLC, and nobody
  writes a second gate under that cost.
- **Move Run into a separate RFC series.** Cleaner, but it renumbers RFC-0002 to RFC-0008, which all amend stages of
  Run, and breaks their links. A section that names the profiles gives the same split without moving text.
- **List the Core schemas in a file instead of the RFC.** A file can drift from the text; the conformance classes
  already exist and are what an implementation is judged by.

## Verification

- `python conformance/run.py --binary <implementation> --verify-arg=--strict` over the `verifier`, `producer` and
  `engine` classes is the Core check; the `run` class is not needed to claim Core.
- `verifiers/python/` shows the split inside one implementation: its receipt checks (the `verifier` class) need none of
  its run-ledger code, which answers the `run` class. It does not compare the ledger's spend with `state.json`
  (`verifiers/python/AMENDMENTS.md`, D6), so it passes R2 except for the truncation vector `V-run-030`, which carries a
  `known_failure` for it.
- The Run core is checked by `python conformance/run.py --class run` for R2, and by the tests named under R1 to R6 for the
  rest.
