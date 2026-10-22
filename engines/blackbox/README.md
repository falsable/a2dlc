# Black-box engine

The black-box engine compares the observable behavior of a base tree and a candidate tree. It is the fallback for
languages without their own engine, and the core's self-test. It performs two families of operations:

- `diff`: builds both trees, generates inputs from each surface's `input_schema`, runs the surface on both sides and
  reports the first input whose observation differs.
- `golden_record` and `golden_replay`: records the observation of each committed case on the base, and replays the
  cases on the candidate against that record.

Every other operation is answered with its own reply type and its `error` set (`fuzz` is refused with exit 1, since its
reply has no error field).

## Declaration

The surfaces are declared in `.a2dlc/blackbox.json` of each tree. The golden operations read the spec, the harness and
the case files from the base tree only, so a candidate cannot change what it is compared against. Surface names must be
unique: a golden case id is `<case path>::<surface name>`.

## What is observed

A surface compares what its `compare` declares, and nothing else:

- `exit_code`, `stdout`, `stderr`: the process exit code (`-1` if the program does not start, `-2` on timeout) and the
  output streams, after `normalize`.
- `files`: each listed path, relative to the surface's `cwd` in the run folder, after `normalize`; `<absent>` if the
  file does not exist.

The diff and the golden operations use the same function for this (`observation` in `main.go`), so they cannot drift.

## Golden hash encoding

`output_hash` is `sha256:` followed by the hex digest of the observation bytes. The bytes are the canonical JSON of the
observation object (RFC 8785 for these values):

- object keys in sorted order, no spaces, integers in decimal;
- strings escaped with `\"`, `\\`, `\b`, `\f`, `\n`, `\r`, `\t`, and `\u00xx` (lower case) for the other bytes below
  `0x20`;
- every other byte is written as it is: no HTML escaping, and invalid UTF-8 is hashed as it came.

Example: a surface that compares `exit_code` and `stdout`, exits 0 and prints `a"b` and a newline, has the observation
`{"exit_code":0,"stdout":"a\"b\n"}`, whose digest is `sha256:bf1c34605b5f72fcd052bd5d5870066cc1c22a9bee1f7c4b5dd4b4f845d8f384`
(pinned by `TestObservationCanonicalBytesArePinned`).

`input_hash` is the digest of the bytes of the case file.

## Golden operations

- `golden_record` builds the base, runs every case on every surface and replies with the cases. The core writes them to
  `golden.json`.
- `golden_replay` reads `/io/golden.json` (`ioDir` in `main.go`) and checks it against the budget: the same case ids,
  each recorded on the same input bytes. Any mismatch is an error. It then rebuilds the base and checks that it still
  reproduces every recorded `output_hash`; if not, the record is stale and the replay is an error. Only then does it
  build the candidate, run the same cases, and reply with the candidate cases and one diff per case whose
  `output_hash` differs from the record. A diff carries the base's observation (`old`) and the candidate's (`new`).

## Failures

Golden failures never exit non-zero for a verdict: they are in the response's `error` field, with no cases and no diffs.
This covers a missing or mismatched `golden.json`, a build failure on either side, a setup failure on any case, and an
invalid budget. The core must read a non-null `error` on a golden replay as a failed check, not as no divergence.
Exit 1 means the engine could not write its reply at all.

A setup failure aborts the whole operation: the setup is part of the observation, so a case that cannot be set up has
no output to record.
