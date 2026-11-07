# RFC 0008: Feature plans: deterministic decomposition of a feature into slices

- **Status:** Draft
- **Comment period:** 2026-11-05 to 2026-11-19 (14 days, [README.md](README.md) step 2); comments on the pull request that opened it
- **Author:** Aldemar Hernández, falsable
- **Amends:** RFC-0001 §3 (a Planner role), §5 (a plan machine above the slice machine), §7 (CB-02b decomposition); new `contracts/plan.schema.json` and `contracts/plan-state.schema.json`; `boundary.allowed_paths` in `contracts/intent.schema.json`
- **Changes `schema_version`:** no

## Problem

`a2dlc run` takes one slice from intent to delivery. Everything above a slice is left to a person:

- **Decomposition.** A feature arrives as one intent. Someone splits it into slices, decides their order and keeps their files apart. Nothing checks the split.
- **Order and integration.** Slices that depend on each other must run one after another, each on a base that carries what the earlier ones integrated. The binary has no notion of that; the factory that did it is archived, and only a note in `internal/run/header.go` remains.
- **CB-02b.** When every Builder exhausts its rounds and `tier_up` fails, the protocol's answer is "decompose the slice", and the run stops at a person (§7; `cb02b.go`).

The tools that do decompose (Spec Kit's `tasks.md`, Task Master's `tasks.json`, BMAD's stories) produce prompts and lists. None checks that two tasks marked parallel never touch the same file, that the dependency graph has no cycle, or that the budgets add up. They trust the LLM at the end of the turn, which is the trap this protocol exists to close (§1).

## Proposal

A **plan** is a set of slices with the files each one owns and the slices it depends on. A Planner proposes it; deterministic gates judge it; a person approves it once, as one readable decision; a plan machine runs its slices in dependency order, each through the existing slice machine.

### 1. The plan (`contracts/plan.schema.json`)

```json
{
  "schema_version": 1,
  "feature_id": "billing.discounts",
  "intent_sha256": "<64 hex of the feature intent>",
  "budget": {"max_cost_usd": 3.0},
  "slices": [
    {"slice_id": "billing.discounts.model", "goal": "...", "owns": ["src/billing/discount.py"], "depends_on": [],
     "criticality": "high", "budget": {"max_cost_usd": 1.0}, "acceptance": ["..."], "invariants": ["..."]},
    {"slice_id": "billing.discounts.api", "goal": "...", "owns": ["src/api/discounts.py"], "depends_on": ["billing.discounts.model"],
     "criticality": "medium", "budget": {"max_cost_usd": 0.8}, "acceptance": ["..."], "invariants": []}
  ],
  "shared": []
}
```

- `owns` are globs of the files the slice may change. Stage 3 of that slice enforces them through the existing boundary: a change outside `owns` escalates `protected_boundary` (CB-04), with no retry.
- `shared` lists files more than one slice may change. Only slices ordered by `depends_on` may share one, never two that can run at the same time.
- Each slice becomes a slice intent (`contracts/intent.schema.json`) derived from the plan: goal, invariants, acceptance, criticality, budget, and `boundary.allowed_paths` set to the slice's `owns` (a new optional field: every file the Builder changes must match one of its globs).

### 2. Who proposes it: the Planner role (§3)

| Role | Sees | Produces | Forbidden |
|---|---|---|---|
| **Planner** | Feature intent + the code graph's files, public signatures and blast radius | A candidate plan | Builder code, other plans' reasoning |

Like the Specifiers, two Planners from different model families may write `P_α` and `P_β`. The core compares them structurally (slices, owns, edges). Each difference becomes a concrete probe for the person, in the form §2.4 requires ("α puts `discount.py` and `api.py` in one slice, β in two; [A] one slice / [B] two").

**The Planner is replaceable.** A Spec Kit `tasks.md` or a Task Master `tasks.json` can be imported as a candidate plan (`a2dlc plan import`). It then goes through the same gates. This is how the protocol absorbs existing decomposition tools instead of competing with them: they propose, a2dlc judges.

### 3. The plan gates (deterministic, before any Builder token)

A plan is rejected with the first rule it breaks, and the reason names the slices:

1. **Shape:** the plan validates against its schema, `slice_id`s are unique, and every `depends_on` names a slice of the plan.
2. **No cycle:** the dependency graph is acyclic (topological sort).
3. **Disjoint concurrent slices:** two slices with no path between them in the graph own no file in common, after glob expansion over the base tree and the files the plan creates. This is Spec Kit's `[P]` rule ("touches different files and does not depend on an unfinished task"), made a check instead of a marker.
4. **Shared files are ordered:** every file in `shared` is owned only by slices that lie on one path of the graph.
5. **Coverage of the intent:** every acceptance criterion of the feature intent is assigned to at least one slice.
6. **Budget:** the slices' budgets add up to no more than the feature's.
7. **Slice intents validate:** each derived slice intent validates against `intent.schema.json`.
8. **Blast radius:** a slice whose `owns` reaches a file another slice's code imports declares a `depends_on` edge to it, or the gate names the missing edge (from the engines' `deps_graph`, when the engine has it).

