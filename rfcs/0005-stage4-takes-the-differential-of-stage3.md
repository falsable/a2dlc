# RFC 0005: Stage 4 can take the differential Stage 3 already ran on the same tree

- **Status:** Draft
- **Author:** Aldemar Hernández, falsable (bead a2dlc-97y)
- **Amends:** RFC-0001 §5 Stage 4 (step 3, the differential), `contracts/config.schema.json`; the description of `timings.engine_ops` in `contracts/verdict.schema.json`
- **Changes `schema_version`:** no

## Problem

In `a2dlc run`, the engines' differential runs twice on the same base and the same candidate. Once Tier 0 and the lemmas accept a Builder round, Stage 3 runs the differential of `config.engines` on that round's tree against the run's base, to send the Builder each divergence it finds (`App.Divergences`, the operation `diff`). The candidate of Stage 3 is the tree of the round it accepted, committed as it is, and Stage 4 runs the same operation on it with the same request: the same engine entry, base, changed files, budget and generators. The only field that can differ is the equivalence relaxations of Stage 1.5, which Stage 4 sends and Stage 3 does not.

Measured on a replay of the sixth factory freeze at its scale, without a model (each lane's recorded builder diff applied by a scripted builder in its first round, `a2dlc run --mode lite` as the factory runs it), with the implementation of RFC-0004 on:

- on the repo whose engine is the Python differential, 6 lanes: the differential took 146 s in Stage 3 and 151 s in Stage 4, out of 612 s of a2dlc time; in Stage 4 it is the critical path, and the other Stage 4 work (stubs, then the NOP run on the base with stubs) takes about 62 s;
- on the repo whose engine is the TypeScript one, 5 lanes reached Stage 4: the differential took 30 s there and is not the critical path.

## Proposal

A new optional top-level field in the config:

```json
"run_differential": "fresh" | "stage3"
```

- `fresh`, the default and what an absent field means: Stage 4 runs the differential itself, as today.
- `stage3`: for each engine, Stage 4 takes the answer Stage 3 got on the Builder's last round instead of running the engine, when the candidate is that round's tree and Stage 4's request to that engine is byte-identical to Stage 3's. An engine without such an answer (it failed in Stage 3, or Stage 4 sends relaxations Stage 3 did not) runs as with `fresh`. The receipt lists a taken answer as the engine operation `diff_from_stage3` with 0 ms, so a reader can tell which verdicts rest on Stage 3's search.

Everything Stage 4 does with an engine answer applies to a taken one unchanged: the diff budget and its cut list, unsupported relaxations, `critical_code` and `run_divergence`, the coverage precondition. A Saboteur round that judges the same candidate again takes the same answer.

The answers are held in memory by the run: a run resumed between Stage 3 and Stage 4 runs the differential again. Nothing is written to a cache that could be planted.

The field is read from the base's config, like the rest, so a diff cannot turn it on for itself. Any other value is refused when the config is read, as `run_divergence` is.

Why it is opt-in: the engines' search is bounded by time (`per_condition_timeout_s`, `total_s`), so two runs of the same request can explore a different number of inputs. Today a divergence that Stage 3's search missed can still be found by Stage 4's. With `stage3`, the verdict rests on one search with the configured budget, which is what `a2dlc check` gives every verdict today. Over the recorded run and up to three replays of the Python repo's lanes, 7 of 8 targets got the same verdict in every run; one got PASS in two runs and FAIL with a counterexample in the third, on code that is not critical, so it was a note and no lane's verdict changed.

## Implementation ahead of acceptance

The reference implementation carries this behavior behind the opt-in before the RFC is accepted, so that the factory can measure it. A config without the field behaves exactly as before. The protocol text in `docs/A2-DLC_v3.5_protocol.md` is marked as proposed until the 14-day comment period of [README.md](README.md) ends and the maintainer decides. If the RFC is rejected, the commit is reverted, together with the schema field and the protocol sentence.

## Compatibility

No existing document becomes invalid: the field is optional and an absent field keeps today's behavior. Per [docs/COMPATIBILITY.md](../docs/COMPATIBILITY.md) an added optional field does not bump `schema_version`. A binary older than this RFC rejects a config that sets the field, because configs are decoded strictly. The receipt gains no field: `timings.engine_ops` already takes any operation name, and only its description widens.

## Alternatives

- **A disk cache of engine answers keyed by the request, like the suite cache.** It would also serve resumed runs and repeated checks, but an engine answer decides the verdict directly, and a cache outside the run is one more thing that could be planted or left stale. The in-memory handoff covers the measured duplicate.
- **Turn it on by default.** It would remove the second independent search for every repo; the default stays two searches until the comment period decides whether one is enough.
- **Skip Stage 3's differential on the last round instead.** On the last round a divergence is only a note, but Stage 3 does not know which round will be accepted until the differential has run, and the round before the last still needs it to send feedback.
- **Do nothing.** Each accepted lane keeps paying the differential twice; on the Python repo that is about a quarter of a2dlc's time.

## Verification

- Unit tests (`internal/app/differential_reuse_test.go`):
  - with `stage3`, `Check` after `Divergences` on the same tree does not call the engine, judges Stage 3's answer, lists `diff_from_stage3`, and a second check of the same tree still takes it unchanged;
  - without the field, with `fresh`, on another tree, and with relaxations in Stage 4, the engine runs again and the receipt lists `diff`;
  - an unknown value is refused when the config is read.
- Run test (`internal/run/divergence_test.go`): with `stage3`, a run whose builder keeps a divergence is judged by Stage 4 without asking the engine, with the same strict `FAIL` and counterexample as without the field.
- Schema examples: valid `config__run-differential-stage3.json`; invalid `config__run-differential-unknown.json`.
- Not measured yet: the replay of the sixth freeze with the field on.
