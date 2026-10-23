---
id: a2dlc
kind: deterministic
command: bun .claude/tools/aidlc-sensor-a2dlc.ts
default_severity: blocking
fire_on: gate
description: Requires a PASS receipt from a2dlc (the old(x) ≠ new(x) differential gate) bound to the committed tree; refuses FAIL, INCONCLUSIVE, stale and missing verdicts
category: code-verification
input_schema:
  output_path: string
  stage_slug: string
output_schema:
  pass: boolean
  findings: string[]
  findings_count: integer
timeout_seconds: 120
---

# a2dlc sensor

Blocking gate sensor for AI-DLC v2 (`awslabs/aidlc-workflows`): fires once per deliverable at the stage gate
(`build-and-test` is the intended binding) and passes only when `a2dlc verify` finds a `PASS` verdict whose
`tree_hash` is the committed tree's. The verdict itself is produced earlier by `a2dlc check` (the agent's end-of-turn
hook or CI), which runs the engines in `--network none` containers; this sensor never runs engines, so it finishes in
well under a second and needs only the `a2dlc` binary and `git`.

Install: copy this file to `.claude/sensors/aidlc-a2dlc.md` and `aidlc-sensor-a2dlc.ts` to
`.claude/tools/aidlc-sensor-a2dlc.ts` (the dispatcher accepts only a `.ts` script run by bun), add `a2dlc` to the
`sensors:` list of the stage(s) that should refuse unverified code, and recompile. No `matches:` glob: at the gate the sensor accepts every declared deliverable, because the
receipt covers the whole tree, not one file.

## Failure mode

`findings` carries the one line `a2dlc verify` printed: `FAIL`, `INCONCLUSIVE` (something the engines could not
examine), `stale` (the tree changed after the verdict: run `a2dlc check` again) or `no verdict`. Under
`default_severity: blocking` any of those, and a missing binary, stop gate entry; the override is AI-DLC's own logged
human decision, which is where a2dlc's signed waiver would be presented.
