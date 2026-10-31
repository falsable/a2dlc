# RFC 0003: A new test that passes on the base, next to one that proves the change, is a regression guard

- **Status:** Accepted (2026-10-31, bead a2dlc-otj)
- **Author:** Aldemar Hernández, falsable (bead a2dlc-otj)
- **Amends:** RFC-0001 §5 Stage 4 (the NOP/Oracle gate), `contracts/config.schema.json` and `contracts/verdict.schema.json`
- **Changes `schema_version`:** no

## Problem

The NOP/Oracle gate judges each new test on its own. In a change that declares the intent to change behavior (the base got stubs, or there is an `enforce_new_behavior` waiver), a new test that passes on the base is `FAIL`: "the test passes on the base with stubs: it does not prove the change". The protocol's way out is the `characterization_test` waiver (§6.4.6), and like every waiver it needs a person's signature (§6.4.7). A consumer that runs a2dlc unattended cannot reach it. The factory, for example, refuses any lane that writes under `.a2dlc/` and accepts only a check with no pending waiver. For such a consumer, the per-test rule rejects a change whose tests include one sentence of the issue that the base already satisfies, however good the other tests are.

It happened in the sixth freeze of the factory (2026-10-31, `rung: gates`, a 9-issue chain on `tinylinks` and a 7-issue chain on `click`). 9 of the 16 lanes were rejected:

- In 4 of them (tinylinks issues 2, 5 and 6, click issue 5), NOP was the only Stage 4 blocker.
- 2 more (tinylinks 7 and 9) had a NOP failure plus an `INCONCLUSIVE` engine target.
- In all 6, each test that passed on the base sits in the same test file as a test that fails on the base and passes on the candidate.

The 10 tests that passed on the base fall into two groups:

- 9 assert behavior the base already had.
  - The 7 in tinylinks each restate a sentence of their issue: the existing validator already rejects `javascript:`, `data:` and `ftp:` URLs; "links without the field never expire"; "still redirects and counts clicks before expiry", the converse of the issue's "after it passes ... instead of redirecting"; "still generates a random slug"; "does not rate limit redirects or stats".
  - The 2 in click were not matched against the issue text: a `Sentinel` lookup by name, and a plain copy of a command keeping `UNSET`, where the issue was about pickle.
- 1 ("DELETE returns 404 when the slug does not exist") passes on the base only because the route does not exist yet.

The sealed holdout of each of the 4 NOP-only issues was run on the candidate and on the candidate's parent, with the factory's holdout image, command and limits. All 4 candidates pass their holdout and all 4 parents fail it. That is n=4, and it shows the candidates meet the issue; it is not a false-accept rate.

## Proposal

**Config.** A new optional field of the `tests` block in `contracts/config.schema.json`, next to `invalid_base_failures`, because it only means something when the repo declares tests:

```json
"nop_base_passing": {
  "enum": ["fail", "guard"],
  "default": "fail",
  "description": "What the NOP/Oracle gate does with a new test that passes on the base in a change that declares the intent to change behavior. fail (the default, and what an absent field means): the test is FAIL, as in RFC-0001. guard: the test is a regression guard, recorded and not blocking, when another new test in the same test file proves the change; otherwise it is FAIL. Read from the base's config, like the rest, so a diff cannot turn it on for itself."
}
```

**Normative text.** A new item under the NOP/Oracle gate in RFC-0001 §5 Stage 4, as it would appear in `docs/A2-DLC_v3.5_protocol.md`:

> - **A new test that passes on the base, next to one that proves the change.** With `config.tests.nop_base_passing: "guard"`, a new test that passes on the base, in a change that declares the intent to change behavior, is a regression guard when another new test in the same test file proves the change: its failure on the base is valid and it passes on the candidate. A guard does not block and is recorded in the receipt with `guard: true`. The test file is the one the runner gives, else the testcase's classname. A testcase that names neither (`junit::<name>`) is never a guard. Without a proving test in the same file, or with the default `"fail"`, the test is `FAIL` as before. A guard never stands in for the proving test: the change still needs one.

