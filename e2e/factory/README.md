Software factory calibration (P0a, bead a2dlc-3fu.1)

Checks that the instrument works before any model is paid for. Nothing here calls a model. The fixtures are in
`e2e/testdata/factory-ref/` (frozen by sha256, MIT notice in `NOTICE.md`).

## Images

    docker build -f e2e/factory/node.Dockerfile -t a2dlc-factory-node:0.1 e2e/testdata/factory-ref/tinylinks
    docker build -f e2e/factory/node-cov.Dockerfile -t a2dlc-factory-node-cov:0.1 e2e/factory

The first is Node 22 with the fixture's lockfile installed (`npm ci`) in `/deps`. The second adds `@vitest/coverage-v8` at
the installed vitest version with `--no-save`, because the frozen lockfile does not have it. Both run with `--network none`.

## Runs

    bash e2e/factory/p0a.sh <empty temporary folder>      C0, C4, C5, C1, C2
    bash e2e/factory/c8_junit.sh <empty temporary folder> C8 (needs dist/a2dlc.exe)
    bash e2e/factory/c3_holdout.sh <empty temporary folder> C3 and the holdout half of C4 (bead a2dlc-3fu.2)
    bash e2e/factory/holdout_smoke.sh <empty temporary folder> real holdout command through factory integrate (a2dlc-3fu.12)

C7 is a Go test plus a Python test, run as the rest of the suite:

    go test -count=1 -run TestCostChain ./internal/run/
    python -I -X utf8 roles/tests/test_read_claude.py

## Results (2026-10-29)

| Check | Result |
|---|---|
| C0 fixtures | 37 files match `MANIFEST.sha256`, none with CR; `tinylinks/` and `reference/` share `package.json` and the lockfile |
| C1 | `reference/` scores 9/9, three runs, about 14 s each |
| C2 | untouched `tinylinks/` fails #1 to #9 in all three runs, each with a failed assertion in its log; it passes none |
| C4 | bare origin seeded from `tinylinks/`: 1 commit on `main`, 12 files, the 13 answer-key blobs absent from every object |
| C5 | `npm test` green on `tinylinks/` and on `reference/`: 3 tests, Node v22.23.3, no network |
| C7 | the cost of a fake `claude -p` result (cost.json, pinned to the adapter) goes through `Run`: per call 140 input (cache included) + 50 output, cache 10 read and 30 written, 3 turns, 0.04 USD; ledger and budget agree; `max_tokens` 300 cuts after 2 calls with `CB-03: 380 tokens exceed budget.max_tokens 300`; `--max-cost-usd` 0.05 cuts after 2 calls with `CB-03: cost 0.0800 USD ...`. Two mutants (cache counted into `spent_tokens`; token cap disabled) make the test fail |
| C8 | the vitest JUnit reporter and V8 `lcovonly` work as `config.tests.command` with no adapter: a fix plus the hidden test of #3 gives `PASS`, the hidden test alone gives `FAIL` |

## Findings for the factory

- C2 says the hidden tests discriminate: the base app fails every one of them, so a builder that passes `#N` did something.
- C8 needs `@vitest/coverage-v8` in the image. Without a coverage file the check cannot examine the changed lines of a language with no
  engine and the verdict is `INCONCLUSIVE`.
- The fixtures are the committed upstream blobs (LF). A Windows checkout with `core.autocrlf=true` rewrites them to CRLF and
  `check_fixtures.sh` fails on the hash; the repo's `.gitattributes` (`* text=auto eol=lf`) keeps them LF.

## Own holdout (bead a2dlc-3fu.2)

Our hidden tests for #1 to #9 are in `e2e/testdata/factory-holdout/`, sealed: `SEAL` is the sha256 of its
`MANIFEST.sha256`, checked by `check_holdout.sh`. The folder is never edited; a change is a new holdout with a new seal.
Its README says how to mount it at validation time and what each test assumes. The lanes' origin must not hold it:
`check_origin.sh` now also rejects any holdout blob and any blob with the canary.

