---
name: a2dlc
description: >-
  Deterministic verification gate and autonomous state machine (A2-DLC v3.5) for AI coding agents.
  Activate this skill whenever working in a repository with `.a2dlc/config.json`, installing a2dlc,
  running `a2dlc check` / `a2dlc verify` / `a2dlc run`, interpreting `.a2dlc/verdict.json` or
  `.a2dlc/escalations.json`, or managing intentional behavioral changes via `.a2dlc/waivers.json`.
---

# A2-DLC Agent Skill (`a2dlc`)

You are operating in a repository guarded by **a2dlc** (Autonomous Agentic Development Lifecycle, RFC-0001 v3.5). `a2dlc` replaces subjective code review with a deterministic verification gate executed inside hermetic (`--network none`) Docker engines.

---

## 1. Golden Rules for Agents (Strictly Enforced by Hooks)

1. **Never bypass the gate:** Never run `git push --no-verify`. The `PreToolUse` hook intercepts and blocks bypass attempts.
2. **Never edit `.a2dlc/verdict.json` or `.a2dlc/approvals.json` by hand:** `a2dlc verify` recomputes the git `tree_hash` and validates cryptographic SSH signatures. Forged receipts fail immediately.
3. **Never modify `protected_paths`:** Files matching globs in `.a2dlc/config.json -> protected_paths` (such as human benchmark or compliance suites) are off-limits to agents.
4. **Every changed line in scope must be exercised:** `PASS` requires 0 uncovered changed lines (`uncovered_change`) and 0 engine errors. Functions must have explicit type annotations (`typed: false` yields `INCONCLUSIVE`).

---

## 2. Standard Commands & Workflows

### A. Installing `a2dlc` in a Repository
If the user asks you to set up or install `a2dlc` in the current repo:

```bash
# For Claude Code:
a2dlc install --agent claude

# For Gemini CLI / Antigravity:
a2dlc install --agent gemini

# For both:
a2dlc install --agent claude --agent gemini
```

This creates `.a2dlc/config.json` (auto-detecting Python or TypeScript), adds `.a2dlc/verdict.json` to `.gitignore`, installs the `pre-push` (`a2dlc verify`) hook (commits are not blocked; run `a2dlc check` before pushing), and wires the agent lifecycle hooks (`PreToolUse` + `Stop`/`AfterAgent`).

### B. Running the Differential & Test Gate (`a2dlc check`)
Before committing or finishing a coding task, always run:

```bash
a2dlc check
```

Exit codes:
- `0` (`PASS`): Every changed function and test passed differential/property checks and coverage preconditions.
- `1` (`FAIL`): Behavioral divergence (`old(x) != new(x)`) or test regression detected.
- `2` (`INCONCLUSIVE`): Missing type annotations, uncovered changed lines (`uncovered_change`), toothless test (`test_without_teeth`), or pending waiver signature (`signature_pending`).

### C. Diagnosing Failures & Escalations
When `a2dlc check` returns `1` or `2`, inspect:
1. `.a2dlc/verdict.json` — Detailed per-target results, counterexamples (`args`, `old`, `new`), and `coverage.files` showing uncovered line numbers.
2. `.a2dlc/escalations.json` — Actionable items categorized by `kind`:

| Escalation `kind` | Meaning | How the Agent Must Fix It |
|---|---|---|
| `behavior_divergence` | Changed function returns different output than base for the counterexample in `evidence.counterexample` | If unintended bug: fix the code so `new(x) == old(x)` on that input. If intentional spec change: add an entry to `.a2dlc/waivers.json` (see below). |
| `uncovered_change` | Changed lines in `source_paths` were neither proved by an engine nor executed by a test | Write a unit or property test that executes the exact uncovered lines listed in `evidence.lines`. |
| `test_without_teeth` | A new test passes even on the base tree (or against a `NotImplementedError` stub) | Strengthen the test assertions so it fails on the base/stub and passes on the candidate. |
| `typed: false` (in `verdict.json`) | Changed Python function lacks parameter or return type hints | Add full type annotations (`def fn(x: int) -> int:`) so CrossHair can symbolize inputs. |
| `dependency_not_allowed` | Added/changed package in manifest/lockfile is not in `.a2dlc/deps.allow.json` | Revert the unauthorized dependency or ask the human operator to allowlist the exact version. |

