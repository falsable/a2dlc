# RFC-0001: Autonomous Agentic Development Lifecycle (A2-DLC) v3.4

**Status:** Consolidated draft (v3.4) — pending empirical validation (see §12); the Lite differential engine has a smoke run (§12, Phase 0). Of what was added in v3.4, the domain guard has a first measurement (§12); the lemmas and the back-translation are unmeasured.
**Target environment:** multi-agent software engineering systems
**Design goal:** minimal human intervention through deterministic verification, adversarial testing with context isolation and differential probing of the specification.

---

## 1. Abstract

Classic engineering frameworks (Agile, Scrum, Clean Architecture, DRY) and first-generation AI lifecycles with human gates (AI-DLC) are optimized for human constraints: limited working memory, slow reading and writing, high cost of rewriting. Applied to agents, they create bottlenecks: sequential human approvals over plans and code, and deep abstraction hierarchies that fragment the context.

**A2-DLC** replaces human review of plans and code with a **deterministic state machine**. The human intervenes in only two places:

1. **Intent & Boundary Disambiguation**: decides behavior from concrete counterexamples, never by reading specs or code.
2. **Circuit Breakers**: deterministic escalation conditions (§7).

Correctness is pursued through clean-room context isolation, differential execution between model families (or against the legacy as oracle), property-based testing (PBT), coverage-guided fuzzing and mutation testing scoped to the diff. None of these is a proof: a `PASS` is a bounded search within a recorded budget (§6.4), and the residual risks are listed in §10.

<div align="center">

<img src="assets/protocol-architecture.svg" alt="A2-DLC Protocol Architecture & Clean-Room State Machine (RFC-0001 v3.4)" width="100%" />

</div>

---

## 2. Principles

1. **Locality of Behavior > DRY** (new slices only). Code is organized into self-contained **Vertical Slices**: schemas, logic, data access and rationale. Duplicating between slices is preferred over cross-module abstractions. Not imposed on legacy (§4.3).
2. **Zero Indirection** (new slices). No DI containers, deep inheritance or reflection. Explicit flows `State In -> State Out`. Side effects (clock, network, DB) are injected.
3. **Clean-Room Context Isolation.** Builders and Saboteurs never share reasoning traces, chain-of-thought or source code.
4. **Concrete Boundary Probing.** Ambiguities reach the human only as concrete input/output pairs in multiple-choice form.
5. **Co-located, machine-readable rationale.** ADRs live as `@a2dlc:*` headers, validated by a linter, inside each slice.
6. **Determinism in the gates.** Anything a deterministic tool can verify (compiler, Z3, fuzzer, mutants, linters, codemods) is not decided by an LLM.
7. **Roles, not models.** Contracts declare roles and orthogonal families; a router resolves the concrete models at runtime.

---

## 3. Roles and isolation boundaries

| Role | Sees | Produces | Forbidden |
|---|---|---|---|
| **Specifier α / β** (distinct families) | Intent + upstream/downstream contracts from the graph | Candidate contracts `S_α`, `S_β` | Internals of other slices |
| **Inquisitor** | `S_α`, `S_β` + domain assumption catalog + back-translation discrepancies | Concrete boundary probes (3–5) | Builder code, Saboteur tests |
| **Back-translator** (family distinct from the Specifiers) | Only `S*` and `π_S` | Concrete natural-language claims of what the contract allows and forbids | The Intent, the Specifiers' reasoning, the code |
| **Complexity Router** | `S*` + dependency graph | Track `N=1` / `N=2` | — (deterministic or lightweight classifier) |
| **Builder(s)** | `S*` + locked probes + Tier 0 feedback | Slice + `@a2dlc` header | Saboteur suite, workspaces of other Builders |
| **Saboteur** | `S*` + public signatures | PBT generators + invariants | Builder code and reasoning |
| **Deterministic Oracle** | Binary/AST + PBT suite (+ legacy in refactor mode) | Pass/fail, coverage, mutant telemetry | Cannot modify spec or implementation |
| **Code Graph Indexer** | Full repo + service contracts | Dependency graph and blast radius | — |

---

## 4. Modes of operation

| Stage | Greenfield / feature | Large refactor | Messy legacy |
|---|---|---|---|
| **−1 Make It Testable** | Omitted | **Mandatory if the legacy is not hermetic** | Mandatory |
| **1 Spec** | Intent + dual-spec + probes | Behavioral capture (golden master) | Behavioral capture at the module boundary |
| **1.5 Validation** | Lint + Z3 + π_S + back-translation | π_S + back-translation of its relaxations + trace coverage gate | Lint + seam verification |
| **2–3 Build** | Builders (N=1/N=2) + Saboteur | Refactor Builder | Slice extraction via Strangler Fig |
| **4 Tier 1** | PBT + fuzzing + cross-model differential | **Differential vs legacy oracle** | Differential vs golden master |
| **5 Tier 2** | Mutation + selection | Mutation scoped to the diff | Mutation scoped to the diff |
| **6 Integration** | Consumer matrix + migrations | Blast radius + consumer matrix | Consumer matrix |
| **7 Delivery** | Canary rings + rollback sentinel | Production parity (read-only) + canary | Canary + rollback sentinel |

### 4.1 Greenfield / new features
Full flow of §5.

### 4.2 Large refactor: legacy as oracle
In a refactor the spec is the current behavior. The legacy acts as a free oracle:
- **Stage 1** captures behavior (golden master, I/O traces + state mutations), instead of starting from an abstract spec.
- **Stage 4** compares `legacy(x) ≡_S refactor(x)` over captured traces + PBT inputs.
- **Bug-or-Feature probes:** when the legacy does something that looks like a bug (e.g. it swallows negative timestamps), the human is asked whether to preserve it. Divergences **are grouped by root cause** before being shown, so as not to flood the human.

### 4.3 Legacy / brownfield: explicit scope
- The protocol covers **new slices** that interact with the legacy through **Brownfield Boundary Adapters** (read-only facades, `@a2dlc:mode = "brownfield_facade"`) and VCR/Pact fixtures.
- **Changes inside the legacy** (internal bug fixes) are outside the autonomous flow unless explicitly scoped. In that case refactor mode (§4.2) applies after Stage −1. Otherwise they follow the conventional flow.
- VCR/Pact fixtures drift over time: they must be re-recorded periodically and versioned together with the contract.