`check_leak.sh <path>...` is the C4 scan of what a lane produced (transcripts, diffs, worktree): the holdout canary, the
internet strings of design v1 (`PromtEngineer`, `software-factory`, `hidden-tests`, github.com URLs) and the local folder
names `factory-ref` and `factory-holdout`. A github.com URL that `tinylinks/` already has (the lockfile's funding links)
does not count. Never point it at the output of a holdout run, which carries the canary.

Results of `c3_holdout.sh` (2026-10-29), seal `a6af6992722edaac6d5b5c2e22b97578812f9cc9c091d89da4cb26389076ed47`:

| Check | Result |
|---|---|
| seal | 13 files match the manifest, none with CR, canary in the 10 test files and `mutants.json`; P0a fixtures unchanged |
| C4 | origin from `tinylinks/`: 12 files, 28 answer-key blobs (reference/, hidden-tests/, own holdout) absent, no canary; an origin with one holdout test, or with the canary line, is rejected. Leak scan: 9 planted strings found, clean on a clone of the origin |
| score.sh | holdout as `HIDDEN_DIR`: `reference/` 9/9 PASS, untouched `tinylinks/` 9/9 FAIL, each by assertions |
| C3 | 27 cases; `reference/` passes all and `tinylinks/` fails all nine files (21 of 27 cases), three runs each with the same result; each of the 35 mutants fails its target file; each case falls to at least one mutant of its own issue |

- GNU grep 3.0 of Git for Windows aborts on `grep -iF` (exit 134); `check_leak.sh` uses `-i` without `-F`.
- The canary is not in `reference/`, which P0a froze first; it is in the holdout and in `mutants.json` only.

## Holdout smoke through the binary (bead a2dlc-3fu.12)

`holdout_smoke.sh` runs `dist/factory.exe integrate` with the production `DockerHoldout` and the command of
`contracts/examples/valid/factory-holdout__tinylinks.json` unchanged, extended to the nine units. No model is called:
the lanes are commits built by hand at `refs/a2dlc/run/<slice>/builder_primary`, and `holdout_smoke_report.py` writes
the report (all counters zero) and the spec. The target repo is `tinylinks/` plus `.a2dlc/config.json` with no engines,
so `a2dlc check` passes with nothing in scope and the holdout is the only container. Results (2026-10-29, Windows host,
Docker Desktop):

| Run | Result |
|---|---|
| (a) reference | lane issue-1 carries `tinylinks/` to `reference/` (`src/`), lanes 2 to 9 add a notes file: all 9 integrated, exit 0, the n-th lane validated with the files of issues 1 to n, the last with all nine (27 tests passed); wall 48.6 s, vitest 0.6 s to 4.6 s per lane |
| (b) tinylinks | nine notes-only lanes on the untouched app: all 9 `holdout_failed` with exit 1 and only their own file, each log with failed assertions; exit 3; wall 23.1 s, vitest 0.4 s to 0.6 s per lane |
| (c) probe | the runner's mounts with a probe command on a copy of the holdout: `touch /src` and `touch /holdout` fail, 1.1.1.1:443 is unreachable (ENETUNREACH), the copy still matches its seal. By hand with `docker run`, outside the script: the same command exits 1 with a writable mount, and exits 1 with read-only mounts and the network on |
| (d) tamper | a copy of the holdout with one byte added to `h01.test.ts`: exit 1 with `holdout changed: tests/h01.test.ts differs from the manifest`, before any container or record |

Main of the target repo did not move in any run, and the sealed holdout still matches its seal after being mounted 18 times.

## Control arms (bead a2dlc-3fu.5)

Arm 1 is the factory as built. The two controls run under `cmd/factory` and are scored by the same path (the lane's `a2dlc run`
PASS, the holdout on the integrated tree, no base test edited or deleted; waivers never count). The test config
(`config.tests`, no differential engine) is the same in every arm.

- Arm 0: `"arm": "control"` in the factory config. `A2DLC_LLM_CMD` is `claude --disallowedTools WebSearch,WebFetch --max-budget-usd N` (the deny is on both arms, flag from `claude --help` of a2dlc-roles:0.11, claude 2.1.302), with no `--setting-sources`,
  `--strict-mcp-config`, `--disable-slash-commands`, `--tools` or `--exclude-dynamic-system-prompt-sections`; same model, same role
  container, same caps. `arm_test.go` runs both arms through one lane path and checks that only the command differs.
- Reference integration: `factory integrate --integrator reference --config <factory-config.json> ...`, from
  `src/integrate.ts` of PromtEngineer/software-factory at f73ad8ab455410f0b0ace8e3e3d873b2b01c556d. Record:
  `integration-reference.json`, branch `factory/<run>/integration-reference`, with `usage` for the conflict agent's calls.
  The cost per accepted change of an arm with this integrator is `(lanes usd + integration usd) / accepted`.

Only a fake `claude` has run so far. The paid comparison is bead a2dlc-3fu.7; the conflict agent has not been exercised against the
real adapter prompt.
