# a2dlc plugin for Claude Code

Wires `a2dlc hook claude` into every Claude Code session, so the gate follows you across repos instead of being
installed per repo:

- `PreToolUse` (Edit, Write, NotebookEdit, Bash): blocks edits to `protected_paths` without a signed approval, and
  `git push --no-verify`.
- `Stop`: runs `a2dlc check` and sends the counterexamples back to the agent until the gate passes, up to
  `agent_hook.max_blocks` times per session.

In a repo without `.a2dlc/config.json` (in the working tree or in HEAD), and outside a git repo, the hook allows
everything and writes nothing.

## Setup

1. Put the `a2dlc` binary on `PATH` (the hook calls it by name).
2. In each repo you want gated, run `a2dlc install` without `--agent`: it writes `.a2dlc/config.json` and the
   pre-push hook. With this plugin enabled, `install --agent claude` would add a second copy of the same hooks to
   `.claude/settings.json`, and `Stop` would run the check twice per turn.
3. `/plugin marketplace add falsable/a2dlc`, then `/plugin install a2dlc@falsable`.

If the binary is missing, Claude Code shows the hook error and the session goes on; the pre-push hook still stops
the push.
