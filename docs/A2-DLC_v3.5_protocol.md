# RFC-0001: Autonomous Agentic Development Lifecycle (A2-DLC) v3.5

**Status:** Consolidated draft (v3.5). §6.4 (the receipt) is normative and describes what the reference implementation does; `contracts/` and `conformance/` accompany it. The rest of the document is unchanged from v3.4 and stays pending empirical validation (see §12); the Lite differential engine has a smoke run (§12, Phase 0). Of what was added in v3.4, the domain guard has a first measurement (§12); the lemmas and the back-translation are unmeasured.
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

<img src="assets/protocol-architecture.svg" alt="A2-DLC Protocol Architecture & Clean-Room State Machine (RFC-0001 v3.5)" width="100%" />

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
[3 Tier 0 Inner Loop (<2s engines; + the declared tests for files no engine covers)]
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

### Stage 3: Tier 0 Inner Loop (< 2 s for steps 1 to 3)
In a hermetic sandbox per Builder:
1. Compiler + strict type check, zero warnings (`cargo check`, `tsc --strict`, `pyright --strict`).
2. AST policy linter: forbidden imports, unbounded loops, reflection, missing `@a2dlc` header, complexity budget.
3. Locked boundary assertions (Stage 1 probes).
4. **The declared tests, for the changed files no engine reviewed** (RFC-0002). If the repo declares `config.tests`, `tests.command` runs in `tests.image` on the candidate, in the same hermetic sandbox, with the files that no engine reviewed in steps 1 to 3 in `$A2DLC_CHANGED`; the suite runs as the command defines it. The run counts as a review only if it is readable: JUnit XML is present, it has at least one testcase, and a non-zero exit comes with at least one identifiable failed or errored testcase. Each failed or errored testcase is a Tier 0 finding of kind `test` and returns to the Builder with the other findings. When the suite is green and the same run also wrote readable coverage (`$A2DLC_COVERAGE`), each source file in `tests.source_paths` with changed lines that no test executed is one Tier 0 finding of kind `coverage` (its first such line, and all of them in the message), once the lines that Stage 4 notes instead of blocking on (defensive exception handlers, declaration-only code) are left out. A run that wrote no readable coverage gives no such finding, and Stage 4 keeps its own coverage precondition. A run that is not readable (no JUnit, no testcase, a red exit with no identifiable failed testcase, a timeout, no runner) is an error of the tool, not a review and not feedback: the stage fails with the reason and spends no round. Step 4 does not run when the engines reviewed every changed file or when the repo declares no `config.tests`.

The structured feedback (JSON) returns to the Builder; maximum `max_inner_iterations = 5`. A finding of step 4 spends a round like any other.

A candidate on which Tier 0 reviewed nothing (no engine had a file or a probe, and step 4 did not run or was not readable) is not accepted: what nobody executed is `INCONCLUSIVE`, never `PASS`, and the stage escalates. The files that step 4 covered stay in the list of what Tier 0 did not review, because Stage 4 keeps its own coverage precondition. The 2 s target is the time of steps 1 to 3; step 4 takes the time of the repo's suite, bounded by `tests.timeout_s`.

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
       - **It is flaky only if it is also intermittent on the base:** it passes and fails across the k runs on the base. Then a `quarantine_flaky` waiver (§6.4.6) puts it in quarantine and it does not block.
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

### 6.4 The receipt: verdict, waivers and approvals (common to Lite and full)

This section is normative. MUST, MUST NOT, SHOULD and MAY are used as in RFC 2119. It describes what the reference implementation (the `a2dlc` binary) does as of v3.5, byte for byte where bytes matter, so that a verifier or a producer written from this text and `contracts/` agrees with it. The JSON Schemas in `contracts/` govern the shape of every document named here; this section governs what the values mean and how the hashes and signatures are computed. `contracts/tests/check_rfc.py` fails if this section contradicts a schema, and `conformance/` holds the test vectors.

Three roles appear below. A **producer** runs the gates and writes a receipt (`a2dlc check`). A **verifier** decides, without running engines, whether a receipt is valid for a commit (`a2dlc verify`). An **engine** judges the files of one language (§4.4).

#### 6.4.1 Files

