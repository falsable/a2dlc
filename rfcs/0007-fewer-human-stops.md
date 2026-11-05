# RFC 0007: Fewer human stops: batch approval and pre-authorized behavior changes

- **Status:** Draft
- **Comment period:** 2026-11-05 to 2026-11-19 (14 days, [README.md](README.md) step 2); comments on the pull request that opened it
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §6.4.7 (approvals), §6.4.8 (verification); `contracts/approvals.schema.json`; new `contracts/authorization.schema.json`
- **Changes `schema_version`:** no

## Problem

A person signs every intended behavior change, one target at a time. That is the property that makes the receipt worth something: the agent can write a waiver and cannot write a valid signature (§6.4.7). It is also the most frequent stop that waits for a person.

- `a2dlc approve <target>` signs one target per call. A slice that intentionally changes five functions means five commands and five passphrase prompts.
- The person decides after the build. They read counterexamples one by one, although the intent (Stage 0) already said which behavior was meant to change before any code was written.
- Escalations already carry a `cluster_id` (§7, escalation hygiene) so that the same root cause is decided once. Approvals do not use it.

The rule this RFC must not break is in CLAUDE.md, "Never": an approval signature that does not bind the file's blob would be valid for future changes to the same function. Part B below is exactly a signature made before the blob exists, so it is bounded much more tightly than an approval, and it is opt-in.

## Proposal

### Part A: batch approval (no change to what is signed)

`a2dlc approve --pending` reads the current tree's receipt and waivers and lists every target waiting for a signature, grouped by `cluster_id`. For each group it shows the targets, one shrunk counterexample (`args`, `old`, `new`) and the waiver's `reason`. The person confirms once, and the command writes one ordinary approval per target, as §6.4.7 defines it today: same signed text, same namespace, same `file_blob` binding.

Nothing in §6.4 changes. This part is an implementation change and is listed here because it is the cheap half of the same problem. `ssh-keygen -Y sign` takes several files in one call, and with OpenSSH 8.9 it asks for the passphrase once for all of them (measured on 2026-11-05 with a counting `SSH_ASKPASS`: one prompt for two files, both signatures valid), so the batch needs no `ssh-agent`. Implemented in the reference as `a2dlc approve --pending` (`internal/app/pending.go`); `--json` gives the same list to an agent or a UI and signs nothing.

### Part B: pre-authorized behavior changes (normative)

A new optional file, `.a2dlc/authorizations.json`, outside the `tree_hash` like `approvals.json`:

```json
{
  "schema_version": 1,
  "authorizations": [
    {
      "base_ref": "<commit id>",
      "intent_sha256": "<64 hex>",
      "targets": ["src/billing.py::apply_discount", "src/billing.py::round_total"],
      "decision": "enforce_new_behavior",
      "expires_at": "2026-11-12T00:00:00Z",
      "principal": "person@example.com",
      "signature": "-----BEGIN SSH SIGNATURE-----..."
    }
  ]
}
```

**Signed text.** An OpenSSH signature with the namespace `a2dlc-authorization` (distinct from `a2dlc-approval`, so neither verifies as the other) over:

```
a2dlc-authorization/1\n<base_ref>\n<intent_sha256>\n<decision>\n<expires_at>\n<target 1>\n...\n<target n>\n
```

with the targets sorted and deduplicated.

**What it covers.** At verification (§6.4.8), a waived target with no valid approval counts as approved when an authorization:

1. names that exact target: no globs, no `<file>::*`;
2. has the receipt's `base_ref`: a new base invalidates it;
3. has an `intent_sha256` equal to the SHA-256 of the intent the run was bound to (`a2dlc run`), or of an intent file committed in the judged tree under `.a2dlc/intents/` (`a2dlc check`); the authorization names which one through its hash, so an intent edited after signing no longer matches;
4. has a `decision` equal to the waiver's decision, which must be `enforce_new_behavior` or `preserve_legacy`;
5. has not expired at the receipt's `created_at`; and
6. verifies against the allowed signers, which never come from the repository (§6.4.7).

**What it never covers:** protected paths, `accept_finding` on checks, test waivers (`characterization_test`, `quarantine_flaky`), and any target that matches `config.critical_code`. Those keep needing an approval bound to the file's blob.

**What the receipt says.** A target covered this way is reported by verify as `authorized`, not `approved`, so a reviewer can tell a decision made before the code from one made after reading it.

**Why this does not undo the blob rule.** The blob rule exists so that an approval cannot be reused for a later, different change. An authorization cannot be reused either: it dies with its base, its intent and its expiry date, and it names exact symbols. What it gives up is that the person did not see the final code of those symbols. That is the trade, it is opt-in, and it is recorded in the receipt.

## Compatibility

- A repository without `authorizations.json` verifies exactly as today.
- `approvals.json` does not change. The new file and schema are additions; per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) no `schema_version` bumps.
- A verifier older than this RFC ignores the new file and refuses the targets it would cover as `unsigned_waiver`. That fails closed, which is the right direction for an older reader.
- `conformance/` needs vectors for each rule of "What it covers" and "What it never covers"; the clean-room verifier implements them from this text.

## Alternatives

- **Globs in authorizations.** Rejected: an agent could move a behavior change into any function matching the glob. Exact symbols only.
- **Trust the intent without a signature.** Rejected: the agent can edit `intent.json`. The signature is what the agent cannot produce.
- **An approval that does not bind the blob.** Rejected by the "Never" rule; an authorization is bounded by base, intent and expiry instead.
- **Approve by `cluster_id` with one signature over the cluster.** Considered for Part A. It changes what is signed and saves nothing over signing each target in one batch, so Part A keeps one approval per target.
- **Do nothing.** Every intended change keeps one stop and one passphrase per target.

## Verification

- **Part A:** `a2dlc approve --pending` on a tree with three pending waivers in two clusters writes three approvals that pass today's verify unchanged; a waiver edited after the batch makes its approval `waiver_changed`, as now.
- **Part B:** conformance vectors and unit tests where verify accepts a target covered by an authorization and reports it `authorized`, and refuses it when the base changed, the intent hash differs, the authorization expired, the target is not named exactly, the target matches `critical_code`, the target is a protected path or a test, or the signature uses the `a2dlc-approval` namespace.
- **Measurement:** on the factory chain runs, the number of human stops per slice before and after, with the same intents.

## Open questions

- Should `a2dlc run` stop at Stage 0 to collect the authorization signature, or should the person sign the intent before launching the run?
- Should an authorization also cover `spec_divergence` probes whose answer the intent already states, or do probes stay outside, as now?
