"""Tabulates the AI-DLC sensor runs written by measure_aidlc.sh. Usage: tabulate_aidlc.py <results folder>"""
import collections
import re
import sys

R = sys.argv[1]
SENSORS = ["claim-sources", "linter", "required-sections", "traceability", "type-check", "upstream-coverage"]


def rows(name):
    for line in open(f"{R}/{name}", encoding="utf-8", errors="replace"):
        p = line.rstrip("\n").split("\t")
        if len(p) >= 5:
            yield p


def classify(rc, last):
    if "does not match sensor" in last:
        return "rejected by matches glob (sensor never ran)"
    m = re.search(r'"(outcome|result|status)"\s*:\s*"([A-Z_-]+)"', last)
    if m:
        return f"ran: {m.group(2)}"
    if "SENSOR_" in last:
        return "ran: " + re.search(r"SENSOR_[A-Z_]+", last).group(0)
    return f"other (rc={rc}): {last[:90]}"


print("## A. 9 frozen cases, every changed file x 6 sensors")
by_tree = collections.defaultdict(lambda: collections.Counter())
files_per_tree = collections.defaultdict(set)
for tree, f, s, rc, last in rows("trees.tsv"):
    by_tree[tree][classify(rc, last)] += 1
    files_per_tree[tree].add(f)
for tree in sorted(by_tree):
    print(f"- {tree}: {len(files_per_tree[tree])} changed file(s), {sum(by_tree[tree].values())} fires ->",
          "; ".join(f"{k}: {v}" for k, v in by_tree[tree].items()))

print("\n## A2. probe.ts (a file that DOES match the two code sensors)")
for label, f, s, rc, last in rows("probe.tsv"):
    print(f"- {label} / {s}: rc={rc} | {last[:160]}")

print("\n## B. files touched by the agent patches of the 10 instances")
patches = collections.defaultdict(set)
files_of = collections.defaultdict(set)
for line in open(f"{R}/swe-files.tsv", encoding="utf-8", errors="replace"):
    p = line.rstrip("\n").split("\t")
    if len(p) == 3:
        patches[p[0]].add(p[1])
        files_of[(p[0], p[1])].add(p[2])
verdict = {}
for _, f, s, rc, last in rows("swe.tsv"):
    verdict[(f, s)] = classify(rc, last)
n_patches = sum(len(v) for v in patches.values())
uniq = {f for fs in files_of.values() for f in fs}
print(f"instances: {len(patches)}, patches: {n_patches}, unique files: {len(uniq)}, fires: {len(verdict)}")
c = collections.Counter(verdict.values())
for k, v in c.most_common():
    print(f"- {k}: {v}")
fired_patches = 0
for key, fs in files_of.items():
    if any(verdict.get((f, s), "").startswith("ran") for f in fs for s in SENSORS):
        fired_patches += 1
print(f"patches where at least one sensor ran: {fired_patches} of {n_patches}")
ext = collections.Counter(f.rsplit(".", 1)[-1] if "." in f.rsplit("/", 1)[-1] else "(none)" for f in uniq)
print("extensions:", dict(ext.most_common(8)))