### 4.4 Lite mode: entry point
A minimal subset, adoptable in minutes, that requires no orchestrator. Inspired by goalie.
- **What it does:** extracts the changed functions/modules from the git diff, generates inputs (PBT) and compares `old(x) ≡_S new(x)`. Optionally runs property tests that are already committed.
- **Integration with the agent:** a **gate run by the harness** (a hook or a loop script, not the agent) runs the diff-test and reads its exit code; the task is not closed without `PASS`. A line in `AGENTS.md` / `CLAUDE.md` only documents the gate: a written rule does not govern the agent's behavior, and the agent cannot be the one who declares the verdict.
- **Output:** JSON verdict (§6.4) with three values, `PASS`, `FAIL` and `INCONCLUSIVE`, which the agent loop can consume directly. Reproducible by seed in the random engines (Hypothesis); in the symbolic ones (CrossHair) the search budget is recorded.
- **Human decisions:** intentional behavior changes are recorded as **versioned waives** in the repo (§6.4). They are the minimal form of the bug-or-feature probes.
- **Protected zones:** a separate `tests/human` suite for modules owned by the human, which the agent cannot modify. Lightweight equivalent of `do_not_refactor`.
- **LLM:** only to propose properties, once; they are written as plain code that the human reviews and commits. Execution is deterministic from then on.
- **Engines:** they are not reimplemented or instrumented internally (e.g. a path pruning guided by a model inside the CrossHair/Z3 loop is out of scope: it breaks the rule of §9; the equivalent, clean option is the domain guard below, which acts before the engine). In Python, the main engine is `crosshair diffbehavior` (differential by symbolic execution). `hypothesis write --equivalent` (ghostwriter) is only a complement, and with three measured conditions (§12, Phase 0): typed functions, `max_examples ≥ 2000` (with the default 100 it detected none of 2 broken refactors) and a patch for when the two functions are called the same way (it generates `result_f_mod.sub = …`, which is not a valid identifier; it is fixed with the regex `\b(result_\w+)\.(\w+)\b → \1_\2`).
- **CrossHair budget:** the time is set by the budget, not by the verdict. In a stdlib-only function the time was the budget; in a real module with dependencies it was about 4 times more (~45 s with a 10 s budget; §12). The total budget of the diff is computed with times measured in the repo, not with the `per_condition_timeout`.
  - Measured on 2026-10-15 (§12, Phase 0): with `--per_condition_timeout 10` both `PASS` and `FAIL` took ~10.5 s, and with 60 s, ~61 s.
  - The default budget (5 iterations without progress, about 2–4 s) gave a **false PASS** in 1 of 2 broken refactors. With 10 s it detected both and gave no false positives on the equivalent ones.
  - Rules:
    - Minimum 10 s per function.
    - Total budget per diff, declared in the verdict.
    - Functions in parallel (one per core).
    - If the diff does not fit, the functions with the most callers or with a criticality tag are prioritized, and the ones not run are listed as `INCONCLUSIVE`, never omitted.
  - A 30-function diff in a real module (~45 s each with a 10 s budget, §12) is ~22 min sequentially, or ~3 min with 8 cores. With stdlib-only functions (~10.5 s) it was ~5 min and ~40 s. The symbolic differential fits in the 15–45 s of Tier 1 only with one or a few functions per core. It normally runs as an asynchronous Tier 2 (1–3 min), and a large diff may also exceed that tier.
- **Domain guard before CrossHair (v3.4, unmeasured):** it concentrates the budget on the inputs where a difference may exist, without touching the engine.
  - **What it is:** a wrapper of each version, `base_g(x)` and `new_g(x)`, whose first line is `assert guard(x)` and which then calls the real function. Outside the guard both raise the same exception, so there is no difference to look for there. **Measured (2026-10-16):** `diffbehavior` treats an initial `assert` as behavior, not as a precondition (3/3: `assert x > 0` versus `raise ValueError` gave a difference at `x=0`). The guard does not make CrossHair skip those inputs; it works because the two versions fail the same way outside it.
  - **Where the guard comes from, and only from there:**
    - The declared domain: types, ranges and preconditions of `S*` or of `tests/human`. It is sound by contract.
    - A mechanical over-approximation, by AST and without an LLM, of the set of inputs that reach a changed line **in either of the two versions**. Example: the diff only touches a branch whose condition depends on parameters with no assignments before it; the guard is that condition, joined (OR) with that of the other version.
  - **Opposite case, which prevails:** if the diff touches code that runs before any branch (an assignment or a normalization at the start), all inputs reach the change and there is no guard. If the analysis cannot guarantee the over-approximation, there is no guard either. A guard that excludes an input where the versions differ turns a `FAIL` into a false `PASS`: when in doubt, no guard.
  - `--exception_equivalence` is not changed to `ALL` to make the guard work: it would hide differences between different exceptions.
  - The verdict records the guard and its origin (`domain_guard`, §6.4). A `PASS` with a guard says "no differences within this domain with this budget".
  - **Adopted only if Phase 0 justifies it** (§12): with the same budget, with and without the guard, over known mutants. If it gives a single false `PASS` that without the guard was not given, it is removed. First measurement in §12: in 1 of 2 mutants, with 3 s, it detected what without the guard needed 10 s; in the other nothing changed. It does not save wall-clock time, which remains the budget.
- **Old tree:** the old version is imported from a clean checkout of the base under **another package name**. In containers, a full clone or `git archive <sha>`, not a `git worktree` (its `.git` points to a host path). The repo's dependencies are installed in the same environment that runs the engine. Measured in §12 (2026-10-16):
  - Absolute imports are rewritten to the new name, including lazy ones inside functions; renaming only the folder silently mixes trees.
  - `PYTHONPATH` carries only the trees under test; the repo image carries its own copy of the package.
  - Modules are imported before starting the engine, because CrossHair blocks effects at import time.
  - `--unblock` is never used with an effect that is not read-only. If it still fails to load, the result is `INCONCLUSIVE` for "side effects at import".
- **Limits:**
  - "Pure-ish" functions without side effects (CrossHair blocks them by default), without sandbox or blast radius. For everything else, the full flow (§5).
  - **Mandatory types.** Without annotations, CrossHair invents the argument's type and returns counterexamples outside the contract (measured: it took a `list[str]` as `str`). A function without types gives `INCONCLUSIVE`, never `FAIL` or `PASS`.
  - **No Saboteur.** In Lite the tests that exist were written by the same agent that implements; the differential covers refactors, not the misunderstanding of the spec in a new feature (§10).

**Lite → full escalation path:** Lite (diff-test) → + NOP gate and baseline (§5, Stage 4) → + mutation scoped to the diff → + hermetic sandbox → + Stage −1 (seams) → + `π_S` and probes → + multi-language and blast radius across services → + delivery (Stages 6–7).

---

## 5. State machine

```
[−1 Make It Testable] ──(failure/timeout)──► CB-05 PREREQUISITE_FAILURE
        │
[0 Intent & Budget]
        │
[1 Spec: Dual-Spec + Probes | Behavioral Capture] ──(ambiguity)──► [Human answers probes]
        │                                                                   │
[1.5 Contract Lint + Z3 + π_S + Back-translation] ◄─────────────────────────┘
        │  (UNSAT → CB-02a) (UNKNOWN → mark invariants as runtime-only)
[2 Complexity & Risk Routing] ── N=1 Standard | N=2 Cross-Model Critical
        │
[3 Tier 0 Inner Loop (<2s)]
        │
[4 Tier 1 Adversarial & Differential (15–45s)] ──(valid divergence)──► CB-01
        │
[5 Tier 2 Diff-Mutation & Selection (1–3 min)]
        │
[6 Integration: Consumer Matrix + Migrations]
        │
[7 Progressive Delivery + Rollback Sentinel + Compensation]
```

