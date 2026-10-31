# Divergence notes: a behavior change is a note unless the code is critical

Bead a2dlc-ff2. Owner decision of 2026-10-26 (option B).

## Decision

The differential engines compare each changed function on the base and on the candidate and, when the outputs differ,
return a counterexample (`FAIL`). Taken strictly, a feature that changes an output on purpose is `FAIL` until a person
signs a waiver. Measured on agencia-ia 60f5c6b: `construir_informe` gains the key `no_evaluado`, the verdict is `FAIL`, and
the owner would sign almost every feature commit.

Now a divergence with a counterexample is a **note** on its target in the receipt and does not make the verdict `FAIL`,
unless the code is declared critical. On critical code it stays `FAIL` until a signed waiver, as before. The risk the owner
accepts: an accidental behavior change in non-critical code passes without a signature. It stays visible, with its
counterexample, in the receipt and in the `a2dlc check` output.

## Declaring critical code

`critical_code` in `.a2dlc/config.json`: a list of globs over `file` or `file::symbol`.

```json
{"schema_version": 1,
 "engines": [{"language": "python", "image": "a2dlc-engine-py:0.7", "paths": ["app/**"], "budget": {}}],
 "critical_code": ["app/domain/billing/**", "app/domain/valor.py::calcular_*", "app/auth/*.py"]}
```

- A glob without `::` names files (the syntax of `paths`) and covers every symbol in them.
- `file::symbol` matches the file part and the symbol part separately; `*` in the symbol never crosses a `/`.
- Absent or empty: nothing is critical, and every divergence is a note.
- The name is not `critical_paths` (that key of `intent.json` raises the criticality of a slice in `a2dlc run`) nor
  `engines[].budget.critical` (a time budget).
- Like the rest of the config, `check` reads it from the base: a diff cannot drop its own critical code.

## What changes in the verdict

| Target | Critical code? | Waiver | Result |
| --- | --- | --- | --- |
| `FAIL` with counterexample | no | none | note; the verdict ignores it |
| `FAIL` with counterexample | yes | none | `FAIL` |
| `FAIL` with counterexample | yes | signed | passes (as before) |
| `FAIL` with counterexample | yes | unsigned | passes in `check`, pending; `verify` blocks |
| `FAIL` without counterexample | any | none | `FAIL` (a note needs the evidence) |
| `INCONCLUSIVE` | any | | unchanged |

A waiver on a non-critical divergence is honored as before (`waived: true`, no note), and it still needs its signature on
`verify`. A divergence note asks for no waiver and produces no escalation.

## Receipt

`targets[].note` (optional; absent in older receipts) is `"behavior_divergence"`. The target keeps `verdict: FAIL` and its
counterexample. It is a category of its own, apart from `coverage.files[].noted`:

```json
{"verdict": "PASS",
 "targets": [{"target": "app/domain/valor.py::construir_informe", "engine": "crosshair-diffbehavior", "engine_version": "0.7.0",
   "verdict": "FAIL", "search_budget": {"iterations": 0, "per_condition_timeout_s": 10}, "generators": "typed_default",
   "typed": true, "domain_guard": null, "equivalence": "strict",
   "counterexample": {"args": "{\"evaluados\": []}", "old": "{\"total\": 0}", "new": "{\"total\": 0, \"no_evaluado\": []}"},
   "waived": false, "note": "behavior_divergence"}]}
```

`a2dlc check` prints one `note:` line per divergence note.

## verify

The receipt is a file an agent could edit, so `a2dlc verify` does not take a note at its word. For each note it checks that
the target is a `FAIL` with a counterexample and no waiver, and decides again whether the code is critical with the
`critical_code` of the config at the receipt's `base_ref`. A note on critical code is a problem ("has no waiver"); so is a
note it cannot confirm because that config cannot be read.

## `a2dlc run` is strict unless the config says otherwise

Stage 4 of `a2dlc run` ranks the candidates by their verdict, and a divergence from the base is how a candidate that
changes behavior is told apart from one that does not. It runs `check` with `StrictDivergence`, which treats every target as
critical, so by default it keeps today's behavior. Without that, `TestN2OnlyOnePassesIsChosen` and
`TestN2NonePassesWorstRemains` fail: both candidates would pass.

A consumer that signs no waivers (an autonomous pipeline) cannot live with that: every intended behavior change on
engine-covered code would be rejected. `config.run_divergence` (`strict`, the default, or `critical_code`, read from the
base's config) lets it choose. With `critical_code`, a run judges a divergence as `check` does: a `FAIL` only on
`config.critical_code`, a note on its target elsewhere. The builder has been sent each divergence once by then (Stage 3
returns the engines' divergences, once per function and input), so a note is a divergence it saw and kept. In the N=2
selection, candidates with the same verdict are ranked by their number of divergence notes, fewer first, before complexity.
The field only sets the severity of an engine divergence; a `FAIL` without a counterexample, a failing test and every other
gate are judged as before.
