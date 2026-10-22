"""VARA-2 table per arm: {rejected, passed} × {FAIL, INCONCLUSIVE, PASS, ERROR} with Wilson 95 %, empty PASS, coverage
of the changed lines, causes of INCONCLUSIVE, FAIL with their trees, and timings.
Usage: tabulate2.py <bench folder> [instances...] (default: the measured ones, in order.json order)"""
import json
import math
import os
import sys
from collections import Counter

B = sys.argv[1]
order = json.load(open(f"{B}/downloads/order.json", encoding="utf-8"))
ids = sys.argv[2:] or [c["instance_id"] for c in order if os.path.exists(f"{B}/work/{c['instance_id']}/commits2.txt")]


def wilson(k, n, z=1.96):
    if n == 0:
        return "–"
    p = k / n
    d = 1 + z * z / n
    c = (p + z * z / (2 * n)) / d
    h = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return f"{100 * p:.0f} % [{100 * max(0, c - h):.0f}–{100 * min(1, c + h):.0f}]"


def load(path):
    try:
        return json.load(open(path, encoding="utf-8"))
    except (OSError, ValueError):
        return None


for arm in ("A1", "A2", "A3"):
    rows = []
    for iid in ids:
        d = f"{B}/work/{iid}/runs2/{arm}"
        if not os.path.isdir(d):
            continue
        for m in sorted(os.listdir(d)):
            if not m.endswith(".meta.json"):
                continue
            meta = load(f"{d}/{m}")
            v = load(f"{d}/{m[:-10]}.verdict.json")
            escalations = load(f"{d}/{m[:-10]}.escalations.json") or {"items": []}
            rows.append((iid, meta, v, escalations))
    if not rows:
        continue
    print(f"\n===== {arm}: {len(rows)} checks in {len({f[0] for f in rows})} instances =====")
    table = {lab: Counter() for lab in ("rejected", "passed")}
    empty, cov_total, cov_covered, causes, fails, times, gates_err = 0, 0, 0, Counter(), [], [], Counter()
    preexisting = 0
    for iid, meta, v, escalations in rows:
        lab = meta["label"]
        if v is None:
            table[lab]["ERROR"] += 1
            times.append(meta["seconds"])
            continue
        table[lab][v["verdict"]] += 1
        times.append(meta["seconds"])
        cov = v.get("coverage") or {"files": []}
        examined = sum(len(f["by_engine"]) + len(f["covered"]) for f in cov["files"])
        without = sum(len(f["uncovered"]) for f in cov["files"])
        cov_total += examined + without
        cov_covered += examined
        if v["verdict"] == "PASS" and (without > 0 or examined == 0 and cov["files"]):
            empty += 1
        for g, x in (v.get("test_gates") or {}).items():
            if x and x.get("status") == "error":
                gates_err[f"{g}: {x.get('reason', '')[:90]}"] += 1
        if v["verdict"] == "INCONCLUSIVE":
            for e in escalations["items"]:
                if e["kind"] in ("uncovered_change", "test_without_teeth", "flaky_test", "tooling"):
                    causes[e["kind"] + (": " + e["summary"][:70] if e["kind"] == "tooling" else "")] += 1
        # "preexist" matches the reason the Go core writes for pre-existing red tests (testsdiff.go), in its old Spanish and current English wording
        preexisting += sum(1 for t in v.get("tests", []) if t.get("gate") == "tests_diff" and "preexist" in (t.get("reason") or ""))
        if v["verdict"] == "FAIL":
            for t in v.get("tests", []):
                if t["verdict"] == "FAIL":
                    fails.append((iid, meta["agent"], lab, t["target"].split("::")[-1], t.get("trees"), (t.get("message") or "")[:80]))
    print(f"{'':12}{'FAIL':>6}{'INCONCL':>9}{'PASS':>6}{'(empty)':>9}{'ERROR':>7}{'n':>5}")
    for lab in ("rejected", "passed"):
        c = table[lab]
        n = sum(c.values())
        print(f"{lab:12}{c['FAIL']:>6}{c['INCONCLUSIVE']:>9}{c['PASS']:>6}{'':>9}{c['ERROR']:>7}{n:>5}")
    r, p = table["rejected"], table["passed"]
    nr, n_passed = sum(r.values()), sum(p.values())
    print(f"detection (FAIL/rejected): {wilson(r['FAIL'], nr)}")
    print(f"FAIL in passed: {wilson(p['FAIL'], n_passed)}")
    print(f"inconclusive (INCONCLUSIVE/all): {wilson(r['INCONCLUSIVE'] + p['INCONCLUSIVE'], nr + n_passed)}")
    print(f"empty PASS: {empty}/{nr + n_passed}")
    print(f"changed lines in scope examined: {cov_covered}/{cov_total}" + (f" ({100 * cov_covered / cov_total:.0f} %)" if cov_total else ""))
    if preexisting:
        print(f"pre-existing red tests (not counted): {preexisting}")
    ts = sorted(times)
    print(f"time per check: median {ts[len(ts) // 2]} s, max {ts[-1]} s, total {sum(ts)} s")
    if gates_err:
        print("gates in error:")
        for k, n in gates_err.most_common():
            print(f"  {n:>3} {k}")
    if causes:
        print("INCONCLUSIVE causes (escalations):")
        for k, n in causes.most_common():
            print(f"  {n:>3} {k}")
    if fails:
        print("FAIL (test, trees):")
        for iid, a, lab, t, trees, msg in fails:
            print(f"  {iid} {a} [{lab}] {t} {trees} {msg}")
