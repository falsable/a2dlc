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

## Exit codes

An agent that drives `a2dlc` branches on the exit code, so the codes are part of the contract and follow the rules above: a code is added in a minor release, and the meaning of an existing one changes only with a `schema_version` bump and an RFC. The table below is `exitTable` in [`cmd/a2dlc/exitcodes.go`](../cmd/a2dlc/exitcodes.go). `cmd/a2dlc/exitcodes_test.go` fails when a command returns a code the table does not list for it, when the table lists a code no command returns, or when this table differs from the Go one. `usage` is the answer to no command, an unknown command, or a directory that is not a git repository. A command that takes `--json` prints its document and uses the same codes.

| Command | Exit code | Meaning |
|---|---|---|
| `version` | 0 | Printed the version. |
| `usage` | 64 | No command, an unknown command, or the working directory is not a git repository (hook allows instead). |
| `check` | 0 | PASS, or --level 0 (informs, never blocks). |
| `check` | 1 | FAIL, or --require-signatures found a waiver without a valid signature. |
| `check` | 2 | INCONCLUSIVE, or a2dlc itself failed. |
| `check` | 64 | Bad usage: unknown flag, --level 0 combined with other flags, or --level not 0 or 1. |
| `run` | 0 | PASS. |
| `run` | 1 | FAIL. |
| `run` | 2 | INCONCLUSIVE, escalated to a person, or a2dlc itself failed. |
| `run` | 64 | Bad usage: missing --slice or unknown flag. |
| `verify-run` | 0 | The ledger chain and the run's artifacts recalculate. |
| `verify-run` | 1 | A problem was found, or the run could not be read. |
| `verify-run` | 64 | Bad usage: missing --slice or unknown flag. |
| `verify` | 0 | The commit may be pushed (warnings go to stderr). |
| `verify` | 1 | Blocked by evidence, or --strict found anything but a PASS receipt for the tree, or a2dlc or git failed. |
| `verify` | 64 | Bad usage: unknown flag. |
| `approve` | 0 | Signed (approve --pending: listed, nothing to sign, or signed). |
| `approve` | 1 | Not signed: the signing failed, or approve --pending was not confirmed. |
| `approve` | 64 | Bad usage: missing target, --key or --principal. |
| `authorize` | 0 | Signed. |
| `authorize` | 1 | Not signed: the request was refused or the signing failed. |
| `authorize` | 64 | Bad usage: missing --key or --principal, or unknown flag. |
| `plan` | 0 | plan check: valid. plan run: PASS. propose, replan, import: written. |
| `plan` | 1 | plan check: not valid. plan run: the plan was refused. propose, replan, import: not written. |
| `plan` | 2 | plan run: escalated. Any subcommand: a2dlc itself failed. |
| `plan` | 64 | Bad usage: missing or unknown subcommand or flag, or an unreadable plan or intent file. |
| `propose` | 0 | The role answered (status ok). |
| `propose` | 1 | The role did not give an answer that holds (status is not ok). |
| `propose` | 2 | a2dlc itself failed. |
| `propose` | 64 | Bad usage: missing --out or unknown flag. |
| `answer` | 0 | The answer was recorded. |
| `answer` | 2 | The run or the probe is not answerable. |
| `answer` | 64 | Bad usage: a flag is missing. |
| `finding` | 0 | Done. |
| `finding` | 1 | label: no such finding. |
| `finding` | 2 | The record is unreadable. |
| `finding` | 64 | Bad usage. |
| `hook` | 0 | Allowed, or informed in the hook's JSON answer (a Stop that blocks does so in that answer, not in the code). |
| `hook` | 2 | PreToolUse blocked the tool call: it edits a protected path without a signed approval, or it is git push --no-verify. |
| `hook` | 64 | Bad usage: the agent is not claude or gemini. |
| `install` | 0 | Installed. |
| `install` | 1 | Not installed. |
| `install` | 64 | Bad usage: unknown flag. |
| `doctor` | 0 | No blocking finding. |
| `doctor` | 1 | At least one blocking finding. |
| `doctor` | 2 | doctor itself failed. |
| `doctor` | 64 | Bad usage: unknown flag. |
| `mcp` | 0 | The client closed the connection. |
| `mcp` | 1 | The server failed. |

## Schema identifiers

The `$id` of each schema is the raw GitHub URL of the file on `main`, for example `https://raw.githubusercontent.com/falsable/a2dlc/main/contracts/verdict.schema.json`. The `$ref`s between schemas are relative, so they resolve under the same base.

Two things follow from `main` being a moving branch:

- The `$id` names the latest schema, not the schema of a release. To pin a release, read the file at the tag: `https://raw.githubusercontent.com/falsable/a2dlc/v0.4.1/contracts/verdict.schema.json`.
- A validator should load the schema files from the release it targets and register them under their `$id`, as `contracts/tests/validate.py` does, instead of fetching the `$id` while validating.

The examples in `contracts/examples/valid` and `contracts/examples/invalid` belong to the contract. Changing which of them validates is a change to the contract and follows the rules above.
