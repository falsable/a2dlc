# Ticket-to-push for an outside agent

*   **Author:** aldemar
*   **Status:** Draft, revised for implementation. The implementation-readiness review of 2026-11-07 found 6 Blockers and 16 Majors in round 1, and 0 Blockers and 12 Majors in a round-2 re-audit of the revision by two fresh reviewers. This revision resolves both rounds on the author's delegation; each finding and its outcome are in [the review record](./ticket-to-push-outside-agent.readiness-review.md). The review stages for the feature spec (5 to 8) were not run. One measurement gates the trial, not the implementation: the Claude CLI answering from the roles image behind the egress proxy (Phase 1, step S1).
*   **Date:** 2026-11-07

--------------------------------------------------------------------------------

## Overview

a2dlc is a Go orchestrator and verification gate for AI coding agents. Its two open claims, P2 ("tickets reach `PASS` with zero human turns when the change class is pre-authorized") and P4 ("every interaction is a versioned contract; a worker goes from ticket to `PASS` to push using only MCP or CLI"), are written in `docs/CLAIMS.md` with their measurement, and neither has been measured. This design removes what stops an agent that is not the author from running that path today, fixes a validation bug in the MCP tool that creates intents, writes the trial protocol that P2 and P4 need, and names the pieces that are built only if the trial shows a need.

It comes out of a brainstorm that asked what a2dlc lacks next to Google's Elephant-Goldfish Model (EGM). The answer was not a design-review stage: a2dlc leaves design review to humans on purpose (`docs/CLAIMS.md`, "Not adopted, on purpose") and its Stage 1 already gives an agent-run second opinion as an executable contract. What it lacks is the plumbing for an outside agent to run `a2dlc run` at all, and the measurement that would say whether the autonomy claim holds.

## The Problem

An agent that reads the README and runs the install script cannot complete `a2dlc run` on a fresh machine, and nobody can say today how many human turns a ticket costs.

1.  **Three images that `a2dlc run` needs are not published.** The release workflow pushes the four engine images and bakes the Python and TypeScript digests into the binary (`.github/workflows/release.yml`, steps "engine images on ghcr" and "binaries"). It does not push `a2dlc-roles`, `a2dlc-egress-proxy` or `a2dlc-engine-z3`, yet `run` defaults to the z3 image (`cmd/a2dlc/main.go:196`, and again in `cmd/a2dlc/plan.go:55`), every role runs in the roles image, and any role with provider network needs the proxy (`internal/run/launcher.go:61`). The author builds them locally from `roles/` (`CLAUDE.md`, Commands).
2.  **`roles.json` is written by hand and the only examples are wrong for a stranger.** The three valid `roles` examples under `contracts/examples/valid/` pin `a2dlc-roles:0.1` against the current 0.15 and carry a host path in `env_file`, and so do the 14 invalid examples that `contracts/tests/generate_invalid.py` derives from one of them. The repo is public and its own rule forbids host paths in tracked files (`CLAUDE.md`, Never).
3.  **The skill and the binary can disagree.** `install.sh:8` pins the binary to a release tag while `install.sh:78` and `install.ps1:48` fetch `skills/a2dlc/SKILL.md` from `main`, so the skill can name commands the installed binary lacks.
4.  **The CI template says the release is private, and it is not.** `templates/ci/a2dlc.yml:15-20` says the release and the ghcr images are private and need a token. Measured on 2026-11-07 with no GitHub session: the v0.5.0 asset `SHA256SUMS` downloads with HTTP 200, and the manifests of the four engine images in that release's `IMAGENES.txt` answer 200 to an anonymous ghcr token. The template is stale. The three images this plan adds are new packages, and their visibility is unknown until they are first pushed.
5.  **The MCP tool that creates intents accepts bad ones and invents content.** `writeIntent` (`internal/mcp/server.go:492-509`) runs only the strict JSON decode and skips `Intent.Validate()`, so an intent with a bad `mode`, `criticality` or `slice_id` reaches disk and fails later at `a2dlc run`. `Validate()` exists and `run` already calls it (`internal/contract/intent.go:111`, `internal/run/intent.go:39`). Because the slice regex rejects `/`, skipping it also leaves a path escape where the unvalidated `slice_id` is joined into the file path. The tool requires only `slice_id` and `goal` and fills `invariants`, `non_goals` and `acceptance` with stock text when they are missing (server.go:462-463, 472), so an intent that says nothing passes the schema. The `required` list in the tool's input schema is never enforced by `dispatchTool`; it only documents.
6.  **Nothing says how the change gets from the run to the branch.** `a2dlc run` leaves the candidate commit on `refs/a2dlc/run/<slice>/<role>` (`internal/run/stage3.go:475`) and records its commit and tree hash in `candidate.json`. No command merges or pushes it, and `skills/a2dlc/SKILL.md` has no merge or push sequence; its only push guidance is the `--no-verify` ban. The pre-push hook warns, and does not block, on a missing or `INCONCLUSIVE` receipt (`README.md:94`), so a push can succeed without a verified change.
7.  **P2 and P4 have no trial protocol.** `docs/CLAIMS.md` gives each a measurement but not a start state, the list of human touches that are allowed, a terminal condition, or caps. Without those, a run cannot be scored.

## Goals

Ordered. The first three are the part of the work that gives most of the P4 answer; the rest depends on what they show.

1.  **An outside agent can run `a2dlc run --mode lite` from the published artifacts alone.** Verified by `a2dlc run --mode lite` completing on a fresh clone with nothing built locally, after the anonymous checks of the seven published images. Delivered by publishing the roles, egress-proxy and z3 images in the release workflow, pulling a missing registry image in the launcher, having `a2dlc install --roles` write a `roles.json` template that contains no host path, removing host paths from tracked files, and shipping the skill as an asset of the same release as the binary.
2.  **The MCP intent tools refuse what `a2dlc run` would refuse, and invent nothing.** `writeIntent` calls `Intent.Validate()`; `../x` as a `slice_id` is refused by a test; the signal tool checks `from_slice` before it reads. `invariants` and `acceptance` become required arguments of `a2dlc_intent_from_ticket`, checked in the tool's code, and a call without them returns the tool's error result and writes no file. A missing `non_goals` becomes `[]`, not stock text. Overwriting an existing intent succeeds when the bytes are identical and is refused when they differ, because the run binds the intent hash (rule R8, `docs/STATE_MACHINE.md:49`). Existing intent files are not rejected after the fact.
3.  **P2 and P4 have a trial protocol in `docs/CLAIMS.md`, and the agent has a written merge-and-push sequence.** The first P2 measurement covers change classes that need no signature, in Lite mode, with the intent written by the agent; allowed human touches are enumerated; the terminal condition is `a2dlc verify --strict` passing on the pushed commit; time and cost caps have numbers; human stops are broken down by `escalation.kind` (`contracts/escalation.schema.json`). `skills/a2dlc/SKILL.md` gains the sequence: commit the intent, run with `--base`, `verify-run`, fast-forward the commit named in `candidate.json`, `a2dlc check --base`, push.
4.  **One manual P4 attempt, then `a2dlc doctor --json` scoped to what failed in it.** First cut, if the attempt shows these failures: git repository present, Docker reachable, the pinned images present, `roles.json` loads and its roles validate, the credential file is readable and outside the repository. Exit code is the signal.
5.  **P2 measured over 10 real tickets**, with filler and vacuous intents counted by `grep` and `jq` over the intent files and human stops by `escalation.kind`.

## Non-Goals

*   **Any intent-capture, ideation or inception stage, and any LLM-judged lens** over the intent (scope, YAGNI, comprehension). Recorded as not adopted in `docs/CLAIMS.md`; an LLM judge before the gate is the circular trap the protocol exists to close.
*   **A new numbered stage, ledger entries for findings, or a rewrite inside Stage 0.** Stage -1 is taken ("Make It Testable", `internal/run/stages.go:9`); the ledger has one entry per container launch; Stage 0 is a byte copy and rule R8 binds the intent hash when the run opens.
*   **`a2dlc intent check`, a mechanical doc-versus-code check, and the full `e2e/ticket_to_push.sh`.** Built only if Goal 5's breakdown shows vacuous intents surviving Goal 2, doc drift causing a failure, or agents getting the Goal 3 sequence wrong. Their constraints are kept in the Technical Plan so that a later session does not rediscover them.
*   **A `promote` command.** The agent uses its own `git` with the written sequence. Reopened only if the P4 attempt shows agents getting it wrong; what counts as wrong is written in the protocol.
*   **Counts, version strings and line numbers in prose as a general checker.** Where a number matters, generate the document from code, as `docs/STATE_MACHINE.md` and `docs/PLAN_MACHINE.md` are, or drop the number.
*   **Matching EGM's prompt-only, one-command install for `run`.** Gates run in `--network none` containers and roles need a credential. The install stays binary plus Docker.
*   **A template for greenfield, refactor or brownfield runs.** Those modes need the dual-spec roles of two model families. The template covers Lite; the operator adds the other roles.
*   **The word "autonomous" in the README** before P2 closes (`docs/CLAIMS.md:32`).
*   **RFC-0007 Part B (pre-authorization), signing by agents, or any change to who holds the signing key.** Signing, Stage 1 probes and protected paths stay human. The trial counts them, it does not remove them.
*   **Rewriting git history** to remove host paths already published. The repo has been public since v0.4.0; this plan fixes the tree from here on.

## Open Questions

*   **Does the Claude CLI answer from the roles image behind the egress proxy?** No role has run the Claude CLI inside the image yet (`docs/COVERAGE_AND_BENCHMARKS.md`, generic LLM role image row): every measured run used `A2DLC_LLM=openrouter`. Whether the CLI honours `HTTPS_PROXY` is recorded as unmeasured. Step S1 settles it before the trial, with a decision rule.
*   **Does the subscription carry ten trial tickets?** The template's roles bill the operator's Claude subscription. The run reports the CLI's cost figures, and the caps apply to them; whether the subscription's usage limits stop a ticket first is observed in the trial, and a ticket stopped by a usage limit is recorded with that reason, not as an a2dlc failure.
*   **Does the signal tool keep its filler for `bugfix` intents?** `a2dlc_intent_from_signal` writes the same stock invariant and non-goal (server.go:608-609) and already refuses to overwrite (server.go:552). Goal 2 leaves it as is until Goal 5 says otherwise.

## Critical User Journeys (CUJs)

### CUJ 1: An outside agent installs a2dlc and completes a Lite run on a fresh clone