### Stage −1: Make It Testable (refactor and legacy)
Mandatory order, because the seaming is itself a refactor and needs a safety net:
1. **Black-box golden master baseline** at the module boundary, *before* touching anything.
2. **Hermetic build** (Docker/Nix), reproducible and local.
3. **Seaming with deterministic codemods** (OpenRewrite, LibCST, jscodeshift, ast-grep), which preserve behavior by construction. Singletons, clock, network and DB are wrapped behind interfaces (`Clock`, `DatabaseGateway`). **No LLMs are used to rewrite here.**
4. **Re-run the baseline**: it must pass identically.

If a hermetic build and a green baseline are not achieved within the timeout, **CB-05 PREREQUISITE_FAILURE** fires. This stage is semi-manual and is usually most of the work.

### Stage 0: Intent, invariants and budget
The human provides the goal in natural language, the SLAs (e.g. `p99 < 25 ms`), the known business invariants and the maximum budget (USD/tokens) per slice.

### Stage 1: Specification

**Greenfield: Dual-Specifier Divergence**
1. Specifier α (family A) and Specifier β (family B ≠ A) translate the intent into typed schemas (Pydantic, Zod, Rust types) with pre/post-conditions.
2. The Inquisitor generates boundary inputs and compares the validation and transition results of `S_α` and `S_β`.
3. **If they diverge:** concrete multiple-choice probes.
   > `transfer(amount=0, currency='EUR')`: α → `InvalidAmountError`; β → idempotent no-op. [A] Reject / [B] No-op?
4. **Even if they do not diverge:** 1–2 **mandatory Consensus Probes**. Agreement ≠ correctness, because both families may share a bias. The probes are chosen from a **deterministic catalog of per-domain assumptions**, not at the LLM's discretion. Examples from the catalog: rounding mode, time zone and day boundaries, replay window and idempotency, empty-list semantics, unicode and normalization, numeric limits.
   - **The catalog takes precedence over the LLM.** The LLM only instantiates concrete examples of each category for this slice (the exact input, the two possible results). It does not choose which categories apply, nor add categories.
   - A counterexample search generated by the LLM from the Intent is a complement, not a replacement: the Intent is natural language, there is nothing to formally refute it against, and the LLM searches within its own distribution.
   - **Opposite case:** a domain with no catalog yet. The categories the LLM proposes go to the human to enter the catalog; until they enter, they generate no probes.
5. The answers are compiled into immutable assertions inside the **Locked Contract `S*`**.

**Refactor / legacy: Behavioral Capture**
- Recording of I/O traces + state mutations (VCR/Pact, traffic recording, PBT against the legacy).
- Real traffic must be anonymized (PII) before being persisted.

### Stage 1.5: Static validation of the contract
1. **`a2dlc-lint`** (deterministic): every error referenced exists in `error_taxonomy`, the schema refs resolve, the invariants reference valid state fields, and the probes are consistent with the taxonomy.
2. **Satisfiability with Z3 (SMT)** over the **invariant DSL** (§6.2). Three results:
   - `SAT`: continue; any later failure of the Builders is `CB-02b BUILDER_EXHAUSTION`.
   - `UNSAT`: `CB-02a UNSAT_SPEC` before spending Builder tokens, with the unsat core as diagnosis.
   - `UNKNOWN` (outside the decidable fragment: quantifiers over collections, idempotency, temporal invariants): the invariant is marked `verification: runtime_and_pbt` and covered only by PBT and runtime assertions.
3. **Equivalence relation `π_S`** (required for the differential):
   - **Strict by default.** Each relaxation (ignored field, set semantics, tolerance ε, comparing `error_code` instead of the message) is explicit and **is validated with a human probe**. This prevents over-masking from silently turning bugs into passes.
4. **Coverage gate (refactor/legacy):** the trace set must reach a branch-coverage threshold of the legacy in the affected area. Parity over traces ≠ equivalence: real traffic does not cover the rare paths.
5. **Blind back-translation (v3.4, unmeasured):** defense against the formalization that silently gets it wrong, and against the over-masking of `π_S`.
   - The **Back-translator** receives only `S*` and `π_S` (§3), without the Intent, and writes concrete claims of what the contract allows and forbids. Example: "two receipts with different `trace_id` count as identical; there are never negative balances".
   - A comparator (another agent with the Intent and the claims, or the human) flags what the reverse translation omitted, added or distorted with respect to the Intent.
   - **A discrepancy opens a probe:** the Inquisitor turns it into a concrete input with its two possible results (§2.4), and it is grouped by root cause like the CB-01 ones. It does not block by itself, nor is it corrected automatically: the contract or the reverse translation may be wrong.
   - **Distinct family** from the Specifiers': if it shares a family, it shares the bias and the reverse translation confirms the error. If no third family is available, this is recorded and the signal is worth less.
   - **A translation that matches proves nothing:** agreement ≠ correctness (§10.1).
   - **By mode:**
     - Greenfield: over the full `S*` and `π_S`.
     - Refactor/legacy: there is no written Intent (the contract comes out of the recorded behavior). It applies only to the relaxations of `π_S`, to write the human probe that step 3 already requires.
     - Lite: there is no `S*`; it does not apply.

### Stage 2: Complexity & Risk Routing
The Router evaluates the depth of state mutation, the estimated complexity of the rules and the criticality tag (security, billing, auth, concurrency).
- **Standard (N=1):** CRUD, UI, transformations.
- **Critical (N=2 cross-model):** Primary Builder (family A, state-machine/imperative paradigm) + orthogonal Builder (family B, functional/immutable paradigm). Reduces, without eliminating, correlated failures (Knight & Leveson).
- In refactor mode, the second "builder" is the legacy itself.
- **Lemmas in Critical slices (v3.4, unmeasured).** The Builder structures the slice as internal pure functions, each with its declared postcondition (icontract `ensure` or a final assert); these are the slice's "lemmas". They serve to locate the failure when the main function gives `FAIL`, `INCONCLUSIVE` or timeout in Tier 1:
  - Each lemma is verified in isolation: `crosshair check` over its postcondition and, in refactor, `diffbehavior` against the lemma of the same name and signature in the base.
  - **Opposite case, Standard slices:** not required. In a CRUD the decomposition is cost without signal.
  - **A lemma that passes does not raise the parent's verdict.** That the lemmas pass separately does not prove that their composition is equivalent, and the differential is already bounded search (§10.8). The parent stays `INCONCLUSIVE`, with the lemma verdicts attached.
  - **A lemma that fails:**
    - `crosshair check` violates its postcondition: the postcondition belongs to the Builder, not to `S*` (which was closed in Stage 1). The failure goes back to the Builder as feedback, like a Tier 0 one: either the lemma or the code is wrong, and it is not a contract `FAIL`.
    - In refactor, `diffbehavior` of the lemma against the base finds a difference: it is a candidate counterexample. It is tested on the main function; if it reproduces, the normal path follows (CB-01 or a bug-or-feature probe). If it does not reproduce, it goes as a probe, because the caller may mask it or the parent's search did not reach that input.
  - **Refactor that changes the decomposition** (lemmas renamed, merged or split): without a pair in the base there is no per-lemma differential, and that lemma gives `INCONCLUSIVE`, not `PASS`.
  - The Saboteur keeps attacking the public function as a black box: it does not see the lemmas, which are Builder code.

