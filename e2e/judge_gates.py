"""Compares the verdict of an E2E case of the gates with what the manifest expects. Prints one line and exits 0 if it matches.
The manifest uses the English schema (cases, expected, tests, not_judged, pending_waivers): the frozen gates-py
casos.json is translated to it by e2e/normalize_manifest.py, which measure_gates.sh runs first.
Usage: python e2e/judge_gates.py <manifest.json> <case id> <verdict.json> <escalations.json>"""
import json
import sys

manifest = json.load(open(sys.argv[1], encoding="utf-8"))
case = next(c for c in manifest["cases"] if c["id"] == sys.argv[2])
try:
    v = json.load(open(sys.argv[3], encoding="utf-8"))
    escalations = json.load(open(sys.argv[4], encoding="utf-8"))
except OSError as e:
    print(f"BAD {case['id']}: no output ({e})")
    sys.exit(1)
bad = []
if v["verdict"] != case["expected"]:
    bad.append(f"verdict {v['verdict']} (want {case['expected']})")
by_name = {t["target"].rsplit("::", 1)[-1]: t for t in v.get("tests") or []}
for q in case["tests"]:
    t = by_name.get(q["name"])
    if t is None:
        bad.append(f"{q['name']} not judged")
        continue
    for k in ("gate", "verdict"):
        if t[k] != q[k]:
            bad.append(f"{q['name']}.{k}={t[k]} (want {q[k]})")
    if q.get("characterization") and not t.get("characterization"):
        bad.append(f"{q['name']} without characterization")
for n in case.get("not_judged", []):
    if n in by_name:
        bad.append(f"{n} was judged as new but already existed")
if case.get("pending_waivers") and v["pending_waivers"] != case["pending_waivers"]:
    bad.append(f"pending_waivers {v['pending_waivers']}")
gates = v.get("test_gates") or {}
summary = " ".join(f"{k}={g['status']}" for k, g in gates.items())
detail = "; ".join(f"{t['target'].rsplit('::', 1)[-1]}:{t['gate']}:{t['verdict']}:{t['base']}->{t['candidate']}"
                    for t in v.get("tests") or [])
kinds = ",".join(sorted({i["kind"] for i in escalations["items"]})) or "-"
print(f"{'ok ' if not bad else 'BAD'} {case['id']}: {v['verdict']} [{summary}] tests[{detail}] escalations[{kinds}]"
      + (f" -- {' | '.join(bad)}" if bad else ""))
for k, g in gates.items():
    if g.get("reason"):
        print(f"      {k}: {g['reason'][:200]}")
sys.exit(1 if bad else 0)