*   **Given**: A machine with Git and Docker, no a2dlc image built locally, a clone of a Python repo with a `pyproject.toml`, a Claude subscription, and the published release.
*   **When**: The agent runs the install script and `a2dlc install --agent claude --roles`. The operator runs `claude setup-token`, writes `CLAUDE_CODE_OAUTH_TOKEN=<token>` to a file outside the clone and sets `A2DLC_PROVIDER_ENV` to that file's path. The agent creates an intent through `a2dlc_intent_from_ticket` with `mode: "lite"`, `invariants` and `acceptance`, commits it, and runs `a2dlc run --slice <id> --intent <file> --mode lite --base <sha>`.
*   **Then**: Every image is pulled by digest from ghcr; no stage is `skipped` for a missing role or image; the run ends `PASS`, `INCONCLUSIVE` or `escalated` for a reason about the change, never about the install; `a2dlc verify-run --slice <id>` is ok.

### CUJ 2: An agent sends a bad ticket to the MCP tool

*   **Given**: The MCP server is running in a repo with a2dlc installed.
*   **When**: The agent calls `a2dlc_intent_from_ticket` with `slice_id` set to `../escape` or without `invariants` or `acceptance`; or calls it twice for the same slice with different content.
*   **Then**: The first two calls return the tool's error result and write no file; the third returns an error that names the existing intent and its hash, and the file is unchanged. A repeated call with identical content returns ok.

### CUJ 3: A worker takes a pre-authorized ticket to a pushed, verified commit

*   **Given**: CUJ 1 completed `PASS`; the ticket's change class needs no signature (no critical code, no protected path, no waiver); a remote the worker may push to.
*   **When**: The worker follows the sequence in `skills/a2dlc/SKILL.md`: fast-forward merge of the commit named in `.a2dlc/run/<id>/candidate.json`, `a2dlc check --base <sha>`, `git push`.
*   **Then**: `a2dlc verify --commit HEAD --strict` exits 0 on the pushed commit; the remote ref equals the merged commit; `.a2dlc/run/<id>/` holds no `answers.json`, and no `waivers.json` or `approvals.json` was created. The trial log records zero human touches for this ticket.

### CUJ 4: The author scores a P2 trial

*   **Given**: Ten tickets run under the protocol in `docs/CLAIMS.md`.
*   **When**: The author runs the counting commands the protocol names over `.a2dlc/intents/*.intent.json` and `.a2dlc/run/*/`.
*   **Then**: A table per ticket: human touches by `escalation.kind`, filler strings found, cost and wall time against the caps, change class confirmed or not, terminal condition met or not. Nothing in the table rests on reading a transcript.

## Technical Plan (Architecture)

The decisions in this section that had more than one defensible answer were delegated by the author to the drafting session (2026-11-07), and the ones the readiness review reopened were decided again on the same delegation. Each is marked "decided on delegation" with the alternatives it beat, so a reviewer can reopen it.

### 1. Publishing everything `a2dlc run` needs, and pinning it in the binary

Today the release bakes two digests into the binary through `-ldflags -X` on package-level string variables in `internal/app` (`install.go:24-27`). The other three images `run` needs are a flag default (z3, in `main.go` and `plan.go`), a constant (`EgressProxyImage` in `internal/run/launcher.go:61`) and whatever the user wrote in `roles.json`.

**Decided on delegation: each reference stays in the package that owns it, as a string variable the release overrides with `-X`.** `-X` sets any package-level string variable, so no accessor layer is needed:

| Reference | Variable | Dev default | `-X` flag in the release |
| --- | --- | --- | --- |
| roles image | `internal/app.rolesImage` (new, next to `enginePyImage`) | `a2dlc-roles:0.15` | `-X $app.rolesImage=$roles` |
| egress proxy | `internal/run.EgressProxyImage` (the constant becomes a `var`, same name) | `a2dlc-egress-proxy:0.1` | `-X github.com/falsable/a2dlc/internal/run.EgressProxyImage=$proxy` |
| z3 engine | `main.z3Image` (new, next to `main.version`) | `a2dlc-engine-z3:0.1` | `-X main.z3Image=$z3` |

Keeping the exported name `EgressProxyImage` means the seven test sites that use it (`launcher_test.go`, `run_test.go`) compile unchanged. `internal/app` gains `BakedImages() []string`, which returns its three references, so `cmd/a2dlc` can print all five pinned references in `a2dlc version` without reaching into unexported names.

**Pulling.** `docker run` pulls a missing image by itself, but the launcher inspects two images before any `docker run`: the proxy (`resolveProxyImage`, which fails when the image is not local) and the role image (`digest`, whose empty answer leaves `image_digest` null in the ledger on a first launch). **Decided on delegation: the launcher pulls.** A new helper `ensureImage(ctx, bin, image)` runs `docker image inspect`; when that fails and the reference names a registry, it runs `docker pull` with its own 5-minute timeout and inspects again. A reference names a registry when it contains a `/` and the part before the first `/` contains a `.` or a `:` or is `localhost`, which is Docker's own rule: `ghcr.io/falsable/a2dlc-roles@sha256:...` does, `a2dlc-roles:0.15` does not. A local reference is never pulled: the proxy keeps today's "build it with" message, and a role image fails with `image <ref>: not available locally; build it, or use the registry reference the release pins`. `resolveProxyImage` and `Launch` call it before they read a digest. The alternative, a manual `docker pull` in the skill and in the `install --roles` output, was rejected because CUJ 1 asks for no manual step and the agent would have to know five references the binary already carries.

**z3, decided on delegation: publish it.** Lite runs never reach Z3: Stage 1 is skipped in Lite (`internal/run/stage1.go:42`) and Stage 1.5 is skipped when there is no `contract.json` (`internal/run/stage15.go:70`), so the missing image does not block CUJ 1. It does block an outside greenfield or brownfield run, which reaches Stage 1.5 with an image it cannot pull unless it passes `--z3-image ""`, and that flag turns off the one deterministic check on the contract. The cost is one more image build in the release; the last release job took about 4 minutes (2026-11-03, 16:30:44 to 16:34:46 UTC) against a 30-minute timeout.

**Anonymous access.** Measured on 2026-11-07, as recorded in The Problem, item 4. The template's "private" paragraph is deleted, its `A2DLC_DOWNLOAD` secret is replaced by the workflow's own token, and its ghcr login step is removed. For the three new packages, the release is followed by the same anonymous check; a package that answers anything but 200 is set to public in its GitHub package settings by the repository owner, because the workflow cannot set visibility, and is checked again.

**Skill pin, decided on delegation: the skill ships as a release asset.** The release copies `skills/a2dlc/SKILL.md` into `dist/`, lists it in `SHA256SUMS`, and uploads it with the binaries. The install scripts download it from the same release URL as the binary and verify it against the same `SHA256SUMS`. The scripts keep their pinned default version: pinning to a verified release is the practice of the existing pin-bump commits (`chore: pin v0.5.0 in the installers, CI templates and the self-gate`), and the bump to the new release is Phase 4 of this plan. The alternative, fetching `SKILL.md` from `raw.githubusercontent.com` at the tag, was rejected because it uses a second host and is not covered by the checksum file.

### 2. How `roles.json` reaches an outside agent

**Decided on delegation: `a2dlc install --roles` writes a template on request.** The alternatives were writing it on every install (rejected: an agent that installs only the gate would get roles that cannot run without a credential, and `run` would then escalate for reasons the agent did not cause) and shipping a corrected example in `contracts/examples/` with copy instructions in the skill (rejected: an agent that installs from the script has no clone, and the example would pin a tag the release does not update).

**Template content, decided on delegation: the two roles Lite launches, on the Claude CLI, billed to the operator's Claude subscription.** A Lite run launches `builder_primary` (Stage 3) and `saboteur` (Stages 4 and 5); the specifiers, the Inquisitor and the back-translator run only in Stages 1 and 1.5, which Lite skips. With only those two roles, `checkFamilies` has no pair to check. The credential is one variable, `CLAUDE_CODE_OAUTH_TOKEN`, the long-lived token that `claude setup-token` prints for a Claude subscription; the outside agent of this design is Claude Code (`install --agent claude`), so its operator has one. Model calls in this work go through the operator's Claude subscription, which the author set as the rule; OpenRouter, the only backend measured inside the image, is the fallback in step S1's decision rule. The template, in full:

```json
{
 "schema_version": 1,
 "roles": {
  "builder_primary": {
   "family": "claude",
   "image": "a2dlc-roles:TEMPLATE",
   "argv": ["python3", "/a2dlc/adapter.py"],
   "env": {
    "A2DLC_LLM": "claude",
    "A2DLC_MODEL": "sonnet",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "DISABLE_TELEMETRY": "1",
    "ENABLE_CLAUDEAI_MCP_SERVERS": "false"
   },
   "env_file": "${A2DLC_PROVIDER_ENV}",
   "network": "provider",
   "egress_allow": ["api.anthropic.com"],
   "timeout_s": 1800,
   "retries": 0,
   "memory": "4g",
   "cpus": 2
  },
  "saboteur": {
   "family": "claude",
   "image": "a2dlc-roles:TEMPLATE",
   "argv": ["python3", "/a2dlc/adapter.py"],
   "env": {
    "A2DLC_LLM": "claude",
    "A2DLC_MODEL": "haiku",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1",
    "DISABLE_TELEMETRY": "1",
    "ENABLE_CLAUDEAI_MCP_SERVERS": "false"
   },
   "env_file": "${A2DLC_PROVIDER_ENV}",
   "network": "provider",
   "egress_allow": ["api.anthropic.com"],
   "timeout_s": 900,
   "retries": 1
  }
 }
}
```

The egress allowlist is the one host Claude Code's network documentation lists for API requests (`api.anthropic.com`, code.claude.com/docs/en/network-config, read 2026-11-07). The three `env` entries switch off the optional traffic the same page lists (telemetry, nonessential traffic, claude.ai connectors), so the allowlist does not need their hosts. `sonnet` builds and `haiku` writes the Saboteur's tests; the measured runs used Haiku 4.5 for the Saboteur through OpenRouter and its tests killed every mutant (`docs/COVERAGE_AND_BENCHMARKS.md`, generic LLM role image row). The timeouts and resources copy `contracts/examples/valid/roles__egress.json`.

**Step S1, decision rule.** The author runs it, because it needs a real token. Phases 1a to 1c can merge before it; its record in file 27 and Phase 4 wait for it.

