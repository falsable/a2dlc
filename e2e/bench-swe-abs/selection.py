"""Deterministic order of instances: candidates with >= 5 usable agents that resolved them, sorted by
sha256(instance_id). Writes order.json (the first N) so that the benchmark is frozen before anything runs."""
import hashlib
import json
import sys

D, n = sys.argv[1], int(sys.argv[2])
candidates = json.load(open(f"{D}/candidates.json", encoding="utf-8"))
eligible = [c for c in candidates if len(c["resolved_by"]) >= 5]
eligible.sort(key=lambda c: hashlib.sha256(c["instance_id"].encode()).hexdigest())
order = eligible[:n]
json.dump(order, open(f"{D}/order.json", "w", encoding="utf-8"), indent=1)
print(len(eligible), "eligible;", len(order), "in order.json")
for i, c in enumerate(order):
    print(f"{i:2} {c['instance_id']:45} py{c['python']} f2p={c['f2p_n']:3} agents={len(c['resolved_by']):2} {c['difficulty']}")
