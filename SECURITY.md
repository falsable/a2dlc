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

**What it checks.** Who wrote the file is enforced when signing is configured (`A2DLC_SIGNING_KEY`): `a2dlc check` signs `.a2dlc/verdict.json` into `.a2dlc/verdict.sig` under namespace `a2dlc-verdict`, and `a2dlc verify --strict` verifies `.a2dlc/verdict.sig` against allowed signers and fails closed if missing, invalid, or unverifiable. When signing is not configured, `.a2dlc/verdict.json` is unauthenticated (`internal/gitx/gitx.go:20` excludes it from `tree_hash`), so CI with `--require-signatures` and protected signers remains the trust boundary (RFC-0001 §6.4.9).

**Status: local machine is not a hard boundary.** A local process running as the developer's OS user can still inspect/edit untracked workspace files or hooks outside agent hooks. Current local mitigations and remaining routes on the local machine:

- **Signed verdicts & pre-push env isolation:** `a2dlc install` installs `pre-push` with `a2dlc verify --commit HEAD --pre-push`, which ignores `A2DLC_ALLOWED_SIGNERS`, `A2DLC_ALLOWED_SIGNERS_DATA`, and `A2DLC_SSH_KEYGEN` from the pushing shell environment (falling back strictly to `~/.config/a2dlc/allowed_signers` and system `ssh-keygen`). CI `a2dlc check --require-signatures` is unaffected and still reads `A2DLC_ALLOWED_SIGNERS_DATA`.
- **`PreToolUse` hook (`internal/app/app.go`, `internal/app/install.go`):** Blocks file-tool edits (`Edit|Write|NotebookEdit`) to `config.protected_paths` as well as `.a2dlc/verdict.json`, `.a2dlc/verdict.sig`, `.git/hooks/pre-push`, and `.beads/hooks/pre-push`. In `Bash`, it blocks `git push --no-verify` / `-n` / `-qn`, `git -c core.hooksPath=...`, nested `bash`/`sh`/`zsh`/`dash -c` and `eval` wrappers, and shell redirections/writes (`>`, `>>`, `tee`, `cp`, `mv`) into those gate artifacts.
- **Unmitigated local routes (not the trust boundary):** Other shell wrappers/obfuscations (scripts executed by file, Perl/Python file writes), agents without `PreToolUse` hooks (`codex`/ACP), third-party MCP tools outside Claude Code's `Edit|Write|NotebookEdit|Bash` matcher (though `a2dlc_answer` is disabled by default unless `A2DLC_MCP_ALLOW_ANSWER=1`), and direct filesystem modification of `.git/hooks/pre-push` or local `git config core.hooksPath`.
- **CI boundary:** CI re-runs checks with the pinned release binary (`check --base "$BASE" --require-signatures`, `.github/workflows/a2dlc.yml`), which runs off-machine with protected CI signers.

## Other known gaps

These come from the 2026-10-29 gap analysis. There is no threat model yet; this list is not one, and none of these is mitigated beyond what is stated.

- **Release integrity.** Releases are not signed. `install.sh` and `install.ps1` download `SHA256SUMS` from the same release as the binary (`install.sh:38-39`), so the checksum protects against a corrupted download, not against a compromised release. `.github/workflows/release.yml` has no signing or attestation step.
- **Engine configuration.** The engine's `image` and `env` from `.a2dlc/config.json` are not validated the way the role launcher validates them, and a container that outlives its time limit is not killed (`internal/engine/engine.go`).
- **Provider keys in role containers.** A role container receives the provider API key through `roles.<role>.env_file` (`internal/run/launcher.go:147-158`) and reaches the provider through a CONNECT tunnel that only allowlists hosts (`roles/egress-proxy/proxy.go:19`). A role can read the key and send it through the allowed host.
- **Run ledger.** `a2dlc verify-run` detects truncation, but removing entries without a cost or rewriting `ledger.json` and `state.json` together is not detected.