1.  Build the local images: `docker build -t a2dlc-roles:0.15 roles`, `docker build -t a2dlc-egress-proxy:0.1 roles/egress-proxy` and the Python engine `a2dlc-engine-py:0.12` from `engines/py`.
2.  Copy `examples/quickstart/repo` into an empty folder, `git init` and commit it as `examples/quickstart/demo.sh` does, run `a2dlc install --roles`, and commit `.a2dlc/` and `.gitignore`.
3.  Write `CLAUDE_CODE_OAUTH_TOKEN=<token>` to a file outside that folder and export `A2DLC_PROVIDER_ENV` with its path.
4.  Write the intent `.a2dlc/intents/s1-tax.intent.json` by hand, since S1 measures the roles and not the MCP tool, and commit it: goal "Add apply_tax(total, rate) to src/pricing.py: total * (1 + rate) rounded to 2 decimals, ValueError when rate < 0"; `mode` `lite`; `invariants` `["apply_discount is unchanged"]`; `non_goals` and `critical_paths` `[]`; `acceptance` `["apply_tax(100, 0.21) == 121.0", "apply_tax(100, -0.1) raises ValueError"]`; `schema_version` `1`; `criticality` `low`; `budget` with the values the MCP tool writes (`max_inner_iterations` 5, `max_tier1_adversarial_rounds` 3, `max_mutants` 30) and `max_cost_usd` 1.0, the trial's cap.
5.  Run `a2dlc run --slice s1-tax --intent .a2dlc/intents/s1-tax.intent.json --mode lite --base HEAD`. Once `docker ps --filter name=-egress` lists the proxy, a second shell runs `docker logs -f $(docker ps -q --latest --filter name=-egress)` (one container: the latest proxy), which shows the proxy's `deny CONNECT <host>` lines (`roles/egress-proxy/proxy.go:64`); the launcher removes the proxy after each role, so the log is read live.

Pass: `jq -r '.entries[] | select(.role=="builder_primary") | .exit' .a2dlc/run/s1-tax/ledger.json` prints `0` and `.a2dlc/run/s1-tax/candidate.json` exists. The template then ships as written. A `deny CONNECT` for a host the CLI needs: that host is added to `egress_allow` after reading what it is for in the same documentation page, and S1 runs again. If the CLI cannot answer after that, both roles switch to `"A2DLC_LLM": "openrouter"` and `"A2DLC_MODEL": "anthropic/claude-haiku-4.5"`, the model of the measured runs; the three Claude Code variables and `egress_allow` are dropped, so the launcher allows `openrouter.ai`; and the credential line becomes `OPENROUTER_API_KEY=<key>`. That changes files 9, 16, 19, 25 and 32 and this section, and the trial waits for the author to allow OpenRouter spending, since it is outside the subscription rule.

**Mechanism.**

*   A boolean flag `--roles` on `a2dlc install` (`cmd/a2dlc/main.go`, case `install`). After `Install` returns, `main` calls a new method `App.WriteRolesTemplate() (string, error)` and appends its line to `done`. `Install`'s signature does not change, so its ten test call sites (`internal/app/app_test.go`, `internal/app/tests_hint_test.go`) do not change either.
*   `WriteRolesTemplate` writes `.a2dlc/roles.json` only when it does not exist: the embedded template with every `"a2dlc-roles:TEMPLATE"` replaced by `rolesImage` as a JSON string. It returns `wrote .a2dlc/roles.json` or `kept .a2dlc/roles.json (it exists)`. In both cases `main` prints two more lines: `set A2DLC_PROVIDER_ENV to a file outside this repository that holds CLAUDE_CODE_OAUTH_TOKEN=<token from claude setup-token>` and `commit .a2dlc/config.json, .a2dlc/roles.json and .gitignore before the first run`. No credential file is written. `a2dlc-roles:TEMPLATE` matches the schema's image pattern, so the example validates as it is.
*   **`roles.json` is committed, decided on delegation.** `a2dlc check` hashes the working tree with untracked files included (`Repo.WorktreeTree`, `internal/gitx/gitx.go:92-127`), `verify` hashes the pushed commit's tree, and `gitx.Excluded` does not list `roles.json`, so a `roles.json` left untracked makes every receipt stale on push. The schema's title already calls the file versioned, and with `env_file` set to `${A2DLC_PROVIDER_ENV}` it holds no secret. The same holds for `config.json` and the `.gitignore` lines `install` writes.
*   **Embedding, decided on delegation: a Go package in `contracts/`.** `//go:embed` cannot name a parent directory, so `internal/app` cannot embed a file under `contracts/`. A new file `contracts/embed.go` (package `contracts`, import path `github.com/falsable/a2dlc/contracts`) embeds `examples/valid/roles__install-template.json` as `var RolesInstallTemplate []byte`, and `internal/app` imports it. The alternative, a copy under `internal/app/` with a test asserting byte equality with the example, was rejected because it keeps two files that must be edited together.
*   **Where the credential lives, decided on delegation: outside the repo, named by an environment variable.** `contracts/roles.schema.json` describes `env_file` as a "Local path (outside the repo)", and its title says `roles.json` is versioned, so a committed `roles.json` cannot carry an absolute host path and must not point inside the tree. A new function `run.ResolveEnvFile(repoDir, value string) (string, error)` in `internal/run/envfile.go` turns the `env_file` value into the path the launcher uses:
    *   the value is either a literal path with no `$`, or exactly `${NAME}` with `NAME` matching `^[A-Z_][A-Z0-9_]*$`; anything else that contains `$` is refused (no `$NAME` form, no expansion inside a longer string);
    *   `${NAME}` is replaced by the variable from the environment of the a2dlc process; an unset or empty variable is refused with an error that names the variable;
    *   the result must be absolute (`filepath.IsAbs`); a relative path would resolve against the working directory, which is usually the repository;
    *   the file must exist; both it and `repoDir` go through `filepath.Abs` and `filepath.EvalSymlinks`, and a pure helper `outsideRepo(repo, file string) bool` decides: it is true when `filepath.Rel(repo, file)` returns an error (on Windows, another volume) or returns `..` or a path that starts with `..` followed by the separator; anything else, `..name.env` included, is inside and refused; on Windows both paths go through `strings.ToLower` first, because the file system ignores case;
    *   errors name the role, the variable or the path, never the file's content.
*   `ResolveEnvFile` is called where `roles.json` is read, because those are the places that know the repository: `Machine.readRoles` (`internal/run/machine.go`, for each role and its `tier_up`, writing the resolved path back into `m.roles`), `cmd/a2dlc/propose.go` after `ProposeRoleConfig`, and the `planner` function in `cmd/a2dlc/plan.go` after `RoleConfig`. `LaunchSpec` and `DockerLauncher` do not change. The launcher refuses an `env_file` that still contains `$` before its first docker call, so a caller that skipped resolution fails with a named error instead of handing `${...}` to docker.
*   `plan run` copies `roles.json` into each slice's worktree (`internal/plan/exec.go:71-75`), and the run there resolves against the worktree. *shortcut: in `plan run`, a credential file inside the main checkout but outside the worktree is not refused; upgrade if `plan run` joins the trial.*
*   When `run` starts without `.a2dlc/roles.json`, it prints one line to stdout after the stage lines: `hint: no .a2dlc/roles.json, so every stage that needs a role ran without one; write it with a2dlc install --roles`. It goes to stdout because an agent reads the command's output and not stderr warnings (bead `a2dlc-pe6.4`). The per-stage skip reasons stay as they are: `skipStale` matches them by prefix (`internal/run/machine.go:126-140`).

### 3. The MCP intent tools

Two tools share `writeIntent` (`internal/mcp/server.go:474,659`). Changes, in the order a reviewer would check them:

*   `writeIntent` calls `intent.Validate()` before `DecodeStrictBytes`. `Validate` already rejects a `slice_id` outside `^[a-z][a-z0-9_.-]{0,99}$`, so the path join only ever sees a safe name.
*   `internal/contract` gains `ValidSliceID(s string) bool`, which applies the unexported `sliceRe`. The signal tool calls it on `from_slice` and on a given `slice_id` before any `os.Stat` or `os.ReadFile` that joins them into a path.
*   `writeIntent` reads the existing file first and compares it with `append(b, '\n')`, the bytes it would write. Same bytes: write nothing, return ok with the same path. Different bytes: return an error that names the path and the sha256 of the existing file, and leave the file unchanged. There is no `overwrite` argument: the repair path is a new `slice_id`, or deleting the file by hand, because a run that already opened on the old intent bound its hash (rule R8) and a silent rewrite would make `resume` refuse later with a worse message. **Decided on delegation**; the alternative, an explicit `overwrite: true` argument refused once `.a2dlc/run/<id>` exists, was rejected as a second code path for a case the trial should first show to be common.
*   `toolIntentFromTicket` reads `invariants` and `acceptance` with a nil fallback and returns `missing required arguments: invariants and acceptance must be non-empty lists of strings` when either is nil. `non_goals` falls back to `[]`, as `critical_paths` already does. The stock strings leave this tool. `invariants` and `acceptance` are added to the tool's `required` list as documentation for MCP clients; the check in code is what refuses.
*   The signal tool keeps its two stock strings through one named constant pair, `bugfixInvariant` and `bugfixNonGoal`, with a comment saying they are for `bugfix` intents that inherit from a source slice and are revisited after the P2 trial.

### 4. The trial protocol and the merge-and-push sequence

`docs/CLAIMS.md` gains a section "Trial protocol for P2 and P4" under the gates table. It is a document change, and `CLAIMS.md:3` says gate changes land there first. The file is untracked today; it is the deliverable of bead `a2dlc-pe6.5`, and Phase 3 commits it. Contents:

