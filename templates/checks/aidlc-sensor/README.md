# AI-DLC v2 sensors as a2dlc checks

The six deterministic sensors of AWS AI-DLC v2 (`awslabs/aidlc-workflows`, commit `306bd83`), fired by their own
dispatcher inside an a2dlc pluggable check (`config.checks`, protocol §5 Checks). a2dlc stays independent: the image
holds the framework, `adapter.ts` translates its verdict line into `check-response.schema.json`, and the core applies
its own truth table. Measured in `e2e/checks/RESULTS.md`.

## Install

    docker build -t a2dlc-check-aidlc-sensor:0.1 templates/checks/aidlc-sensor

The build clones the pinned commit and packages its `dist/claude/.claude` into the image (`bun scripts/package.ts`);
the check then runs with `--network none`. `--build-arg AIDLC_REF=<sha>` pins another commit.

## Configure

One check per sensor, with the manifest's `matches` glob expanded (a2dlc globs have no braces):

```json
"checks": [
  {"id": "aidlc-linter", "image": "a2dlc-check-aidlc-sensor:0.1", "command": "bun /adapter.ts linter",
   "severity": "blocking", "matches": ["**/*.ts", "**/*.js"], "timeout_s": 300},
  {"id": "aidlc-type-check", "image": "a2dlc-check-aidlc-sensor:0.1", "command": "bun /adapter.ts type-check",
   "severity": "blocking", "matches": ["**/*.ts", "**/*.tsx"], "timeout_s": 300},
  {"id": "aidlc-traceability", "image": "a2dlc-check-aidlc-sensor:0.1", "command": "bun /adapter.ts traceability",
   "severity": "blocking", "matches": ["**/traceability.json"], "timeout_s": 300},
  {"id": "aidlc-required-sections", "image": "a2dlc-check-aidlc-sensor:0.1", "command": "bun /adapter.ts required-sections",
   "severity": "advisory", "matches": ["**/aidlc-docs/**", "**/intents/**", "**/codekb/**"], "timeout_s": 300},
  {"id": "aidlc-upstream-coverage", "image": "a2dlc-check-aidlc-sensor:0.1", "command": "bun /adapter.ts upstream-coverage",
   "severity": "advisory", "matches": ["**/aidlc-docs/**", "**/intents/**", "**/codekb/**"], "timeout_s": 300},
  {"id": "aidlc-claim-sources", "image": "a2dlc-check-aidlc-sensor:0.1", "command": "bun /adapter.ts claim-sources",
   "severity": "advisory", "matches": ["**/aidlc-docs/**", "**/intents/**"], "timeout_s": 300}
]
```

`AIDLC_STAGE` (default `build-and-test`) is the stage slug the dispatcher is told; `severity` is the repo's decision,
there is no default. The linter and type-check sensors need `eslint` and `tsc` in the image: this one does not ship
them, so on a `.ts` file they report `tool-unavailable`, which here is an error (below), never a pass.

## What changes against AI-DLC's own binding

| AI-DLC v2 (`core/sensors`, `06-sensors.md`) | Here |
|---|---|
| `default_severity: advisory` on all six: a finding, a missing tool or a script error is `PASSED` with a note | `severity` is required; a finding under `blocking` is FAIL, under `advisory` it is recorded in `verdict.checks` |
| `tool-unavailable`, `script-error` and a timeout are a pass under advisory | They are `error` or `timeout` in `verdict.checks` under both severities; under `blocking` the verdict is INCONCLUSIVE |
| A file outside the sensor's `matches` is refused by the dispatcher (exit 1) and nothing is recorded | 0 matching files is `not_applicable` with the count in the verdict; a file the dispatcher's own filter rejects counts the same way |
| Override: a logged human decision at the gate | Override: a signed `accept_finding` waiver on `<file>::<sensor id>`, bound to the file's blob |
| Binding on the stage side (`sensors:` in the stage frontmatter, recompiled) | Binding in the repo's config (`fire_on`) |

## Finding target

`<file>::<sensor id>` (one per file the sensor failed on); the message is the sensor's detail file (its `findings`
messages when it is JSON, else its text).

## Measured (2026-10-23, `bash e2e/checks/measure_aidlc_adapter.sh <empty folder>`, `results/aidlc-adapter.tsv`)

| trees | config | result |
|---|---|---|
| the 9 frozen `gates-py` cases | six checks, blocking, with the manifests' globs as `matches` | 54 of 54 entries `not_applicable` with the count (`0 of 2 changed files match **/*.ts, **/*.js`); 9 of 9 PASS. The same fact `e2e/aidlc-sensors/RESULTS.md` found through AI-DLC's dispatcher (0 of 84 fires applied), now readable in the verdict |
| C1 plus `probe.ts`, no `matches` | the six receive all 3 files | `linter`: **error** (`sensor linter did not run on probe.ts: tool-unavailable`); `type-check`: **error** (`script-error: exit-1`); the other four `not_applicable` (the dispatcher's own filter); verdict **INCONCLUSIVE**, exit 2. Under AI-DLC's advisory default both were `PASSED` |
| C1 plus `aidlc-docs/intent.md`, no `matches` | the six receive all 3 files | `upstream-coverage`: ran, **1 finding** (`aidlc-docs/intent.md::upstream-coverage`, escalation `check_failed`, resolved only with a signed `accept_finding`); `claim-sources` and `required-sections`: ran, 0 findings; the three code sensors `not_applicable`; verdict **FAIL**, exit 1 |

The 22 verdict and escalation files validate against `contracts/` (0 failures).
