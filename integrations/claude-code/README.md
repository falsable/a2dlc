# a2dlc plugin for Claude Code

Wires `a2dlc hook claude` into every Claude Code session, so the gate follows you across repos instead of being
installed per repo:

- `PreToolUse` (Edit, Write, NotebookEdit, Bash): blocks edits to `protected_paths` without a signed approval, and
  `git push --no-verify`.
- `Stop` (the end of every turn): runs level 0, `a2dlc check --level 0`. It runs no test, informs and never blocks: it
  writes `.a2dlc/reports/level0.json` (changed sources and tests, the test functions the diff brings, changed sources
  no changed test imports directly, changed lines that are declaration-only or defensive handlers, protected paths
  touched) and shows a one-line summary to the person. Claude Code does not pass a Stop hook's output to the model, so
  the agent reads the receipt file. `agent_hook.max_blocks` no longer applies to `Stop`.

The two levels and when they run:

| Level | Command | When | Blocks |
|---|---|---|---|
| 0, static | `a2dlc check --level 0` (the `Stop` hook) | every agent turn | no |
| 1, tests | `a2dlc check` | when the agent says "done" (it runs the command itself), then `a2dlc verify` at pre-push and `a2dlc check --base <ref> --require-signatures` in the PR check (`.github/workflows/a2dlc.yml`) | yes |

Claude Code has no "done" event, so level 1 is not wired to a hook: ask the agent in the repo's `CLAUDE.md` to run
`a2dlc check` before it reports a change as finished. Without a current PASS receipt, the push is stopped.

In a repo without `.a2dlc/config.json` (in the working tree or in HEAD), and outside a git repo, the hook allows
everything and writes nothing.

## Setup

1. Put the `a2dlc` binary on `PATH` (the hook calls it by name).
2. In each repo you want gated, run `a2dlc install` without `--agent`: it writes `.a2dlc/config.json` and the
   pre-push hook. With this plugin enabled, `install --agent claude` would add a second copy of the same hooks to
   `.claude/settings.json`, and `Stop` would run level 0 twice per turn.
3. `/plugin marketplace add falsable/a2dlc`, then `/plugin install a2dlc@falsable`.

If the binary is missing, Claude Code shows the hook error and the session goes on; the pre-push hook still stops
the push.