**Receipt.** A new optional field on the entries of `tests` in `contracts/verdict.schema.json`:

```json
"guard": { "type": "boolean", "description": "nop_oracle: passes on the base in a change that declares the intent to change behavior, and another new test in the same test file proves the change (config.tests.nop_base_passing is guard, RFC-0003): it is recorded and does not block." }
```

An entry has at most one of `guard` and `characterization` set to true. `characterization` keeps its meaning (the change declares no intent), so a receipt still tells a refactor's characterization test apart from a guard.

**Why it keeps what NOP proves.** NOP is the single mutant of Stage 5, the whole diff reverted, at a fraction of the cost. A guard is admitted only next to a test that fails when the diff is reverted, so the single mutant is still killed in every file that carries a guard. A guard passes on the base and on the candidate, so it cannot by itself make a candidate pass.

**Coverage does not change.** The coverage the check judges is merged from every run on the candidate tree, whatever each test's verdict. A guard's executed lines already counted before this RFC, and they count the same after it. The same holds for the note `inconclusive_covered_by_tests`: it reads that coverage and no test verdict. What changes is reach. A lane that NOP used to fail is now judged by its other gates, and a coverage note can be what lets it pass. A guard has the standing every existing test already has: running a changed line counts as executing it, and only mutation (Stage 5) measures whether the line's behavior is asserted. A consumer that runs `rung: gates` gives up that measure, as it already does.

**Who turns it on.** The field is read from the base's config. A candidate cannot enable it for its own run.

## Compatibility

The field is optional, and an absent field keeps today's behavior, so every existing config and verdict stays valid and `schema_version` does not change. The RFC text gets a minor version bump, because it adds a requirement without making a valid document invalid ([docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md)), and the first release that implements it is a minor release (`0.Y.0`). The schemas are closed, so a verdict carrying `guard: true` is rejected by a reader that has an older schema. That is the pairing rule working as intended. A consumer that reads only part of the verdict and ignores the rest, as the factory does, needs no change.

## Alternatives

- **Keep the per-test rule, and have a person sign `characterization_test` waivers.** This puts back the person that unattended use removes. The waiver stays available to anyone who wants it.
- **Change the default for everyone.** This would change what a `FAIL` on NOP means in every existing receipt. The opt-in is the way to gather evidence, and a later RFC can change the default.
- **Admit a guard when a proving test exists anywhere in the run.** This is looser. Requiring the same file cost nothing on the evidence: 6 lanes out of 6.
- **Tell the Builder to assert such clauses inside a test that also proves the change.** The gate then passes the same assertion, but the receipt no longer shows it. It stays as a fallback probe (bead a2dlc-cwo) if this RFC is rejected.
- **Reuse `characterization: true`.** Its schema defines it as a change that declares no intent, so reusing it would make receipts ambiguous.
- **Let a2dlc sign the waiver itself.** This contradicts §6.4.7: the agent can write a waiver and cannot write a valid signature.

## Verification

- Unit tests of the NOP judgment, one per case:
  - with `guard`, a base-passing test and a proving test in the same file gives `PASS` with `guard: true`;
  - with `guard`, no proving test in the file gives `FAIL`;
  - with `guard`, a proving test only in another file gives `FAIL`;
  - with the default, `FAIL` as today;
  - a `junit::<name>` testcase gives `FAIL`;
  - without declared intent, `characterization` as today.
- Schema examples:
  - valid: a config with `nop_base_passing: "guard"`, and a verdict test entry with `guard: true`;
  - invalid: an unknown `nop_base_passing` value, and an entry with both `guard` and `characterization` true.
- `contracts/tests/check_rfc.py` and `contracts/tests/validate.py` green.
- Replay of the sixth-freeze inputs with `guard`:
  - tinylinks 2, 5 and 6 and click 5 pass Stage 4;
  - tinylinks 7 and 9 stay `INCONCLUSIVE` on their engine target;
  - the lanes accepted in that freeze keep their verdict.
