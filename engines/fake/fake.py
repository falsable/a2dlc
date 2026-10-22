"""Fake engine for the core's tests: meets the /io contract without analysing anything. For each requested file,
different between /base and /cand is FAIL; equal is PASS; with "INCONCLUSIVE" in the candidate, INCONCLUSIVE; with
"CRASH", it exits without writing a response (engine down)."""
import json
import sys
from pathlib import Path

req = json.loads(Path("/io/request.json").read_text(encoding="utf-8"))
targets = []
for f in req["changed_files"]:
    base, cand = Path(req["base_dir"]) / f, Path(req["candidate_dir"]) / f
    old = base.read_text(encoding="utf-8") if base.exists() else ""
    new = cand.read_text(encoding="utf-8")
    if "CRASH" in new:
        sys.exit(3)
    t = {"target": f"{f}::f", "engine": "fast-check", "engine_version": "fake-0.1", "verdict": "PASS",
         "search_budget": {"examples": 1, "seed": 1}, "generators": "typed_default", "typed": True,
         "domain_guard": None, "equivalence": "strict", "counterexample": None}
    if "INCONCLUSIVE" in new:
        t.update(verdict="INCONCLUSIVE", reason="marked in the candidate")
    elif old != new:
        t.update(verdict="FAIL", counterexample={"args": "[]", "old": old, "new": new})
    targets.append(t)
Path("/io/response.json").write_text(json.dumps({"schema_version": 1, "targets": targets, "out_of_scope": [], "error": None}), encoding="utf-8")
