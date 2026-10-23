# Pluggable checks fired by the real binary over the 9 frozen cases

Measured 2026-10-23 with `dist/a2dlc.exe` built from this tree (`bash e2e/checks/measure_checks.sh <empty folder>`),
over the 9 frozen `e2e/testdata/gates-py` cases, each as a tree with the base committed and the candidate uncommitted,
with `engines: []` so that only `config.checks` decides. One throwaway image (`alpine:3.20` plus `grep-check.sh`)
and five checks per tree:

| id | command | severity | matches | what it proves |
|---|---|---|---|---|
| `grep` | `/check.sh`: a finding per line with `raises(Exception)` or `import os` | blocking | `**/*.py` | a finding lowers the verdict to FAIL |
| `grep-advisory` | the same | advisory | `**/*.py` | advisory is recorded only |
| `md-only` | the same | blocking | `**/*.md` | 0 matching files is `not_applicable`, with the count |
| `missing-tool` | `no-such-tool --version` (exit 127) | advisory (blocking in the extra C1 run) | all | a tool that is not there is `error`, never a pass |
| `slow` | `sleep 20` with `timeout_s: 2` | advisory | all | past the timeout is `timeout`, never a pass |

## Result (`results/checks.tsv`, 55 rows; `results/verify.tsv`)

| tree | verdict | exit | `grep` (blocking) | `grep-advisory` | `md-only` | `missing-tool` | `slow` |
|---|---|---|---|---|---|---|---|
| C1-feature-con-test | PASS | 0 | ran, 0 findings | ran, 0 | not_applicable (0 of 2) | error | timeout |
| C2-test-sin-dientes | **FAIL** | 1 | ran, 1 finding (`tests/test_media.py::L7`) | ran, 1 | not_applicable (0 of 2) | error | timeout |
| C3-bugfix-con-waiver | PASS | 0 | ran, 0 | ran, 0 | not_applicable (0 of 2) | error | timeout |
| C4-refactor-caracterizacion | PASS | 0 | ran, 0 | ran, 0 | not_applicable (0 of 2) | error | timeout |
| C5-test-nuevo-rojo | PASS | 0 | ran, 0 | ran, 0 | not_applicable (0 of 1) | error | timeout |
| C6-regresion | PASS | 0 | ran, 0 | ran, 0 | not_applicable (0 of 1) | error | timeout |
| C7-metodo-anidado-sin-stub | PASS | 0 | ran, 0 | ran, 0 | not_applicable (0 of 2) | error | timeout |
| C8-corrida-sin-tests | **FAIL** | 1 | ran, 1 finding (`tests/conftest.py::L1`) | ran, 1 | not_applicable (0 of 1) | error | timeout |
| C9-test-modificado | PASS | 0 | ran, 0 | ran, 0 | not_applicable (0 of 1) | error | timeout |
| C1 with `missing-tool` blocking | **INCONCLUSIVE** | 2 | ran, 0 | ran, 0 | not_applicable | **error (blocking)** | timeout |
| C2 with an unsigned `accept_finding` on `tests/test_media.py::L7` | PASS, waiver pending | 0 | ran, 1 finding, waived | ran, 1, waived | not_applicable | error | timeout |

The last tree, committed, then `a2dlc verify --commit HEAD`: exit 1, one problem, `waiver for tests/test_media.py::L7:
unsigned: approve it with a2dlc approve tests/test_media.py::L7`. The same waiver target was reported by two checks and
verify names it once.

Every `verdict.json` and `escalations.json` the binary wrote (22 files) validates against `contracts/` with
`a2dlc-contract-test` (0 failures). The reasons recorded: `the check exited 127: sh: no-such-tool: not found`,
`the check exceeded its timeout (timeout_s=2)`, `0 of 2 changed files match **/*.md`.

## What this shows against the AI-DLC v2 sensor semantics (`e2e/aidlc-sensors/RESULTS.md`)

- A check that cannot run is visible as `error` or `timeout` in `verdict.checks` in all 11 trees; under `advisory`
  it changes nothing, under `blocking` the verdict is INCONCLUSIVE. There is no severity under which it is a pass.
- `not_applicable` carries the count (`0 of 2 changed files match`), so a check that never applies is readable from
  the verdict, which is how the six AI-DLC sensors would have shown up over the same cases (0 of 84 fires applied).
- The only override is a signed `accept_finding` waiver bound to the file's blob: unsigned, the gate passes with the
  waiver pending and the push is refused by `verify`.

## The six AI-DLC v2 sensors as checks (`measure_aidlc_adapter.sh`, `results/aidlc-adapter.tsv`)

Same binary, same 9 trees, image `templates/checks/aidlc-sensor/` (AI-DLC's own dispatcher at `306bd83` behind
`adapter.ts`), six blocking checks with the manifests' globs as `matches`: 54 of 54 entries `not_applicable` with the
count, 9 of 9 PASS, which is what `e2e/aidlc-sensors/RESULTS.md` measured through AI-DLC's dispatcher (0 of 84 fires
applied) now in the verdict. Two probes without `matches`: C1 plus `probe.ts` gives `linter` error (`tool-unavailable`)
and `type-check` error (`script-error: exit-1`), verdict INCONCLUSIVE, where AI-DLC's advisory default said `PASSED`
twice; C1 plus `aidlc-docs/intent.md` gives `upstream-coverage` one finding (FAIL, escalation `check_failed`),
`claim-sources` and `required-sections` ran clean. 22 outputs validate against the schema. Details and the config in
`templates/checks/aidlc-sensor/README.md`.

## On a real diff of a private repo of the ecosystem (`templates/checks/cmd/`, 2026-10-23)

68 changed files (25 `.py`) between two real commits, the same binary, seven checks through the command adapter, all
`advisory` for the measurement: `ruff` (F and B rules, over the changed files only) 0 findings; two of the repo's own
`tests/check_*.py` gates ran clean; one gate reported pre-existing debt at two lines of a file the diff did not touch,
which the adapter cannot bind to the change, so it is an `error` (kept `advisory` until that debt is cleared or the
script accepts a file list); two gates shell out to `git` and one imports the app, none of which exists in the
exported tree, so they stay in CI. With a `print(` and an undefined name appended to a changed file, `ruff` and the
print gate each produced one finding on the right line (`app/…/crm_dlq.py::L376`, `::L375`). The repo now declares
`ruff`, the print gate and the destructive-migration gate as `blocking`.

On the TypeScript repo of the ecosystem (`templates/checks/tsc/`, image built from its lockfile): `tsc --noEmit` ran
clean over the one changed `.ts` file of its last diff; with a type error appended to that file the check produced
one finding on the right line (`server.ts::L1725`, TS2322) and the verdict was FAIL. The repo now declares it
`blocking`.

## Limits

- The findings are grep's: the rule (`raises(Exception)` or `import os`) was chosen because the frozen cases contain
  no long lines, `TODO` or `print(`; it fires in C2 (the test that catches any exception) and C8 (`conftest.py`).
- A container past its timeout keeps running until its command ends (`sleep 20` here): the core stops waiting and
  records `timeout`, it does not kill the container. The engine and test runners behave the same way.
- `engines: []` is allowed by the schema; a real repo keeps its engines and the checks run after the test gates.

## Reproduce

    bash e2e/checks/measure_checks.sh <empty folder>      # builds a2dlc-check-e2e:0.1, needs a2dlc-contract-test