<!-- a2dlc:files -->
| Path | Schema | In the `tree_hash` | Role |
|---|---|---|---|
| `.a2dlc/config.json` | `config` | yes | What is judged. A producer reads it at the receipt's `base_ref`, not from the working tree. |
| `.a2dlc/waivers.json` | `waivers` | yes | Decisions the agent declares. Declaring is not approving. |
| `.a2dlc/approvals.json` | `approvals` | no | Signatures over waivers (§6.4.7). |
| `.a2dlc/verdict.json` | `verdict` | no | The receipt. A producer writes it; it is not committed by default. |
| `.a2dlc/escalations.json` | `escalation` | no | What a person has to decide. |
| `.a2dlc/reports/` | none | no | Gate output. |
| `.a2dlc/run/` | none | no | State of `a2dlc run`. |

#### 6.4.2 The tree hash

`tree_hash` binds a receipt to the content of one commit. It is the string `git-tree:` followed by the lowercase hexadecimal id of a git tree object, in the object format of the repository (SHA-1: 40 digits; SHA-256: 64 digits). The schema pattern is `^git-tree:[0-9a-f]{40}([0-9a-f]{24})?$`.

**Definition (normative).** Let `C` be a commit and `R` the root tree of `C`. The tree of `C` is `R` with these changes, in this order:

1. Remove every entry whose path, relative to the repository root, equals an excluded path or lies under one (the excluded path followed by `/`).
2. Remove every directory that has no entry left.

`tree_hash(C)` is the id git gives the resulting root tree: the hash, in the repository's object format, of the bytes `tree <n>\0` followed by the entries in git's tree order. Nothing else enters the computation: not the commit message, author, parents or dates, not local configuration, not `.gitignore`.

The excluded paths are the gate outputs and the signatures:

<!-- a2dlc:excluded -->
```text
.a2dlc/verdict.json
.a2dlc/reports
.a2dlc/approvals.json
.a2dlc/escalations.json
.a2dlc/run
```

- Matching is exact and case-sensitive, from the repository root. None of these paths is excluded:

<!-- a2dlc:not-excluded -->
```text
.a2dlc/runner/x
.a2dlc/verdict.json.bak
sub/.a2dlc/verdict.json
.A2DLC/verdict.json
```

- Everything else is in the hash: `.a2dlc/config.json`, `.a2dlc/waivers.json`, `.gitattributes`, executable bits, symbolic links, and gitlinks (a submodule counts as its commit id).
- The hash is of the stored objects. Line-ending conversion, `.gitattributes` filters and `core.autocrlf` act when a file is added to git and are not applied again, so a verifier reading the commit's objects does not depend on them.
- Informative: with git, `read-tree <C>` into a temporary index, `rm --cached -r --ignore-unmatch -- <excluded paths>`, `write-tree` gives the same id. That is how the reference computes it.

**Working-tree preview.** `a2dlc check` without a candidate commit hashes the working tree instead: a temporary copy of the index, `add -A`, the same removal, `write-tree`. That tree depends on `.gitignore`, `.gitattributes`, `core.autocrlf` and on what is untracked, so it is a local preview. The preview becomes the receipt's `tree_hash`, and the receipt is valid for a commit only when the preview equals `tree_hash(C)` at verification (§6.4.8). A producer SHOULD run on a clean checkout of the commit it expects to be verified.

#### 6.4.3 The receipt

`.a2dlc/verdict.json` is one JSON object per run, with one entry per judged target. Its schema is `contracts/verdict.schema.json`; `schema_version` is `1`. A producer MUST NOT emit a field the schema does not name. A verifier MUST reject a receipt that does not validate against the schema, with the exceptions in §6.4.11.

<!-- a2dlc:fields verdict -->
| Field | Required | Meaning |
|---|---|---|
| `schema_version` | yes | `1`. |
| `a2dlc_version` | yes | Version of the producer. Informative. |
| `verdict` | yes | `PASS`, `FAIL` or `INCONCLUSIVE` (§6.4.4). |
| `base_ref` | yes | Commit id the diff was taken from. |
| `tree_hash` | yes | `git-tree:<hex>` of the judged tree (§6.4.2). |
| `created_at` | yes | RFC 3339 date-time. |
| `targets` | yes | One result per function the engines judged. |
| `out_of_scope` | yes | Changed files no engine judged, each with a reason. A receipt says nothing about them. |
| `pending_waivers` | yes | Targets whose waiver has no valid signature. |
| `stage` | no | Stage of `a2dlc run` that wrote it; absent from `a2dlc check`. |
| `contract_hash` | no | `sha256:<hex>` of the slice contract the run judged against. |
| `engine_errors` | no | Engines that did not answer, or answered badly. |
| `diff_budget` | no | Time budget of the engines and the functions it cut. |
| `test_gates` | no | Status of each test gate. |
| `coverage` | no | Which changed lines something examined. |
| `tests` | no | Test cases the gates judged. |
| `checks` | no | Pluggable checks (§5, Checks). |
| `level` | no | `0` for the static receipt (`a2dlc check --level 0`, never a pass); absent means `1`. |
| `static` | no | Level 0 only: what the diff shows without running anything of the repo. |
| `timings` | no | Milliseconds the check spent per step, measured by the core. |

