<p align="center">
  <img src="docs/assets/a2dlc-logo.jpg" alt="A2-DLC" width="160">
</p>

<h1 align="center">A2-DLC</h1>

<p align="center"><b>The provider-agnostic, clean-room orchestrator and deterministic verification protocol for AI software engineering.<br>Your AI agent wrote the bug — and the test that hides it. A2-DLC orchestrates isolated roles and mathematical oracles so no single model grades its own work.</b></p>

<p align="center">
  <a href="https://github.com/falsable/a2dlc/releases"><img src="https://img.shields.io/github/v/release/falsable/a2dlc?style=flat-square&color=10b981" alt="Release"></a>
  <a href="https://github.com/falsable/a2dlc/actions"><img src="https://img.shields.io/github/actions/workflow/status/falsable/a2dlc/a2dlc.yml?style=flat-square&label=self-gated" alt="CI"></a>
  <a href="docs/A2-DLC_v3.5_protocol.md"><img src="https://img.shields.io/badge/protocol-RFC--0001%20v3.5-6366f1.svg?style=flat-square" alt="Protocol"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-3b82f6.svg?style=flat-square" alt="License"></a>
  <a href="skills/a2dlc/SKILL.md"><img src="https://img.shields.io/badge/agent_skill-SKILL.md-8b5cf6.svg?style=flat-square" alt="Skill"></a>
</p>

<p align="center">
  <a href="#try-it-in-60-seconds">Try it in 60 seconds</a> ·
  <a href="#how-it-works">How it works</a> ·
  <a href="#hook-it-into-your-agent">Claude Code / Gemini / Cursor / MCP</a> ·
  <a href="#does-it-actually-catch-anything">Benchmarks</a> ·
  <a href="docs/A2-DLC_v3.5_protocol.md">Spec</a>
</p>

```
$ a2dlc check
FAIL: 1 target, 6 changed lines examined
  ✗ src/pricing.py::apply_discount   (crosshair diffbehavior, 10.4s)
    counterexample: args=(100, 101) -> old=0, new=-1
```

That is a real refactor an agent called "equivalent". Its own tests were green. The old version clamped negative totals to 0; the new one returns `-1`. No LLM graded this — a symbolic execution engine searched for an input where `old(x) ≠ new(x)` inside a `--network none` container, and found one.

---

## The problem: the Circular Trap

Every agentic framework (Spec Kit, BMAD, OpenSpec, Superpowers) ends the turn the same way: **the model that wrote the code decides whether the code is right.** When it misunderstands the spec, it writes the bug into the implementation *and* into the test, and both pass. Review by a second prompt doesn't help — it shares the same context and the same blind spot.

A2-DLC replaces self-grading with **executable evidence**:

| Question | Who answers it today | Who answers it with A2-DLC |
|---|---|---|
| Did this refactor change behavior? | the agent ("no, it's equivalent") | CrossHair / fast-check / black-box oracle — with a counterexample |
| Does this new test actually test anything? | the agent ("yes, it passes") | the test is run against the *base* tree; if it still passes, it has no teeth |
| Was every changed line exercised? | nobody | coverage precondition — unexamined lines block the gate |
| Was the verdict tampered with? | nobody | receipt bound to the Git `tree_hash`, waivers signed with your SSH key |

`PASS` means *no divergence found within the recorded search budget* — a bounded search, not a proof. Everything it could not decide is handed to a human as a concrete, reproducible question, not a vibe.

---

## Try it in 60 seconds

Requires Git and Docker. The engines run inside hermetic containers; nothing talks to the network.

```bash
# 1. Install the single binary (+ bundled agent skill)
curl -fsSL https://raw.githubusercontent.com/falsable/a2dlc/main/install.sh | sh     # Linux / macOS / WSL
irm https://raw.githubusercontent.com/falsable/a2dlc/main/install.ps1 | iex          # Windows

# 2. Run the seeded-bug demo
git clone https://github.com/falsable/a2dlc && cd a2dlc/examples/quickstart
./demo.sh
```

