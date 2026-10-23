# AI-DLC v2 sensors over the a2dlc measurement sets (run 2026-10-23)

Question: what does the verification layer of AWS's AI-DLC v2 ("an adversarial verifier backed by a deterministic
checker") decide on the same data a2dlc and ADLC self-verification were measured on? Its deterministic half is the
**sensor system**: six Markdown manifests under `core/sensors/`, fired by the engine's dispatcher at a stage gate
(`aidlc engine sensor fire <id> --stage <slug> --output-path <file>`). This folder runs that dispatcher, from the
framework's own distribution, over the 9 frozen gates-py cases and the agent patches of the 10 SWE-bench Verified
instances of VARA-2.

Source: `awslabs/aidlc-workflows` at `306bd83` (main, 2026-10-23), distribution built with `bun scripts/package.ts`
(bun 1.4.4 in `oven/bun:1`), installed layout `dist/claude/.claude`. Everything ran with `--network none`.

## Result

| set | fires (file × sensor) | sensor ran | gate outcome |
|---|---|---|---|
| 9 frozen cases, 14 changed files | 84 | 0 | opens for all 9: the 5 blocking cases go through, the 4 good ones too |
| 156 agent patches, 10 instances, 180 unique files | 1,080 | 0 | opens for all 156, so for the 38 measured in VARA-2 too: 0 of 18 rejected caught, 0 of 20 good ones flagged |
| `probe.ts`, a file that matches the two code sensors | 12 | 2 (`linter`, `type-check`) | both `"result":"passed"` with `note: tool-unavailable` / `script-error: exit-1` |

Every one of the 1,164 fires on the measurement sets was rejected before any sensor process started, with the
dispatcher's own message, for example:

    aidlc-sensor: output path "/t/C6-regresion/src/calc.py" does not match sensor "linter" filter "**/*.{ts,js}"

The six manifests filter by path: `linter` `**/*.{ts,js}`, `type-check` `**/*.{ts,tsx}`, `traceability`
`**/traceability.json`, and `claim-sources`, `required-sections`, `upstream-coverage` `**/{aidlc-docs,intents,codekb}/**`.
The files in both sets are `.py` (171 of the 180 unique patch files; the rest `.rst`, `.md`, `.txt`), so no sensor
applies. The `type-check` script says it itself: "v0.5.0 defaults to tsc; multi-language auto-detection (mypy, go vet,
cargo-check) is deferred to v0.6.0+".

Two more facts from the code, confirmed by the probe:

- A sensor that cannot run **passes**. The dispatcher's truth table (`core/tools/aidlc-sensor.ts`, header) maps exit 127
  to `PASSED tool-unavailable` and any other non-zero exit to `PASSED script-error: exit-<n>`; "Sensor failure ≠ CLI
  failure". The probe shows both: no eslint in the sandbox → `passed`, no `tsconfig.json` → `passed`.
- All six manifests ship `default_severity: advisory`: an advisory finding is recorded and the gate still opens
  (`docs/harness-engineering/06-sensors.md`). The shipped stage graph binds them 79 times over 33 stages (`linter` in 5
  stages, `type-check` in 6, `required-sections` 30, `upstream-coverage` 29, `traceability` 8, `claim-sources` 1).

None of the six executes the old and the new code: the four document sensors check headings, ID cross-references and
source tags in AI-DLC's own artifacts; the two code sensors wrap a linter and a type checker. That is the gap between
AI-DLC's stage 03 (build) and 04 (oracle) that the a2dlc gate fills, and the place where an `aidlc-a2dlc` sensor plugs in
(bead a2dlc-59z).

## Head to head on the two sets

| | ADLC self-verification | AI-DLC v2 sensors | a2dlc |
|---|---|---|---|
| 9 frozen cases, 5 must block | lets 3 of 5 through (changed-files reading), 2 of 5 (whole suite) | lets 5 of 5 through (no sensor applies) | blocks 5 of 5, passes 4 of 4 |
| 38 agent patches, 18 rejected | 0 of 18 (by construction) | 0 of 18 (no sensor applies) | 18 of 18 with a peer, 12 of 18 INCONCLUSIVE without one |

## Limits

- The sandbox had no network, so `bunx eslint` and `bunx tsc` could not be fetched: the probe measures the
  cannot-run semantics, not the quality of the two code sensors on a real TS/JS project.
- The dispatcher was invoked by hand at stage `build-and-test`, not through a full `/aidlc` session. The `matches`
  filter it applied is the same one the engine's PostToolUse hook and the gate apply (comment at the filter in
  `aidlc-sensor.ts`): a human-callable invocation "can't bypass the shape contract".
- The 38 patches measured in VARA-2 are a subset of the 156 re-downloaded here (the per-instance labels of that run
  are not in the repo); since 156 of 156 had no sensor run, the subset has none either.
- AI-DLC is a methodology with human gates; this measures only its deterministic layer. Its adversarial verifier is an
  agent and was not run.

## Reproduce

    bash e2e/aidlc-sensors/build_trees.sh <empty folder>/trees        # 9 trees + tree-files.tsv
    git clone --depth 1 https://github.com/awslabs/aidlc-workflows <folder>/aidlc-src && cd <folder>/aidlc-src \
      && docker run --rm -v "$PWD:/a" -w /a oven/bun:1 sh -c 'bun install --frozen-lockfile && bun scripts/package.ts'
    python -I e2e/bench-swe-abs/patches.py <downloads> <folder>/work <the 10 instance ids in RESULTS.md of bench-swe-abs>
    # then curl each line of work/pending.txt, and:
    docker run --rm --network none -v "<folder>/aidlc-src/dist/claude/.claude:/dist:ro" -v "<folder>/trees:/t:ro" \
      -v "<folder>/results:/out" -v "$PWD/e2e/aidlc-sensors/fire_sensors.sh:/m.sh:ro" oven/bun:1 sh /m.sh
    python -I e2e/aidlc-sensors/tabulate.py <folder>/results

`results/` holds the raw rows of this run (`trees.tsv`, `probe.tsv`, `swe.tsv.gz`, the file lists).
