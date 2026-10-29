Frozen fixtures for the software factory calibration (a2dlc-3fu.1)
====================================================================

These files are a verbatim copy of `demo-repo/` and `LICENSE` from
https://github.com/PromtEngineer/software-factory at commit
`f73ad8ab455410f0b0ace8e3e3d873b2b01c556d` (2026-10-08). That project is MIT-licensed
(`LICENSE` in this directory: Copyright (c) 2026 Prompt Engineering). The copy keeps their
copyright and permission notice, as the MIT license requires.

| Directory | What it is |
|---|---|
| `tinylinks/` | The app the agents work on. Seeded as the only content of the bare origin |
| `reference/` | The reference solution. It is the answer key: never in the origin, never mounted for a builder |
| `hidden-tests/` | Their nine hidden acceptance tests (`h01` to `h09`). Also public upstream |
| `issues.json` | The ten issues (#10 is deliberately vague) |

The files are the committed blobs (`git show <commit>:<path>`, line endings LF), not a Windows checkout: the upstream
`demo-repo/` tree is `3a2b84479e329de718ac04ae2b07e483e43e4c5f`. This repository's `.gitattributes` keeps them LF, so the
hashes in `MANIFEST.sha256` hold on any checkout.

Not copied: `score.sh` and `seed_github.sh` (zsh and GitHub-only). The scorer is ported to
bash/GNU in `e2e/factory/score.sh`; the port is ours, not theirs.

Frozen means the same as `e2e/testdata/mutantes-bb/`: nothing here is edited or regenerated.
`MANIFEST.sha256` lists the sha256 of every file except `NOTICE.md` and the manifest itself;
`e2e/factory/check_fixtures.sh` fails on any difference, missing file or extra file.