You will see the gate **FAIL** on a refactor that silently changed behavior, print the counterexample, then **PASS** once the fix is applied. Then do it on your own repo:

```bash
cd your-repo
a2dlc install --agent claude     # or --agent gemini; auto-detects Python / TypeScript
a2dlc check                      # exit 0 PASS · 1 FAIL · 2 INCONCLUSIVE
```

---

## How it works

**1. Clean-room protocol orchestrator (`a2dlc run`, Stages −1 to 7)** — orchestrates isolated container roles across any LLM provider (`claude`, `gemini`, `openrouter`, `entropos`) so no single model sees the whole picture:
- **Stages 0–2 (Spec & Track Routing):** dual **Specifiers** ($\alpha \not\equiv \beta$) write the spec independently, an **Inquisitor** probes boundaries, **Z3** checks invariants for vacuity/contradiction (Stage 1.5), and the deterministic Track Router assigns `N=1` or `N=2`.
- **Stage 3 (Clean-Room Build & Saboteur):** **Builders** implement against Tier 0 gates (declared repo tests, LCOV/XML coverage, complexity, CrossHair lemmas) with round-to-round candidate file anchoring (`feedback_work_files`), and a blind **Saboteur** attacks public signatures without ever seeing the implementation.
- **Stages 4–5 (Proportional Differential & Mutation Verification):** Stage 4 reuses Stage 3 coverage (`diff_from_stage3`, `candidate_cached`) and applies guarded base stubs (`guard: true`) when extending prior `@a2dlc`-contracted files. Stage 5 runs diff-scoped mutation testing proportional to risk (`config.rung: "critical_code"` runs Stage 5 only when `critical_code` globs are touched).
- **Stages 6–7 (Blast Radius & Progressive Delivery):** AST/contract blast-radius analysis (imports, OpenAPI, Protobuf, SQL DDL) and canary rollback plan with circuit breaker `CB-06`. Every stage writes to a SHA-256 hash-chained `ledger.json` verified by `a2dlc verify-run`.

Measured: a greenfield slice converges in **84–127 s** across 6–8 role containers at **$0.025–$0.069 per slice** (Claude Haiku 4.5 + Gemini 2.5 Flash). Sequential 3-issue brownfield chains with `rung: "critical_code"` converge in **117.4 s** on `tinylinks` (`47.1 s`, `41.6 s`, `28.7 s`) and **425.6 s** on `click` (`91.9 s`, `65.0 s`, `268.7 s`), passing all sealed holdout suites and final `a2dlc check --base B0`. Full reference in the [spec](docs/A2-DLC_v3.5_protocol.md), [RFC-0002](rfcs/0002-declared-tests-as-tier0.md), and [coverage matrix](docs/COVERAGE_AND_BENCHMARKS.md).

