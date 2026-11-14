# RFC 0010: A new test that does not appear on the base can pass NOP, when the repo opts in

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable (bead a2dlc-4dt.6)
- **Amends:** RFC-0001 §5 Stage 4 (the NOP/Oracle gate), `contracts/config.schema.json` and `contracts/verdict.schema.json`
- **Changes `schema_version`:** no

## Problem

The NOP/Oracle gate runs the candidate's new tests on the base. A new test of new code fails there validly only when the
file loads, and in a typed language it loads only when stubs stand in for the new symbols. Stubs come from an engine
(`tests.stub_engine` or the language engine). A repo gated by tests alone, with `engines: []`, has none. Its new test
file does not load on the base, the runner reports none of its testcases, and each new test is `INCONCLUSIVE`: "the test
did not appear on the base (collection or import error): stub missing". A run turns that into an `INCONCLUSIVE` outcome,
and an unattended consumer, such as the factory, rejects it.

It happened in a measured comparison on a TypeScript service (Entropos, about 100 `node:test` tests, a tests-only
config). In 2 of 5 feature tasks (`nyd`, `scopehash`), the builder delivered in one round, Tier 0 passed, and Stage 4
ended `INCONCLUSIVE` only for this reason. The sealed holdout of each task, run afterwards on those candidates, passed:
90 of 90 and 105 of 105. That is n=2. It shows the gate rejected candidates that met their issue; it is not a
false-accept rate.

## Proposal

**Config.** A new optional field of the `tests` block, next to `nop_base_passing`:

```json
"nop_base_missing": {
  "enum": ["inconclusive", "pass"],
  "default": "inconclusive",
  "description": "What the NOP/Oracle gate does with a new test that does not appear on the base at all and passes on the candidate. inconclusive (the default): INCONCLUSIVE, stub missing. pass: PASS, recorded with tests[].absent_on_base. Read from the base's config."
}
```

**Normative text.** A new item under the NOP/Oracle gate in RFC-0001 §5 Stage 4, as it appears in
`docs/A2-DLC_v3.5_protocol.md`:

> - **A new test that does not appear on the base (RFC-0010).** With `config.tests.nop_base_missing: "pass"`, a new test
>   that does not appear on the base at all (its file does not load there and no stub made it load) and passes on the
>   candidate is `PASS`, recorded in the receipt with `absent_on_base: true`. It shows that the test needs the change to
>   load, which is weaker than a valid failure on the base: it does not show that its assertions need the change. A new
>   test whose file loads on the base and fails there for an invalid reason (import, collection, or a symbol without a
>   stub, such as a method the base's object does not have) and passes on the candidate is `PASS` too, recorded with
>   `invalid_on_base: true`: it needs the change to run, with the same weaker evidence. A test that is red on the candidate
>   is `FAIL`. With the default `"inconclusive"`, both stay `INCONCLUSIVE`, as above.

**Receipt.** A new optional field on the entries of `tests` in `contracts/verdict.schema.json`, `absent_on_base`. An
entry with `absent_on_base: true` has gate `nop_oracle`, verdict `PASS`, base `missing`, and neither `guard` nor
`characterization`. A second optional field, `invalid_on_base`, marks the case of a test that loaded on the base and failed there for
an invalid reason: gate `nop_oracle`, verdict `PASS`, base `failed`, and neither `guard`, `characterization` nor
`absent_on_base`.

**Amendment, 2026-11-14 (bead a2dlc-4dt.10).** The first text kept a test that loads on the base and fails there for an
invalid reason `INCONCLUSIVE` under `pass`. In a measured run on Entropos (keycap, a non-Anthropic builder), the builder's
new tests called methods the base's store object lacks (`productionStore.setApiKeyMonthlyCap is not a function`); Stage 4
ended `INCONCLUSIVE` and the sealed holdout on that candidate passed 103/103. A test that fails on the base because a member
it calls does not exist there needs the change in the same way as one whose file does not load, so `pass` now admits both,
each named in the receipt.

**What is given up.** NOP is the single mutant of Stage 5: the whole diff reverted. A test absent on the base does die
when the diff is reverted, but by failing to load, so a test with no assertion would pass this gate as well. The other
gates still apply (the candidate's full suite, the baseline, coverage of the changed lines), and only mutation measures
whether assertions bite. A repo that opts in states that it accepts this weaker evidence for new code it cannot stub. The
receipt names every test admitted this way, so a reader can tell them from tests that failed validly on the base.

**Who turns it on.** The field is read from the base's config. A candidate cannot enable it for its own run.

## Compatibility

The field is optional and an absent field keeps today's behavior, so every existing config and verdict stays valid and
`schema_version` does not change. Under [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) it is a minor change of the
RFC text. The schemas are closed, so a verdict carrying `absent_on_base: true` is rejected by a reader with an older
schema, which is the pairing rule working as intended.

## Alternatives

- **Generate stubs for TypeScript without an engine.** Stubs need the symbols' signatures, which is engine work. It
  remains the stronger answer and does not exclude this one.
- **Run the new tests on the base plus the candidate's added files.** A test of a new module would then pass on the
  base and be `FAIL` for not proving the change, which is wrong in the other direction.
- **Change the default for everyone.** It would change what `INCONCLUSIVE` on NOP means in existing receipts. The
  opt-in gathers evidence first, as RFC-0003 did.
- **Let a consumer accept `INCONCLUSIVE` runs.** That would also accept every other cause of `INCONCLUSIVE`, such as an
  unjudged source file. This RFC admits one named case and records it.

## Verification

- `TestJudgeNOPMissingOnBase` (`internal/app/gates_test.go`): with `pass`, a new test absent on the base and green on the
  candidate is `PASS` with `absent_on_base`, an import failure on the base is `PASS` with `invalid_on_base`, and a red
  one is `FAIL`. With the default, both are `INCONCLUSIVE`.
- Schema examples:
  - valid: `config__tests-nop-missing-pass.json` and `verdict__tests-absent-on-base.json`;
  - invalid: `config__nop-base-missing-unknown.json`, `verdict__absent-on-base-but-ran.json` and
    `verdict__invalid-on-base-but-missing.json`.
- `contracts/tests/check_rfc.py` and `contracts/tests/validate.py` green.
- Replay with `nop_base_missing: "pass"` in the target overlay of the two candidates above: Stage 4 is `PASS`.