### 4. The person's decision

`a2dlc plan show` renders the plan for a person: one line per slice saying what it changes and why, the order as an indented tree, the budget, and the probes still open. `--json` gives the same plan to an agent or a UI, compact, as `approve --pending` does (RFC-0007).

The person approves the plan once. The approval is an SSH signature under the namespace `a2dlc-plan` over the plan's SHA-256 and the base commit. A plan edited after approval no longer matches its signature.

A plan can also be accepted without that signature by an authorization (RFC-0007 Part B) of change class `plan.accept`: the same signing as any authorization, with `decision: plan.accept`, the target `plan::<feature_id>`, the feature intent's `intent_sha256` and the feature's `base_ref`. It covers whatever plan passes the gates for that feature, intent and base until it expires, and it never covers a waived engine target.

### 5. The plan machine (§5)

Each slice has a plan status: `pending → ready → running → passed | escalated | failed`, and `blocked` when a slice it depends on did not pass.

- **P1.** A slice is `ready` when every slice it depends on has `passed`.
- **P2.** A ready slice runs as an ordinary `a2dlc run` on a base that is the feature's base with the passed candidates of its dependencies merged, in topological order and ties broken by `slice_id`. Ready slices with no path between them may run at the same time, each in its own worktree.
- **P3.** A slice that ends `escalated` or `failed` blocks every slice that depends on it, transitively, and nothing else. Its siblings go on.
- **P4.** The plan's outcome is `PASS` when every slice has passed and the merge of all candidates is the tree a final `a2dlc check` against the feature's base judges `PASS`. Otherwise it is the worst slice outcome.
- **P5, CB-02b:** a slice whose Builders are exhausted is sent back to the Planner once, to be decomposed into sub-slices that own a partition of the exhausted slice's `owns` and keep its edges. The new sub-plan goes through the gates of §3 and the person's approval. The depth is bounded (`max_replan`, default 1). Past it, CB-02b stops at a person, as today.
- **P6.** Resume follows the slice machine's rules: a passed slice whose run still verifies (`verify-run`) is kept, and a slice re-run makes every slice that depends on it run again (the plan-level R3).

The plan machine is written like the slice machine (`internal/run/fsm.go`): as data and a pure function, rendered to `docs/PLAN_MACHINE.md`, model-checked to a fixpoint and held to the implementation by a conformance test. Its invariants:

- no slice runs before every slice it depends on has passed;
- a failure blocks exactly the slices that depend on it;
- two slices that can run at the same time have disjoint files, so their merge does not depend on the order they finished in;
- with `max_replan` bounded, every plan terminates.

### 6. Tracking

The plan is the source of truth. `a2dlc plan export --beads` writes one issue per slice with its dependencies, so `bd ready` shows the same ready set as P1. Beads is a view of the plan, never an input to it.

## Implementation ahead of acceptance

