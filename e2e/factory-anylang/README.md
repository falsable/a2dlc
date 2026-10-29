Any-language proof of the factory (bead a2dlc-3fu.6)

The same `factory` binary runs a toy Python repo of 3 issues. What changes from one language to another is configuration:
the repo's `.a2dlc/config.json` (engine image, tests image, test command that writes JUnit to `$A2DLC_JUNIT`), the factory
config and the batch. This bead changes 0 lines in `internal/factory` and `cmd/factory`: `git diff origin/main -- internal/factory cmd/factory`
is empty for the commit that adds this folder. No model runs: the roles image carries a `claude` that applies a canned patch
(`fake/bin/claude`), which the real adapter calls exactly as it calls `claude -p`.

    bash e2e/factory-anylang/run.sh <empty temporary folder>

Needs `dist/factory.exe` (the Windows build in CLAUDE.md) and the local images `a2dlc-roles:0.11`, `a2dlc-engine-py:0.7` and
`a2dlc-tests-pytest:0.2`. The script builds `a2dlc-roles-fake:0.1` from `fake/Dockerfile`.

## What is here

- `toy/`: the repo (`src/textkit.py`, `tests/test_textkit.py`, `.a2dlc/config.json`), typed because the Python engine's Tier 0
  type-checks changed source.
- `batch.json`: three issues. Issue 1 adds `truncate`, issue 2 adds `unique_words`, and issue 3 asks for `clamp` to reject
  `lo > hi` but its canned patch only edits an existing test.
- `holdout/issue-N/`: tests the lanes never saw. The script exports each accepted candidate, adds that issue's holdout files and
  runs them in the tests image with `--network none`.
- `fake/`: the `claude` stand-in, its canned patches and the Dockerfile that puts it ahead of the real one on `PATH`.

## Result (2026-10-29, Windows 11, Docker Desktop)

    issue-1  accepted  0.0200 usd
    issue-2  accepted  0.0200 usd
    issue-3  rejected  0.0200 usd  modified or deleted 1 base test files: tests/test_textkit.py (modified)
    run completed, 2 accepted, 0.0600 usd; cost per accepted change 0.0300 usd
    holdout issue-1: pass, holdout issue-2: pass

The run total is 3 calls, 9 turns, 4500 input tokens (2400 cache read, 900 cache write), 270 output tokens, all in Stage 3; the
usd and tokens are the fake's canned figures, not a measurement of a model. The run took 88 s, nearly all of it Docker starts for
the engine and the test gates. Exit code 0.

## Cost of preparing a new repo

Image build: 1 s for the roles image with the fake claude (layers cached; the tests image alone takes 8 s with `--no-cache` and
network, and the engine and tests images were already built). Files written: `.a2dlc/config.json` (6 lines), the factory config
(3 lines), the batch (5 lines), holdout (2 files, 22 lines). Repo setup 1 s. Model tokens: 0. Typed source was the only thing that
failed on the first try: the first run with untyped `truncate` ended in a Tier 0 `no-untyped-def` finding on 5 rounds.

## Findings

- A bug fix that changes an existing function's behavior is not accepted by the lane. A first variant of issue 2 (`word_count("")`
  returning 0 instead of 1) ended `FAIL` in the differential, with the counterexample `text=''`, and needs a signed waiver from a
  person. That is the protocol working; the batch uses an additive issue so the proof can finish without one.
- A lane that edits a base test is rejected after the builder's call and before Tier 0 or the oracle (bead a2dlc-3fu.9).
- The holdout here is a script step, not a factory feature: the factory does not run it.