### D. Handling Intentional Behavior Changes (Waivers)
When a task intentionally changes a function's behavior (`behavior_divergence`) or adds a characterization test (`characterization_test`):

1. Declare the waiver in `.a2dlc/waivers.json`:
   ```json
   {
     "schema_version": 1,
     "waivers": [
       {
         "target": "src/module.py::my_function",
         "decision": "enforce_new_behavior",
         "reason": "Clear explanation of why the behavior changed per user request"
       }
     ]
   }
   ```
   *(Valid `decision` values: `"enforce_new_behavior"`, `"preserve_legacy"`, `"characterization_test"`, `"quarantine_flaky"`).*
2. Re-run `a2dlc check`.
3. If `--require-signatures` is active, ask the human operator to sign the waiver in their terminal (agents never hold the SSH passphrase):
   ```bash
   a2dlc approve src/module.py::my_function --key ~/.config/a2dlc/firma
   ```

### E. Running the Full Autonomous State Machine (`a2dlc run`)
When executing a full slice through Stages −1 to 7:

```bash
# Run or resume a slice from an intent JSON file:
a2dlc run --slice <slice-id> --intent <path/to/intent.json> --mode greenfield

# Verify the hash-chained isolation ledger and stage artifacts:
a2dlc verify-run --slice <slice-id>
```

If Stage 1 (`spec_divergence`) or Stage 1.5 (`backtranslation_mismatch`) escalates a probe to the human:
```bash
a2dlc answer --slice <slice-id> --probe <probe-id> --choice alpha
```
Then re-run `a2dlc run --slice <slice-id>` to resume deterministically from the last verified stage.

### F. Using `A2-DLC` via Model Context Protocol (`a2dlc mcp`)
In MCP-enabled IDEs (Cursor, Windsurf, Claude Desktop, Cline), you can also register the built-in JSON-RPC 2.0 `stdio` server:

```json
{
  "mcpServers": {
    "a2dlc": {
      "command": "a2dlc",
      "args": ["mcp"]
    }
  }
}
```

Available MCP tools:
- `a2dlc_status`: Inspect `.a2dlc/verdict.json`, `.a2dlc/escalations.json`, `.a2dlc/waivers.json`, and `HEAD` verification status without launching Docker containers.
- `a2dlc_check`: Execute the differential and property verification gate (`optional: base`).
- `a2dlc_verify`: Verify in `<50 ms` that `.a2dlc/verdict.json` matches the git `tree_hash` (`optional: commit`).
- `a2dlc_verify_run`: Verify the SHA-256 hash-chained `ledger.json` and Stage −1..7 artifacts for a `slice`.
- `a2dlc_intent_from_ticket`: Convert a ticket or PRD fetched from Jira, Linear, GitHub Issues, GitLab, or Confluence (via Composio or vendor MCP servers) into a schema-validated `.a2dlc/intents/<slice_id>.intent.json` file for Stage 0 (`a2dlc run`).
- `a2dlc_intent_from_signal`: Close the Stage 7 → Stage 0 loop. A Rollback Sentinel alert (`error_rate_delta`, `latency_p99_ms` or a runtime `invariant` breached on a canary ring of `from_slice`) becomes a schema-validated bugfix intent that inherits the alerted slice's invariants, boundary, delivery and budget cap; the run it starts still stops at probes, circuit breakers and signed waivers.
- `a2dlc_format_report`: Format `.a2dlc/verdict.json` and counterexamples into a Markdown compliance table ready to post to GitHub PRs, GitLab MRs, Jira, Linear, Slack, or Confluence.

