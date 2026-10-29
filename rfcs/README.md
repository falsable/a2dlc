# RFCs

An RFC is a written proposal to change what the protocol requires or what a contract looks like. The protocol itself is RFC-0001; this directory holds its status and the RFCs that amend it.

| RFC | Title | Status |
|---|---|---|
| [0001](0001-a2dlc-protocol.md) | A2-DLC protocol | Consolidated draft (v3.5); §6.4, the receipt, is normative |
| [0002](0002-declared-tests-as-tier0.md) | Declared tests as the Tier 0 of files no engine covers | Draft |

## What needs an RFC

- A change to what a conforming implementation must do: stages, verdict semantics, what is signed, what is hashed.
- A change to the shape of a document in [`contracts/`](../contracts): a field added, removed or renamed, a changed enum, a changed `schema_version` rule.
- A change to the compatibility, naming or conformance policies.

These do not: bug fixes in the reference implementation that bring it in line with the text, new examples, typo and wording fixes in the RFC text (a pull request is enough; the maintainer bumps no version for them), and new engines that follow the existing contracts.

## Process

1. **Propose.** Copy [0000-template.md](0000-template.md) to `rfcs/NNNN-short-title.md`, where `NNNN` is the next free number, and open a pull request. The status is `Draft`. To amend RFC-0001 itself, the proposal states the section it changes and the new text.
2. **Comment.** The pull request stays open for at least 14 days for anything that changes normative behavior or a contract. Discussion happens on the pull request, in public. The author updates the RFC to answer objections or records why not.
3. **Decide.** The maintainer sets the status to `Accepted` or `Rejected` and writes the reason on the pull request. While there is one maintainer, this is a review of their own proposal when they are the author, which is why step 2 is not skipped ([GOVERNANCE.md](../GOVERNANCE.md)). Once there are two maintainers, an RFC needs approval from one who is not its author.
4. **Implement.** An accepted RFC is merged. The change to the normative text and to `contracts/` lands in the same pull request or in follow-ups that link the RFC. Contract changes follow [CONTRIBUTING.md](../CONTRIBUTING.md): schema first, with valid and invalid examples.
5. **Version.** The RFC text and `schema_version` are versioned as [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) says. The release notes of the first binary release that implements the RFC name the RFC and the version.

## Statuses

`Draft` (proposed, open for comment) → `Accepted` (merged; implementable) → `Final` (implemented in a release and no open amendments) or `Rejected`, `Withdrawn`, `Superseded by NNNN`.

## Precedence

The reference implementation is not the standard. If the RFC text, the schemas and the Go binary disagree, that is a bug: file an issue naming the three. For the shape of a document the schema in `contracts/` is the authority; for what a conforming implementation does, the RFC is. Where the RFC prose describes a shape that a schema contradicts, the schema wins until the RFC is corrected.