As RFC-0004 did for its opt-in, two new commands are in the reference implementation. They change nothing else: a repository that never runs them is unaffected. If the RFC is rejected, the commands, `internal/plan`, both schemas and `boundary.allowed_paths` are removed.

**Section 3, `a2dlc plan check`.** Rules G1 to G7 are implemented. G8 (blast radius) is reported as not checked until the command runs the engines' `deps_graph`. The glob rule of G3 is conservative: two globs whose fixed directories contain one another are taken to overlap even when no file matches both yet, because accepting two concurrent slices on one future file is the failure the gate exists to prevent.

**Section 5, `a2dlc plan run`.** One invocation runs every slice it can, one after another, and stops; running it again resumes. Its rules, invariants and checks are in [docs/PLAN_MACHINE.md](../docs/PLAN_MACHINE.md), generated from `internal/plan/fsm.go`: a model checker over the 64 dependency graphs on four slices (118,650 states, every slice outcome, the final check passing or not, a passed slice that stops verifying), a conformance test that drives the real runner with real git merges against the model, and an end-to-end test through real `a2dlc run`s with a fake Builder and engine. How it maps to this section:

- `ready` and `running` are not stored: within one invocation a slice is ready when its dependencies passed and it runs at once. The stored statuses are `pending`, `passed`, `escalated`, `failed` and `blocked` (`contracts/plan-state.schema.json`).
- P1 to P4 and P6 are implemented as written, with two decisions the text left open. The outcome is `escalated` when some slice escalated or the final merge or check did not pass, and `INCONCLUSIVE` when some slice failed and none escalated (the worst outcome, as `a2dlc run` ranks them). A slice whose dependencies do not merge escalates without launching a Builder.
- The state and the final receipt live in `.a2dlc/run/_plan/<feature>/`, under `.a2dlc/run`, so `gitx.Excluded` already keeps them out of the `tree_hash` and §6.4 does not change. A slice id starts with a letter, so `_plan` is never a slice's run.
- Merges are `git merge-tree` commits with a fixed author and date: the same base and candidates always give the same commit, so a resumed slice finds its run on the base it ran on. A single dependency is a fast-forward.
- Slices with no path between them run at the same time (P2), each in its own worktree inside the git directory. How many at once is measured, not configured: the host that runs a2dlc and the Docker daemon that runs the containers (with Docker Desktop, its VM) are measured at the start of each invocation, and a slice's peak is read from the limits `roles.json` and `config.json` already declare for its roles, engines, tests and checks. `--jobs` overrides it, and the state records the decision. Because a slice's outcome depends only on how the slices it depends on settled, the concurrent schedule gives the statuses of the sequential model; the conformance test drives the runner with 1 to 3 slots against it.
- Section 2: the planner role (`a2dlc plan propose`) proposes, the gates judge, and a plan that breaks a rule goes back with the findings for at most `--rounds` rounds (`role-payloads.schema.json` planner_request and planner_response). `a2dlc plan import` takes a Spec Kit `tasks.md` or a Task Master `tasks.json` as the candidate instead; with two parallel tasks on one file, both are rejected by G3, as the Verification section asks. The two-planner comparison (P_α and P_β) is not implemented.
- P5, CB-02b: a slice whose Builders are exhausted is marked in the state; `a2dlc plan run --max-replan` (default 1) asks the planner to split it, and `a2dlc plan replan` does it for one slice. A split must pass four replan rules (R1 to R4: only the slice changes, its sub-slices own a partition of its files, depend only on its dependencies or each other, and every former dependent depends on the sub-slices' sinks) and the gates. It is written next to the plan and running it is the person's decision; a new version of the plan keeps the state of every slice it did not change, transitively (`definition_sha256`).
- Section 4, the signed acceptance, is implemented. `a2dlc plan approve --plan <plan.json> --expires <RFC 3339> --key <key> [--principal <id>] [--base <ref>]` signs, under the namespace `a2dlc-plan`, the text `a2dlc-plan/1`, the plan file's bare sha256, the base commit and the expiry, one per line each ended by a newline, checks the signature against the allowed signers (never the repo) and writes `.a2dlc/run/_plan/<feature>/approval.json` (`plan_approval` in `contracts/plan-state.schema.json`). The expiry is required, as for an authorization; the passphrase is asked by `ssh-keygen` and never reaches a2dlc. `a2dlc plan run` refuses a plan with neither that approval (for this plan's sha256, this base, not expired) nor an authorization in `.a2dlc/authorizations.json` whose `decision` is `plan.accept` (target `plan::<feature_id>`, this base, the feature intent's hash, not expired), and says why for each it rejected. The state records what accepted the plan in `acceptance` (kind `approval` or `authorization`, the whole signed record and the time of use). `a2dlc verify-run --plan <feature>` checks it again against the allowed signers and the state's own plan hash, base and feature: it fails when the acceptance is missing, altered, had expired when it was used, or belongs to another plan, feature or base. The state is a file in the run's folder, so verify-run rejects a run that was never accepted or whose acceptance was altered, not one whose whole folder was rewritten; for an authorization it cannot compare the feature intent, which the state does not hold, and relies on `plan run` having done it. A state written before this section has no `acceptance`, so verify-run rejects it and the next `plan run` needs a signature.
- Not implemented yet: `plan show`, `plan export --beads`, and G8.

## Compatibility

- `a2dlc run` on one slice does not change. A plan is a new artifact, a new command family (`a2dlc plan`) and a new role.
- New schemas, and one new optional field in an existing one (`boundary.allowed_paths` in `intent.schema.json`); no existing field changes. Per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md), no `schema_version` bumps.
- CB-02b keeps its current behavior unless the run belongs to a plan with `max_replan` above 0.

