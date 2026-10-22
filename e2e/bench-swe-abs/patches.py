"""Extracts each agent's patch for the requested instances. Agents with all_preds.jsonl: reads model_patch; the others:
writes their S3 URL to pending.txt to download with curl (this script uses no network).
Usage: patches.py <downloads> <output> <instance_id>..."""
import json
import os
import sys

D, OUT, ids = sys.argv[1], sys.argv[2], sys.argv[3:]
agents = [a.strip() for a in open(f"{D}/usable_agents.txt", encoding="utf-8") if a.strip()]
order = {c["instance_id"]: c for c in json.load(open(f"{D}/order.json", encoding="utf-8"))}
pending = []
for a in agents:
    p = f"{D}/preds/{a}.jsonl"
    with_jsonl = os.path.exists(p) and open(p, encoding="utf-8", errors="replace").readline().lstrip().startswith("{")
    per_instance = {}
    if with_jsonl:
        for line in open(p, encoding="utf-8", errors="replace"):
            line = line.strip()
            if not line:
                continue
            d = json.loads(line)
            per_instance[d["instance_id"]] = d.get("model_patch") or ""
    for iid in ids:
        if a not in order[iid]["resolved_by"]:
            continue
        dest = f"{OUT}/{iid}/patches/{a}.diff"
        os.makedirs(os.path.dirname(dest), exist_ok=True)
        if with_jsonl:
            if iid not in per_instance:
                print("no patch in jsonl:", a, iid)
                continue
            txt = per_instance[iid]
            if not txt.endswith("\n"):
                txt += "\n"
            open(dest, "w", encoding="utf-8", newline="\n").write(txt)
        else:
            pending.append(f"{dest}\thttps://swe-bench-submissions.s3.amazonaws.com/verified/{a}/logs/{iid}/patch.diff")
open(f"{OUT}/pending.txt", "w", encoding="utf-8", newline="\n").write("\n".join(pending) + ("\n" if pending else ""))
print(len(pending), "to download from S3")
