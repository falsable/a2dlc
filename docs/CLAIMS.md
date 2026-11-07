# Claims and the gates that back them

A2-DLC makes four claims. Each one is backed by a numbered gate with a reproducible measurement, or it is not made. The README states only what a gate below has passed; when a gate changes status, this file changes first and the README follows. Tracker: epic `a2dlc-pe6` (`bd show a2dlc-pe6`).

Decision of 2026-11-06. Source: a mechanism-level comparison of A2-DLC `0254043`, AI-DLC `awslabs/aidlc-workflows@11daa8ff` (main, 2026-11-06, MIT-0) and Entropos `2f82218`, read from code and reference docs, not from READMEs.

## Scope of the claims

From a signal (ticket, issue, alert) to a verified diff plus a `delivery.json`. A2-DLC does not deploy; the platform executes the plan. A2-DLC orchestrates its own roles (Specifiers, Builders, Saboteur, Planner); Hermes, CI and coding agents are clients, not a second orchestrator.

## The four gates

| Gate | Claim | Status | Number published | Reproduce | Bead |
|---|---|---|---|---|---|
| P1 | **Verified quality** — a `PASS` means the oracles ran on this tree; outside the declared scope the verdict is `INCONCLUSIVE`, never `PASS` | OPEN | 0 false `PASS` on frozen cases + SWE-ABS after closing `fjl`/`5fq`/`zs7`/`aln`; false-positive rate over ≥ 50 real commits from ≥ 2 repos | — | `a2dlc-pe6.1` |
| P2 | **Autonomous** — tickets reach `PASS` with zero human turns when the change class is pre-authorized | OPEN | ≥ 10 real tickets through `a2dlc_intent_from_ticket` with 0 human turns; "human stops per ticket" with escalation `kind` breakdown | — | `a2dlc-pe6.2` |
| P3 | **Faster** — fewer human reviews and reverts per verified slice, not fewer seconds per commit (A2-DLC always adds seconds) | OPEN | `check` p95 ≤ 60 s (3 reps, median + max; max > 120 s ⇒ async receipt) on ≥ 20 commits; before/after ≥ 2 weeks each on a ~4 commits/day repo: verified slices/day and revert rate | — | `a2dlc-pe6.3` |
| P4 | **Agent-oriented** — every interaction is a versioned contract (JSON, exit codes, MCP), none requires a chat turn | OPEN | One Hermes kanban worker completes ticket → `a2dlc run` → `PASS` → push using only MCP/CLI, with ledger and transcript in `e2e/`; `escalations.schema.json` and the exit-code table validated by the conformance kit | — | `a2dlc-pe6.4` |

Order: P1 → P2 → P4 → P3. P1 first because a false `PASS` voids the other three. P3 is measured last, once P1 and P2 are stable; its latency work can run in parallel.

## README sentences and the gate behind each

| README sentence | Gate | Current evidence |
|---|---|---|
| SWE-bench Verified: 38 real agent patches, 18/18 rejected caught with peer | P1 | `docs/COVERAGE_AND_BENCHMARKS.md`, frozen fixtures pinned by SHA-256 |
| TypeScript engine 8/8, black-box 4/4 behavior-changing mutants (`mutantes-bb-2`, 2026-11-07; the fifth is equivalent under the surface's schema), test & property gates 12/12, self-verification 3/5 vs `check` | P1 | `e2e/testdata/`, seeds 1–3 |
| Greenfield slice converges in 84–127 s at $0.025–$0.069 | P3 (cost/latency of `run`, not of `check`) | measured run, see README §Measured |
| `a2dlc verify` runs no engine and no Docker at `pre-push` | P4 | `internal/app` verify path; no latency figure is published until P3 measures it (one Windows sample on 2026-11-07: about 1 s wall with the unsigned dev build) |
| "How it compares" table, AI-DLC column | — (descriptive, read from source at 11daa8ff) | this document, §What is not adopted |

Sentences that are **not** in the README and must not be added until their gate closes ("Autonomous" in the protocol's name, the expansion of A2, is a name and not this claim): "autonomous", "faster development", "increases velocity", "agent-native orchestration of the SDLC".

## What A2-DLC adopts from AI-DLC, and what it does not

AI-DLC closes its gates with a human turn: the approval gate requires a `HUMAN_TURN` audit event after the last gate (`core/tools/aidlc-audit.ts`), the learnings ritual asks the person whether to keep a correction (`core/tools/aidlc-learnings.ts`), and the interface is chat plus `AskUserQuestion`. A2-DLC closes its gates with an oracle or a signature. That is the only sense in which A2-DLC is an evolution of AI-DLC, and the four gates measure exactly that.

Adopted, in A2-DLC form:

- **Learning loop** (AI-DLC `aidlc-learnings.ts surface/persist`): A2-DLC learns from executed counterexamples instead of human corrections. `a2dlc propose --from-escalations` (`a2dlc-pe6.2.3`).
- **Doctor / version skew** (AI-DLC `aidlc doctor`, install lifecycle): `a2dlc doctor --json` (`a2dlc-pe6.4.3`).
- **Machine-readable audit vocabulary** (AI-DLC 133-event taxonomy): `contracts/escalations.schema.json` and a versioned exit-code table (`a2dlc-pe6.4.2`).
- **Pre-authorization** (AI-DLC approval gates, inverted): RFC-0007 B signed `authorizations.json` so the run does not stop for covered change classes (`a2dlc-pe6.2.1`); RFC-0008 §4 plan signature (`a2dlc-pe6.2.2`).

Not adopted, on purpose: ideation and inception stages (intent capture, requirements, user stories, domain design), the 11 workflow profiles, the 14 domain agents, multi-chat sessions, and the user-guide corpus. They are the human-facing lifecycle. Adding them would dilute the one property A2-DLC has and AI-DLC does not, and would repeat the pattern where tooling consumes the product.

## Entropos

Entropos is the model-routing and cost layer of P3. It counts toward a gate only once the A2-DLC adapter (a) requires `x-entropos-routed-model` and enforces the α≢β family rule on the resolved model, not on the declared label, and (b) posts the role's `PASS`/`FAIL` back to `/api/feedback` so the Thompson loop closes on real signal (`a2dlc-pe6.3.1`). Until then Entropos's own offline eval (`eval/results/router_noise_2026-11-06.json`: 17/20 dev and 19/20 held-out prompts change model across reruns, under-route 26 % → 52 % on held-out) says its routing is noise, and no cost or quality number from it is cited here.
