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

**The run ledger and `verify-run` (normative).** A verifier of a run reads `.a2dlc/run/<slice>/state.json` and `.a2dlc/run/<slice>/ledger.json` (contracts `run-state` and `run-ledger`) from the working tree, and the allowed signers as for §6.4.7. `verify-run --slice <id>` exits 0 and prints `a2dlc verify-run: ok` when none of the following finds a problem, 1 when one does or when state.json cannot be read or does not match its contract, 64 for a call that does not give exactly one of `--slice` and `--plan`. Every problem is one line on standard error. The kinds the conformance kit looks for are in brackets.

1. *The run.* `ledger.json` belongs to the run of state.json: equal `run_id` [`ledger_run`]. A ledger that does not match its contract is a problem. When there is no ledger, state.json must record no launches: `budget.spent_usd` 0, `unreported_roles` 0 and no `by_role`.
2. *The chain* [`ledger_chain`]. Entry number `i` (from 1) has `seq` `i`. The first has `prev_hash` null; every other has the `hash` of the entry before. The `hash` of an entry is `sha256:` followed by the hex of the SHA-256 of the UTF-8 text `prev_hash` (empty for the first entry) followed immediately by the text of the entry. The text of an entry is JSON with no spaces whose keys come in this order, `hash` written as the empty string: `seq`, `prev_hash`, `hash`, `at`, `stage`, `role`, `attempt`, `family`, `image`, `image_digest`, `argv_sha256`, `inputs`, `outputs`, `network`, `env_keys`, `exit`, `status`, `cost`, then `egress_proxy` and `authorization` only when the entry has them. `prev_hash`, `image_digest` and `cost` are `null` when absent; `inputs`, `outputs` and `env_keys` are `[]` when absent; each element of `inputs` and `outputs` is `{"path","sha256"}`; `cost` is `usd`, `input_tokens`, `cache_read_input_tokens`, `cache_creation_input_tokens`, `output_tokens`, `turns`, every one but `usd` left out when it is 0; `egress_proxy` is `image`, `image_digest`; `authorization` is `target`, `id`, `sha256`. Strings are written as in the `waiver_sha256` of §6.4.6 (`<`, `>`, `&`, U+2028 and U+2029 as `\u003c`, `\u003e`, `\u0026`, `\u2028`, `\u2029`). A number is written in its shortest form that reads back as itself, without an exponent from 1e-6 up to below 1e21.
3. *The binding* [`authorization_unbound`]. An entry that has `authorization` has role `check`; an entry of role `check` has `authorization`. Each such entry is matched with one record of `authorizations_used`, and each record with one entry, a record being taken by at most one entry. A record and an entry match when they have the same `stage` and the same `at` (compared as written) and the entry's `authorization` equals `{target: <the record's target>, id: <hex of the SHA-256 of the authorization's signed text, without a prefix>, sha256: sha256:<hex of the SHA-256 of the text of the record's `authorization` object>}`. The text of that object is JSON with no spaces whose keys are `base_ref`, `intent_sha256`, `targets` (in the order written), `decision`, `expires_at`, `principal`, `signature`. An entry with no record, or a record with no entry, is a problem. Entries of role `check` launched nothing: they are not spend and not roles without a report.
4. *Each record* [`authorization_invalid`]. Whatever the chain said, every record is checked, and every problem of every record is reported: the `target` is among the `targets` of its authorization; the `decision` is `enforce_new_behavior` or `preserve_legacy`; `at` is not after `expires_at` (equal passes; either is an RFC 3339 date-time with a zone, or it is a problem); the signature verifies over the signed text of this RFC in the namespace `a2dlc-authorization` for the `principal` against the allowed signers. With records and no allowed signers, one problem says none of them can be checked.
5. *The artifacts.* A stage whose status is `done` has an `artifact`; the SHA-256 of the file `.a2dlc/run/<slice>/<artifact.path>`, written `sha256:<hex>`, equals `artifact.sha256` for every stage that has one [`artifact_changed`].
6. *What is not checked here.* That the ledger records at least the spend of state.json (a deleted or truncated ledger) is a rule of the reference that this section does not describe yet; a verifier may leave it out, and the conformance vectors do not exercise it.

`verify-run --plan <feature>` reads `.a2dlc/run/_plan/<feature>/state.json` (contract `plan-state`) and checks the acceptance that RFC-0008 section 4 describes [`acceptance_invalid`]: the state has an `acceptance`, with a time of use `at` that is a date-time; at least one allowed signer is configured; and, by its `kind`, either `approval` with an `approval` record and no `authorization`, or `authorization` with an `authorization` record and no `approval` (any other combination is a problem). An approval is accepted when its `plan_sha256` equals the state's, its `base_ref` equals the state's `base_sha`, `at` is not after its `expires_at` and its signature verifies in the namespace `a2dlc-plan` over `a2dlc-plan/1\n<plan_sha256>\n<base_ref>\n<expires_at>\n`. An authorization is accepted when its decision is `plan.accept`, its `targets` include `plan::<feature_id of the state>`, its `base_ref` equals the state's `base_sha`, `at` is not after its `expires_at` and its signature verifies in the namespace `a2dlc-authorization`. The verifier does not compare the intent: the state does not hold the feature intent's hash. The checks run in the order written and the first that fails is the problem reported.
- *Signing.* `a2dlc authorize --target <path::symbol>... --decision <decision> --intent <file> --expires <RFC 3339> --key <key> [--base <commit>]` refuses globs, any decision but `enforce_new_behavior`, `preserve_legacy` and `plan.accept`, an expiry in the past and a missing intent, signs with the namespace above (the passphrase is asked by `ssh-keygen` at the terminal and never reaches a2dlc), checks the signature against the allowed signers and only then writes `.a2dlc/authorizations.json`, replacing an entry with the same signed text.

**Who signs.** The property the signature gives is that whoever wrote the change cannot produce it. A person's key gives it. The key of an orchestrator, a service that runs the builders and talks to the person through a tracker, gives it too when all of this holds:

1. the key is never within a role's reach: it is not mounted into a role's container, not in a role's environment and not in the judged tree;
2. a person added its public key to the allowed signers, under a principal of its own that is no person's; and
3. the orchestrator signs only after an approval by the person that it can show (the tracker comment and its author), and only for the targets and the intent that the person was asked about.

A person then approves with a word where they already are, and no terminal is needed. What the receipt proves is different: that the orchestrator recorded an approval made with the person's identity on the tracker, where a person's key proves that a person held that key. The `principal` of the authorization says which of the two it was, so a reviewer can tell them apart. The approval is as strong as the tracker's identity: a tracker any local process can write to as the owner (a file-based one on the builder's host, for example) gives less than one with its own accounts.

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
- **Status of the checks:** the reference has the unit tests (`internal/app` for the rules above, `internal/run` for the run and `verify-run`); the conformance vector for the new excluded path is `V-tree-025`. The conformance class `run` (vectors `V-run-001` to `V-run-027` for `verify-run --slice` and `V-run-101` to `V-run-117` for `verify-run --plan`) covers the section above, and the clean-room verifier implements it. The per-rule vectors for the six rules of "What it covers", which the receipt-level `verify` decides, and the clean-room verifier's support for them are not written yet.
- **Measurement:** on the factory chain runs, the number of human stops per slice before and after, with the same intents.

## Open questions

- An orchestrator's key in the allowed signers can also sign an approval bound to a blob (`a2dlc approve`), which "Who signs" does not intend: critical code, protected paths and tests should keep needing a person. The reference does not limit what a principal may sign. Until it does, an orchestrator must call `authorize` only, and the limit is a rule of the orchestrator, not something verify enforces.
- The two questions of the first draft are closed in this text. The person signs before launching the run, so Stage 0 gains no stop. Probes (`spec_divergence`, π_S relaxations) stay outside: they are answered with `a2dlc answer`, and an authorization covers waived engine targets only.