*   **Start state**: a fresh clone at a named base commit; a2dlc installed from the release; `roles.json` from `install --roles`, committed with the other files `install` writes (`.a2dlc/config.json`, `.gitignore`) before the first ticket's base; an intent created only through `a2dlc_intent_from_ticket` with `mode: "lite"`.
*   **Allowed human touches**: writing the ticket text; starting the worker; nothing else. Every other interaction counts as a human turn and is classified by `escalation.kind` (`contracts/escalation.schema.json`), with two added labels for touches the schema does not carry: `intent_written_by_human` and `push_by_human`.
*   **Trial log**: `trial.jsonl`, kept outside the clones, one JSON object per line with the keys `ticket`, `event`, `at`, `kind` and `note`. `event` is `start` or `pushed`, written by the worker, or `human_turn`, written by the person when the turn happens, with `kind` set to an `escalation.kind`, `intent_written_by_human`, `push_by_human` or `other`. `at` is UTC in the form `date -u +%Y-%m-%dT%H:%M:%SZ` prints. Human touches and wall time come from this log; a2dlc's own stops come from `escalations.json`.
*   **Terminal condition**: `a2dlc verify --commit HEAD --strict` exits 0 on the pushed commit, and the remote ref equals it.
*   **Caps, decided on delegation as starting values**: 1.00 USD of reported cost and 20 minutes of wall time per ticket, revised after the first three tickets. The measured points are 0.025 to 0.069 USD and 84 to 127 s for single greenfield slices, and 117 to 426 s for three-issue chains (`README.md:85-87`); the caps sit an order of magnitude above the cost and about three times above the slowest chain, so a ticket that hits them is a defect in the path, not a slow run.
*   **Change class, checked after the push**: a ticket counts toward P2 when the intent's `critical_paths` is `[]` and the pushed diff does not touch `.a2dlc/waivers.json` or `.a2dlc/approvals.json`. Both are repo-level, committed files (`internal/app/app.go:27-28`), and critical code or a protected path needs a signed approval before `verify` passes (`internal/app/verifypolicy.go:79`). With the terminal condition met, an unchanged pair means no signature was needed, and the repository's own matcher decided it, not a shell glob. A ticket outside the class is reported in its own row, not dropped.
*   **Scoring commands**, written in the document so the count is reproducible:

    ```sh
    # in the ticket's clone
    # stock text left in intents
    grep -l -F -e 'Preserve existing public contract invariants' -e 'Unrelated module modifications' .a2dlc/intents/*.intent.json
    # acceptance that only repeats the goal
    jq -r 'select(.acceptance == [.goal]) | .slice_id' .a2dlc/intents/*.intent.json
    # a2dlc stops by kind (no output when no run escalated)
    cat .a2dlc/run/*/escalations.json 2>/dev/null | jq -r '.items[].kind' | sort | uniq -c
    # reported cost of the run
    jq -r '[.slice_id, .budget.spent_usd] | @tsv' .a2dlc/run/<id>/state.json
    # change class: prints nothing for a ticket in the class
    git diff --name-only <base> <pushed> -- .a2dlc/waivers.json .a2dlc/approvals.json
    # terminal condition: exit 0, and the last two lines are the same hash
    a2dlc verify --commit HEAD --strict; echo "exit $?"; git rev-parse HEAD
    git ls-remote origin "refs/heads/$(git branch --show-current)" | cut -f1

    # in the folder that holds trial.jsonl
    # human touches by kind
    jq -r --arg t <ticket> 'select(.ticket == $t and .event == "human_turn") | .kind' trial.jsonl | sort | uniq -c
    # wall time in seconds, start to push
    jq -rs --arg t <ticket> 'map(select(.ticket == $t)) | (map(select(.event == "pushed"))[0].at | fromdateiso8601) - (map(select(.event == "start"))[0].at | fromdateiso8601)' trial.jsonl
    ```

*   **P2 wording**: the claim text stays as written, because pre-authorization (RFC-0007 Part B) is what it claims. The "Number published" cell gains one sentence: the first measurement covers change classes that need no signature, in Lite mode; the gate closes only when pre-authorized classes are measured too. The "Reproduce" cell, `—` today, points to this section. The ten rows of CUJ 4's table go into `docs/COVERAGE_AND_BENCHMARKS.md` with the trial log.

`skills/a2dlc/SKILL.md` section E gains the sequence, in this order, with the reason for each step: once after install, commit `.a2dlc/config.json`, `.a2dlc/roles.json` and `.gitignore`; for each ticket, commit the intent (none of these is in `gitx.Excluded`, and `check` hashes untracked files too, so the base must contain them); `a2dlc run --slice <id> --intent <file> --mode lite --base <sha>`; `a2dlc verify-run --slice <id>`; read `commit` from `.a2dlc/run/<id>/candidate.json`; `git merge --ff-only <commit>` (it works only while HEAD is still the base the run used; otherwise the agent reruns on the new base rather than rebasing a verified candidate); `a2dlc check --base <sha>`; `git push`. The section also says what the agent must not do: rebase or amend the candidate, push with `--no-verify`, or edit `.a2dlc/` by hand.

### 5. `a2dlc doctor --json`, after the first manual P4 attempt

The first P4 attempt is run by hand with a real agent against the sequence above before `doctor` is written, and `doctor` checks what that attempt showed failing. Phase 5's files are a first cut and are amended from the attempt's record before they are written. The expected first cut, from the failure modes this design already knows:

*   Git repository present: `doctor` is dispatched before `gitx.Open`, as `version` is, and runs `gitx.Open` itself, so a missing repository is a failed check with exit 1 and not the generic exit 64.
*   Docker reachable: its own `docker info` with a 10-second timeout that reports the error (`resources.dockerPlace` does this but is unexported and silent).
*   The pinned images present: the five references of `a2dlc version`, each through `engine.DockerTests{}.ImageDigest(ctx, image)` (`internal/engine/engine.go:273`). The proxy is one of the five, so there is no separate proxy check. A registry reference that is not local is `ok: true` with detail `not pulled yet; a2dlc run pulls it`, because CUJ 1 starts with no local image; a missing local reference is `ok: false`.
*   `roles.json` loads: `run.LoadRoles(repoDir)`, extracted from `Machine.readRoles` (read, decode, known role names, `validateRoleConfig`, `ResolveEnvFile`, `checkFamilies`), which `readRoles` then calls. An absent `roles.json` is `ok: false`, with the hint line of section 2 as detail.
*   The credential file of each role reads as `NAME=value` lines: `run.EnvKeys(path)`, an exported wrapper of `readEnvKeys`.

Placement: `cmd/a2dlc/doctor.go`, because it imports both `app` and `run` and `app` may not import `run`. Its entry point is `doctor(args []string, dir, docker string, stdout io.Writer) int`: `main` passes the working directory and `docker`, and the test passes a temporary folder and a fake docker script. `doctor` reaches outside agents in the release after Phase 5 (S5). Output: one JSON object `{"ok": bool, "checks": [{"name": string, "ok": bool, "detail": string}]}` with `--json`, one line per check otherwise; exit 0 when every check is ok, 1 otherwise, 64 on bad flags. No schema under `contracts/` for the first cut: nothing but a person reads it yet.

### 6. Pieces kept out of this plan, with the constraints a later session needs

These are built only if the P2 breakdown asks for them. The constraints come from reading the code and are recorded so they are not rediscovered.

*   **`a2dlc intent check`.** A leaf package (`internal/intentlint`) that imports `contract`, `verdict` and `app` and neither `run` nor `plan`: `plan` imports `run` (`internal/plan/exec.go:13`), so anything `run` calls cannot import `plan`, and `plan`'s glob helpers are unexported anyway. Glob matching through `app.Partition` (`internal/app/app.go:155`), the same matcher `run` applies to `critical_paths` and `boundary`. Rules: filler literals, `acceptance == [goal]`, globs matching zero base files only in `critical_paths`, `forbidden_paths` and `do_not_refactor`, since greenfield `allowed_paths` match nothing by design. Findings to stdout as JSON; exit 1 on a finding, 64 unreadable, 2 on IO (as `plan check`, `cmd/a2dlc/plan.go:86-90`); the callers in `stageIntent` and the MCP tool decide warn or block. A persisted artifact comes only with a blocking mode, and then under `.a2dlc/run/_intent/`, never in `.a2dlc/intents/`.
*   **Doc-versus-code check.** A sibling `contracts/tests/check_docs.py`, standard library only, not wired into `release.yml`. One rule at first: a backticked repository path exists or the line carries `<!-- a2dlc:planned -->`. The MCP tool-name drift is a Go test over `toolsCatalog()` compared with the README row, in the pattern of `internal/run/fsm_test.go:177`. Image tags in prose are not checked: `CLAUDE.md` keeps historical tags on purpose.
*   **`e2e/ticket_to_push.sh`.** Scoped to the merge-and-push sequence. Its inputs: the fake engine cannot `PASS` a real change (`engines/fake/fake.py:21-22`), so it uses a tests-only config or the real Python engine with `examples/quickstart/repo/candidates/pricing_fixed.py`; the fake builder needs a patch directory and a `[patch:<name>]` marker in the ticket goal; the quickstart repo needs a written `config.json`; the MCP server needs a newline-delimited JSON-RPC driver.
*   **The proxy holding the credential.** Bead `a2dlc-az1` (the egress proxy re-originates TLS and injects the key, so roles never hold it) would remove the token from the role container. `ResolveEnvFile` does not block it: the file would move from the role to the proxy.

### Data flow after this plan

```text
ticket text --> a2dlc_intent_from_ticket --> Validate() --> .a2dlc/intents/<id>.intent.json (committed)
                (invariants, acceptance required; no filler)            |
                                                                        v
install.sh (binary + SKILL.md from one release, both in SHA256SUMS) --> a2dlc install --roles --> .a2dlc/roles.json
                                                                        |   (env_file: ${A2DLC_PROVIDER_ENV}, resolved
ghcr: engine-py, engine-ts, engine-bb, engine-bb-go,                    v    and checked when roles.json is read)
      engine-z3, roles, egress-proxy (references in the binary) ----> a2dlc run --mode lite --base <sha>
                                                                        |   (launcher pulls a missing registry image)
                                                                        v
                              refs/a2dlc/run/<id>/<role> + candidate.json {commit, tree_hash}
                                                                        |  git merge --ff-only <commit>
                                                                        v
                              a2dlc check --base <sha> --> git push --> a2dlc verify --strict (terminal condition)
                                                                        |
                    escalations.json by kind + intents grep ----------> P2 / P4 score (docs/CLAIMS.md protocol)
```

## Alternatives Considered

Taken from the brainstorm's rejected options, from the decisions delegated during drafting, and from the decisions the readiness review reopened. "Rejected by author" marks a decision the author took or delegated to the drafting session on 2026-11-07; "rejected by the <lens>" marks a brainstorm reviewer's finding the author then adopted.

### Alternative A: A pre-Stage-0 stage with LLM lenses that challenges the intent (the original concept)