**2. Standalone differential gate (`a2dlc check`)** — extracts every changed function from `git diff` and searches for an input where `old(x) ≠ new(x)`:
- Python → [CrossHair](https://github.com/pschanely/CrossHair) symbolic execution (`diffbehavior`)
- TypeScript → [fast-check](https://github.com/dubzzz/fast-check) property-based testing
- Go / any CLI → black-box I/O oracle

Plus test gates: *no-op test* detection (passes on the base tree), coverage precondition on changed lines, dependency allowlist. Output is `.a2dlc/verdict.json`, hash-bound to the Git tree it judged.

**3. Agent lifecycle hooks** — `PreToolUse` blocks edits to `protected_paths` and `git push --no-verify` *before* they run; the `Stop` / `AfterAgent` hook runs level 0 (`a2dlc check --level 0`) at the end of every turn: no test runs, it informs and never blocks, and it writes `.a2dlc/reports/level0.json`. Level 1 (`a2dlc check`, which runs the tests) is the gate: the agent runs it when it says "done", `pre-push` verifies its receipt (`a2dlc verify`, <50 ms), and CI runs it on the PR. `pre-push` blocks a push only on evidence about the pushed tree: a `FAIL`, a waiver without a valid signed approval, a protected path without approval, `critical_code` left unjudged. A missing or stale receipt (its `tree_hash` is not the commit's tree), one based on commits that were not pushed, or an `INCONCLUSIVE` verdict only warns; `a2dlc verify --strict` blocks on all of them. Closing the authorship gap takes `a2dlc check --require-signatures` in CI over the same commit ([RFC-0001 §6.4.9](docs/A2-DLC_v3.5_protocol.md#649-what-a-receipt-does-not-claim), [SECURITY.md](SECURITY.md)).

**4. Intentional changes are signed, not argued** — the agent declares the change in `.a2dlc/waivers.json`; a human signs it with `a2dlc approve src/pricing.py::apply_discount --key ~/.ssh/id_ed25519`. Agents never hold the key. A behavior change the engines find in code your `.a2dlc/config.json` does not list under `critical_code` is only a note in the receipt, with its counterexample; on critical code it fails until signed ([docs/DIVERGENCE_NOTES.md](docs/DIVERGENCE_NOTES.md)).

<p align="center"><img src="docs/assets/protocol-architecture.svg" alt="A2-DLC architecture" width="820"></p>

---

## Hook it into your agent

| Harness | Setup | What gets wired |
|---|---|---|
| **Claude Code** | `a2dlc install --agent claude` | `.a2dlc/config.json`, `pre-push`, `.claude/settings.json` (`PreToolUse` + `Stop`) |
| **Gemini CLI / Antigravity** | `a2dlc install --agent gemini` | same + `.gemini/settings.json` (`PreToolUse` + `AfterAgent`) |
| **Codex / Cursor / Copilot** | `a2dlc install` + [`SKILL.md`](skills/a2dlc/SKILL.md) | `pre-push`; the agent runs `a2dlc check` per the skill |
| **Any MCP client** | `"a2dlc": { "command": "a2dlc", "args": ["mcp"] }` | `a2dlc_status`, `a2dlc_check`, `a2dlc_verify`, `a2dlc_verify_run`, `a2dlc_run`, `a2dlc_plan_run`, `a2dlc_approve_pending`, `a2dlc_answer`, `a2dlc_format_report` |
| **Skill registries** | `npx skills add falsable/a2dlc` · `agy plugin install https://github.com/falsable/a2dlc` | bundled `SKILL.md` |

Or paste this to your agent:

```
Install A2-DLC here: run the install script from https://github.com/falsable/a2dlc,
read .claude/skills/a2dlc/SKILL.md, run `a2dlc install --agent claude`, then `a2dlc check`
and resolve every FAIL or INCONCLUSIVE target in .a2dlc/verdict.json.
```

Works underneath Spec Kit, BMAD, OpenSpec or Superpowers: they structure the prompts, A2-DLC verifies the output.

---

## Does it actually catch anything?

All numbers come from frozen mutants (`e2e/testdata/`, pinned by SHA-256) and fixed seeds. Methodology, Wilson 95% intervals and per-stage coverage: [`docs/COVERAGE_AND_BENCHMARKS.md`](docs/COVERAGE_AND_BENCHMARKS.md).

| Benchmark | Result |
|---|---|
| **SWE-bench Verified** — 38 real agent patches (18 rejected by maintainers, 20 accepted) | CrossHair alone: **0/18** caught. Repo tests + coverage precondition + Saboteur overlay: **18/18 caught (100%, CI 82–100%)**, **0/20 false positives** |
| **Multi-issue `a2dlc run` chains** (`rung: "critical_code"`, Stage 4 `diff_from_stage3` + `guard: true` base stubs, round-2 file anchoring) | **`tinylinks` (`#1 → #2 → #5`)**: **3/3 PASS** in **117.4 s** (`3/3` hidden suites green, final `a2dlc check` `PASS` in `14.5 s`). **`click` (`#1 → #5 → #6`)**: **3/3 PASS** in **425.6 s** (`28/28` sealed holdout tests green, final `a2dlc check` `PASS` in `73.9 s`) |
| **"Every session verifies its own work"** (rule from the ADLC infographic, not AI-DLC) vs `a2dlc check`, 9 frozen trees | Self-verification lets **3/5** blocking cases through; the gate blocks **5/5**, passes **4/4** |
| TypeScript engine — 8 frozen mutants + 1 equivalent refactor, seeds 1–3 | **8/8** caught on every seed; refactor `PASS` |
| Black-box engine — 5 mutants of `internal/verdict` + 1 equivalent refactor | **5/5** caught in 8–12 s; refactor `PASS` |
| Equivalent-mutant pruning (`mutmut`, `billing.py`) | kill rate **48.9% → 65.6%** after pruning 13 equivalent mutants |
| Test & property gates — 12 frozen cases | **12/12** judged as expected; every receipt validates against the JSON Schemas |

The gate also gates this repo: every push to `main` runs `a2dlc check` against a pinned, hash-verified release of itself ([`.github/workflows/a2dlc.yml`](.github/workflows/a2dlc.yml)).

---

## How it compares

| | Spec Kit · BMAD · OpenSpec · Superpowers | AI-DLC v2.11 | **A2-DLC** |
|---|---|---|---|
| Primary mechanism | Markdown plans, personas, prompt skills | workflow engine with hooks, an audit log and, by default, a human approval gate per stage | Go state machine + `--network none` Docker engines |
| Who grades the code | the agent, or a human reading `tasks.md` | the exit code of the test command a person approved, which the engine runs, and an LLM reviewer subagent; advisory lint and type-check sensors for TS/JS | symbolic execution, PBT, Z3, fuzzing, mutation |
| Same agent writes code + test | ✗ undefended | partial: the reviewer is separate, the tests are the builder's | ✓ clean-room roles, blind Saboteur |
| Refactor equivalence (`old(x) ≡ new(x)`) | ✗ | ✗ | ✓ native, per function, with counterexample |
| Anti-bypass | ✗ | ✓ review receipts bound to a source fingerprint; `aidlc attest resolve` can require independent, signed records | ✓ `tree_hash`-bound receipt, hook guard, SSH-signed waivers |
| What the receipt certifies | — | an LLM review marked the unit READY | the oracles ran on this tree: differential, NOP test gate, mutation |

The AI-DLC column was read from the source code and docs of [awslabs/aidlc-workflows](https://github.com/awslabs/aidlc-workflows) at v2.11.2 and at its main branch of 2026-11-03; it was not run or benchmarked. Stage-by-stage against classic SDLC and the ADLC infographic: [`docs/VS_SDLC_AND_ADLC.md`](docs/VS_SDLC_AND_ADLC.md).

---

## CLI reference

| Command | Purpose |
|---|---|
| `a2dlc install [--agent claude] [--agent gemini]` | Config, `pre-push` hook, agent lifecycle hooks; when it finds a test runner (vitest, `node --test`, pytest, or a `test` script) and the config has no `tests` section, it warns that changes to code no engine examines stay INCONCLUSIVE and prints a `tests` section to edit, with a `baseline_command` that runs the whole suite, without writing it |
| `a2dlc check [--base <ref>] [--peer <ref>]` | Differential + test gates → `.a2dlc/verdict.json` |
| `a2dlc verify [--commit HEAD] [--strict]` | <50 ms, the `pre-push`: blocks only on evidence about the pushed tree (a `FAIL`, an unsigned waiver of one, a protected path without approval, `critical_code` left unjudged); no receipt, a stale one or `INCONCLUSIVE` only warn, because on a repo with history "could not judge" is the normal case and a gate that blocks on it gets bypassed. `--strict`: receipt is `PASS`, matches `tree_hash`, waivers signed |
| `a2dlc approve <file>::<symbol> --key <key>` | Sign an intentional behavior change (`ssh-keygen -Y`) |
| `a2dlc run --slice <id> --intent <file>` | Full Protocol Mode, Stages −1 to 7 |
| `a2dlc plan propose\|import\|check\|run\|replan` | Feature plans (RFC-0008, Draft): a planner role or a Spec Kit / Task Master task list proposes slices, deterministic gates judge them, `plan run` runs them in dependency order, side by side when their files are disjoint, as many at once as the host and the Docker daemon hold ([docs/PLAN_MACHINE.md](docs/PLAN_MACHINE.md)) |
| `a2dlc verify-run --slice <id>` | Verify the hash-chained container ledger |
| `a2dlc answer --slice <id> --probe <id> --choice alpha` | Answer a human probe and resume |
| `a2dlc propose --from-escalations --out <empty dir outside the repo>` | No role call: the counterexamples that earlier runs left in `.a2dlc/escalations.json` and `.a2dlc/run/*/escalations.json` become properties for the next intent, each with a probe (the arguments and the base's result), in `proposals.json` ([schema](contracts/proposals.schema.json)). Deduplicated by the hash of target, args and old output, against `.a2dlc/reports/proposals.jsonl`; a target waived with `enforce_new_behavior` is left out. It proposes only and never edits an intent. Implemented, not measured |
| `a2dlc mcp` | Built-in MCP server (JSON-RPC 2.0 over stdio) |

`a2dlc` (`cmd/a2dlc`) is both the clean-room protocol orchestrator (`a2dlc run`, Stages −1 to 7) and the differential verification gate (`a2dlc check` / `a2dlc verify`), governed by the JSON Schema contracts under `contracts/` and RFC-0001.

<details>
<summary><b>Enterprise: Jira · Linear · GitHub · GitLab · Confluence · Slack via MCP / Composio</b></summary>

The engines never touch the network, but the protocol plugs into your SDLC through MCP connectors and CI:

```
ticket / PRD ──► a2dlc_intent_from_ticket ──► intent.json (Stage 0)
                        │
                 a2dlc run / check  (clean-room, --network none)
                        │
             a2dlc_format_report ──► PR / MR / Jira / Linear / Confluence comment
                        │
       CI: a2dlc verify --commit HEAD ──► ISO 42001 / EU AI Act audit package
                        │
   canary alert ──► a2dlc_intent_from_signal ──► bugfix intent, same invariants & budget cap
```

A CI template lives in [`templates/ci/`](templates/ci). Example multi-server MCP config:

```json
{ "mcpServers": {
    "a2dlc":    { "command": "a2dlc", "args": ["mcp"] },
    "composio": { "command": "npx", "args": ["-y", "@composio/mcp@latest", "--apps", "jira,linear,github,gitlab,confluence,slack"] } } }
```
</details>

<details>
<summary><b>Optional companions (zero lock-in)</b></summary>

A2-DLC needs only the `a2dlc` binary, Git and Docker, and works with your existing Claude, Gemini or OpenRouter keys. Sibling projects connect through open JSON contracts and are strictly optional: [`Entropos`](https://github.com/falsable/Entropos) (entropy-based LLM routing), [`compiled-policies`](https://github.com/falsable/compiled-policies) (Z3 business-policy compilation), `ChronosGraph` (cross-repo AST graphs, public soon).
</details>

---

## Docs

- [Protocol specification — RFC-0001 v3.5](docs/A2-DLC_v3.5_protocol.md)
- [Coverage matrix, stage reference, deviations & benchmarks](docs/COVERAGE_AND_BENCHMARKS.md)
- [Agent skill](skills/a2dlc/SKILL.md) · [JSON Schema contracts](contracts)
- [Quickstart example](examples/quickstart)

## Contributing

Bug reports with a frozen mutant are the most valuable thing you can send: see [`e2e/testdata/`](e2e/testdata) for the format. Engines for new languages implement the contract in [`contracts/`](contracts) — Rust and Java are the most requested. Open an issue if you want to add one.

Project policies: [CONTRIBUTING](CONTRIBUTING.md) · [GOVERNANCE](GOVERNANCE.md) · [SECURITY](SECURITY.md) · [Compatibility](docs/COMPATIBILITY.md) · [Name and conformance](docs/NAME_AND_CONFORMANCE.md) · [Conformance kit](conformance/README.md) · [RFCs](rfcs/README.md)

## License

[Apache-2.0](LICENSE) · © 2026 Aldemar Hernández · [falsable](https://github.com/falsable)
