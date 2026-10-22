"""Candidate instances of the benchmark: SWE-ABS Verified with Python >= 3.9 (CrossHair requires it), gold touching only
.py files, and per agent (among those with downloadable patches) whether it resolved the instance according to the
SWE-bench results.json. Output: candidates.json with {instance_id, repo, version, python, gold_files, resolved_by:[agents]}."""
import json
import re
import sys

import pandas as pd

D = sys.argv[1]
agents = [a.strip() for a in open(f"{D}/usable_agents.txt", encoding="utf-8") if a.strip()]
df = pd.read_parquet(f"{D}/abs.parquet")
py = json.load(open(f"{D}/python_versions.json", encoding="utf-8"))


def pyver(r):
    v = py.get(r.repo, {}).get(r.version)
    return v


df["python"] = df.apply(pyver, axis=1)
df["gold_files"] = df["patch"].map(lambda p: re.findall(r"^\+\+\+ b/(\S+)", p, re.M))
df["gold_py"] = df["gold_files"].map(lambda a: all(x.endswith(".py") for x in a))
df["py_ok"] = df["python"].map(lambda v: v is not None and tuple(int(x) for x in v.split(".")[:2]) >= (3, 9))
print("python per instance (None = no spec):", df["python"].value_counts(dropna=False).to_dict())
print("py>=3.9:", int(df.py_ok.sum()), "| and gold only .py:", int((df.py_ok & df.gold_py).sum()))
resolved = {}
for a in agents:
    try:
        resolved[a] = set(json.load(open(f"{D}/results/{a}.json", encoding="utf-8"))["resolved"])
    except FileNotFoundError:
        print("no results.json:", a)
selected = df[df.py_ok & df.gold_py].sort_values("instance_id")
out = []
for _, r in selected.iterrows():
    out.append({
        "instance_id": r.instance_id, "repo": r.repo, "version": r.version, "python": r.python,
        "base_commit": r.base_commit, "gold_files": list(r.gold_files),
        "f2p_n": len(json.loads(r.FAIL_TO_PASS)), "difficulty": r.difficulty,
        "resolved_by": sorted(a for a, s in resolved.items() if r.instance_id in s),
    })
json.dump(out, open(f"{D}/candidates.json", "w", encoding="utf-8"), indent=1)
import collections
print("candidates:", len(out), "| per repo:", dict(collections.Counter(c["repo"] for c in out)))
print("with >=1 agent that resolved:", sum(1 for c in out if c["resolved_by"]),
      "| with >=5:", sum(1 for c in out if len(c["resolved_by"]) >= 5))