*   **Pros**: Mirrors EGM's brainstorm lenses (scope, YAGNI, comprehension) for agents; catches vague or oversized intents before Specifier tokens are spent.
*   **Cons**: Puts an LLM judge before the gate, which is the circular trap the protocol names (`README.md:38`); the protocol says the catalog takes precedence over the LLM (`docs/A2-DLC_v3.5_protocol.md:171`); a greenfield slice costs 0.025 to 0.069 USD, so there is little to save; it adds human stops against the direction of RFC-0007; `docs/CLAIMS.md` records ideation and inception stages as not adopted on purpose.
*   **Reason for Rejection**: Rejected by author on the design and zoom-out lenses' findings. The filler that motivated it is produced at one place in the MCP tool and is refused there instead (Technical Plan, section 3).

### Alternative B: Adopt EGM into a2dlc

*   **Pros**: Fresh-context review across seven lenses, packaged for six harnesses, with an active upstream.
*   **Cons**: The author's measured comparison of 2026-11-07 (one RFC, six seeded defects, one run per arm, same model) gave the same recall as a single-session control, about 10 % more cost, 22 % more time and 3.4 times more text to read; every EGM verdict is an LLM reading a document.
*   **Reason for Rejection**: Rejected by author after that measurement. Not reopened in this design. Its implementation-readiness stage was used once on this document; the record is next to it.

### Alternative C: `a2dlc intent check` as a first deliverable

*   **Pros**: A deterministic lint with exit codes fits the repo's rule that a check lowers a verdict and never raises it; it would guard hand-written intents too.
*   **Cons**: Needs a new package, a finding format, fixtures and a sidecar decision, to guard against intents that P2 does not involve (hand-written ones); its "dead glob" rule false-rejects greenfield intents unless scoped to `critical_paths`, `forbidden_paths` and `do_not_refactor`.
*   **Reason for Rejection**: Deferred by author on the zoom-out lens's finding. Reopened only if the P2 grep count shows vacuous intents surviving the required-fields change. Its constraints are kept in Technical Plan, section 6.

### Alternative D: Write `roles.json` on every `a2dlc install`

*   **Pros**: Zero extra steps for the agent; no reliance on the skill being read.
*   **Cons**: An agent that installs only the gate (`check`) receives roles it will not use and cannot run without a credential; `run` then escalates for a missing credential, a failure the agent did not cause.
*   **Reason for Rejection**: Rejected by author (delegated). `install --roles` writes the template on request, and `run` without `roles.json` says how to create it.

### Alternative E: Ship a corrected `roles.json` example in `contracts/examples/` and tell the skill to copy it

*   **Pros**: No Go code.
*   **Cons**: An agent that installs from the script has no clone, so it would fetch the example from `main`, the same skill-versus-binary drift Goal 1 closes; the example would pin a tag the release does not update.
*   **Reason for Rejection**: Rejected by author (delegated). The template is still a file under `contracts/examples/valid/` so the example tests check it, but `install` embeds it and substitutes the image the release pins.

### Alternative F: Leave the z3 image unpublished and document `--z3-image ""`

*   **Pros**: One image fewer to build and host.
*   **Cons**: Lite does not need it, but an outside greenfield or brownfield run reaches Stage 1.5 with an image it cannot pull, and the flag that avoids the failure turns off the one deterministic check on the contract.
*   **Reason for Rejection**: Rejected by author (delegated). The image joins the release with the other two.

### Alternative G: An `overwrite: true` argument on `a2dlc_intent_from_ticket`

*   **Pros**: Lets an agent repair a rejected intent through the same tool.
*   **Cons**: A second code path, refused once `.a2dlc/run/<id>` exists because rule R8 binds the intent hash at run open; the trial has not yet shown that repeated calls for the same slice are common.
*   **Reason for Rejection**: Rejected by author (delegated). Identical bytes return ok, different bytes return an error naming the existing hash, and the repair path is a new `slice_id`.

### Alternative H: A `promote` command that merges and pushes the candidate