Each entry of `targets` is an engine's result (`engine`, `engine_version`, `verdict`, `search_budget`, `generators`, `typed`, `domain_guard`, `equivalence`, `counterexample`, and `reason` when `INCONCLUSIVE`) plus `waived` and, rarely, `note` (§6.4.5). A `PASS` target with no recorded `search_budget` is not valid: "no difference found" is a bounded search (§10.8), and the receipt carries the bound.

<!-- a2dlc:schema verdict -->
```json
{
  "schema_version": 1,
  "a2dlc_version": "0.4.1",
  "verdict": "PASS",
  "base_ref": "0123456789abcdef0123456789abcdef01234567",
  "tree_hash": "git-tree:5d968b6758b92dea858e8d9cc9254c37a45b6c1d",
  "created_at": "2026-10-29T12:00:00Z",
  "targets": [
    {
      "target": "src/m.py::f",
      "engine": "crosshair-diffbehavior",
      "engine_version": "0.0.111",
      "verdict": "FAIL",
      "search_budget": {"iterations": 12, "per_condition_timeout_s": 10},
      "generators": "typed_default",
      "typed": true,
      "domain_guard": null,
      "equivalence": "strict",
      "counterexample": {"args": "{\"x\": 0}", "old": "0", "new": "1"},
      "waived": false,
      "note": "behavior_divergence"
    }
  ],
  "out_of_scope": [{"file": "README.md", "reason": "no_engine"}],
  "pending_waivers": [],
  "coverage": {"status": "not_configured", "files": []}
}
```

The `engine` field names one of the engines the schema lists today (`fast-check`, `crosshair-diffbehavior`, `hypothesis`, `rapid`, `blackbox`), and `search_budget` has the shape that engine's entry in the schema requires. A new engine name is a schema change.

#### 6.4.4 Verdict values

`verdict` is computed from the rest of the receipt. A producer MUST compute it as follows, and a verifier MAY recompute it.

1. `FAIL` if any of these holds:
   - A target has `verdict: FAIL`, `waived: false`, and no `note` (§6.4.5).
   - A test in `tests` has `verdict: FAIL` and no waiver of its class.
   - A check in `checks` has `severity: blocking`, `status: ran` and a finding with `waived: false`.
2. Otherwise `INCONCLUSIVE` if any of these holds:
   - A target has `verdict: INCONCLUSIVE`. A waiver never covers it: waivers cover `FAIL` only.
   - `engine_errors` is not empty. An engine that crashed, timed out, answered with a document that does not validate, or reported an error of its own harness (CrossHair or the generator raising on either the old or the new side) is a tool problem, never a `FAIL` and never a `PASS`.
   - A test in `tests` is `INCONCLUSIVE` without a waiver of its class, or a gate in `test_gates` has `status: error`.
   - A check with `severity: blocking` has `status: error` or `timeout`.
   - `coverage.files[].uncovered` is not empty for some file: a changed line in scope that no engine examined and no test executed. Lines in `coverage.files[].noted` do not count.
   - `diff_budget.cut` is not empty: a function the time budget skipped was not judged.
   - A path matched by `config.protected_paths` changed and has no valid approval.
3. Otherwise `PASS`.

`FAIL` wins over `INCONCLUSIVE`; the conditions in item 2 never turn a `FAIL` into `INCONCLUSIVE`. A check lowers the verdict and never raises it. A waived `FAIL` does not count toward `FAIL`, signed or not; an unsigned one is listed in `pending_waivers`, and a verifier refuses it (§6.4.8).

The reference binary exits `0` for `PASS`, `1` for `FAIL` and `2` for `INCONCLUSIVE`. The exit code is not part of the receipt.

#### 6.4.5 Notes and critical code

A behavior divergence (a differential engine found an input where the old and the new code differ) is a finding for a person to look at. Whether it blocks depends on where the code lives.

