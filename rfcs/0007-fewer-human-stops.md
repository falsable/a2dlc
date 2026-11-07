# RFC 0007: Fewer human stops: batch approval and pre-authorized behavior changes

- **Status:** Draft
- **Comment period:** 2026-11-05 to 2026-11-19 (14 days, [README.md](README.md) step 2); comments on the pull request that opened it
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §6.2 (the excluded paths), §6.4.7 (approvals), §6.4.8 (verification); `contracts/verdict.schema.json` and `contracts/run-ledger.schema.json` (one optional field each); new `contracts/authorization.schema.json`
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

A new optional file, `.a2dlc/authorizations.json`, outside the `tree_hash` like `approvals.json` (it is added to the excluded paths of §6.4.2, so signing one after a receipt does not make the receipt stale):

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

**What the receipt says.** A target covered this way is `authorized`, not `approved`, so a reviewer can tell a decision made before the code from one made after reading it. The receipt carries it in one optional top-level object, `authorization`: `covered` (target, the authorization's `id`, which is the sha256 of its signed text, its principal and expiry), `refused` (target and reason, for a matching authorization that could not cover it: invalid signature, signer not allowed, expired, critical or protected code) and, for a run, `intent_sha256`. The object only reports. Verify decides again from `authorizations.json`, the allowed signers and the base's config, and believes nothing in the receipt but the intent hash, which the authorization's own signature has to match. A refusal also travels in the `signature_pending` escalation of the target, so the person who reads it sees why the authorization did not stand in for an approval.

**How the reference applies it.** It is a rule of the checks of §6.4.8 and of the check that makes the receipt, in one place, so `a2dlc check`, `a2dlc run`, `verify` and the pre-push cannot disagree.

- *Only engine targets.* The authorization stands in for the approval of a target of an engine result that a waiver exempts from a FAIL. Test results, findings of checks, golden cases and protected paths never reach the rule, so "what it never covers" holds without a list of exceptions. `critical_code` and `protected_paths` are the base's.
- *The waiver is still needed.* An authorization replaces the signature, not the declaration: the waiver for the target, with the same `decision`, has to be in `waivers.json`. An agent writes it; it cannot write the signature.
- *Fail closed.* A file that does not decode, a date that does not parse, a receipt with no readable `created_at`, a base config that cannot be read or a signature that does not verify leaves the target waiting for an approval, exactly as without the file.
- *The intent.* `intent_sha256` is the bare hex sha256 of the bytes of the intent file, without the `sha256:` prefix the run's own artifacts carry. For `a2dlc run` it is the intent the run was bound to, recorded in the receipt as `authorization.intent_sha256`; for `a2dlc check` and for verify it may also be any file committed under `.a2dlc/intents/` in the judged tree. A receipt is a file the agent could edit, so the hash it names counts only if an authorization signed that same hash.
- *The ledger.* Each use of an authorization by a run is recorded in the run's ledger, in an optional top-level array `authorizations_used`: the stage, the time of use, the target and the whole signed authorization. `a2dlc verify-run` checks every record again against the allowed signers: the signature in the namespace `a2dlc-authorization`, the target among the authorization's, a decision of the two (`plan.accept` is not one: it accepts a feature plan, RFC-0008 section 4, and never covers a waived engine target), and `expires_at` not passed at the time of use. Each record is also bound into the hash chain by one entry of role `check` (family `script`, no image, no network, no cost) whose optional `authorization` object holds the target, the authorization's `id` and the sha256 of the whole signed record; the entry has the record's stage and time. verify-run fails the chain when a record has no entry, an entry has no record, or either was edited, so a ledger cannot hide or alter a reliance. Entries that bind nothing launched nothing, so they do not count as spend or as roles without a report. The limit is the ledger's own: the chain has no anchor outside the folder, so removing its last entries together with their records is not detected.
- *Signing.* `a2dlc authorize --target <path::symbol>... --decision <decision> --intent <file> --expires <RFC 3339> --key <key> [--base <commit>]` refuses globs, any decision but `enforce_new_behavior`, `preserve_legacy` and `plan.accept`, an expiry in the past and a missing intent, signs with the namespace above (the passphrase is asked by `ssh-keygen` at the terminal and never reaches a2dlc), checks the signature against the allowed signers and only then writes `.a2dlc/authorizations.json`, replacing an entry with the same signed text.

**Why this does not undo the blob rule.** The blob rule exists so that an approval cannot be reused for a later, different change. An authorization cannot be reused either: it dies with its base, its intent and its expiry date, and it names exact symbols. What it gives up is that the person did not see the final code of those symbols. That is the trade, it is opt-in, and it is recorded in the receipt.

## Compatibility

- A repository without `authorizations.json` verifies exactly as today.
- `.a2dlc/authorizations.json` joins the paths that do not enter the `tree_hash`. A verifier older than this RFC keeps hashing it, so for a repository that has the file its tree hash differs from the receipt's and it reports the receipt `stale`: it fails closed.
- `approvals.json` does not change. The new file and schema are additions; per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) no `schema_version` bumps.
- A verifier older than this RFC ignores the file and refuses the targets it would cover as `unsigned_waiver`, if it gets that far. That fails closed, which is the right direction for an older reader.
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
- **Status of the checks:** the reference has the unit tests (`internal/app` for the rules above, `internal/run` for the run and `verify-run`); the conformance vector for the new excluded path is `V-tree-025`. The per-rule Part B vectors and the clean-room verifier's support for rules 1 to 6 are not written yet.
- **Measurement:** on the factory chain runs, the number of human stops per slice before and after, with the same intents.

## Open questions

- None. The two questions of the first draft are closed in this text. The person signs before launching the run, so Stage 0 gains no stop. Probes (`spec_divergence`, π_S relaxations) stay outside: they are answered with `a2dlc answer`, and an authorization covers waived engine targets only.