*   **Pros**: One versioned command instead of a sequence the agent must follow; fits P4's "every interaction is a contract".
*   **Cons**: New surface before anyone has watched an agent follow the written sequence; the fast-forward precondition (HEAD still at the run's base) is a decision about the agent's branch state that a2dlc has stayed out of.
*   **Reason for Rejection**: Rejected by author for now. The sequence goes into `skills/a2dlc/SKILL.md`; `promote` is reopened only if the manual P4 attempt shows agents getting it wrong, with the failure criterion written in the trial protocol.

### Alternative I: Extend `contracts/tests/check_rfc.py` into a general doc-versus-code checker

*   **Pros**: Reuses an existing mechanical check that CI already runs.
*   **Cons**: It scans only the protocol document and is hard-wired to section 6.4, to the schemas and to `internal/gitx/gitx.go`; it needs `jsonschema`; it gates the release, so a new rule that fails on day one blocks releases; its `<!-- a2dlc:... -->` tag attaches only to the line before a fenced block or table.
*   **Reason for Rejection**: Rejected by the codebase and readiness lenses, adopted by author. If a doc check is built, it is a sibling standard-library script not wired into the release, with a path-exists rule first.

### Alternative J: An LLM drift reader (an `egm-drift-report` equivalent in Go)

*   **Pros**: Catches prose drift that no path or symbol check can.
*   **Cons**: It is the weakness the author wants to avoid, and the PoC did not measure a drift report; the PoC's control session found the seeded nonexistent path by reading, so the case for a mechanical check does not rest on LLM failure either.
*   **Reason for Rejection**: Rejected by the zoom-out and design lenses, adopted by author.

### Alternative K: Reuse Stage -1 as an intake stage, write findings into the run ledger, or challenge the intent inside Stage 0 or through a Specifier

*   **Pros**: No new artifact locations.
*   **Cons**: Stage -1 is "Make It Testable" (`internal/run/stages.go:9`) shared by five schemas; the ledger's negative attestation depends on one entry per container launch, so fake entries weaken it; Stage 0 is a byte copy and R8 binds the hash when the run opens; a Specifier's refusal fires after tokens are spent and shares the intent's bias (`internal/run/stage1.go:325`).
*   **Reason for Rejection**: Rejected by the structural and design lenses, adopted by author.

### Alternative L: A new end-to-end success metric for the ticket-to-push path

*   **Pros**: A metric written for this design would fit it exactly.
*   **Cons**: P2 and P4 already define the measurement (`docs/CLAIMS.md:16,18`), and `CLAIMS.md:3` says gate changes land there first.
*   **Reason for Rejection**: Rejected by three brainstorm lenses, adopted by author. The gates are reused and given a trial protocol.

### Alternative M: A provenance field (ticket hash or verbatim quote) in the intent schema

*   **Pros**: Lets a later lint check that the intent came from the ticket.
*   **Cons**: The intent schema is strict (`contracts/intent.schema.json:127`), so older binaries and the clean-room verifier reject unknown fields; nothing consumes the field yet.
*   **Reason for Rejection**: Rejected by author for now. Reopened only if P2 shows a lint needs the ticket text, and then as a sidecar.

### Alternative N: Keep the provider credential in a gitignored `.a2dlc/provider.env`

*   **Pros**: `install --roles` could create the file and the `.gitignore` entry itself; no resolution step; the relative path works when `a2dlc run` starts at the repository root.
*   **Cons**: `contracts/roles.schema.json` describes `env_file` as a path outside the repo, so the plan would contradict the contract; the credential would sit in the tree that any agent session on the host reads; a relative path resolves against the process's working directory, not the repository.
*   **Reason for Rejection**: Rejected by author (delegated). `ResolveEnvFile` expands `${A2DLC_PROVIDER_ENV}` and refuses a path inside the repository (Technical Plan, section 2). Builder containers would not have seen the file either way, since `/work` is a `git archive` of the base commit (`internal/run/stage3.go:259`).

### Alternative O: A template with all six dual-spec roles on OpenRouter

*   **Pros**: OpenRouter is the only backend measured inside the image (every full run in `docs/COVERAGE_AND_BENCHMARKS.md`); one key reaches models of two families, so the specifier pair passes `checkFamilies`; the launcher already allows `openrouter.ai` with no `egress_allow`; the same template would serve greenfield runs.
*   **Cons**: The trial's model calls would leave the operator's Claude subscription, which the author set as the rule for this work; four of the six roles never run in Lite, the mode the trial uses.
*   **Reason for Rejection**: Rejected by author (delegated). It is the fallback of step S1's decision rule if the Claude CLI cannot answer behind the proxy.

### Alternative P: Add the `roles` flag to `Install`'s signature

*   **Pros**: One call from `main` does the whole install.
*   **Cons**: Ten call sites in `internal/app/app_test.go` and `internal/app/tests_hint_test.go` change for a step that shares nothing with the rest of `Install`.
*   **Reason for Rejection**: Rejected by author (delegated). `WriteRolesTemplate` is a separate method that `main` calls when the flag is set.

### Alternative Q: Append the "write one with `a2dlc install --roles`" hint to every stage's skip reason

*   **Pros**: The hint would sit next to the stage it explains, inside `state.json`.
*   **Cons**: There is no single role-absent reason: Stages 1, 3, 4 and 5 each write their own, `skipStale` matches some of them by prefix, and four stage files and their tests would change.
*   **Reason for Rejection**: Rejected by author (delegated). One stdout line from `a2dlc run` reaches the agent with one change in `main.go`.

### Alternative R: Have `install` add `.a2dlc/roles.json` to `.gitignore`

*   **Pros**: A `roles.json` that is never committed cannot leak a host path into history; `check` would not hash it, since `git add -A` honours `.gitignore`.
*   **Cons**: `Install`'s "added N lines" output and its tests change; the schema's title calls the file versioned; a run could no longer be reproduced from the repository, since its roles would live only on one machine.
*   **Reason for Rejection**: Rejected by author (delegated). `roles.json` is committed, and `install --roles` says so.

--------------------------------------------------------------------------------

## Non-Functional Requirements

*   **Security & Privacy**: The provider credential stays outside the repository, as `contracts/roles.schema.json` requires; `roles.json` names it only through `${A2DLC_PROVIDER_ENV}`, so a committed `roles.json` carries no host path and no secret. `ResolveEnvFile` refuses a value that expands to nothing, is relative, or resolves into the repository, and the launcher keeps passing the file to `docker` by path while reading only the variable names. The variable is visible to anything in the environment of `a2dlc run`, including the agent session that starts it; that session can already read a file it is told the path of, so the change does not widen what the agent can reach. The token `claude setup-token` prints is valid for one year and for the operator's whole Claude account, and the role container holds it while it runs; the egress proxy limits where it can be sent to `api.anthropic.com`, and bead `a2dlc-az1` is the change that would keep it out of the container. Host paths leave every tracked file except the rule in `CLAUDE.md` that names the pattern: the three valid `roles` examples, the 14 invalid ones generated from them, `e2e/measure_ts.sh` and one example in `docs/COVERAGE_AND_BENCHMARKS.md`; the history keeps them. `Validate()` in `writeIntent` closes the path escape where an unvalidated `slice_id` is joined into a file path, and the signal tool applies the same regex to `from_slice` before reading. No change to who holds the signing key.
*   **Backward compatibility**: Making `invariants` and `acceptance` required in `a2dlc_intent_from_ticket` is a breaking change for an MCP client that relied on the filler; it is called out in the release notes. Existing intent files are not re-validated. The rejection of a different-content rewrite is new behavior for callers that re-issued intents; same-content repeats keep returning ok. A `roles.json` whose `env_file` is relative, inside the repository, or names a file that does not exist now fails when the run opens instead of when that role launches; the release notes say so. `install --roles` asks the operator to commit `roles.json`, which the schema already describes as versioned. The `install --roles` flag, the extra lines of `a2dlc version` and the `doctor` command are additive; the first line of `version` stays the bare version string. Turning the proxy constant into a variable changes no default for dev builds.
*   **Rollout & Rollback**: Image publishing and digest baking land in one release; a release whose binaries step cannot find all five digests fails before publishing binaries, so a half-published release cannot ship. Rolling back is re-tagging the previous release. The `docs/CLAIMS.md` protocol and the `SKILL.md` sequence are document changes with no runtime effect.
*   **Performance**: `writeIntent` adds one file read and a sha256 of at most a few kilobytes. `ensureImage` adds one `docker image inspect` per launch, which `digest` already ran; the pull happens once per machine. `doctor` runs `docker info` and five `docker image inspect`, each with a timeout, and is not on any hot path.

--------------------------------------------------------------------------------

## Detailed Implementation Plan

Five phases, each ending in a state that builds and passes its tests. A file touched in two phases is listed in each, marked "second touch". Steps that are not file edits are listed with an S number in the phase where they happen. The trial itself (Goal 5) and the conditional pieces of Technical Plan section 6 touch no file in this plan beyond the documents listed.

### Phase 1: an outside agent can run `a2dlc run` (code)

Ends with `go test` green for `./contracts/... ./internal/app/... ./internal/run/... ./cmd/...` and the contract tests passing. Two sub-phases, each green on its own.

#### Phase 1a: pinned references and pulls

##### 1. `internal/app/install.go` (Modify)

*   **Proposed Changes**: Add `rolesImage = "a2dlc-roles:0.15"` to the `var` block that holds `enginePyImage` and `engineTSImage`, under the same comment about `-X`. Add `func BakedImages() []string`, returning the three references in the order py, ts, roles. Rationale: Technical Plan section 1, table.

##### 2. `internal/run/launcher.go` (Modify)

*   **Proposed Changes**:
    *   `const EgressProxyImage` becomes `var EgressProxyImage = "a2dlc-egress-proxy:0.1"`, with one line saying the release overrides it with `-X`.
    *   New `ensureImage(ctx context.Context, bin, image string) error`: `docker image inspect` (30 s); on failure, if `registryRef(image)` is true, `docker pull` with its own 5-minute context and a second inspect; on a pull failure, `image %s: docker pull failed (the package may not be public): <tail of the output>`. `registryRef` is true when the reference contains a `/` and the part before the first `/` contains `.` or `:` or equals `localhost`. A local reference that `inspect` does not find returns `image %s: not available locally; build it, or use the registry reference the release pins`.
    *   `resolveProxyImage` calls `ensureImage` first. For a local tag the error keeps today's text, `not available (build it with docker build -t ... roles/egress-proxy)`.
    *   `Launch` calls `ensureImage` before `out.ImageDigest = digest(...)`, and returns its error, so a first launch records the digest.
    *   `Launch` refuses `r.EnvFile` containing `$` before the first docker call: `roles.%s.env_file %q was not resolved: a2dlc expands ${NAME} when it reads roles.json`.
*   **Rationale**: Technical Plan section 1, pulling; section 2, the unresolved guard.

##### 3. `internal/run/launcher_test.go` (Modify)

*   **Proposed Changes**: With the file's existing fake docker binary: a registry reference that `inspect` does not find is pulled and then inspected; `a2dlc-roles:0.15` and `a2dlc-egress-proxy:0.1` are not pulled, and the proxy's error names `docker build`; a failed pull names the reference; a role on a registry image records a non-empty `ImageDigest` on the first launch; an `env_file` of `${A2DLC_PROVIDER_ENV}` is refused before any docker call. The existing malformed-`env_file` and reserved-variable tests stay as they are. One existing case changes: in `TestEgressProxyThatCannotBeIdentifiedFailsClosed`, the "image missing" fake (`launcher_test.go:423`) fails every `image inspect`, which now stops at the role image before the proxy; its rule becomes `*"image inspect"*"a2dlc-egress-proxy"*) exit 1`, so the case still reaches the proxy. Rationale: the error text is what the agent reads.

##### 4. `cmd/a2dlc/main.go` (Modify)

*   **Proposed Changes**: Add `var z3Image = "a2dlc-engine-z3:0.1"` next to `version`. The `run` flag `--z3-image` (line 196) takes `z3Image` as its default. The usage text for `--z3-image` (line 55) says "the z3 image this binary pins by default" instead of the literal tag. `version` calls a new `printVersion(w io.Writer)`, which prints `version` on the first line, then one line `image <ref>` for each of `app.BakedImages()`, `a2run.EgressProxyImage` and `z3Image`, five in all; `run` passes `os.Stdout`. Rationale: Technical Plan section 1; the Verification Plan reads the pins from `version`.

##### 5. `cmd/a2dlc/plan.go` (Modify)

*   **Proposed Changes**: The `plan` flag `--z3-image` (line 55) takes `z3Image` as its default. Rationale: the second literal of the z3 tag.

##### 6. `cmd/a2dlc/main_dispatch_test.go` (Modify)

*   **Proposed Changes**: `printVersion` into a `bytes.Buffer` writes the version on its first line and five lines that start with `image `. No stdout capture is needed. Rationale: the release check depends on that shape.

##### 7. `.github/workflows/release.yml` (Modify)

*   **Proposed Changes**:
    *   Step "engine images on ghcr": after the four `docker build` lines, three more with the same `--label "$fuente"`: `-t "$r/a2dlc-engine-z3:$v" engines/z3`, `-t "$r/a2dlc-roles:$v" roles`, `-t "$r/a2dlc-egress-proxy:$v" roles/egress-proxy`. The push loop iterates full image names, `for n in a2dlc-engine-bb a2dlc-engine-bb-go a2dlc-engine-ts a2dlc-engine-py a2dlc-engine-z3 a2dlc-roles a2dlc-egress-proxy`, pushing and inspecting `"$r/$n:$v"`.
    *   Step "binaries": three more `grep`s, `'/a2dlc-engine-z3@sha256:'`, `'/a2dlc-roles@sha256:'` and `'/a2dlc-egress-proxy@sha256:'`; the existing guard fails when any of the five is empty; `-ldflags` gains `-X $app.rolesImage=$roles -X github.com/falsable/a2dlc/internal/run.EgressProxyImage=$proxy -X main.z3Image=$z3`.
    *   Same step: `cp skills/a2dlc/SKILL.md dist/SKILL.md` before the checksum line, which becomes `sha256sum a2dlc-* a2dlc-sbom.spdx.json SKILL.md > SHA256SUMS`. The publish step already uploads `dist/*`.
    *   `timeout-minutes` stays 30: the job took about 4 minutes on 2026-11-03, and the roles build adds one `npm ci`.
*   **Rationale**: Technical Plan section 1.

#### Phase 1b: the roles template and `env_file`

##### 8. `contracts/roles.schema.json` (Modify, description text only)

*   **Proposed Changes**: The `env_file` description gains: "The value is a literal absolute path or exactly ${NAME}, expanded from the environment of a2dlc; it must name an existing file outside the repository, and anything else is refused when roles.json is read." No change to type, pattern or required fields. Rationale: the contract governs (`CLAUDE.md`, Rules); the behavior goes into the schema text before the code.

##### 9. `contracts/examples/valid/roles__install-template.json` (Create)

*   **Proposed Changes**: The JSON in Technical Plan section 2, byte for byte. Rationale: one source for the template, checked by both example tests (file 20).

##### 10. `contracts/embed.go` (Create)

*   **Proposed Changes**: `package contracts`, with `import _ "embed"`, `//go:embed examples/valid/roles__install-template.json` and `var RolesInstallTemplate []byte`, plus a package comment saying the directory holds the contract and this file only embeds the install template. Rationale: Technical Plan section 2, embedding.

##### 11. `internal/app/roles_template.go` (Create)

*   **Proposed Changes**: `func (a App) WriteRolesTemplate() (string, error)` as in Technical Plan section 2: it returns `kept .a2dlc/roles.json (it exists)` when the file is there; otherwise it replaces `"a2dlc-roles:TEMPLATE"` in `contracts.RolesInstallTemplate` with `strconv.Quote(rolesImage)`, creates `.a2dlc` if needed, writes the file with mode 0644 and returns `wrote .a2dlc/roles.json`. Rationale: the template reaches an agent that has no clone.

##### 12. `internal/app/roles_template_test.go` (Create)

*   **Proposed Changes**: On a temporary repo with no `roles.json`, the written file decodes strictly into `contract.Roles`, holds no `TEMPLATE`, and every role's image is `rolesImage`. With an existing `roles.json`, its bytes are unchanged and the line says `kept`. No file other than `.a2dlc/roles.json` appears under the repo. Rationale: the behavior of file 11.

##### 13. `internal/run/envfile.go` (Create)

*   **Proposed Changes**: `func ResolveEnvFile(repoDir, value string) (string, error)` with the grammar and checks of Technical Plan section 2. Rationale: one place for the rule, called by the three readers of `roles.json`.

##### 14. `internal/run/envfile_test.go` (Create)

*   **Proposed Changes**: Cases: `${VAR}` set to an absolute file outside the repo returns that path; unset and empty variable are refused naming `VAR`; `$VAR`, `prefix${VAR}` and `${lower}` are refused; a relative path is refused; a file inside the repo and a symlink outside the repo that points into it are refused; a literal absolute path outside the repo passes; a missing file is refused naming the path; `<repo>/..name.env` is inside and refused. `outsideRepo` alone: `C:\repo` against `D:\x.env` is outside, because `filepath.Rel` fails across volumes on Windows (the case is skipped on other systems). Rationale: the refusal is the only guard on where the credential lives.

##### 15. `internal/run/machine.go` (Modify)

*   **Proposed Changes**: In `readRoles`, after `validateRoleConfig` for a role and its `tier_up`, resolve `EnvFile` when it is not empty with `ResolveEnvFile(m.app.Repo.Dir, ...)` and store the role back into `m.roles.Roles[name]`, copying the `tier_up` struct before changing it. The error is prefixed with `roles.<name>.env_file:`. Rationale: Technical Plan section 2.

##### 16. `internal/run/roles_template_test.go` (Create)

*   **Proposed Changes**: In package `run`: a temporary repo, `app.App{...}.WriteRolesTemplate()`, then the file decodes, every role passes `validateRoleConfig`, `checkFamilies` passes, and with `A2DLC_PROVIDER_ENV` set (`t.Setenv`) to a `NAME=value` file outside the repo, `ResolveEnvFile` returns it for both roles. Rationale: the template is only useful if `run` accepts it, and that validation lives in `run`, which `app` may not import.

##### 17. `cmd/a2dlc/propose.go` (Modify)

*   **Proposed Changes**: After `ProposeRoleConfig` returns the role, resolve its `EnvFile` with `ResolveEnvFile` against the repository directory the command already reads `roles.json` from, and use the resolved role for the launch. Rationale: a second reader of `roles.json`.

##### 18. `cmd/a2dlc/plan.go` (Modify, second touch)

*   **Proposed Changes**: In `planner`, after `RoleConfig`, resolve the role's `EnvFile` against `a.Repo.Dir` the same way; a failure prints the error and returns 64, as the `RoleConfig` failure does. Rationale: the third reader of `roles.json`.

##### 19. `cmd/a2dlc/main.go` (Modify, second touch)

*   **Proposed Changes**:
    *   `install` gains `--roles`; after `Install`, when it is set, append to `done` the lines of a new helper `rolesInstallLines(a app.App) ([]string, error)`: the line of `a.WriteRolesTemplate()`, then the two lines of Technical Plan section 2. Usage text for `install` names the flag.
    *   `run`: after the stage lines and before the outcome line, print `rolesHint(repo.Dir)`, a new helper that returns the hint line of Technical Plan section 2 when `.a2dlc/roles.json` does not exist and `""` otherwise.
*   **Rationale**: Technical Plan section 2.

##### 20. `cmd/a2dlc/main_dispatch_test.go` (Modify, second touch)

*   **Proposed Changes**: `rolesHint` on a directory without `.a2dlc/roles.json` returns the hint, and `""` with one. `rolesInstallLines(app.App{Repo: gitx.Repo{Dir: t.TempDir()}})` creates `.a2dlc/roles.json`, and its lines name `A2DLC_PROVIDER_ENV` and the commit. Both helpers are called directly, so no test changes the working directory (`t.Chdir` needs Go 1.24, and the repo builds with 1.23). Rationale: the flag and the hint.

##### 21. `contracts/examples/valid/roles__tier-up.json`, `roles__claude-gemini.json`, `roles__egress.json` (Modify)

*   **Proposed Changes**: Every `env_file` becomes `${A2DLC_PROVIDER_ENV}`; `a2dlc-roles:0.1` becomes `a2dlc-roles:0.15`, the tag `CLAUDE.md` lists as current. `a2dlc-role-openrouter:0.1` in the back-translator entries is a different image and stays. Rationale: host paths in a public repo.

##### 22. `contracts/examples/invalid/roles__*.json` (Modify, generated)

*   **Proposed Changes**: Regenerated by `python contracts/tests/generate_invalid.py`, which derives 14 of them from `roles__claude-gemini.json`; each loses its host path. They are not edited by hand. Rationale: same as file 21.

S1 (after file 22): the measurement of Technical Plan section 2, decision rule included. The author runs it; Phase 1c can merge before it, and S1's record in file 27 and Phase 4 wait for it.

#### Phase 1c: documents and scripts that describe Phase 1

##### 23. `templates/ci/a2dlc.yml` (Modify)

*   **Proposed Changes**: Delete the requirements paragraph lines that say the release and the images are private (the `A2DLC_DOWNLOAD` secret and "Manage Actions access" lines), keeping the `A2DLC_ALLOWED_SIGNERS` requirement. In the binary step, `GH_TOKEN` takes `${{ github.token }}` instead of `secrets['A2DLC_DOWNLOAD']`. Delete the step "login to ghcr (engine images)". Rationale: measured anonymous access, The Problem, item 4.

##### 24. `install.sh` and `install.ps1` (Modify)

*   **Proposed Changes**: Inside the existing condition that installs the skill only in a git repository, after the binary is in place:
    *   Download `SKILL.md` from `${BASE_URL}` (`$BaseUrl`), the same release as the binary. Its expected hash is the first field of the line of the already downloaded `SHA256SUMS` that ends in ` SKILL.md` (`grep ' SKILL.md$'` in `install.sh`, `-match ' SKILL\.md$'` in `install.ps1`).
    *   A failed download means the release ships no skill, which is the case for every release before this plan, the pinned v0.5.0 included. The script then falls back to today's URL on `main` and prints `this release ships no SKILL.md; installed the one on main, which may not match the binary`. That keeps today's behavior until S4 bumps the pin.
    *   A downloaded `SKILL.md` with no line in `SHA256SUMS`, or with another hash, is not installed: the script prints `SKILL.md does not match SHA256SUMS; skill not installed` and exits 1. The binary stays installed, since it was verified first.
    *   In `install.ps1` the `try`/`catch` covers only the release download; the hash check runs after it and `throw`s on a mismatch, as the binary check does.
    *   The pinned default version does not change here (S4). Rationale: the skill must describe the binary that was installed.

##### 25. `README.md` (Modify)

*   **Proposed Changes**: The CLI reference row for `install` (line 161) adds `--roles`. "Hook it into your agent" (lines 106-108) says that `run` needs `install --roles` and `A2DLC_PROVIDER_ENV` pointing to a credential file outside the repo. The MCP row (line 109) lists all seven tools, both intent tools included. The "Try it in 60 seconds" block gains one line for `run --mode lite`. Rationale: the documented path must be the one the trial uses.

##### 26. `e2e/measure_ts.sh` (Modify)

*   **Proposed Changes**: Line 11 becomes `ENTROPOS="${ENTROPOS:?set ENTROPOS to a checkout of the Entropos repository}"`, and the usage comment names the variable. Rationale: the host path rule.

##### 27. `docs/COVERAGE_AND_BENCHMARKS.md` (Modify)

*   **Proposed Changes**:
    *   Under "Installation" (line 250), a subsection "Anonymous access, measured" with the commands and results of 2026-11-07: the `curl -fsSL` of `SHA256SUMS` of v0.5.0 (200) and the anonymous ghcr token and manifest request for each of the four images of that release (200 each), and the decision taken (CI template rewritten).
    *   Under "Role images" (line 676): the example whose `env_file` is a Windows home-directory path uses `${A2DLC_PROVIDER_ENV}`, and the line that names `a2dlc-engine-py:0.7` as current names 0.12, the default in `internal/app/install.go`.
    *   Under "Role images": the record of S1 (date, images, model per role, hosts the proxy refused, outcome, the decision-rule branch taken).
*   **Rationale**: the measurements live with the other measurements.

### Phase 2: the MCP intent tools

Ends with `go test` green for `./internal/contract/... ./internal/mcp/...`.

##### 28. `internal/contract/intent.go` (Modify)

*   **Proposed Changes**: `func ValidSliceID(s string) bool { return sliceRe.MatchString(s) }`, next to `sliceRe`. Rationale: the signal tool needs the regex and it is unexported.

##### 29. `internal/mcp/server.go` (Modify)

*   **Proposed Changes**: Technical Plan section 3: `Validate()` and the existing-file comparison in `writeIntent`; the nil-fallback reads and the named error in `toolIntentFromTicket`, `non_goals` falling back to `[]`, the two names added to `required`; `ValidSliceID` on `from_slice` and on a given `slice_id` at the top of `toolIntentFromSignal`; the constants `bugfixInvariant` and `bugfixNonGoal`. Rationale: Technical Plan section 3.

##### 30. `internal/mcp/server_test.go` (Modify)

*   **Proposed Changes**: New cases call `dispatchTool` directly with a temporary repo: `slice_id` `../x` refused, no file under `.a2dlc/`; no `acceptance` refused with the named error, no file; no `non_goals` accepted and the file holds `"non_goals": []`; `mode` `"fast"` refused by `Validate`, no file; identical repeat ok and the file's modification time unchanged; different repeat refused, the error holds the sha256 of the existing file, the bytes unchanged; the signal tool refuses `from_slice` `a/b`. The `Serve` test at line 99 passes `invariants` and `acceptance` and omits `non_goals`, so it keeps passing with `[]`. Rationale: the bug was found by reading; the tests make it executable evidence.

### Phase 3: documents for the trial

##### 31. `docs/CLAIMS.md` (Modify, and track)

*   **Proposed Changes**: The section "Trial protocol for P2 and P4" of Technical Plan section 4, scoring commands included. In the P2 row (line 16), the "Number published" cell gains the scope sentence of section 4 and the "Reproduce" cell points to the new section; the claim cell does not change. Line 18 names `escalations.schema.json`, which is planned work under bead `a2dlc-pe6.4.2` (line 42): mark it `(planned)` in both lines. Commit the file for the first time, after `git grep`-equivalent checks on it for host paths, owner quotes and worker or session ids, the three things `CLAUDE.md` forbids in tracked files. Rationale: `CLAIMS.md:3` says gate changes land here first, and the trial must be reproducible from the public repo.

##### 32. `skills/a2dlc/SKILL.md` (Modify)

*   **Proposed Changes**: Section A ("Installing `a2dlc` in a Repository", lines 27-41) says when to run `install --roles` and that `A2DLC_PROVIDER_ENV` must point to a credential file outside the repo, which the operator provides, and that `.a2dlc/config.json`, `.a2dlc/roles.json` and `.gitignore` are committed before the first run. Section E gains the merge-and-push sequence and the list of what the agent must not do (Technical Plan section 4). Section F's `a2dlc_intent_from_ticket` entry says `invariants` and `acceptance` are required and `mode: "lite"` is what the trial uses. Rationale: the sequence is what P4 measures; it did not exist.

##### 33. `rfcs/0007-fewer-human-stops.md` (Modify, one marker)

*   **Proposed Changes**: Line 6 ends with ` <!-- a2dlc:planned -->`, the marker the doc-versus-code rule of Technical Plan section 6 accepts for a path that does not exist yet (`contracts/authorization.schema.json`). Rationale: the author kept this marker during drafting; its consumer is that rule, if it is built. It does not change the RFC's substance, so the comment period is unaffected.

### Phase 4: release and pins

No code. Phases 1 to 3 are on the default branch before the tag.

*   **S2.** Push the tag `vX.Y.0`. The release builds seven images; `IMAGENES.txt` has seven lines; `a2dlc version` on a downloaded binary prints five `ghcr.io/...@sha256:` references.
*   **S3.** Anonymous check of the seven images and of `SKILL.md`, from a shell with no GitHub session:

    ```sh
    curl -fsSL -o /dev/null https://github.com/falsable/a2dlc/releases/download/vX.Y.0/SKILL.md
    curl -sL https://github.com/falsable/a2dlc/releases/download/vX.Y.0/IMAGENES.txt | while read ref; do
      n=${ref#ghcr.io/}; repo=${n%@*}; dig=${n#*@}
      tok=$(curl -s "https://ghcr.io/token?scope=repository:${repo}:pull&service=ghcr.io" | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
      echo "$repo $(curl -s -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $tok" \
        -H 'Accept: application/vnd.oci.image.index.v1+json, application/vnd.oci.image.manifest.v1+json, application/vnd.docker.distribution.manifest.v2+json' \
        "https://ghcr.io/v2/${repo}/manifests/${dig}")"
    done
    ```

    Every line must end in 200. A package that does not is set to public by the repository owner in its package settings, and S3 runs again. The result goes into `docs/COVERAGE_AND_BENCHMARKS.md`, "Anonymous access, measured" (file 27, second touch).
*   **S4.** One commit, as the earlier pin bumps did: the default version in `install.sh:8` and `install.ps1:7`, `A2DLC_VERSION` in `templates/ci/a2dlc.yml:39`, the self-gate in `.github/workflows/a2dlc.yml`, and the version mentions in `docs/COVERAGE_AND_BENCHMARKS.md`.

### Phase 5: manual P4 attempt, then `doctor`

The files of this phase are a first cut. The record of the attempt (file 34) can change their check list before they are written; if it shows no install failure, files 35 to 39 are not written and the decision is recorded in file 34.

##### 34. `docs/COVERAGE_AND_BENCHMARKS.md` (Modify, third touch)

*   **Proposed Changes**: A subsection under "The state machine" (line 416) recording the first manual P4 attempt: base commit, ticket, the sequence followed, where it stopped or how it ended, human touches by kind, cost and time against the caps. Rationale: `doctor` is scoped to what this attempt showed, and the trial protocol says the measurement lives here.

##### 35. `internal/run/doctor.go` (Create)

*   **Proposed Changes**: `func LoadRoles(repoDir string) (contract.Roles, error)`, the body of `Machine.readRoles` moved here (read, missing file as an empty role set, strict decode, `Visible`, `validateRoleConfig`, `ResolveEnvFile`, `checkFamilies`); `readRoles` becomes `m.roles, err = LoadRoles(m.app.Repo.Dir)`. `func EnvKeys(path string) ([]string, error) { return readEnvKeys(path) }`. Rationale: `doctor` lives above `run` and needs these two checks.

##### 36. `cmd/a2dlc/doctor.go` (Create)

*   **Proposed Changes**: `func doctor(args []string, dir, docker string, stdout io.Writer) int` with the checks of Technical Plan section 5 in order, `--json` for the JSON object, text otherwise. Rationale: Technical Plan section 5.

##### 37. `cmd/a2dlc/main.go` (Modify, third touch)

*   **Proposed Changes**: In `run`, next to the `version` case and before `gitx.Open`: `if args[0] == "doctor" { cwd, _ := os.Getwd(); return doctor(args[1:], cwd, "docker", os.Stdout) }`. A usage line for `doctor`. Rationale: a missing repository must be a failed check, not exit 64.

##### 38. `cmd/a2dlc/doctor_test.go` (Create)

*   **Proposed Changes**: With a fake docker script in `t.TempDir()` passed as `docker`, and a temporary repo passed as `dir`: a missing local image gives that check `ok: false` and exit 1; a registry reference that is not local is `ok: true`; an absent `roles.json` is `ok: false`; all checks passing gives exit 0; outside a git repository the first check fails and the exit is 1; the JSON has the keys `ok` and `checks`, and each check has `name`, `ok` and `detail`. Rationale: the exit code is the signal the trial reads.

##### 39. `README.md` (Modify, second touch) and `skills/a2dlc/SKILL.md` (Modify, second touch)

*   **Proposed Changes**: A `doctor` row in the CLI reference; the skill tells the agent to run `a2dlc doctor` before `run` and to stop and report its output when it exits 1. Rationale: the command only helps if the agent is told to run it.

*   **S5.** When files 35 to 39 were written: a release and a pin bump as in S2 to S4, so `doctor` reaches outside agents.

### Files deliberately not touched

*   `contracts/*.schema.json` other than the `env_file` description of `roles.schema.json`: no shape changes. The two trial labels live in the protocol document, not in the escalation schema.
*   `internal/plan/`, `internal/run/stage0.go`, `internal/run/stage1.go` and the other stage files: the intent is not challenged inside the run, and the role-absent hint is printed by `main`.
*   `internal/app/app_test.go` and `internal/app/tests_hint_test.go`: `Install`'s signature does not change.
*   `contracts/tests/check_rfc.py`: left as the section 6.4 checker.
*   `rfcs/`: no new RFC; `rfcs/0008` is not touched.

### Verification Plan

*   **Build and unit tests**, inside `golang:1.23-bookworm` as `CLAUDE.md` requires (Go is not installed on the host): `gofmt -l .` prints nothing, `go vet ./...`, then per phase the packages it names, and the whole `go test -count=1 ./...` once before the tag.
*   **Contract**: `python contracts/tests/generate_invalid.py`, then `validate.py` over `contracts/` (file 8 changes a schema's text, files 9, 21 and 22 change examples). `TestExamplesOfContractDecode` decodes the new template with the other valid examples. `check_rfc.py` and `python e2e/make_conformance_vectors.py --check` must pass unchanged; neither reads `roles.schema.json`.
*   **No host paths**: `git grep -n -I 'C:/Users' -- ':!CLAUDE.md' ':!docs/designs/'` prints nothing after Phase 1 (`CLAUDE.md` states the rule with the pattern, and this design quotes the command).
*   **E2E of the binary**: `bash e2e/windows.sh <empty folder>` still passes (install, pre-push, push to a bare remote); it does not exercise `--roles`, so files 12, 16 and 20 cover the template.
*   **S1**: the local Lite run of Technical Plan section 2, before the release; the author runs it with a real token.
*   **Install scripts**: after S2, each script with `A2DLC_VERSION=vX.Y.0` in a scratch git repo installs the skill from the release; with the pinned v0.5.0 it installs the one on `main` and prints the fallback line. The mismatch branch has no automated test, as the scripts have none today.
*   **Release**: S2 and S3 of Phase 4.
*   **Cold install, by hand once**: on a machine with no local a2dlc image, the install script, `install --agent claude --roles`, `A2DLC_PROVIDER_ENV` pointing to a credential file outside the clone, and `a2dlc run --mode lite` on a Python fixture. Expected: no stage skipped for a missing role or image; `verify-run` ok. This is CUJ 1 and the P4 dry run; its record is file 34.
*   **MCP fix**: `go test ./internal/mcp/...` with the new cases; by hand, one `a2dlc_intent_from_ticket` call over stdio without `acceptance` returns the error result and writes no file.
*   **Documents**: every backticked repository path in `docs/CLAIMS.md` and `skills/a2dlc/SKILL.md` exists or is marked planned. Runtime paths such as `.a2dlc/verdict.json` and bare file names are not repository paths and are not checked:

    ```sh
    for p in $(grep -ohE '`(docs|skills|contracts|internal|cmd|e2e|rfcs|engines|templates|roles|conformance|verifiers|examples)/[A-Za-z0-9_./-]+`' docs/CLAIMS.md skills/a2dlc/SKILL.md | tr -d '`' | sort -u); do
      [ -e "$p" ] || grep -q "$p.*planned" docs/CLAIMS.md skills/a2dlc/SKILL.md || echo "missing $p"
    done
    ```

    prints nothing.
*   **Gates of this repo**: `a2dlc check` over the diff as the repo gates itself (`.a2dlc/config.json`), and the repository's pre-push.

--------------------------------------------------------------------------------

### Additional Resources

*   [The readiness review record](./ticket-to-push-outside-agent.readiness-review.md): the round-1 findings, which reviewer raised each, and how this revision resolved it.
*   `docs/CLAIMS.md`: gates P1 to P4 and the "not adopted, on purpose" list.
*   `docs/A2-DLC_v3.5_protocol.md` sections 0 to 2 and 10: what Stage 1 already does for the intent, and what stays undecidable.
*   `docs/VS_SDLC_AND_ADLC.md`, "Work that is not executable code": why design review stays with humans.
*   `rfcs/0007-fewer-human-stops.md` and `rfcs/0008-feature-plans.md`: the signed-approval and plan work this design must not pre-empt.
*   `docs/STATE_MACHINE.md` rule R8: the intent hash is bound at run open.
*   Claude Code's documentation on authentication (`claude setup-token`, `CLAUDE_CODE_OAUTH_TOKEN`) and network configuration, read 2026-11-07: the template's credential and egress host.

### Future Improvements

*   **`a2dlc intent check`** as specified in Technical Plan section 6, if the P2 count shows vacuous intents surviving the required fields.
*   **A doc-versus-code sibling script** with a path-exists rule, if drift causes a trial failure.
*   **`a2dlc promote`**, if the manual P4 attempt shows agents getting the merge-and-push sequence wrong by the protocol's criterion.
*   **A template for greenfield runs**, with the dual-spec roles on two families, once a second credential is accepted.
*   **RFC-0007 Part B** (pre-authorization), if authorization stops dominate the P2 breakdown.
*   **A versioned exit-code table and `escalations.schema.json`** as `docs/CLAIMS.md:42` plans under P4.
*   **The credential in the proxy, not the role** (bead `a2dlc-az1`).

--------------------------------------------------------------------------------

*Drafted and reviewed with the EGM plugin (brainstorm, draft, and the implementation-readiness stage of its review). Alternative B records why EGM itself is not adopted into a2dlc.*