- `config.critical_code` is a list of globs naming code where any divergence blocks. It is read from `.a2dlc/config.json` at the receipt's `base_ref`.
- A target with `verdict: FAIL`, a non-null `counterexample`, no waiver, whose id does not match `critical_code`, is recorded with `note: "behavior_divergence"`. It keeps `verdict: FAIL` and its counterexample, and it does not count toward the aggregate (§6.4.4).
- A `FAIL` target on critical code, or without a counterexample, has no note and makes the receipt `FAIL` until a signed waiver covers it.
- With no `critical_code`, nothing is critical. `a2dlc run` treats everything as critical.
- A verifier MUST refuse a note unless the target is `FAIL` with a counterexample and `waived: false`, and MUST refuse it when the target matches `critical_code` of the config at `base_ref`. If that config cannot be read, every target is critical.

A glob is matched against a target `<file>::<symbol>`, split at its first `::`. A glob without `::` matches the file part and covers every symbol in the file. A glob with `::` is split the same way, and its two halves match the file part and the symbol part separately. In a half, `**/` matches any number of leading directories including none, `**` matches any characters including `/`, `*` matches any run of characters except `/`, `?` matches one character except `/`, and every other character matches itself. The whole half must match.

#### 6.4.6 Waivers and the WaiverSHA

`.a2dlc/waivers.json` (`contracts/waivers.schema.json`) lists decisions. Each has a `target`, a `decision` and a free-text `reason`. The decision has a class, and a waiver covers only targets of its class:

- Engine targets (`<file>::<symbol>`): `enforce_new_behavior` and `preserve_legacy`.
- Test targets: `characterization_test` (a test that passes on the base on purpose) and `quarantine_flaky`; `enforce_new_behavior` also covers a test another tree passes and the candidate fails on purpose.
- Findings of a check: `accept_finding`.
- A protected path: a waiver whose target is `<file>::*`.

<!-- a2dlc:schema waivers -->
```json
{
  "schema_version": 1,
  "waivers": [
    {
      "target": "src/m.py::f",
      "decision": "enforce_new_behavior",
      "reason": "The result now includes the offset <1> & more"
    }
  ]
}
```

When two entries of `waivers` have the same `target`, the last one is the waiver. A producer MUST NOT write a repeated object key; the reference keeps the last value.

**WaiverSHA.** The `waiver_sha256` of an approval is the SHA-256, as 64 lowercase hexadecimal digits, of these bytes, which are not the bytes of `waivers.json`:

```text
{"target":<T>,"decision":<D>,"reason":<R>}
```

The object has exactly these three keys, in this order, with no whitespace and no trailing newline. `<T>`, `<D>` and `<R>` are the three strings written as JSON strings in UTF-8 with these rules:

