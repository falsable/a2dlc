# Own holdout of the software factory (bead a2dlc-3fu.2)

Hidden acceptance tests for the nine issues of the tinylinks demo (`../factory-ref/issues.json`, #1 to #9), written for
the factory's validation stage. Builders never see them: they are not in the lanes' bare origin (checked by
`e2e/factory/check_origin.sh`), and they are mounted only when a lane's result is validated.

This folder is sealed. `SEAL` holds the sha256 of `MANIFEST.sha256`, and the manifest holds the sha256 of every other
file here. After the seal nothing in this folder is edited; any change is a new holdout with a new seal, and the old one
is retired, not patched. `bash e2e/factory/check_holdout.sh` checks the seal.

## Layout

- `tests/h01.test.ts` .. `tests/h09.test.ts`: one vitest file per issue. The file is the unit that passes or fails,
  as `score.sh` scores it (`#N PASS` or `#N FAIL`); each file has two to five cases.
- `tests/helpers.ts`: starts the app with `createApp` (option `slugFn` only, which the base app already has) and talks
  to it over `node:http`. Every request leaves from its own loopback address unless a test names a client, so the
  per-client rate limit of #9 never touches the other issues' tests in an integrated tree.
- `mutants.json`: the frozen mutants, each one exact replacement on a copy of `../factory-ref/reference` whose base
  files are pinned by sha256. Each mutant names the issue whose file it must make fail.
- `CANARY`: the canary line.

## Mounting

Mount `tests/` as a subfolder of the checkout's `test/` (score.sh uses `test/_hidden/`, `holdout_c3.mjs` uses
`test/_holdout/`) and rewrite `../src/` to `../../src/` in `helpers.ts`. Do not mount it at `test/` itself: the app has
its own `test/helpers.ts` and a lane may change it. JUnit: `vitest run test/_holdout --reporter=junit`.

## What the tests assume

Only what the issues say, plus the base app's public surface: `createApp(opts)` from `src/server.ts`, its `slugFn`
option, and the routes of the README. Where an issue leaves a choice open the test accepts every option the issue
allows: #2 accepts 400 or https normalization for a scheme-less URL, and #6 checks the click count of an expired link
only when stats answer 200 for it. Time for #6, #8 and #9 is vitest's fake clock over Date, performance, hrtime and
the timer functions, moved with `vi.advanceTimersByTime`, so a window or an expiry built on timers also moves. The
client of #9 is the socket's address.

## Calibration (C3)

`bash e2e/factory/c3_holdout.sh <empty temporary folder>`: reference/ passes all 27 cases and untouched tinylinks/ fails
all nine files, three runs each; each of the 35 mutants fails its target file by an assertion; each case falls to at
least one mutant of its own issue. Collateral failures in other files do not count. `M05-null-not-undefined` (a missing
slug read as an invalid custom slug) makes every random-slug creation fail, so it fails every file; it is kept because it
is the only mutant that fails "without a slug a random one is still generated".

## Canary

The line in `CANARY` is in every test file (first line, and in each `describe` title and request `user-agent` through
`helpers.ts`) and in `mutants.json`. `e2e/factory/check_leak.sh` looks for it, and for the C4 internet strings, in what
a lane produced. Do not scan the output of a holdout run: its logs and JUnit carry the canary by design.
`../factory-ref/reference/` does not carry it: that fixture was frozen by P0a before this holdout existed.

## Authorship

Written by a session that is not a factory lane: it read the issues, `reference/` and `tinylinks/`, and did not open
the upstream hidden tests (`../factory-ref/hidden-tests/`), `internal/factory`, `cmd/factory` or any lane branch. No
model was called to write or check it.
