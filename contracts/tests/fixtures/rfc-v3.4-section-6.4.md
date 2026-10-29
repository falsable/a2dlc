# Fixture: section 6.4 of RFC-0001 v3.4, verbatim from commit c90be55 (docs/A2-DLC_v3.4_protocol.md lines 391-436)

## 6. Artifacts

### 6.4 Verdict and waives (common to Lite and full)

**Verdict** (`.a2dlc/latest.json`; history in `.a2dlc/reports/`):

```json
{
  "target": "core/rolling.py::rolling_max",
  "stage": "tier1_differential",
  "verdict": "FAIL",
  "engine": "crosshair-diffbehavior",
  "search_budget": {"iterations": 216, "per_condition_timeout_s": 10},
  "typed": true,
  "domain_guard": null,
  "equivalence": "strict",
  "base_ref": "sha1:…",
  "tree_hash": "sha256:…",
  "contract_hash": "sha256:…",
  "counterexample": {"kwargs": {"xs": "[1, 2, 2]", "w": "2"}, "old": "2", "new": "1"},
  "cluster_id": "div-07"
}
```

- **`search_budget` depends on the engine:**
  - CrossHair: `{"iterations", "per_condition_timeout_s"}`.
  - Hypothesis: `{"examples", "seed"}`, for example `{"examples": 2000, "seed": 42}`.
  - A field from another engine is a schema error.
- **`domain_guard`:** `null` when there is no guard; if there is one, `{"expr": "…", "source": "contract" | "ast_overapprox"}` (§4.4). Any other origin is a schema error.
- **`verdict`:** `PASS` | `FAIL` | `INCONCLUSIVE`. "No differences found" is a **bounded search**, not an equivalence proof: that is why the verdict carries `engine` and `search_budget`, and a `PASS` without a recorded budget is not valid. `INCONCLUSIVE` covers functions without types, timeouts without a single useful iteration and red runs without identifiable tests; it blocks just like `FAIL`, but it escalates as a tool problem, not as a divergence.
- **Tree-bound receipt:** `tree_hash` is the hash of the content of the tree on which the verdict was issued. Before merge it is recomputed; if someone edited or committed afterwards, the verdict becomes **stale** (the files are named) and it must be run again. There is no flag to skip it.

**Waive** (`.a2dlc/waivers.yaml`, versioned, reviewable in PR):

```yaml
- target: "core/rolling.py::rolling_max"
  cluster_id: "div-07"
  decision: "enforce_new_behavior"     # preserve_legacy | enforce_new_behavior
  reason: "The window must exclude the current element (historical bug)"
  approved_by: "aldemar"
  date: "2026-10-15"
```

Waives feed the Locked Contract `S*` as assertions, so the same divergence does not escalate again.

On a test the decisions are `characterization_test` and `quarantine_flaky` (§4.4); on a finding of a pluggable check
(§5, Checks) the decision is `accept_finding` on `<file>::<check id>` or `<file>::L<n>`. A decision of another class
does not cover the target, and every signature binds the blob of the target's file.


---

## 7. Circuit Breakers