### Stage 3: Tier 0 Inner Loop (< 2 s)
In a hermetic sandbox per Builder:
1. Compiler + strict type check, zero warnings (`cargo check`, `tsc --strict`, `pyright --strict`).
2. AST policy linter: forbidden imports, unbounded loops, reflection, missing `@a2dlc` header, complexity budget.
3. Locked boundary assertions (Stage 1 probes).

The structured feedback (JSON) returns to the Builder; maximum `max_inner_iterations = 5`.

### Stage 4: Tier 1 Adversarial & Differential (15–45 s)
The Saboteur generates the PBT suite **in parallel** with the Builder, without seeing its code.
0. **Cheap gates before the expensive ones:**
   - **NOP/Oracle gate** (features over existing code and bug fixes; in pure greenfield it holds trivially): the new or modified tests are run alone on a clean checkout of the base and **must fail**; then, on the candidate, **must pass**. A test that passes without the implementation does not prove the change. It is the case of the single mutant (revert the entire diff) of Stage 5, at a fraction of the cost.
     - **The failure on the base must be the test's, not the collection's.** A collection or import error, or an `ImportError` / `AttributeError` / `NameError` on the new symbol, counts as a false holding: the function does not exist yet.
       - When the symbol is new, the base receives a stub with its signature that raises `NotImplementedError`.
       - **Valid failures on the base:** an assertion, the `NotImplementedError` raised by the stub, or a domain exception (the crash that a bug fix repairs).
       - **Invalid failures:** a collection or import error, and an `ImportError` / `AttributeError` / `NameError`.
       - Without a stub, the result is `INCONCLUSIVE`, not `PASS`.
       - A test that passes against the stub (e.g. `pytest.raises(Exception)`, which also catches `NotImplementedError`) is refuted by the gate: it does not distinguish the stub from the implementation. It is the gate working, not a false positive.
   - **No regression against baseline:** the existing suite is run once per base commit and saved. "Passing" means **does not get worse**: a new red blocks, a pre-existing red does not. If the parser identifies no failing test in a red run, the result is `INCONCLUSIVE`, not `PASS`. The baseline expires when the base commit changes; an old one turns a new red into "pre-existing".
     - **Flaky tests:** before blocking, each new red is re-run k times (k = 3) on the base and on the candidate.
       - **It is flaky only if it is also intermittent on the base:** it passes and fails across the k runs on the base. Then it goes to a versioned quarantine (`.a2dlc/flaky.yaml`, with date and owner) and does not block.
       - **Stable green on the base and red on the candidate blocks, even if the red is intermittent.** An intermittent failure only on the candidate may be a race condition introduced by the change.
       - A test in quarantine counts neither for `PASS` nor for `FAIL`, and the quarantine is reviewed like the waives.
   - If a cheap gate fails, the following ones are not paid for.
1. **Property invariants** (1k–5k inputs): idempotency, conservation, round-trip, metamorphic relations.
2. **Deterministic coverage-guided fuzzing** (Atheris, cargo-fuzz, Jazzer), about 10 s.
3. **Differential:**
   - N=2: same inputs to both Builders, compared with `≡_S`.
   - Refactor: `legacy(x) ≡_S refactor(x)`.
   - If a candidate crashes or violates `S*`, it is discarded.
   - If both satisfy `S*` but are not `≡_S`, the spec is under-specified and **CB-01** fires with the minimal (shrunk) counterexample. In refactor a *bug-or-feature probe* fires, grouped by root cause.

### Stage 5: Tier 2 Diff-Mutation and selection (1–3 min, async)
1. **Mutation testing scoped to the diff** (`cargo-mutants --in-diff`, `Stryker --since`, `mutmut` over the diff lines).
2. **Pruning of equivalent mutants** (AST analyzer: logging, branches dead by type guarantee).
3. **Gate on the suite, not on the candidate:** the Saboteur's suite must kill ≥ 80% of the non-equivalent mutants. If it does not reach that, the surviving mutants (without implementation comments) are returned to the Saboteur to strengthen its generators.
4. **Selection (N=2), lexicographic, Occam-style:** among the candidates that pass 100% of the gates,
   `Winner = argmin( w1·CognitiveComplexity + w2·ASTNodeCount + w3·ExternalImports )`.
   The MutationKillRate is not used (it measures the tests, not the code), nor the microbenchmark speed (noise).

### Stage 6: Integration
1. **Blast radius via Code Graph Indexer:**
   - Within the code: Tree-sitter + SCIP, with graph storage (SQLite/DuckDB/Neo4j).
   - **Across services:** the HTTP/gRPC/queue edges between microservices in different languages do not appear in SCIP. They are extracted from contracts (OpenAPI, protobuf, topic and event schemas). In polyglot repos is where most of the blast radius lies.
2. **Consumer-Driven Contract Matrix:** if the output schema of slice A changes, the Tier 1 suites and the Pact contracts of all consumers (B, C…) are run against the candidate. If a consumer breaks, the candidate is rejected with its logs.
3. **DB migrations, expand-and-contract:**
   - Destructive DDL is forbidden (`DROP COLUMN`, `RENAME`, in-place type change). It is validated with a migration linter (squawk, atlas) against an anonymized snapshot.
   - Expand → dual-write → backfill → contract **spans several releases**. It is managed by a **long-lived durable workflow** (checkpointed) that knows the state of each migration and triggers the *contract* only when its preconditions hold (no reads of the old field for N days, verified backfill).

### Stage 7: Progressive Delivery
1. Feature flag + canary rings (1% → 10% → 100%).
2. **Rollback Sentinel** over telemetry: `error_rate_delta`, `latency_p99` and runtime invariant assertions. Expensive invariants (e.g. global conservation) are verified with **periodic reconciliation**, not per request.
3. **Rollback ≠ undo.** Slices with writes must declare a **compensation strategy** (compensating events, a data repair script, or tagging records with the slice version to isolate them). Without it, the slice cannot move past the 1% ring.
4. **Production parity (refactor):** dual legacy/new execution in the style of GitHub Scientist, **only on read-only paths**. On paths with writes, canary with compensation is used.

### Checks: pluggable deterministic gates (Lite and full)

