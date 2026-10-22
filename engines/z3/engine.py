"""a2dlc z3 engine: satisfiability of the smt clauses of S* (Stage 1.5 of a2dlc run, protocol §5 step 2).

Contract: reads /io/request.json (contracts/z3-request.schema.json) and writes /io/response.json
(contracts/z3-response.schema.json). It does not see the repo and has no network.
Each function is resolved on its own: its SMT-LIB 2 fragments are concatenated and parsed with z3.parse_smt2_string.
Code is never evaluated: a fragment that is not valid SMT-LIB leaves the function skipped with the parser's error.
Each assert is tracked with its own literal, so an unsat brings its unsat core: the asserts that clash, with their number.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

import z3

IO = Path("/io")


def resolve(function: str, smt: list[str], timeout_ms: int) -> dict:
    res = {"function": function, "status": "skipped", "model": None, "reason": None}
    # one context per function: the z3 parser carries declarations and errors from one call to the next
    ctx = z3.Context()
    try:
        asserts = z3.parse_smt2_string("\n".join(smt), ctx=ctx)
    except z3.Z3Exception as e:
        msg = e.value.decode("utf-8", "replace") if isinstance(e.value, bytes) else str(e.value)
        res["reason"] = f"invalid SMT-LIB: {msg.strip()}"[:300]
        return res
    if len(asserts) == 0:
        res["reason"] = "the smt has no assert: there is nothing to resolve"
        return res
    s = z3.Solver(ctx=ctx)
    s.set("timeout", timeout_ms)
    marks = {}
    for i, a in enumerate(asserts, 1):
        p = z3.Bool(f"a2dlc_assert_{i}", ctx)
        marks[str(p)] = (i, a)
        s.assert_and_track(a, p)
    r = s.check()
    if r == z3.sat:
        m = s.model()
        own = sorted((d for d in m.decls() if not d.name().startswith("a2dlc_assert_")), key=lambda d: d.name())
        res["status"], res["model"] = "sat", "[" + ", ".join(f"{d.name()} = {m[d]}" for d in own) + "]"
    elif r == z3.unsat:
        core = sorted(marks[str(p)] for p in s.unsat_core())
        res["status"] = "unsat"
        res["reason"] = "unsat core: " + "; ".join(f"#{i} {a.sexpr()}" for i, a in core)
    else:
        res["status"] = "unknown"
        res["reason"] = f"z3 did not decide in {timeout_ms} ms ({s.reason_unknown()}): the function stays runtime_and_pbt"
    return res


def main() -> int:
    resp = {"schema_version": 1, "z3_version": z3.get_version_string(), "results": [], "error": None}
    try:
        req = json.loads((IO / "request.json").read_text(encoding="utf-8"))
        if req.get("schema_version") != 1:
            raise ValueError(f"schema_version {req.get('schema_version')!r}")
        resp["results"] = [resolve(f["function"], f["smt"], int(req["timeout_ms"])) for f in req["functions"]]
    except Exception as e:  # the request could not be read: error, no result is valid
        resp["results"], resp["error"] = [], f"{type(e).__name__}: {e}"[:500]
    (IO / "response.json").write_text(json.dumps(resp, ensure_ascii=False, indent=1) + "\n", encoding="utf-8")
    return 0


if __name__ == "__main__":
    sys.exit(main())
