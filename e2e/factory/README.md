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
