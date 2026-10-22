"""Tabulates the benchmark runs (VARA.md): {rejected, passed} × {FAIL, INCONCLUSIVE, PASS, ERROR}, Wilson 95 %,
causes of INCONCLUSIVE by frequency, counterexamples of the FAILs and timings.
Usage: tabulate.py <bench folder>/work <instance_id>... (the measured ones; the pilot is passed separately)."""
import collections
import json
import math
import os
import sys

T, ids = sys.argv[1], sys.argv[2:]


def wilson(k, n, z=1.96):
    if n == 0:
        return "–"
    p = k / n
    d = 1 + z * z / n
    c = (p + z * z / (2 * n)) / d
    h = z * math.sqrt(p * (1 - p) / n + z * z / (4 * n * n)) / d
    return f"{100 * p:.0f} % [{100 * max(0, c - h):.0f}–{100 * min(1, c + h):.0f}]"


table = {lab: collections.Counter() for lab in ("rejected", "passed")}
causes = collections.Counter()
fails = []
times = []
states = collections.Counter()
labels = collections.Counter()
for iid in ids:
    w = f"{T}/{iid}"
    state = open(f"{w}/status.txt").read().strip() if os.path.exists(f"{w}/status.txt") else "sin_correr"
    states[state] += 1
    for f in os.listdir(f"{w}/labels") if os.path.isdir(f"{w}/labels") else []:
        if f != "gold.json":
            r = json.load(open(f"{w}/labels/{f}", encoding="utf-8")).get("resolved")
            labels[{True: "passed", False: "rejected"}.get(r, "no_label")] += 1
    if state != "medida":
        continue
    for f in sorted(os.listdir(f"{w}/runs")):
        if not f.endswith(".meta.json"):
            continue
        m = json.load(open(f"{w}/runs/{f}", encoding="utf-8"))
        a = m["agent"]
        times.append(m["seconds"])
        vp = f"{w}/runs/{a}.verdict.json"
        if not os.path.exists(vp):
            table[m["label"]]["ERROR"] += 1
            continue
        v = json.load(open(vp, encoding="utf-8"))
        table[m["label"]][v["verdict"]] += 1
        # empty PASS: no target at all (everything out of scope). Counts as PASS in the table (benchmark rule) and separately here.
        if v["verdict"] == "PASS" and not v.get("targets"):
            table[m["label"]]["PASS_empty"] += 1
            for o in v.get("out_of_scope", []):
                causes["out of scope: " + o.get("reason", "?")] += 1
        for t in v.get("targets", []):
            if t["verdict"] == "INCONCLUSIVE":
                cause = (t.get("reason") or "").split(":")[0][:70]
                causes[cause] += 1
            if t["verdict"] == "FAIL":
                fails.append((iid, a, m["label"], t["target"], t.get("counterexample")))
        for e in v.get("engine_errors", []):
            causes["engine error: " + (e.get("error") or "")[:60]] += 1

print("instances:", dict(states), "| labels computed:", dict(labels))
print(f"{'':10} {'FAIL':>6} {'INCONCL':>8} {'PASS':>6} {'(empty)':>8} {'ERROR':>6} {'n':>4}")
total = lambda c: c["FAIL"] + c["INCONCLUSIVE"] + c["PASS"] + c["ERROR"]  # noqa: E731
for lab, c in table.items():
    print(f"{lab:10} {c['FAIL']:6} {c['INCONCLUSIVE']:8} {c['PASS']:6} {c['PASS_empty']:8} {c['ERROR']:6} {total(c):4}")
nr, n_passed = total(table["rejected"]), total(table["passed"])
everything = nr + n_passed
print("detection (FAIL/rejected):", wilson(table["rejected"]["FAIL"], nr))
print("inconclusive (INCONCLUSIVE/all):", wilson(table["rejected"]["INCONCLUSIVE"] + table["passed"]["INCONCLUSIVE"], everything))
print("FAIL in passed:", wilson(table["passed"]["FAIL"], n_passed))
if times:
    print(f"time per check: median {sorted(times)[len(times) // 2]} s, max {max(times)} s, total {sum(times)} s")
print("\nINCONCLUSIVE causes (targets):")
for c, k in causes.most_common():
    print(f"  {k:4} {c}")
print("\nFAIL (counterexamples):")
for iid, a, lab, tg, ce in fails:
    print(f"  {iid} {a} [{lab}] {tg}: {json.dumps(ce, ensure_ascii=False)[:200]}")