## Alternatives

- **Keep decomposition with the person.** The status quo: every feature costs a person the split, the order and the integration, with nothing checking any of it.
- **Use Spec Kit or Task Master as they are.** Their output is a proposal the LLM wrote and the LLM trusts. This RFC keeps them as proposers and adds the gates they lack.
- **One big slice per feature.** It defeats Locality of Behavior (§2.1), puts the whole feature on one Builder's context and makes CB-02b more likely.
- **Let slices share files freely and resolve merge conflicts afterwards.** A conflict found after two Builders spent their budgets is the expensive failure; disjoint `owns` makes it impossible for slices that run at the same time.

## Verification

- **Gates:** frozen plan fixtures (valid, cyclic, overlapping concurrent `owns`, unordered `shared`, budget over the feature's, uncovered acceptance criterion, missing blast-radius edge), each rejected with the rule it breaks; the valid one accepted.
- **Plan machine:** the model checker explores the plan machine for up to four slices in every graph shape (chain, fork, join, diamond), with every slice outcome, and checks the invariants above; the conformance test drives the real plan runner with scripted slice runs.
- **End to end:** one feature with three slices (two concurrent, one depending on both), fake roles and no model, ends `PASS` with the merged tree checked once more; the same feature with an exhausted slice replans once, then stops at CB-02b.
- **Import:** a Spec Kit `tasks.md` and a Task Master `tasks.json` with overlapping parallel tasks are imported and rejected by rule 3.

## Open questions

- Should every plan need a person's approval, or only plans whose feature criticality is above `low`?
- How fine should slices be? Task Master scores complexity 1 to 10 and expands above a threshold; the Router's `rule_count` and `state_mutation_depth` (Stage 2) could play that role here.
- Plans across repositories (one slice per service) need the cross-service graph, which stays outside the binary (Stage 6).

## Prior art

- [Spec Kit `tasks.md`](https://skills.cat/skills/kbrdn1/dotfiles/speckit-tasks): phases, `[P]` for tasks on different files with no unfinished dependency, exact file paths per task.
- [Task Master task structure](https://docs.task-master.dev/capabilities/task-structure): dependencies by id, `testStrategy`, an AI complexity score that drives `expand` into subtasks, and `validate-dependencies` for circular or invalid dependencies.
- Beads: the dependency graph and the ready set this repository already tracks its own work with.