The protocol judges behavior. What a repo also wants judged deterministically and has a tool for (a linter, a type
checker, a policy or secrets scanner, another framework's sensors) is declared in `config.checks` and run by the gate
under the same discipline as an engine; the core knows no tool, only the contract (`check-response.schema.json`).

- **Declaration:** `{id, image, command, severity, fire_on?, matches?, timeout_s?, memory?, cpus?}`. `severity` is
  `advisory` or `blocking` and has **no default**: the repo says what a finding costs. `fire_on` is `check` (the
  pre-push gate, the default) or `stage:<id>` of `a2dlc run`; `matches` are globs over the changed files, absent
  means every changed file.
- **Execution:** the image runs with `--network none`, the base at `/base:ro`, the candidate at `/cand:ro` (the cwd),
  the matched changed files in `$A2DLC_CHANGED` and the response file to write in `$A2DLC_OUT`. There is no host
  command: what runs is the image. The response says `ran` or `not_applicable`, `pass`, and the findings, each bound to
  a file (`<file>::<check id>` or `<file>::L<n>`) with a message. A check never reports an error about itself.
- **Truth table, applied after the engines and the test gates:** a `blocking` check with an unwaived finding is
  `FAIL` (escalation `check_failed`); a `blocking` check in `error` (missing image, non-zero exit, missing or invalid
  response, a finding on a file it did not receive) or `timeout` is `INCONCLUSIVE` (escalation `tooling`, no waiver
  resolves it); an `advisory` check is recorded only, and its errors are recorded as errors, never as a pass; 0
  matching files is `not_applicable` with the count. **A check lowers the verdict, never raises it.** Everything that
  fired is in `verdict.checks`, in config order: what did not run stays in view.
- **Override:** a waiver with decision `accept_finding` on the finding's target, signed like any other (§6.4): the
  signature binds the file's blob, so it covers this content of the file and no future finding. `verify` runs no
  checks: it reads the receipt and refuses an unsigned `accept_finding` like any unsigned waiver.

Measured over the 9 frozen `gates-py` cases with one throwaway image (`e2e/checks/RESULTS.md`) and with the six
AI-DLC v2 sensors behind an adapter (`templates/checks/aidlc-sensor/`).

---

## 6. Artifacts

### 6.1 `slice_contract.yaml`

```yaml
protocol_version: "a2-dlc/3.3"
slice_id: "billing.ledger_settlement.v1"
mode: "greenfield"            # greenfield | refactor | brownfield_facade
criticality: "high"           # → N=2

routing:
  builders:
    - role: "builder_primary"
      family: "A"
      paradigm_constraint: "explicit_state_machine"
    - role: "builder_orthogonal"
      family: "B"             # must be != A
      paradigm_constraint: "immutable_functional_pipeline"
  saboteur:
    role: "saboteur"
    family: "C"               # preferably != A and != B
    isolation_level: "strict_blackbox"

budget:
  max_cost_usd: 1.50
  max_tier0_retries: 5
  max_tier1_adversarial_rounds: 3

io_contract:
  input_schema_ref: "./schemas/SettlementRequest.json"
  output_schema_ref: "./schemas/SettlementReceipt.json"
  error_taxonomy:
    - "InvalidAmountError"
    - "InsufficientFundsError"
    - "DuplicateTransactionIdError"
    - "CurrencyMismatchError"

capabilities:
  allowed_imports: ["std::collections", "decimal::Decimal", "internal::db::ledger_append"]
  forbidden_side_effects:
    - "network.http.outbound"
    - "fs.write"
    - "db.raw_sql_unparameterized"
    - "time.system_clock_direct"   # use the injected Clock

locked_boundary_probes:
  - probe_id: "BP-01"
    description: "Transfer with amount 0 is rejected"
    input: {tx_id: "tx_100", from_acct: "A1", to_acct: "A2", amount: "0.00", currency: "EUR"}
    expected_error: "InvalidAmountError"
  - probe_id: "BP-02"
    description: "Duplicate tx_id with identical payload returns the cached receipt; the duplicate adds no ledger entries"
    input_sequence:
      - {tx_id: "tx_101", from_acct: "A1", to_acct: "A2", amount: "50.00", currency: "EUR"}
      - {tx_id: "tx_101", from_acct: "A1", to_acct: "A2", amount: "50.00", currency: "EUR"}
    expected_invariant: "response[0] == response[1] && ledger_rows_added_total == 2"  # 2 = debit + credit of ONE transfer

invariants:
  - id: "INV-01"
    type: "conservation"
    expr: "sum(pre.accounts.balance) == sum(post.accounts.balance)"
    verification: "smt+pbt"
  - id: "INV-02"
    type: "non_negative_balance"
    expr: "forall a in post.accounts: a.balance >= 0"
    verification: "runtime_and_pbt"   # quantifier → Z3 may return UNKNOWN

equivalence:                 # π_S — strict by default
  ignore_fields: ["receipt.trace_id"]          # each entry validated by a human probe
  unordered_lists: []
  numeric_tolerance: null                       # exact Decimal
  compare_errors_by: "error_code"

delivery:
  compensation_strategy: "compensating_ledger_entry"
  rollout_rings: [0.01, 0.10, 1.0]
```

### 6.2 Invariant DSL
- Subset translatable to SMT: linear arithmetic (scaled integers/decimals), booleans, comparisons, sums over bounded collections.
- What falls outside (unbounded quantifiers, `f(f(x)) == f(x)`, temporal properties) is declared `verification: runtime_and_pbt` and does not block Stage 1.5.

### 6.3 Co-located ADR header `@a2dlc`

```python
# @a2dlc:slice_id = "billing.ledger_settlement.v1"
# @a2dlc:mode = "greenfield"
# @a2dlc:contract_hash = "sha256:…"
# @a2dlc:rationale = "Integer minor units (cents) instead of IEEE 754 floats to guarantee exact conservation."
# @a2dlc:rejected_alternatives = ["float64 (breaks rounding invariants)", "pessimistic table lock (violates p99)"]
# @a2dlc:protected_invariants = ["INV-01", "INV-02"]
# @a2dlc:do_not_refactor = ["idempotency_key_hash_canonicalization"]
```

When the slice is modified, the orchestrator injects `rejected_alternatives` and `do_not_refactor` as constraints on the agent.

### 6.4 Verdict and waives (common to Lite and full)

**Verdict** (`.a2dlc/latest.json`; history in `.a2dlc/reports/`):

```json
{
  "target": "core/rolling.py::rolling_max",
  "stage": "tier1_differential",
  "verdict": "FAIL",
  "engine": "crosshair-diffbehavior",
  "search_budget": {"iterations": 216, "per_condition_timeout_s": 10},
  "typed": true,
  "domain_guard": null,
  "equivalence": "strict",
  "base_ref": "sha1:…",
  "tree_hash": "sha256:…",
  "contract_hash": "sha256:…",
  "counterexample": {"kwargs": {"xs": "[1, 2, 2]", "w": "2"}, "old": "2", "new": "1"},
  "cluster_id": "div-07"
}
```

- **`search_budget` depends on the engine:**
  - CrossHair: `{"iterations", "per_condition_timeout_s"}`.
  - Hypothesis: `{"examples", "seed"}`, for example `{"examples": 2000, "seed": 42}`.
  - A field from another engine is a schema error.
- **`domain_guard`:** `null` when there is no guard; if there is one, `{"expr": "…", "source": "contract" | "ast_overapprox"}` (§4.4). Any other origin is a schema error.
- **`verdict`:** `PASS` | `FAIL` | `INCONCLUSIVE`. "No differences found" is a **bounded search**, not an equivalence proof: that is why the verdict carries `engine` and `search_budget`, and a `PASS` without a recorded budget is not valid. `INCONCLUSIVE` covers functions without types, timeouts without a single useful iteration and red runs without identifiable tests; it blocks just like `FAIL`, but it escalates as a tool problem, not as a divergence.
- **Tree-bound receipt:** `tree_hash` is the hash of the content of the tree on which the verdict was issued. Before merge it is recomputed; if someone edited or committed afterwards, the verdict becomes **stale** (the files are named) and it must be run again. There is no flag to skip it.

**Waive** (`.a2dlc/waivers.yaml`, versioned, reviewable in PR):

```yaml
- target: "core/rolling.py::rolling_max"
  cluster_id: "div-07"
  decision: "enforce_new_behavior"     # preserve_legacy | enforce_new_behavior
  reason: "The window must exclude the current element (historical bug)"
  approved_by: "aldemar"
  date: "2026-10-15"
```

Waives feed the Locked Contract `S*` as assertions, so the same divergence does not escalate again.

On a test the decisions are `characterization_test` and `quarantine_flaky` (§4.4); on a finding of a pluggable check
(§5, Checks) the decision is `accept_finding` on `<file>::<check id>` or `<file>::L<n>`. A decision of another class
does not cover the target, and every signature binds the blob of the target's file.

---

## 7. Circuit Breakers

| ID | Trigger | Human action |
|---|---|---|
| **CB-01** Spec divergence | Two implementations that satisfy `S*` are not `≡_S` on a valid input (or Stage 1 divergence) | Choose A/B over the counterexample |
| **CB-02a** UNSAT spec | Z3 returns UNSAT in Stage 1.5 | Relax invariants (the unsat core is sampled) |
| **CB-02b** Builder exhaustion | Spec is SAT but all Builders fail after `max_tier1_adversarial_rounds` | First automatically: higher-tier model or slice decomposition; human if it persists |
| **CB-03** Budget | Accumulated cost > `max_cost_usd` | Review telemetry, refine intent or raise the budget |
| **CB-04** Protected boundary | Touching `forbidden_side_effects` or `do_not_refactor` is required | Explicit privilege elevation |
| **CB-05** Prerequisite failure | Stage −1 does not achieve a hermetic build + green baseline | Intervention in Stage −1 |
| **CB-06** Delivery | The Rollback Sentinel reverts and the compensation fails or does not exist | Manual data repair |

**Escalation hygiene:** probes are grouped by root cause, processed in batches per session, and each includes a minimal (shrunk) counterexample.

---

## 8. Reference stack

| Layer | Recommended | Alternative |
|---|---|---|
| Control plane / state machine | **LangGraph** (checkpointers, `interrupt()` for probes and CBs) | Pydantic AI + Temporal/DBOS |
| Roles with typed output (Specifier, Inquisitor, Saboteur) | **Pydantic AI** (multi-provider, validated outputs) | Direct calls + Pydantic |
| Builder family A | **Claude Agent SDK** inside the sandbox | — |
| Builder family B | Coding agent from another family, or Pydantic AI with tools | — |
| Observability / pilot metrics | **LangSmith** | OpenTelemetry + own backend |
| Sandbox | Docker read-only rootfs, `--network=none`, pre-warmed toolchains | Nix, Firecracker, WASM/WASI |
| Schemas | Pydantic v2 · Zod/TypeBox · serde + newtypes | — |
| PBT / fuzzing | Hypothesis + Atheris · fast-check · proptest + cargo-fuzz | — |
| Differential (Python, Lite mode) | `crosshair diffbehavior` (main) | `hypothesis write --equivalent` with types, `max_examples ≥ 2000` and name patch (§4.4); goalie (reference, unmaintained) |
| Mutation | mutmut · Stryker `--since` · cargo-mutants `--in-diff` | — |
| SMT | Z3 | cvc5 |
| Codemods (Stage −1) | OpenRewrite · LibCST · jscodeshift · ast-grep | Comby |
| Indexer | Tree-sitter + SCIP + service contracts in graph | — |
| Migrations | squawk · atlas | — |

The gates are deterministic code, not agents. The greatest implementation effort lies in the **sandbox runner**, not in the orchestration framework.

---

## 9. Prior art absorbed

- **Test-based selection among candidates:** AlphaCode, CodeT.
- **N-version programming and correlated failures:** Knight & Leveson (1986).
- **Golden master / approval testing:** ApprovalTests; *Working Effectively with Legacy Code* (Feathers).
- **Legacy vs new parity:** GitHub Scientist; Diffy (Twitter).
- **Consumer-driven contracts:** Pact.
- **Strangler Fig, expand-and-contract, canary releases:** established delivery patterns.
- **Deterministic mass refactors:** OpenRewrite / Moderne.
- **Diff-test for AI-generated code:** goalie (MIT, alpha, a learning project without maintenance). It contributes to Lite mode the JSON verdict per seed, the waives and the `tests/human` suite. Its adoption by a line in `AGENTS.md` was replaced in v3.3 by a gate run by the harness (§4.4). Its core is equivalent to Stage 4 of refactor mode for pure Python functions.
- **Symbolic differential and PBT generation:** CrossHair (`diffbehavior`), Hypothesis ghostwriter. They are mature engines: they are integrated, not rebuilt.
- **Oracle/NOP gate** (tests must fail without the implementation and pass with it): Skill2Env (NVIDIA). Task acceptance in `NVlabs/Skill2Env`, `skill2env/buildaudit.py` ("require Oracle pass and NOP fail in Harbor"), checked on 2026-10-15.
- **Lemma decomposition and back-translation in autoformalization:** techniques from the line of mathematical proof with LLMs (verify subgoals separately; translate the formal statement back into natural language to audit the formalization). v3.4 adapts them (Stage 2 and Stage 1.5). The critique that proposed them attributed them to OpenAI and DeepMind; **the source is not verified in this version.**
- **Verdicts bound to the reviewed tree:** AI-DLC (`awslabs/aidlc-workflows`). The README calls it "source-bound review evidence", and the repo ships a review-freeze hook (`core/hooks/aidlc-review-freeze.ts`). Checked on 2026-10-15.

---

## 10. Known limits and open risks

1. **Agreement ≠ correctness remains a residual risk.** The consensus probes mitigate it but do not eliminate it.
2. **Z3 covers only a fragment.** Many real invariants will end up in `runtime_and_pbt`. Interactive provers (Lean 4, Isabelle) express quantifiers, induction and idempotency, but **are out of the pilot**: translating real imperative code to Lean is an open problem, and if an LLM does the translation the risk returns in the form of a formalization that does not reflect the code. They would only make sense for isolated pure algorithms or critical protocols, and not in v3.x.
3. **Equivalence by traces is statistical**, not a proof. It depends on the coverage gate.
4. **Stage −1 is expensive and semi-manual.** In very degraded repos it can dominate the total cost.
5. **Changes inside the legacy** without explicit scoping are outside the autonomous flow.
6. **Cost per slice** (dual-spec + N=2 + Saboteur + mutation) is unmeasured; the example budget is indicative.
7. **Lite mode without Saboteur:** the differential and the NOP gate do not detect a misunderstanding of the spec that the Builder codifies in both the test and the code at once. Only the Saboteur, blind to the code (full flow), attacks that gap.
8. **The differential is bounded search:** a `PASS` from CrossHair or Hypothesis says "I found no difference with this budget", not "they are equivalent".
9. **Method risk:** v2–v3.4 were refined by iterating critiques between LLMs. That optimizes for passing LLM reviews, not for working in production. **The only measured figures are those in §12, Phase 0: the smoke run, the domain guard and the imported base (three real functions, a few cases, not at scale).** All the others (thresholds, times per tier, budgets) remain unvalidated. The critique that originated v3.4 claimed that the domain guard makes the budget go "5 times further": it is a figure without measurement and is not adopted.
10. **Domain guard miscalculated (v3.4):** a guard that excludes inputs where the versions differ produces a false `PASS` with an appearance of rigor. That is why only contract guards or mechanical over-approximations are admitted; the verdict records the guard, and Phase 0 compares it against the run without the guard.
11. **Back-translation with shared bias (v3.4):** if the Back-translator and the Specifiers share a family or training data, the reverse translation may repeat the same misunderstanding. It mitigates, but does not eliminate, as the consensus probes do.

---

## 11. Operational security (minimums)

- Sandboxes without network by default; explicit allowlist for package registries.
- New dependencies subject to allowlist + lockfile + supply-chain scanning.
- Agent outputs and recorded data are treated as untrusted (prompt injection via fixtures, comments or traffic data).
- Production traces anonymized before being persisted.

---

## 12. Pilot plan (next step)

**Recommended scope:** 5–10 real slices, starting with a **bounded refactor with the legacy as oracle**. It is where the protocol contributes most and the oracle costs nothing.

**Phases:**

0. **Lite mode (§4.4)** on a Python repo: CrossHair (+ Hypothesis as a complement) + JSON verdict + waives + gate run by the harness. Validates the agent ↔ oracle loop in days, not weeks.
   - **Engine smoke run (2026-10-15).** Docker without network; crosshair-tool 0.0.111, hypothesis 6.169.4. On two real functions from a personal script, three old/new pairs: a real, historical refactor bug (`lstrip("./")` instead of `removeprefix("./")`), a hand-written mutant and equivalent refactors written for the test.
     - **CrossHair (`--per_condition_timeout 60`): 6/6.** "No differences found" on the equivalent ones (573–806 iterations) and a counterexample on the broken ones. About 60 s per function, which is the cap set, not the time to the first counterexample.
     - **CrossHair by budget, over 2 broken and 2 equivalent typed ones:**
       - Default: 3/4 in 1.7–4.5 s; it gave a false PASS on the broken `_en_alcance` with 42 iterations.
       - `--per_condition_timeout 10`: 4/4 in ~10.5 s each.
       - In both cases the time is the same for `PASS` as for `FAIL`.
     - **Ghostwriter: 0/2 broken with 100 examples and 2/2 with 5000** (2–6 s), provided there were types and the name patch.
   - **Domain guard (2026-10-16).** Same Docker and versions; same pairs; default and 3 s budgets (3 repetitions), and 10 s (1). Guards written by hand following the rule of §4.4: there is no AST synthesizer yet.
     - `_en_alcance` broken:
       - Default: false `PASS`, with and without guard (3/3 each).
       - **With 3 s: without guard, false `PASS` 3/3; with guard, `FAIL` 3/3.**
       - With 10 s, both detect it.
     - `_norm` broken: detected with and without guard at all budgets. The guard changed nothing.
     - Equivalent with guard: 0 false `FAIL`s in 7 runs.
     - `_norm` equivalent: the diff rewrites the entire body, so no guard applies (the opposite case).
     - **Bad guards** (under-approximating): false `PASS` in 14/14 runs, even with 10 s. The risk in §10.10 is real and the measurement detects it.
     - Wall-clock time is the budget with or without guard. What the guard gains is detection within the same budget, in 1 of 2 mutants; the sample is minimal.
   - **Imported base with real dependencies (2026-10-16).**
     - **Setup:**
       - Personal Python repo with dependencies (SQLAlchemy, asyncpg, pydantic-settings and others), using its own Docker image, without network.
       - Three trees extracted with `git archive <sha>` (neither clone nor worktree; the repo is not touched).
       - A real function changed in a feature commit (`_graph_prefixes`: adds a fourth prefix), and the same pair without the change as control.
     - **Renaming the package is not enough:**
       - A copied tree without rewriting its absolute imports loads 34 modules from the image's package (`/app/src`, in the default `PYTHONPATH`); without that path it does not import.
       - With the imports rewritten (650–813 per tree, including the lazy ones inside functions) and a clean `PYTHONPATH`, 0 foreign modules load.
       - 91–109 references to the original name remain inside strings (mocks, `importlib`) that the rewrite does not touch.
     - **CrossHair does not load the function as is:** importing the module triggers effects that it blocks.
       - First a `fcntl.ioctl` (TIOCGWINSZ). After unblocking it, a `subprocess.Popen` of `ldconfig -p`, which launches a dependency (python-magic via `ctypes.util.find_library`).
       - What worked: importing the target modules in the same process **before** starting CrossHair, which watches effects only from there. So nothing is unblocked inside the analysis.
     - **Result:**
       - Pair with change: `FAIL` with the correct counterexample (`tenant_id='\xa0'` → the new version adds `plenum_t_`), with the default budget and with 10 s.
       - Pair without change: `PASS` with 36 and 10 iterations.
     - **Time:** ~45 s per function with `--per_condition_timeout 10`, and 86–160 s with the default budget, versus ~10.5 s on the stdlib-only function. It includes about 5–7 s of import per tree, but most of it is slower analysis over real code. **The rule "time is the budget" of §4.4 does not hold in real modules**, and the count "30 functions ≈ 40 s with 8 cores" falls short several times over.
     - **Rules that come out of this for §4.4:**
       - Rewrite the absolute imports.
       - `PYTHONPATH` only with the trees under test.
       - Import the modules before the analysis.
       - Never `--unblock` an effect that is not read-only.
       - If the import fails or the analysis needs to unblock something, the result is `INCONCLUSIVE` for "side effects at import".
   - **TypeScript pilot, with token cost (2026-10-16).**
     - **Setup:**
       - Own small TypeScript repo. Real backlog tasks; the agent is Sonnet in headless mode, with isolated config (credentials only, no hooks or user rules).
       - The gate is the agent's Stop hook: fast-check with 2000 examples over the exported pure functions of `src/lib` and `src/server` that changed, against the base extracted with `git archive`.
       - Runs in Docker without network, with 1 GB and 2 CPU. Peak RAM of ~125 MB.
       - `FAIL` or `INCONCLUSIVE` return the turn with the counterexample; after 4 blocks the gate lets it finish.
     - **Generator calibration:** with frozen mutants (sha256), 5/5 of the first set and 2/3 of a new set are detected; the new set was written after tuning the generators. An equivalent refactor gave 0 false `FAIL`s. With 100 examples, 2 of the 5 escaped.
     - **A/B with 3 tasks, 1 run per arm:**
       - Without gate, USD 0.85; with gate, USD 0.67. The difference is not attributed to the gate: the gate never blocked.
       - In the navigation task, the arm without the gate changed two pure route functions, on purpose and without recording the change. The final gate would have stopped it and asked for a waiver; it was not a bug.
       - In all 3 tasks, the arm with the gate (warned in the prompt) did not touch `src/lib`. **Explaining the gate in the prompt changes the design the agent makes:** it is a side effect of its own that must be measured separately.
     - **Cost of a block:** forced in 3 runs of the same task, with the gate active but not explained, and with the instruction to change the routes.
       - All 3 were blocked: 2 runs with 1 block and 1 with 2. All 3 ended in `PASS` with two well-targeted waivers and lint green.
       - The loop cost USD 0.06–0.10 per run, 14–25% of the total cost (USD 0.39–0.42), in 3–5 turns.
       - The gate takes 4–6 s and returns ~490 bytes to the agent.
       - The cost is almost all cache re-reads (180–300 thousand tokens), not output tokens (340–680).
     - **The gate accepts any waiver the agent writes.** Of the 3 runs, 2 sent the old `flow` tab to `/app/history` and 1 to `/app/routing`. Each waiver describes its own code well, but the gate cannot decide which of the two readings of the ticket is correct. Only the human approval of the waiver (`approved_by`) decides that, so that step cannot be decorative.
     - **Limits:** n = 1 in the A/B and n = 3 in the block, in a single repo, with 2 affected functions. How the cost grows with more functions was not measured.
   1. Sandbox runner + Tier 0 (compiler, linter, probes).
   2. Stage 1/1.5 (dual-spec or behavioral capture, lint, Z3, π_S, back-translation).
   3. Tier 1 (PBT + fuzzing + differential).
   4. Tier 2 and Stages 6–7 only if the previous ones prove valuable.

**Metrics:**

| Metric | Question it answers |
|---|---|
| Probe precision (% useful according to the human) | Does the human decide things that matter? |
| Escalations per slice and % false positives of CB-01 | Is `π_S` well calibrated? |
| Escaped defects (post-merge, post-canary) | Do the gates detect what they should? |
| USD cost and time per slice, per stage | Is it economically viable? Where does the budget go? |
| Mutation kill rate of the Saboteur's suite | Does the Saboteur generate tests with teeth? |
| % of invariants SAT / UNSAT / UNKNOWN | How much does Z3 actually cover? |
| Real Tier 0 time | Is the < 2 s target achievable in the repo? |
| % of `INCONCLUSIVE` in Lite, by cause (no types, side effects blocked, timeout, out of budget) | How much real code does Lite mode cover? If high, Lite does not work as-is in that repo. |
| % of tests in quarantine due to flakiness | Is the "does not get worse" baseline reliable in that repo? |
| `PASS` falses and iterations per second, with and without domain guard | Does the guard concentrate the budget without hiding differences? |
| % of back-translation discrepancies that the human judges useful | Does the reverse translation find misunderstandings that the catalog probes did not see? |
| In Critical slices with `FAIL` or `INCONCLUSIVE`, % where a lemma located the failure | Do the lemmas pay for what they cost the Builder? |

The pilot data decides v4, not another round of paper review.

---

## 13. History

| Version | Changes |
|---|---|
| v1.0 | Initial proposal: adversarial triads, N-version tournament, deterministic gates, shadow canary. |
| v2.0 | Dual-specifier + boundary probing, N=1/N=2 routing, latency tiers, diff-scoped mutation (80%), `@a2dlc` ADRs. |
| v3.0 | Consensus probes, contract linter, Z3 in Stage 1, `π_S`, complexity-based selection, roles instead of models, brownfield adapters, consumer matrix, expand-contract, canary rings. |
| v3.1 | Modes (greenfield / refactor / legacy), Stage −1 with ordered baseline → seaming via codemods, Z3 with `UNKNOWN` and invariant DSL, strict-by-default `π_S`, deterministic assumption catalog, trace coverage gate, probe grouping by root cause, cross-service edges in the indexer, migrations as a durable workflow, mandatory compensation strategy, read-only-only production parity, CB-05/CB-06, reference stack, pilot plan. Fixed `InvalidAmountError` missing from the taxonomy and the BP-02 ambiguity. |
| v3.2 | Lite mode (§4.4) as entry point, with an escalation path to the full flow. Common JSON verdict format and versioned waives (§6.4). CrossHair and Hypothesis ghostwriter as Python differential engines. goalie added to prior art. Phase 0 of the pilot in Lite mode. |
| v3.3 | Changes from the smoke run of 2026-10-15:<br>- CrossHair as the main differential engine; ghostwriter only as a complement, with types, `max_examples ≥ 2000` and name patch.<br>- Mandatory types in Lite.<br>- Verdict with three values (`PASS` / `FAIL` / `INCONCLUSIVE`), tool and search budget.<br>- The gate is run by the harness; the line in `CLAUDE.md` only documents it.<br>- Clean checkout of the base with another package name.<br><br>Mechanisms taken from a harness in use:<br>- NOP/Oracle gate.<br>- "Does not get worse" baseline.<br>- Tree-bound receipt (`tree_hash`, stale before merge).<br>- Risks 7 and 8 in §10.<br><br>Same-day revision, after external critique:<br>- CrossHair budget measured (minimum 10 s per function; the default value gives false PASSes), in parallel and with priority.<br>- `search_budget` depends on the engine.<br>- NOP gate: the base fails against a stub that raises `NotImplementedError` (valid failure), not because of an import; a test that passes against the stub is refuted.<br>- Flaky only if also intermittent on the base; stable green on the base and red on the candidate blocks.<br>- §10.9 says which figures are measured.<br>- `INCONCLUSIVE` and flakiness metrics.<br>- goalie aligned with §4.4.<br>- Sources for Skill2Env and AI-DLC verified in their repos. |
| v3.4 | Following an external critique (from an LLM) with three options and three techniques, all unmeasured:<br><br>Discarded, with reasons:<br>- Lean 4 / Isabelle for Z3's `UNKNOWN` (§10.2).<br>- Path pruning inside CrossHair (§4.4, "Engines").<br>- The "5 times" figure (§10.9).<br><br>Added:<br>- Assumption catalog above the LLM, which only instantiates examples (Stage 1).<br>- Domain guard before CrossHair, only contract-based or by mechanical over-approximation, recorded in the verdict (§4.4, §6.4).<br>- Lemmas with postcondition in Critical slices, without raising the parent verdict (Stage 2).<br>- Blind back-translation of `S*` and `π_S` with its own role (§3, Stage 1.5).<br><br>Each technique carries its opposite case and its metric (§12). Risks 10 and 11 in §10. |
