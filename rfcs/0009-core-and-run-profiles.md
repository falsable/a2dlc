# RFC 0009: Two profiles, Core (the gate) and Run (the orchestrator)

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 (a new section "Profiles" after §1), `conformance/README.md`
- **Changes `schema_version`:** no

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
> and pre-authorizations. It builds on Core (every stage that judges code calls the Core gate) and stays Draft as a
> whole until §12 validates it.
>
> A Core implementation does not have to implement Run. A Run implementation must implement Core.

`conformance/README.md` gains one line: the `verifier`, `producer` and `engine` classes are the Core profile; the
`run` class is the Run profile.

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
  its run-ledger code, which answers the `run` class.