<!-- a2dlc:waiver-escapes -->
| Character | Written as |
|---|---|
| `"` | `\"` |
| `\` | `\\` |
| U+0008, U+000C, U+000A, U+000D, U+0009 | `\b`, `\f`, `\n`, `\r`, `\t` |
| any other U+0000 to U+001F | `\u00` and two lowercase hexadecimal digits |
| `<`, `>`, `&` | `\u003c`, `\u003e`, `\u0026` |
| U+2028, U+2029 | `\u2028`, `\u2029` |
| a lone surrogate in the source document (`\ud800`) | the character U+FFFD, written as its UTF-8 bytes |
| anything else, including `/`, U+007F and characters above U+007F | itself, as UTF-8 |

The strings are the values the document has after JSON decoding, so `\u003c` and `<` in `waivers.json` give the same hash. A `waivers.json` that is not valid UTF-8 is invalid; the reference replaces each offending byte with U+FFFD, and a verifier MAY reject the file instead. The rules above are the output of Go's `encoding/json` for these three fields (`HTMLEscape` on), and they are identical in Go 1.23 and 1.25. For the waiver above the bytes are:

```text
{"target":"src/m.py::f","decision":"enforce_new_behavior","reason":"The result now includes the offset <1> & more"}
```

and `waiver_sha256` is `b4359b1a5f1e2b51b00940912264895bed79bc82512cbbf965b99113ba7bf957`.

#### 6.4.7 Approvals

An approval is a person's SSH signature over a waiver and over the file it concerns. The agent can write a waiver and cannot write a valid signature.

<!-- a2dlc:schema approvals -->
```json
{
  "schema_version": 1,
  "approvals": [
    {
      "target": "src/m.py::f",
      "waiver_sha256": "b4359b1a5f1e2b51b00940912264895bed79bc82512cbbf965b99113ba7bf957",
      "file_blob": "7b9ebfea08897d6daa1eace601ca7a9288519547",
      "principal": "t@t",
      "signature": "-----BEGIN SSH SIGNATURE-----\nU1NIU0lHAAAAAQAAADMAAAALc3NoLWVkMjU1MTkAAAAgjEjY+EG2IPL9glcPLCHthM+9Mr\nTxQQ2WzclfXc1EmnoAAAAOYTJkbGMtYXBwcm92YWwAAAAAAAAABnNoYTUxMgAAAFMAAAAL\nc3NoLWVkMjU1MTkAAABA7h+fM1TE696o1xgVYh0NFIQrvbUkCRoHNDJN9wB8/66G4u60rx\naaKjyAlNT5uyr+0Ho/zqCNbSNZD4hDsJeVAg==\n-----END SSH SIGNATURE-----\n"
    }
  ]
}
```

**Signed text.** The signature is made over these bytes, in UTF-8:

```text
a2dlc-approval/1\n<target>\n<waiver_sha256>\n<file_blob>\n
```

where `\n` is U+000A and each field is written as it appears in the approval, with no quotes. `file_blob` is the id, in the repository's object format, of the blob at the file part of `target` (the text before the first `::`) in the tree of the commit (§6.4.2). Because it names the blob, any later edit to that file, even to another function in it, invalidates the approval.

**Signature.** The signature is an OpenSSH signature (`ssh-keygen -Y sign`; format in OpenSSH `PROTOCOL.sshsig`) with the namespace `a2dlc-approval`, armored as in the `signature` field. A verifier checks it against an allowed-signers file in the format of `ssh-keygen -Y verify`: the line for `principal` must list the signing key, and a `namespaces` option on that line, if present, must include `a2dlc-approval`. A verifier MUST support `ssh-ed25519` keys and MAY support others. The reference delegates to `ssh-keygen -Y verify -f <allowed_signers> -I <principal> -n a2dlc-approval -s <signature file>` with the signed text on standard input, so it accepts whatever that command accepts.

**Allowed signers** never come from the repository, because an agent could add its own key in the same diff. In the reference they come from, in this order: the file named by `A2DLC_ALLOWED_SIGNERS`; the contents of `A2DLC_ALLOWED_SIGNERS_DATA`; `~/.config/a2dlc/allowed_signers`. With none of them, nothing is approved.

An approval is valid for a commit when all of these hold:

1. The commit's `.a2dlc/waivers.json` has a waiver for `target`.
2. `waiver_sha256` equals the WaiverSHA of that waiver (§6.4.6).
3. `file_blob` equals the blob id of the target's file in the commit's tree.
4. The signature verifies for `principal` over the signed text.

When `approvals` holds several entries for one target, one valid entry is enough.

#### 6.4.8 Verification

To verify a receipt for a commit `C` (default `HEAD`) a verifier reads `.a2dlc/verdict.json` from the working tree, and `.a2dlc/waivers.json`, `.a2dlc/approvals.json` and, for notes, `.a2dlc/config.json` at `base_ref` from the objects of the commit. It runs no engine. It reports every problem it finds, using these kinds:

| Kind | Problem |
|---|---|
| `receipt_missing` | There is no `.a2dlc/verdict.json`. |
| `receipt_invalid` | The file is not a receipt of this schema. |
| `stale` | `tree_hash` differs from `tree_hash(C)`. The reference names up to ten files that differ. |
| `not_pass` | `verdict` is not `PASS`. |
| `base_unpushed` | The repository has a pushed base (the upstream of `HEAD`, else the fork point of `HEAD` from `refs/remotes/origin/HEAD`) and `base_ref` is not an ancestor of it or equal to it. |
| `note_invalid` | A note that §6.4.5 does not allow. |
| `note_on_critical_code` | A note on a target that `critical_code` names. |
| `unsigned_waiver` | A waived target, test or finding has no valid approval. The reference also says why: no waiver in the commit, `waiver_changed`, `file_changed` or `signature_invalid`. |

The set of waived targets is every entry of `targets` and `tests` with `waived: true` plus every finding of `checks` with `waived: true`, each target once. A receipt is **valid** for `C` when no problem is found. `pending_waivers` is not read: the reference derives the same fact from the `waived` flags and the approvals.

The reference prints `a2dlc verify: ok` and exits `0` when the receipt is valid. Otherwise it prints one line per problem on standard error and exits `1`; it also exits `1` when it cannot read a document at all, so the exit code does not tell an invalid receipt from a verifier error.

#### 6.4.9 What a receipt does not claim

A valid receipt says that one tree, judged against one base with the base's configuration, got this verdict from these engines at `created_at`. It does not say:

- That the rest of the test suite still passes. Gates run the changed tests and, with `baseline_command`, the tests the change affects. The full suite never runs by default (owner decision of 2026-10-26).
- That the candidate is equivalent to the base. `PASS` means no difference was found within `search_budget`; `INCONCLUSIVE` and `out_of_scope` list what was not judged, and a receipt with no targets and files in `out_of_scope` is a `PASS`.
- That a person read the code. An approval says a key holder agreed to one waiver on one version of one file.
- Who produced the receipt. A verifier checks the receipt against the commit and the approvals against the allowed signers; nothing in `verdict.json` is signed. A hand-written `PASS` for the same tree passes verification. What closes that gap is running the producer in CI with `--require-signatures` over the same commit, not the verifier.
- That the engine was correct. A receipt names the engine and its version, and trusts both.

#### 6.4.10 Changes from v3.4

v3.4 described the receipt from the goalie format before the implementation existed. v3.5 states what the implementation does. These are the differences, and the reason for each:

| v3.4 | v3.5 | Reason |
|---|---|---|
| `.a2dlc/latest.json` | `.a2dlc/verdict.json`, with history in `.a2dlc/reports/` | Naming chosen before the specification was frozen. |
| `.a2dlc/waivers.yaml` | `.a2dlc/waivers.json` | JSON only, so that one JSON Schema validates every artifact and the core needs no YAML parser. |
| `tree_hash: "sha256:…"` | `tree_hash: "git-tree:<hex>"` | The implementation uses the id `git write-tree` returns; v3.4 named a SHA-256 of the content without defining it. |
| One target per verdict | One verdict per run, with `targets[]` and `out_of_scope[]` | One receipt per tree, which is what verification checks. |
| `stage`, `contract_hash`, `cluster_id` in the verdict | `stage` and `contract_hash` are optional (`a2dlc run` sets them); `cluster_id` is on every escalation, not on the verdict | The verdict has no divergence clusters. |
| `approved_by: <name>` | An SSH signature over the target, the waiver and the file's git blob | A free-text name can be written by the agent itself. |
| `cluster_id` and `date` in a waiver | A waiver has `target`, `decision` and `reason` | The approver is the signing principal of the approval, and the date is in git history. |
| No note | `note: behavior_divergence` and `config.critical_code` | A divergence on code that is not critical asks for review without blocking the push. |
| Quarantine in `.a2dlc/flaky.yaml` | The `quarantine_flaky` waiver | One file and one signature path for every decision. |

Where the reference falls short of this section is in §6.4.11.

#### 6.4.11 Known gaps of the reference

- The reference rejects unknown fields when it reads a JSON document, but it does not enforce `required`, enums or string patterns (the cause of bead a2dlc-5fq). A receipt missing `created_at` is accepted by `a2dlc verify` although §6.4.3 says to reject it. `conformance/` marks such vectors as expected failures of the reference.
- The reference matches object keys case-insensitively (`"Target"` is read as `target`), and reads `null` or a missing string as the empty string, in `waivers.json` as in every document it decodes. The schemas reject both; a verifier that validates against the schema is the stricter one, and a producer MUST NOT rely on either.
- On a repository with `core.ignorecase=true`, git matches pathspecs case-insensitively in some commands. The reference was exercised on git 2.51.0 with `core.ignorecase` both `true` and `false` and excluded the same paths, so §6.4.2 holds there; other git versions are not covered.
- A working tree that holds another repository's checkout inside the repository (a nested worktree that is not tracked) enters the preview and makes the receipt stale at verification (bead a2dlc-3hd).

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
| v3.5 | The receipt (§6.4) is rewritten as a normative section from the schemas in `contracts/` and the reference implementation:<br>- `tree_hash` is `git-tree:<hex>`, the tree of a commit minus five excluded paths (§6.4.2), not `sha256:`.<br>- The bytes of the waiver hash and the signed approval text are fixed (§6.4.6, §6.4.7).<br>- Notes, `critical_code`, harness errors and what the receipt does not claim are stated (§6.4.4, §6.4.5, §6.4.9).<br>- The deviations of §6.4 from v3.4 are folded in (§6.4.10) and removed from the coverage matrix.<br>- One sentence of §4.4 follows it: the `.a2dlc/flaky.yaml` quarantine is the `quarantine_flaky` waiver.<br>- `contracts/tests/check_rfc.py` fails if §6.4 contradicts a schema. |
