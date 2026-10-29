# Compatibility policy

This file says how the three version numbers of A2-DLC relate, what counts as a breaking change, and what a consumer does with fields and versions it does not know. It describes the rules as they apply from the commit that added it; changes to it follow [rfcs/README.md](../rfcs/README.md).

## The three version numbers

| Number | Where it is | What it identifies | Example |
|---|---|---|---|
| RFC version `vMAJOR.MINOR` | Title and status line of the RFC text ([rfcs/0001-a2dlc-protocol.md](../rfcs/0001-a2dlc-protocol.md)) | What a conforming implementation must do | `v3.4` |
| Release version `vX.Y.Z` | Git tag of the reference binary; `a2dlc_version` in `verdict.json` | One build of the reference implementation | `v0.4.1` |
| `schema_version` | An integer field in every top-level document of [`contracts/`](../contracts); `const 1` in `contracts/defs.schema.json:6` | The shape of the JSON documents | `1` |

They move independently, under the rules below.

## Relations

1. **A release implements one RFC version and one `schema_version`.** The release notes on GitHub name both. There is no `CHANGELOG.md`; the release notes are the changelog.
2. **A change to the RFC text** bumps the RFC minor version when it adds or clarifies requirements without making a document that was valid invalid, and the major version when an implementation that followed the previous version stops being conforming. Typo and wording fixes bump nothing.
3. **A `schema_version` bump needs an RFC version change in the same release**, and the RFC says why.
4. **A release can change without the RFC changing**: bug fixes, speed, new engines that follow the existing contracts.
5. **While the release version is `0.x`**, a minor release (`0.Y.0`) may add optional fields and commands; a patch release (`0.Y.Z`) changes no schema. From `1.0` on, a major release is the one that bumps `schema_version`.

## When `schema_version` bumps

It bumps when a consumer built for the previous value could misread a document of the new one without noticing:

- a field is removed or renamed;
- the type, format or meaning of an existing field changes;
- a value is removed from an enum, or its meaning changes;
- a new field is required;
- the rule for unknown fields changes (see below).

It does not bump when an optional field or an enum value is added, because of rule 5 and the pairing rule below. That is the practice up to now: `config.critical_code` and `targets[].note` were added under `schema_version` 1 after `v0.4.1`.

## Unknown fields

**Today every schema is closed.** `contracts/*.schema.json` set `additionalProperties: false`, and the reference implementation decodes with `DisallowUnknownFields` (`internal/contract/contract.go:540-543`). A document with a field its schema does not list is invalid, and the reference binary rejects it. A consumer does not drop unknown fields silently: a field the consumer does not know can be a restriction (for example `protected_paths` or `critical_code`), and rejecting is what keeps an older binary from ignoring it.

**The pairing rule.** Because the schemas are closed, a document must be read with the schema of the release that produced it. The verdict says which release that is (`a2dlc_version`). Other documents do not carry it, so the producer's release has to be known another way, for example from the repository's pinned `A2DLC_VERSION`. A consumer that reads documents from a newer release than its own schema will reject the new fields, which is the intended outcome.

**Not decided here.** Making consumers ignore unknown fields (the rule in-toto's attestation spec uses) would let a minor release add fields without breaking older readers. It also lets an older reader ignore a restriction. It changes the rule for unknown fields, so it is a `schema_version` bump and an RFC, and it is not part of this policy.

## Unknown `schema_version`

A document whose `schema_version` the consumer does not implement is invalid, never a pass. The schemas declare `const 1`, so the reference implementation refuses any other value.

## Deprecation

A field or behavior that will be removed is marked as deprecated in the schema `description` and in the release notes for at least one minor release. It is removed only together with a `schema_version` bump.

## Schema identifiers

The `$id` of each schema is the raw GitHub URL of the file on `main`, for example `https://raw.githubusercontent.com/falsable/a2dlc/main/contracts/verdict.schema.json`. The `$ref`s between schemas are relative, so they resolve under the same base.

Two things follow from `main` being a moving branch:

- The `$id` names the latest schema, not the schema of a release. To pin a release, read the file at the tag: `https://raw.githubusercontent.com/falsable/a2dlc/v0.4.1/contracts/verdict.schema.json`.
- A validator should load the schema files from the release it targets and register them under their `$id`, as `contracts/tests/validate.py` does, instead of fetching the `$id` while validating.

The examples in `contracts/examples/valid` and `contracts/examples/invalid` belong to the contract. Changing which of them validates is a change to the contract and follows the rules above.
