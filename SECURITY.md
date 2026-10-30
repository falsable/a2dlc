# Security policy

## Reporting a vulnerability

Use GitHub private vulnerability reporting: open the **Security** tab of [falsable/a2dlc](https://github.com/falsable/a2dlc/security) and choose **Report a vulnerability**. There is no security email address and none is published here.

Private reporting has been enabled on this repository since 2026-10-29. If the button is ever not shown, the setting has been switched off: open a public issue that says only "security report, private reporting is off" and put no details in it. The maintainer will answer there.

There is one maintainer ([GOVERNANCE.md](GOVERNANCE.md)), so there is no response-time guarantee. Reports are handled in the order they arrive. A reporter who wants credit is named in the fix's release notes.

## What is supported

Only the latest release and `main`. Fixes are not backported.

## What a verdict claims, and what it does not

A `PASS` is a bounded search inside a recorded budget, not a proof (RFC-0001 §1 and §10, [docs/A2-DLC_v3.5_protocol.md](docs/A2-DLC_v3.5_protocol.md)). A wrong `PASS` from a gate that ran is a correctness bug: report it as an issue with a frozen mutant (see [CONTRIBUTING.md](CONTRIBUTING.md)). The rest of this file is about ways to get a `PASS` without the gates having run, or to run code the user did not intend.

## Threat 1: the agent writes a `PASS` verdict.json

**Attack.** The agent the gate is meant to constrain writes `.a2dlc/verdict.json` itself, with `"verdict": "PASS"` and the `tree_hash` of the tree it is about to push, instead of running `a2dlc check`.

**What `a2dlc verify --strict` checks.** It reads the receipt from the working directory (`internal/app/app.go:46-64`, `:713`) and checks, in `internal/app/app.go:711-784`:

- `tree_hash` equals the tree of the commit (`:728`);
- the verdict is `PASS` (`:735`);
- the verdict's base is an ancestor of what is being pushed (`:739`);
- every waiver used has a valid SSH signature (`:742-781`);
- every divergence note is one the base's `config.critical_code` allows (`:751`, `:789-816`).

**What it does not check.** Who wrote the file. The receipt carries no signature and no MAC, and `verify` does not recompute `targets`, `tests` or `coverage`. `.a2dlc/verdict.json` is excluded from the `tree_hash` (`internal/gitx/gitx.go:20`), and the tree hash of a commit can be computed by anyone with `git`. A hand-written receipt with the right `tree_hash`, a `PASS` and an ancestor base passes `verify`.

**Status: not mitigated on the machine that runs the pre-push hook.** Any process that can write to the working directory can produce a receipt `verify` accepts. With `a2dlc verify --strict` the protection is against stale receipts, receipts for another tree, unsigned waivers and an agent that does not try to forge. The default `pre-push` blocks only on evidence (a `FAIL`, an unsigned waiver of one, a protected path without approval, `critical_code` left unjudged); a missing or stale receipt, or one that does not cover the unpushed commits, only warns, so without a receipt and without `critical_code` or `protected_paths` it stops nothing. The README states the same limit, and RFC-0001 §6.4.9 (`docs/A2-DLC_v3.5_protocol.md:668`) says what closes it: running the producer in CI with `--require-signatures` over the same commit.

**Partial controls, none of which prevents the forgery:**

- The Claude Code and Gemini `PreToolUse` hook (`internal/app/app.go:961-996`) blocks edits through file tools only to paths in `config.protected_paths`. `.a2dlc/config.json` of this repository sets no `protected_paths`. `.a2dlc/verdict.json` is not protected by default. The shell branch (`internal/app/app.go:998-1012`) blocks only `git push --no-verify`; a shell redirect into `.a2dlc/verdict.json` is not inspected.
- The hook is installed by `a2dlc install` and exists only for agents that have an adapter (`claude`, `gemini`). Other agents are not covered.
- The pre-push hook is a file the agent's user account can edit (`internal/app/install.go:246-283` writes it). Nothing in this repository protects it, and it is outside the `tree_hash`.
- CI re-runs the check with the pinned release binary (`.github/workflows/a2dlc.yml:62`, `check --base "$BASE" --require-signatures`). This detects a forged receipt after the push, only when the push touches the paths the workflow lists (`.github/workflows/a2dlc.yml:12`), and the workflow's own comment says "Audit, not boundary" (`.github/workflows/a2dlc.yml:6`). A repository that has not installed the CI template has no such check.

Closing this needs the receipt to be authenticated by something the agent cannot produce, for example a signature from a CI identity. That work is planned and not done; this document does not claim it.

## Other known gaps

These come from the 2026-10-29 gap analysis. There is no threat model yet; this list is not one, and none of these is mitigated beyond what is stated.

- **Release integrity.** Releases are not signed. `install.sh` and `install.ps1` download `SHA256SUMS` from the same release as the binary (`install.sh:38-39`), so the checksum protects against a corrupted download, not against a compromised release. `.github/workflows/release.yml` has no signing or attestation step.
- **Engine configuration.** The engine's `image` and `env` from `.a2dlc/config.json` are not validated the way the role launcher validates them, and a container that outlives its time limit is not killed (`internal/engine/engine.go`).
- **Provider keys in role containers.** A role container receives the provider API key through `roles.<role>.env_file` (`internal/run/launcher.go:147-158`) and reaches the provider through a CONNECT tunnel that only allowlists hosts (`roles/egress-proxy/proxy.go:19`). A role can read the key and send it through the allowed host.
- **Run ledger.** `a2dlc verify-run` detects truncation, but removing entries without a cost or rewriting `ledger.json` and `state.json` together is not detected.
